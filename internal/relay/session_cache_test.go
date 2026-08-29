package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSessionCacheReturnsIsolatedUserValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, SessionValidation{
			UserID: "user-1", Email: "member@example.com",
			OrgIDs: []string{"org-1"}, OrgRoles: map[string]string{"org-1": "admin"},
		})
	}))
	defer server.Close()

	cache := NewSessionCache()
	first := cache.Validate("token", server.URL)
	first.OrgIDs[0] = "mutated"
	first.OrgRoles["org-1"] = "owner"
	*first.Email = "mutated@example.com"

	second := cache.Validate("token", server.URL)
	if second.OrgIDs[0] != "org-1" || second.OrgRoles["org-1"] != "admin" || *second.Email != "member@example.com" {
		t.Fatalf("caller mutated cached identity: %#v", second)
	}
}

func TestSessionCacheOrgRefreshDoesNotRaceReaders(t *testing.T) {
	cache := NewSessionCache()
	cache.entries["token"] = &sessionCacheEntry{
		user:      &User{ID: "user-1", OrgIDs: []string{"org-1"}, OrgRoles: map[string]string{"org-1": "member"}},
		fetchedAt: time.Now(),
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 100 {
				user := cache.Validate("token", "")
				_, _ = json.Marshal(user)
			}
		}()
		go func() {
			defer wg.Done()
			for range 100 {
				cache.UpdateUserOrgs("user-1", []string{"org-1", "org-2"})
			}
		}()
	}
	wg.Wait()

	user := cache.Validate("token", "")
	if len(user.OrgIDs) != 2 || user.OrgRoles["org-1"] != "member" {
		t.Fatalf("refreshed user = %#v", user)
	}
}

func TestSessionCacheExactOrgRefreshAppliesRoleDemotion(t *testing.T) {
	cache := NewSessionCache()
	cache.entries["token"] = &sessionCacheEntry{
		user: &User{
			ID:       "user-1",
			OrgIDs:   []string{"org-1", "org-removed"},
			OrgRoles: map[string]string{"org-1": "admin", "org-removed": "owner"},
		},
		fetchedAt: time.Now(),
	}

	cache.UpdateUserOrgContext(
		"user-1",
		[]string{"org-1"},
		map[string]string{"org-1": "member"},
	)

	user := cache.Validate("token", "")
	if len(user.OrgIDs) != 1 || user.OrgIDs[0] != "org-1" {
		t.Fatalf("memberships after exact refresh = %v", user.OrgIDs)
	}
	if len(user.OrgRoles) != 1 || user.OrgRoles["org-1"] != "member" {
		t.Fatalf("roles after exact refresh = %v", user.OrgRoles)
	}
}

func TestRemoteOrgChangedRefreshAppliesExactRolesToSessionCache(t *testing.T) {
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(internalSecretHeader); got != "shared-secret" {
			http.Error(w, "missing internal secret", http.StatusForbidden)
			return
		}
		if r.URL.Path != "/internal/user-orgs/user-1" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, userOrgContext{
			OrgIDs: []string{"org-1"},
			OrgRoles: map[string]string{
				"org-1": "member",
			},
		})
	}))
	defer login.Close()

	cache := NewSessionCache()
	cache.entries["token"] = &sessionCacheEntry{
		user: &User{
			ID:       "user-1",
			OrgIDs:   []string{"org-1"},
			OrgRoles: map[string]string{"org-1": "admin"},
		},
		fetchedAt: time.Now(),
	}
	edge := NewServer(testStore(t), ServerConfig{NodeRole: "edge", LoginNodeAddr: login.URL, InternalSecret: "shared-secret"})
	edge.SetSessionCache(cache)

	edge.refreshRemoteUserOrgs("user-1")

	user := cache.Validate("token", "")
	if got := user.OrgRoles["org-1"]; got != "member" {
		t.Fatalf("role after org.changed refresh = %q, want member", got)
	}
}

