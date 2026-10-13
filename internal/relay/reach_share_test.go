package relay

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReachSharePreviewRequiresLocalDevRoost(t *testing.T) {
	for _, dev := range []bool{false, true} {
		for _, local := range []bool{false, true} {
			t.Run(testBoolName(dev)+"-"+testBoolName(local), func(t *testing.T) {
				store, err := OpenRelay(":memory:")
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				user, _, err := store.CreateLocalUser()
				if err != nil {
					t.Fatal(err)
				}
				s := NewServer(store, ServerConfig{})
				s.DevMode = dev
				s.LocalMode = local
				s.SetLocalUser(user)
				req := httptest.NewRequest("GET", "http://localhost/api/app/me", nil)
				if !local {
					// Unauthenticated hosted dev never advertises local fixture controls.
					rec := httptest.NewRecorder()
					s.handleAppMe(rec, req)
					if rec.Code != 401 {
						t.Fatalf("hosted unauthenticated status %d", rec.Code)
					}
					if err := store.CreateSession("fixture-owner-session", user.ID, time.Now().Add(time.Hour)); err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Cookie", "wt_session=fixture-owner-session")
				}
				rec := httptest.NewRecorder()
				s.handleAppMe(rec, req)
				var result map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result["reach_share_preview"] != (dev && local) {
					t.Fatalf("preview = %v", result["reach_share_preview"])
				}
			})
		}
	}
}
func testBoolName(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
