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
	mu                   sync.RWMutex
	tiers                map[string]string // userID → tier
	relay                map[string]RelayAccess
	enrollment           map[string]bool
	initialized          bool
	policyDecisionsKnown bool
	loginAddr            string
	client               *http.Client
	secret               string
}

const maxEntitlementResponseBytes = 1 << 20

func NewEntitlementCache(loginAddr string, internalSecret ...string) *EntitlementCache {
	cache := &EntitlementCache{
		tiers:      make(map[string]string),
		relay:      make(map[string]RelayAccess),
		enrollment: make(map[string]bool),
		loginAddr:  loginAddr,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
	if len(internalSecret) > 0 {
		cache.secret = internalSecret[0]
	}
	return cache
}

// GetRelayAccess returns the login node's cached hosted relay decision. A
// missing entry fails closed while directory and signaling remain available.
func (c *EntitlementCache) GetRelayAccess(userID string) RelayAccess {
	c.mu.RLock()
	access, ok := c.relay[userID]
	initialized := c.initialized
	known := c.policyDecisionsKnown
	c.mu.RUnlock()
	if initialized && !known {
		// N-1 login nodes made the relay available to every authenticated user.
		// Preserve that behavior until the authoritative login node is upgraded.
		return RelayAccess{Allowed: true, Reason: "legacy-login"}
	}
	if !ok {
		return RelayAccess{Allowed: false, Reason: "entitlement-unavailable"}
	}
	return access
}

// GetEnrollment returns the authoritative private-roost enrollment decision.
// N-1 login nodes did not publish such a decision, so callers opting into the
// new enrollment boundary must fail closed until the login node is upgraded.
func (c *EntitlementCache) GetEnrollment(userID string) (bool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.initialized || !c.policyDecisionsKnown {
		return false, false
	}
	allowed, ok := c.enrollment[userID]
	return allowed, ok
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
	authorizeInternalRequest(req, c.secret)

	resp, err := c.client.Do(req)
	if err != nil {
		log.Printf("entitlement cache: fetch failed: %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		log.Printf("entitlement cache: fetch status %d", resp.StatusCode)
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxEntitlementResponseBytes+1))
	if err != nil {
		return
	}
	if len(body) > maxEntitlementResponseBytes {
		log.Printf("entitlement cache: response exceeds %d bytes", maxEntitlementResponseBytes)
		return
	}

	var entries []EntitlementEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return
	}

	decisionVersion := resp.Header.Get(entitlementDecisionVersionHeader)
	if decisionVersion != "" && decisionVersion != "2" {
		// An absent header is the explicitly supported N-1 tier-only shape. Do
		// not mistake an unknown future protocol for that permissive legacy
		// contract: keep the last known-good cache (or fail closed before the
		// first successful sync) until this edge understands the new version.
		log.Printf("entitlement cache: unsupported decision version %q", decisionVersion)
		return
	}
	policyDecisionsKnown := decisionVersion == "2"
	newTiers := make(map[string]string, len(entries))
	newRelay := make(map[string]RelayAccess, len(entries))
	newEnrollment := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.UserID == "" || (policyDecisionsKnown && e.RelayReason == "") {
			log.Printf("entitlement cache: invalid versioned entry for user %q", e.UserID)
			return
		}
		newTiers[e.UserID] = e.Tier
		if policyDecisionsKnown {
			newRelay[e.UserID] = RelayAccess{Allowed: e.RelayAllowed, Reason: e.RelayReason}
			newEnrollment[e.UserID] = e.Enrolled
		}
	}

	c.mu.Lock()
	c.tiers = newTiers
	c.relay = newRelay
	c.enrollment = newEnrollment
	c.initialized = true
	c.policyDecisionsKnown = policyDecisionsKnown
	c.mu.Unlock()

	log.Printf("entitlement cache: synced %d entries", len(entries))
}
