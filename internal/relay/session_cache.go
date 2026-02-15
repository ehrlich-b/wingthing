package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// SessionCache caches session token → user info on edge nodes.
// Each validated session is cached for 5 minutes to avoid hitting the login node on every request.
type SessionCache struct {
	mu      sync.RWMutex
	entries map[string]*sessionCacheEntry
	client  *http.Client
}

type sessionCacheEntry struct {
	user      *User
	fetchedAt time.Time
}

func NewSessionCache() *SessionCache {
	return &SessionCache{
		entries: make(map[string]*sessionCacheEntry),
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

// Validate checks the cache or calls the login node to validate a session token.
func (sc *SessionCache) Validate(token, loginAddr string) *User {
	sc.mu.RLock()
	entry := sc.entries[token]
	sc.mu.RUnlock()

	if entry != nil && time.Since(entry.fetchedAt) < 5*time.Minute {
		return entry.user
	}

	// Call login node
	resp, err := sc.client.Get(loginAddr + "/internal/sessions/" + token)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Cache negative result briefly to avoid hammering
		sc.mu.Lock()
		sc.entries[token] = &sessionCacheEntry{user: nil, fetchedAt: time.Now()}
		sc.mu.Unlock()
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return nil
	}

	var sv SessionValidation
	if err := json.Unmarshal(body, &sv); err != nil {
		return nil
	}

	user := &User{
		ID:          sv.UserID,
		DisplayName: sv.DisplayName,
		OrgIDs:      sv.OrgIDs,
	}

	sc.mu.Lock()
	sc.entries[token] = &sessionCacheEntry{user: user, fetchedAt: time.Now()}
	sc.mu.Unlock()

	return user
}

// UpdateUserOrgs updates the cached org IDs for all sessions belonging to userID.
func (sc *SessionCache) UpdateUserOrgs(userID string, orgIDs []string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for _, entry := range sc.entries {
		if entry.user != nil && entry.user.ID == userID {
			entry.user.OrgIDs = orgIDs
		}
	}
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
	resp, err := sc.client.Post(loginAddr+"/internal/user-orgs-bulk", "application/json", bytes.NewReader(payload))
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var result map[string][]string // user_id → org_ids
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return
	}
	for uid, orgIDs := range result {
		sc.UpdateUserOrgs(uid, orgIDs)
	}
	log.Printf("session cache: synced org memberships for %d users", len(result))
}
