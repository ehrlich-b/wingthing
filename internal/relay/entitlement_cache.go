package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
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
	syncMu               sync.Mutex
	updatedAt            time.Time
	retryAt              time.Time
	ttl                  time.Duration
	grace                time.Duration
	pageSize             int
}

const maxEntitlementResponseBytes = 1 << 20

func NewEntitlementCache(loginAddr string, internalSecret ...string) *EntitlementCache {
	cache := &EntitlementCache{
		tiers:      make(map[string]string),
		relay:      make(map[string]RelayAccess),
		enrollment: make(map[string]bool),
		loginAddr:  loginAddr,
		client:     &http.Client{Timeout: 10 * time.Second},
		updatedAt:  time.Now(),
		ttl:        relayLimitDuration(0, "ENTITLEMENT_TTL", 15*time.Minute),
		grace:      relayLimitDuration(0, "ENTITLEMENT_GRACE", 5*time.Minute),
		pageSize:   relayLimitInt(0, "ENTITLEMENT_PAGE_SIZE", 500),
	}
	if len(internalSecret) > 0 {
		cache.secret = internalSecret[0]
	}
	if cache.pageSize > maxEntitlementPageSize {
		cache.pageSize = maxEntitlementPageSize
	}
	return cache
}

// GetRelayAccess returns the login node's cached hosted relay decision. A
// missing entry fails closed while directory and signaling remain available.
func (c *EntitlementCache) GetRelayAccess(userID string) RelayAccess {
	c.ensureFresh()
	c.mu.RLock()
	access, ok := c.relay[userID]
	initialized := c.initialized
	known := c.policyDecisionsKnown
	expired := c.expiredLocked()
	c.mu.RUnlock()
	if expired {
		return RelayAccess{Allowed: false, Reason: "entitlement-stale"}
	}
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
	c.ensureFresh()
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.initialized || !c.policyDecisionsKnown || c.expiredLocked() {
		return false, false
	}
	allowed, ok := c.enrollment[userID]
	return allowed, ok
}

// GetTier returns the cached tier for a user, defaulting to "free".
func (c *EntitlementCache) GetTier(userID string) string {
	c.ensureFresh()
	c.mu.RLock()
	tier := c.tiers[userID]
	if c.expiredLocked() {
		tier = "free"
	}
	c.mu.RUnlock()
	if tier == "" {
		return "free"
	}
	return tier
}

func (c *EntitlementCache) expiredLocked() bool {
	return time.Since(c.updatedAt) > c.ttl+c.grace
}

// Stale reads trigger a refetch. Only one request does I/O, and failures back
// off for a minute; the last decision survives only the bounded grace period.
func (c *EntitlementCache) ensureFresh() {
	c.mu.RLock()
	due := time.Since(c.updatedAt) >= c.ttl && !time.Now().Before(c.retryAt)
	c.mu.RUnlock()
	if !due || !c.syncMu.TryLock() {
		return
	}
	defer c.syncMu.Unlock()
	c.mu.RLock()
	due = time.Since(c.updatedAt) >= c.ttl && !time.Now().Before(c.retryAt)
	c.mu.RUnlock()
	if due {
		c.fetchLocked(context.Background())
	}
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
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	c.fetchLocked(ctx)
}

func (c *EntitlementCache) fetchLocked(ctx context.Context) {
	c.mu.Lock()
	c.retryAt = time.Now().Add(time.Minute)
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	newTiers := make(map[string]string)
	newRelay := make(map[string]RelayAccess)
	newEnrollment := make(map[string]bool)
	var policyDecisionsKnown bool
	after := ""
	for {
		entries, version, next, err := c.fetchPage(ctx, after)
		if err != nil {
			log.Printf("entitlement cache: sync failed: %v", err)
			return
		}
		known := version == "2"
		if after != "" && known != policyDecisionsKnown {
			log.Printf("entitlement cache: decision version changed during sync")
			return
		}
		policyDecisionsKnown = known
		for _, e := range entries {
			if e.UserID == "" || (known && e.RelayReason == "") {
				log.Printf("entitlement cache: invalid versioned entry for user %q", e.UserID)
				return
			}
			if _, duplicate := newTiers[e.UserID]; duplicate {
				log.Printf("entitlement cache: duplicate user during sync")
				return
			}
			newTiers[e.UserID] = e.Tier
			if known {
				newRelay[e.UserID] = RelayAccess{Allowed: e.RelayAllowed, Reason: e.RelayReason}
				newEnrollment[e.UserID] = e.Enrolled
			}
		}
		if next == "" {
			break
		}
		if next <= after || len(entries) == 0 || next != entries[len(entries)-1].UserID {
			log.Printf("entitlement cache: invalid pagination cursor")
			return
		}
		after = next
	}
	c.mu.Lock()
	c.tiers = newTiers
	c.relay = newRelay
	c.enrollment = newEnrollment
	c.initialized = true
	c.policyDecisionsKnown = policyDecisionsKnown
	c.updatedAt = time.Now()
	c.mu.Unlock()
	log.Printf("entitlement cache: synced %d entries", len(newTiers))
}

func (c *EntitlementCache) fetchPage(ctx context.Context, after string) ([]EntitlementEntry, string, string, error) {
	query := url.Values{"limit": {strconv.Itoa(c.pageSize)}}
	if after != "" {
		query.Set("after", after)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.loginAddr+"/internal/entitlements?"+query.Encode(), nil)
	if err != nil {
		return nil, "", "", err
	}
	authorizeInternalRequest(req, c.secret)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, "", "", fmt.Errorf("fetch status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxEntitlementResponseBytes+1))
	if err != nil {
		return nil, "", "", err
	}
	if len(body) > maxEntitlementResponseBytes {
		return nil, "", "", fmt.Errorf("response exceeds %d bytes", maxEntitlementResponseBytes)
	}

	var entries []EntitlementEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, "", "", err
	}

	decisionVersion := resp.Header.Get(entitlementDecisionVersionHeader)
	if decisionVersion != "" && decisionVersion != "2" {
		// An absent header is the explicitly supported N-1 tier-only shape. Do
		// not mistake an unknown future protocol for that permissive legacy
		// contract: keep the last known-good cache (or fail closed before the
		// first successful sync) until this edge understands the new version.
		return nil, "", "", fmt.Errorf("unsupported decision version %q", decisionVersion)
	}
	return entries, decisionVersion, resp.Header.Get(entitlementNextHeader), nil
}