func TestSessionCachePropagatesInternalSecret(t *testing.T) {
	var validated, synced bool
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(internalSecretHeader); got != "shared-secret" {
			http.Error(w, "missing internal secret", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/internal/sessions/token":
			validated = true
			writeJSON(w, http.StatusOK, SessionValidation{UserID: "user-1"})
		case "/internal/user-orgs-bulk":
			synced = true
			writeJSON(w, http.StatusOK, map[string][]string{"user-1": {"org-1"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer login.Close()

	cache := NewSessionCache("shared-secret")
	if user := cache.Validate("token", login.URL); user == nil || user.ID != "user-1" {
		t.Fatalf("validated user = %#v", user)
	}
	cache.syncOrgs(login.URL)
	if !validated || !synced {
		t.Fatalf("internal calls: validated=%v synced=%v", validated, synced)
	}
}

func TestSessionCacheDoesNotCacheTransientLoginFailure(t *testing.T) {
	calls := 0
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, SessionValidation{UserID: "user-1"})
	}))
	defer login.Close()

	cache := NewSessionCache()
	if user := cache.Validate("token", login.URL); user != nil {
		t.Fatalf("transient failure returned a user: %#v", user)
	}
	if user := cache.Validate("token", login.URL); user == nil || user.ID != "user-1" {
		t.Fatalf("recovered login validation returned %#v", user)
	}
	if calls != 2 {
		t.Fatalf("login calls = %d, want 2", calls)
	}
}

func TestSessionCacheCachesAuthoritativeDenial(t *testing.T) {
	calls := 0
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "invalid session", http.StatusUnauthorized)
	}))
	defer login.Close()

	cache := NewSessionCache()
	for attempt := 1; attempt <= 2; attempt++ {
		if user := cache.Validate("token", login.URL); user != nil {
			t.Fatalf("unauthorized token unexpectedly validated on attempt %d: %#v", attempt, user)
		}
	}
	if calls != 1 {
		t.Fatalf("login calls = %d, want 1 cached denial", calls)
	}
}

func TestSessionCacheRejectsOversizedValidationResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		payload := []byte(`{"user_id":"user-1"}`)
		_, _ = w.Write(append(payload, strings.Repeat(" ", maxSessionValidationBytes)...))
	}))
	defer server.Close()

	cache := NewSessionCache()
	if user := cache.Validate("token", server.URL); user != nil {
		t.Fatalf("oversized session response was accepted: %#v", user)
	}
}

func TestSessionCacheIsBoundedAndEvictsOldestToken(t *testing.T) {
	cache := NewSessionCache()
	now := time.Now()
	for index := 0; index < maxSessionCacheEntries; index++ {
		cache.store(fmt.Sprintf("token-%d", index), &sessionCacheEntry{fetchedAt: now})
	}
	cache.store("overflow", &sessionCacheEntry{fetchedAt: now})

	cache.mu.RLock()
	defer cache.mu.RUnlock()
	if got := len(cache.entries); got != maxSessionCacheEntries {
		t.Fatalf("cache entries = %d, want %d", got, maxSessionCacheEntries)
	}
	if got := len(cache.order); got != maxSessionCacheEntries {
		t.Fatalf("cache order = %d, want %d", got, maxSessionCacheEntries)
	}
	if _, exists := cache.entries["token-0"]; exists {
		t.Fatal("oldest token was not evicted")
	}
	if _, exists := cache.entries["overflow"]; !exists {
		t.Fatal("new token was not cached")
	}
}

func TestSessionCacheRejectsOversizedBulkOrgResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"user-1":["%s"]}`, strings.Repeat("x", maxBulkOrgResponseBytes))
	}))
	defer server.Close()

	cache := NewSessionCache()
	cache.store("token", &sessionCacheEntry{
		user:      &User{ID: "user-1", OrgIDs: []string{"org-original"}},
		fetchedAt: time.Now(),
	})
	cache.syncOrgs(server.URL)

	user := cache.Validate("token", "")
	if len(user.OrgIDs) != 1 || user.OrgIDs[0] != "org-original" {
		t.Fatalf("oversized bulk response changed memberships: %v", user.OrgIDs)
	}
}
