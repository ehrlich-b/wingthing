package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestDeviceCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/device" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}

		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["machine_id"] != "test-machine" {
			t.Errorf("unexpected machine_id: %s", body["machine_id"])
		}

		json.NewEncoder(w).Encode(DeviceCodeResponse{
			DeviceCode:      "DCOD-1234",
			UserCode:        "ABCD-EFGH",
			VerificationURL: "https://wingthing.ai/activate",
			ExpiresIn:       900,
			Interval:        5,
		})
	}))
	defer srv.Close()

	resp, err := RequestDeviceCode(srv.URL, "test-machine")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.DeviceCode != "DCOD-1234" {
		t.Errorf("device_code = %q, want DCOD-1234", resp.DeviceCode)
	}
	if resp.UserCode != "ABCD-EFGH" {
		t.Errorf("user_code = %q, want ABCD-EFGH", resp.UserCode)
	}
	if resp.VerificationURL != "https://wingthing.ai/activate" {
		t.Errorf("verification_url = %q", resp.VerificationURL)
	}
	if resp.ExpiresIn != 900 {
		t.Errorf("expires_in = %d, want 900", resp.ExpiresIn)
	}
	if resp.Interval != 5 {
		t.Errorf("interval = %d, want 5", resp.Interval)
	}
}

func TestPollForToken(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)

		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["device_code"] != "DCOD-1234" {
			t.Errorf("unexpected device_code: %s", body["device_code"])
		}

		if n == 1 {
			json.NewEncoder(w).Encode(TokenResponse{Error: "authorization_pending"})
			return
		}
		json.NewEncoder(w).Encode(TokenResponse{
			Token:     "tok_abc123",
			ExpiresAt: time.Now().Add(24 * time.Hour).Unix(),
		})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := PollForToken(ctx, srv.URL, "DCOD-1234", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Token != "tok_abc123" {
		t.Errorf("token = %q, want tok_abc123", resp.Token)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 calls, got %d", calls.Load())
	}
}

func TestPollForTokenTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(TokenResponse{Error: "authorization_pending"})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := PollForToken(ctx, srv.URL, "DCOD-1234", 1)
	if err == nil {
		t.Fatal("expected error on timeout")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got: %v", err)
	}
}

func TestRefreshToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/refresh" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var tok DeviceToken
		json.NewDecoder(r.Body).Decode(&tok)
		if tok.Token != "tok_old" {
			t.Errorf("unexpected token: %s", tok.Token)
		}

		json.NewEncoder(w).Encode(TokenResponse{
			Token:     "tok_new",
			ExpiresAt: time.Now().Add(48 * time.Hour).Unix(),
		})
	}))
	defer srv.Close()

	resp, err := RefreshToken(srv.URL, DeviceToken{
		Token:    "tok_old",
		DeviceID: "machine-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Token != "tok_new" {
		t.Errorf("token = %q, want tok_new", resp.Token)
	}
}

func TestTokenStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewTokenStore(dir)

	original := &DeviceToken{
		Token:     "tok_test",
		ExpiresAt: 1700000000,
		IssuedAt:  1699999000,
		DeviceID:  "dev-1",
	}

	if err := store.Save(original); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Verify file permissions
	info, err := os.Stat(filepath.Join(dir, "device_token.yaml"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("permissions = %o, want 0600", perm)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Token != original.Token {
		t.Errorf("token = %q, want %q", loaded.Token, original.Token)
	}
	if loaded.ExpiresAt != original.ExpiresAt {
		t.Errorf("expires_at = %d, want %d", loaded.ExpiresAt, original.ExpiresAt)
	}
	if loaded.IssuedAt != original.IssuedAt {
		t.Errorf("issued_at = %d, want %d", loaded.IssuedAt, original.IssuedAt)
	}
	if loaded.DeviceID != original.DeviceID {
		t.Errorf("device_id = %q, want %q", loaded.DeviceID, original.DeviceID)
	}
}

func TestTokenStoreDelete(t *testing.T) {
	dir := t.TempDir()
	store := NewTokenStore(dir)

	if err := store.Save(&DeviceToken{Token: "tok_delete"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := store.Delete(); err != nil {
		t.Fatalf("delete: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil after delete, got %+v", loaded)
	}
}

func TestIsValid(t *testing.T) {
	store := NewTokenStore(t.TempDir())

	// nil token
	if store.IsValid(nil) {
		t.Error("nil token should be invalid")
	}

	// expired token
	expired := &DeviceToken{
		Token:     "tok_expired",
		ExpiresAt: time.Now().Add(-1 * time.Hour).Unix(),
	}
	if store.IsValid(expired) {
		t.Error("expired token should be invalid")
	}

	// valid token
	valid := &DeviceToken{
		Token:     "tok_valid",
		ExpiresAt: time.Now().Add(1 * time.Hour).Unix(),
	}
	if !store.IsValid(valid) {
		t.Error("valid token should be valid")
	}

	// no-expiry token
	noExpiry := &DeviceToken{
		Token:     "tok_forever",
		ExpiresAt: 0,
	}
	if !store.IsValid(noExpiry) {
		t.Error("no-expiry token should be valid")
	}
}
