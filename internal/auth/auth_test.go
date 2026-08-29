package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode device request: %v", err)
			return
		}
		if body["wing_id"] != "test-machine" {
			t.Errorf("unexpected wing_id: %s", body["wing_id"])
		}

		if err := json.NewEncoder(w).Encode(DeviceCodeResponse{
			DeviceCode:      "DCOD-1234",
			UserCode:        "ABCD-EFGH",
			VerificationURL: "https://wingthing.ai/activate",
			ExpiresIn:       900,
			Interval:        5,
		}); err != nil {
			t.Errorf("encode device response: %v", err)
		}
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

func TestAuthenticationResponsesAreBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"padding":"` + strings.Repeat("x", maxAuthResponseBytes) + `"}`))
	}))
	defer srv.Close()

	if _, err := RequestDeviceCode(srv.URL, "test-machine"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized device response error = %v", err)
	}
	if _, err := FetchUserInfo(srv.URL, "token"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized user response error = %v", err)
	}
	if _, err := RefreshToken(srv.URL, DeviceToken{Token: "token"}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized refresh response error = %v", err)
	}
}

func TestPollForTokenCancelsInflightRequest(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		select {
		case <-r.Context().Done():
		case <-releaseHandler:
		}
	}))
	defer func() {
		close(releaseHandler)
		srv.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 1100*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := PollForToken(ctx, srv.URL, "DCOD-1234", 1)
		result <- err
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("poll request did not start")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("poll cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight poll did not honor context cancellation")
	}
}

func TestPollForToken(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)

		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode poll request: %v", err)
			return
		}
		if body["device_code"] != "DCOD-1234" {
			t.Errorf("unexpected device_code: %s", body["device_code"])
		}

		if n == 1 {
			if err := json.NewEncoder(w).Encode(TokenResponse{Error: "authorization_pending"}); err != nil {
				t.Errorf("encode pending response: %v", err)
			}
			return
		}
		if err := json.NewEncoder(w).Encode(TokenResponse{
			Token:     "tok_abc123",
			ExpiresAt: time.Now().Add(24 * time.Hour).Unix(),
		}); err != nil {
			t.Errorf("encode token response: %v", err)
		}
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
		if err := json.NewEncoder(w).Encode(TokenResponse{Error: "authorization_pending"}); err != nil {
			t.Errorf("encode pending response: %v", err)
		}
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
		if err := json.NewDecoder(r.Body).Decode(&tok); err != nil {
			t.Errorf("decode refresh request: %v", err)
			return
		}
		if tok.Token != "tok_old" {
			t.Errorf("unexpected token: %s", tok.Token)
		}

		if err := json.NewEncoder(w).Encode(TokenResponse{
			Token:     "tok_new",
			ExpiresAt: time.Now().Add(48 * time.Hour).Unix(),
		}); err != nil {
			t.Errorf("encode refresh response: %v", err)
		}
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

func TestLocalTokenStoreDoesNotReplaceOrdinaryPortalLogin(t *testing.T) {
	dir := t.TempDir()
	ordinary := NewTokenStore(dir)
	local := NewLocalTokenStore(dir)
	if err := ordinary.Save(&DeviceToken{Token: "hosted-login", DeviceID: "hosted"}); err != nil {
		t.Fatal(err)
	}
	if err := local.Save(&DeviceToken{Token: "localhost-login", DeviceID: "local"}); err != nil {
		t.Fatal(err)
	}

	ordinaryToken, err := ordinary.Load()
	if err != nil {
		t.Fatal(err)
	}
	localToken, err := local.Load()
	if err != nil {
		t.Fatal(err)
	}
	if ordinaryToken.Token != "hosted-login" || localToken.Token != "localhost-login" {
		t.Fatalf("token stores overlapped: ordinary=%#v local=%#v", ordinaryToken, localToken)
	}
	for _, name := range []string{"device_token.yaml", "local_device_token.yaml"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s permissions = %o, want 600", name, info.Mode().Perm())
		}
	}
}

func TestTokenStoreSaveReplacesPermissiveFilePrivately(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "device_token.yaml")
	if err := os.WriteFile(path, []byte("token: old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewTokenStore(dir)
	if err := store.Save(&DeviceToken{Token: "new-secret", DeviceID: "device"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode = %o, want 600", info.Mode().Perm())
	}
}

func TestTokenStoreSaveReplacesSymlinkWithoutFollowingIt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "unrelated")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "device_token.yaml")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	store := NewTokenStore(dir)
	if err := store.Save(&DeviceToken{Token: "local-secret"}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "unchanged" {
		t.Fatalf("symlink target changed: data=%q err=%v", data, err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("token path remained a symlink: info=%v err=%v", info, err)
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

func TestFetchUserInfo_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/check" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer valid-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"ok":           true,
			"user_id":      "u1",
			"display_name": "Phil Heckel",
			"email":        "phil@test.com",
			"provider":     "github",
		}); err != nil {
			t.Errorf("encode check response: %v", err)
		}
	}))
	defer srv.Close()

	info, err := FetchUserInfo(srv.URL, "valid-token")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if info.UserID != "u1" {
		t.Errorf("user_id = %q, want u1", info.UserID)
	}
	if info.DisplayName != "Phil Heckel" {
		t.Errorf("display_name = %q, want Phil Heckel", info.DisplayName)
	}
	if info.Email != "phil@test.com" {
		t.Errorf("email = %q, want phil@test.com", info.Email)
	}
	if info.Provider != "github" {
		t.Errorf("provider = %q, want github", info.Provider)
	}
}

func TestFetchUserInfo_NoProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"user_id": "u2",
		}); err != nil {
			t.Errorf("encode user response: %v", err)
		}
	}))
	defer srv.Close()

	info, err := FetchUserInfo(srv.URL, "any-token")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if info.UserID != "u2" {
		t.Errorf("user_id = %q, want u2", info.UserID)
	}
	if info.DisplayName != "" {
		t.Errorf("display_name = %q, want empty", info.DisplayName)
	}
}

func TestValidateTokenRemote_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/check" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer valid-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"ok": true, "user_id": "u1"}); err != nil {
			t.Errorf("encode validation response: %v", err)
		}
	}))
	defer srv.Close()

	if err := ValidateTokenRemote(srv.URL, "valid-token"); err != nil {
		t.Fatalf("expected nil, got: %v", err)
	}
}

func TestFetchUserInfo_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := FetchUserInfo(srv.URL, "bad-token")
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("expected ErrAuthFailed, got: %v", err)
	}
}

func TestFetchUserInfo_Unreachable(t *testing.T) {
	_, err := FetchUserInfo("http://127.0.0.1:1", "any-token")
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
	if errors.Is(err, ErrAuthFailed) {
		t.Fatal("should not be ErrAuthFailed for network error")
	}
}

func TestFetchUserInfo_UnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := FetchUserInfo(srv.URL, "any-token")
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if errors.Is(err, ErrAuthFailed) {
		t.Fatal("404 should not be ErrAuthFailed")
	}
}

func TestValidateTokenRemote_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	err := ValidateTokenRemote(srv.URL, "bad-token")
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("expected ErrAuthFailed, got: %v", err)
	}
}

func TestValidateTokenRemote_Unreachable(t *testing.T) {
	err := ValidateTokenRemote("http://127.0.0.1:1", "any-token")
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
	if errors.Is(err, ErrAuthFailed) {
		t.Fatal("should not be ErrAuthFailed for network error")
	}
}

func TestErrAuthFailed_ErrorsIs(t *testing.T) {
	// Verify ErrAuthFailed works with errors.Is (was a bug with fmt.Errorf)
	err := ErrAuthFailed
	if !errors.Is(err, ErrAuthFailed) {
		t.Error("errors.Is should match ErrAuthFailed")
	}
	wrapped := fmt.Errorf("outer: %w", ErrAuthFailed)
	if !errors.Is(wrapped, ErrAuthFailed) {
		t.Error("errors.Is should match wrapped ErrAuthFailed")
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
