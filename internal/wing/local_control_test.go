package wing

import (
	"github.com/ehrlich-b/wingthing/internal/auth"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalWingOwnerBindingSurvivesOfflineStart(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "local"}[local], func(t *testing.T) {
			dir := t.TempDir()
			ordinary := auth.NewTokenStore(dir)
			store := ordinary
			if local {
				if err := ordinary.Save(&auth.DeviceToken{Token: "independent-hosted-login", UserID: "hosted-owner"}); err != nil {
					t.Fatal(err)
				}
				store = auth.NewLocalTokenStore(dir)
			}
			token := &auth.DeviceToken{Token: "fixture-credential", DeviceID: "fixture-device"}
			if err := store.Save(token); err != nil {
				t.Fatal(err)
			}
			roost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/auth/check" || r.Header.Get("Authorization") != "Bearer fixture-credential" {
					t.Error("unexpected owner lookup")
					w.WriteHeader(401)
					return
				}
				_, _ = w.Write([]byte(`{"user_id":"owner-user"}`))
			}))
			owner, err := bindLocalWingOwner(dir, roost.URL, token, local)
			if err != nil || owner != "owner-user" {
				t.Fatalf("owner binding: %s %v", owner, err)
			}
			roost.Close()
			persisted, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if owner, err := bindLocalWingOwner(dir, roost.URL, persisted, local); err != nil || owner != "owner-user" {
				t.Fatalf("offline binding: %s %v", owner, err)
			}
			if local {
				hosted, err := ordinary.Load()
				if err != nil || hosted.Token != "independent-hosted-login" || hosted.UserID != "hosted-owner" {
					t.Fatal("local binding changed hosted login")
				}
			}
		})
	}
}

func TestLocalWingOwnerBindingRejectsMissingIdentity(t *testing.T) {
	dir := t.TempDir()
	token := &auth.DeviceToken{Token: "fixture"}
	store := auth.NewTokenStore(dir)
	if err := store.Save(token); err != nil {
		t.Fatal(err)
	}
	roost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer roost.Close()
	if _, err := bindLocalWingOwner(dir, roost.URL, token, false); err == nil {
		t.Fatal("accepted ownerless roost response")
	}
	persisted, err := store.Load()
	if err != nil || persisted.UserID != "" {
		t.Fatal("failed binding mutated credential")
	}
}
