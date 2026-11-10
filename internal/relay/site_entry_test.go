package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSiteAppEntryRedirect(t *testing.T) {
	for _, tc := range []struct {
		name, host, path, base, app, location string
		local, roost                          bool
	}{
		{name: "site", host: "wingthing.ai", path: "/app/", base: "https://wingthing.ai", app: "app.wingthing.ai", location: "https://app.wingthing.ai/"},
		{name: "query", host: "WINGTHING.AI:443", path: "/app/?from=install", base: "https://wingthing.ai", app: "app.wingthing.ai", location: "https://app.wingthing.ai/?from=install"},
		{name: "no slash", host: "wingthing.ai", path: "/app", base: "https://wingthing.ai", app: "app.wingthing.ai", location: "https://app.wingthing.ai/"},
		{name: "custom http", host: "site.example:8080", path: "/app/", base: "http://site.example:8080", app: "app.example:8080", location: "http://app.example:8080/"},
		{name: "app host", host: "APP.WINGTHING.AI:443", path: "/app/", base: "https://wingthing.ai", app: "app.wingthing.ai"},
		{name: "single host", host: "wingthing.ai", path: "/app/", base: "https://wingthing.ai"},
		{name: "local", host: "localhost", path: "/app/", base: "http://localhost", app: "app.wingthing.ai", local: true},
		{name: "self hosted", host: "team.example", path: "/app/", base: "https://team.example", app: "app.team.example", roost: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(nil, ServerConfig{BaseURL: tc.base, AppHost: tc.app})
			s.LocalMode, s.RoostMode = tc.local, tc.roost
			r := httptest.NewRequest(http.MethodGet, tc.base+tc.path, nil)
			r.Host = tc.host
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if tc.location != "" {
				if w.Code != http.StatusPermanentRedirect || w.Header().Get("Location") != tc.location {
					t.Fatalf("GET %s%s: %d %q, want 308 %q", tc.host, tc.path, w.Code, w.Header().Get("Location"), tc.location)
				}
			} else if w.Code != http.StatusOK {
				t.Fatalf("existing app entry: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
