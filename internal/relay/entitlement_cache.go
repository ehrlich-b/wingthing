package relay

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// EntitlementCache caches user tier info on edge nodes, polling the login node periodically.
type EntitlementCache struct {
	mu        sync.RWMutex
	tiers     map[string]string // userID → tier
	loginAddr string
	client    *http.Client
}

func NewEntitlementCache(loginAddr string) *EntitlementCache {
	return &EntitlementCache{
		tiers:     make(map[string]string),
		loginAddr: loginAddr,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

// GetTier returns the cached tier for a user, defaulting to "free".
func (c *EntitlementCache) GetTier(userID string) string {
	c.mu.RLock()
	tier := c.tiers[userID]
	c.mu.RUnlock()
	if tier == "" {
		return "free"
	}
	return tier
}

// StartSync begins periodic polling of the login node for entitlement data.
func (c *EntitlementCache) StartSync(ctx context.Context, interval time.Duration) {
	// Initial fetch
	c.fetch(ctx)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.fetch(ctx)
			}
		}
	}()
}

func (c *EntitlementCache) fetch(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.loginAddr+"/internal/entitlements", nil)
	if err != nil {
		return
	}

	resp, err := c.client.Do(req)
	if err != nil {
		log.Printf("entitlement cache: fetch failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("entitlement cache: fetch status %d", resp.StatusCode)
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return
	}

	var entries []EntitlementEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return
	}

	newTiers := make(map[string]string, len(entries))
	for _, e := range entries {
		newTiers[e.UserID] = e.Tier
	}

	c.mu.Lock()
	c.tiers = newTiers
	c.mu.Unlock()

	log.Printf("entitlement cache: synced %d entries", len(entries))
}
