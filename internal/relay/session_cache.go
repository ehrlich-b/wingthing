package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// SessionCache caches session token → user info on edge nodes.
// Each validated session is cached for 5 minutes to avoid hitting the login node on every request.
type SessionCache struct {
	mu      sync.RWMutex
	entries map[string]*sessionCacheEntry
	order   []string
	next    int
	client  *http.Client
	secret  string
}

type sessionCacheEntry struct {
	user      *User
	fetchedAt time.Time
}

const (
	maxSessionValidationBytes = 8192
	maxBulkOrgResponseBytes   = 4 << 20
	maxSessionCacheEntries    = 10_000
)

func NewSessionCache(internalSecret ...string) *SessionCache {
	cache := &SessionCache{
		entries: make(map[string]*sessionCacheEntry),
		client:  &http.Client{Timeout: 5 * time.Second},
	}
	if len(internalSecret) > 0 {
		cache.secret = internalSecret[0]
	}
	return cache
}

// store keeps both successful and negative validation results in a bounded
// FIFO cache. An evicted valid session is harmless: the next request simply
// revalidates it with the login node.
func (sc *SessionCache) store(token string, entry *sessionCacheEntry) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if _, exists := sc.entries[token]; exists {
		sc.entries[token] = entry
		return
	}
	if len(sc.order) < maxSessionCacheEntries {
		sc.order = append(sc.order, token)
	} else {
		evicted := sc.order[sc.next]
		delete(sc.entries, evicted)
		sc.order[sc.next] = token
		sc.next = (sc.next + 1) % maxSessionCacheEntries
	}
	sc.entries[token] = entry
}

// Validate checks the cache or calls the login node to validate a session token.
func (sc *SessionCache) Validate(token, loginAddr string) *User {
	sc.mu.RLock()
	entry := sc.entries[token]
	if entry != nil && time.Since(entry.fetchedAt) < 5*time.Minute {
		user := cloneUser(entry.user)
		sc.mu.RUnlock()
		return user
	}
	sc.mu.RUnlock()

	// Call login node
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(loginAddr, "/")+"/internal/sessions/"+url.PathEscape(token), nil)
	if err != nil {
		return nil
	}
	authorizeInternalRequest(req, sc.secret)
	resp, err := sc.client.Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Only authoritative authentication/enrollment denials are cacheable.
		// Treat login-node outages, rate limits, and deployment mismatches as
		// transient so a recovered login node can validate the next request.
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			sc.store(token, &sessionCacheEntry{user: nil, fetchedAt: time.Now()})
		}
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSessionValidationBytes+1))
	if err != nil {
		return nil
	}
	if len(body) > maxSessionValidationBytes {
		return nil
	}

	var sv SessionValidation
	if err := json.Unmarshal(body, &sv); err != nil {
		return nil
	}

	user := &User{
		ID:          sv.UserID,
		DisplayName: sv.DisplayName,
		Tier:        sv.Tier,
		OrgIDs:      sv.OrgIDs,
		OrgRoles:    sv.OrgRoles,
	}
	if sv.Email != "" {
		user.Email = &sv.Email
	}

	sc.store(token, &sessionCacheEntry{user: cloneUser(user), fetchedAt: time.Now()})

	return cloneUser(user)
}

// UpdateUserOrgs updates the cached org IDs for all sessions belonging to userID.
func (sc *SessionCache) UpdateUserOrgs(userID string, orgIDs []string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for _, entry := range sc.entries {
		if entry.user != nil && entry.user.ID == userID {
			updated := cloneUser(entry.user)
			updated.OrgIDs = append([]string(nil), orgIDs...)
			if len(updated.OrgRoles) > 0 {
				retained := make(map[string]string, len(updated.OrgRoles))
				for _, orgID := range orgIDs {
					if role := updated.OrgRoles[orgID]; role != "" {
						retained[orgID] = role
					}
				}
				updated.OrgRoles = retained
			}
			entry.user = updated
		}
	}
}

// UpdateUserOrgContext replaces the cached memberships and exact roles for a
// user after an authoritative single-user refresh. Unlike UpdateUserOrgs,
// which consumes the legacy bulk endpoint and must retain known roles for
// backwards compatibility, this method applies demotions immediately.
func (sc *SessionCache) UpdateUserOrgContext(userID string, orgIDs []string, orgRoles map[string]string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for _, entry := range sc.entries {
		if entry.user == nil || entry.user.ID != userID {
			continue
		}
		updated := cloneUser(entry.user)
		updated.OrgIDs = append([]string(nil), orgIDs...)
		updated.OrgRoles = make(map[string]string, len(orgRoles))
		for orgID, role := range orgRoles {
			updated.OrgRoles[orgID] = role
		}
		entry.user = updated
	}
}

// cloneUser isolates mutable slices, maps, and pointers from both cache writers
// and request handlers. Edge org synchronization may replace an entry while a
// concurrent request is reading the previously returned user.
func cloneUser(user *User) *User {
	if user == nil {
		return nil
	}
	cloned := *user
	if user.AvatarURL != nil {
		avatar := *user.AvatarURL
		cloned.AvatarURL = &avatar
	}
	if user.Email != nil {
		email := *user.Email
		cloned.Email = &email
	}
	cloned.OrgIDs = append([]string(nil), user.OrgIDs...)
	if user.OrgRoles != nil {
		cloned.OrgRoles = make(map[string]string, len(user.OrgRoles))
		for orgID, role := range user.OrgRoles {
			cloned.OrgRoles[orgID] = role
		}
	}
	return &cloned
}

// ActiveUserIDs returns the deduplicated user IDs of all valid cached sessions.
func (sc *SessionCache) ActiveUserIDs() []string {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	seen := make(map[string]bool)
	var ids []string
	for _, entry := range sc.entries {
		if entry.user != nil && !seen[entry.user.ID] && time.Since(entry.fetchedAt) < 5*time.Minute {
			seen[entry.user.ID] = true
			ids = append(ids, entry.user.ID)
		}
	}
	return ids
}

// StartOrgSync periodically bulk-refreshes org memberships for all cached sessions.
func (sc *SessionCache) StartOrgSync(ctx context.Context, loginAddr string, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sc.syncOrgs(loginAddr)
			}
		}
	}()
}

func (sc *SessionCache) syncOrgs(loginAddr string) {
	userIDs := sc.ActiveUserIDs()
	if len(userIDs) == 0 {
		return
	}
	payload, _ := json.Marshal(map[string]any{"user_ids": userIDs})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(loginAddr, "/")+"/internal/user-orgs-bulk", bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	authorizeInternalRequest(req, sc.secret)
	resp, err := sc.client.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBulkOrgResponseBytes+1))
	if err != nil || len(body) > maxBulkOrgResponseBytes {
		return
	}
	var result map[string][]string // user_id → org_ids
	if err := json.Unmarshal(body, &result); err != nil {
		return
	}
	for uid, orgIDs := range result {
		sc.UpdateUserOrgs(uid, orgIDs)
	}
	log.Printf("session cache: synced org memberships for %d users", len(result))
}
