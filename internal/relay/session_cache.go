package relay

import (
	"encoding/json"
	"io"
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
