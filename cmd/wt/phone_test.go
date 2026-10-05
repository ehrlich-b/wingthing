package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
)

func TestPhoneLinkConstructionAndEncoding(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32))
	const account, wing, bearer = "account +&/=?", "wing +&/=?", "private +&/=?%#"
	for _, noToken := range []bool{false, true} {
		link, err := buildPhoneLink("https://home.example:8443", account, wing, key, bearer, noToken)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(link, "wingthing://home?v=1&") || strings.Contains(link, "+") || strings.Contains(link, " ") {
			t.Fatal("link was not URL-encoded for Foundation query items")
		}
		parsed, err := url.Parse(link)
		if err != nil {
			t.Fatal(err)
		}
		values := parsed.Query()
		for name, want := range map[string]string{"v": "1", "origin": "https://home.example:8443", "account": account, "wing": wing, "key": key} {
			if got := values.Get(name); got != want {
				t.Errorf("%s did not round-trip", name)
			}
		}
		if noToken {
			if values.Has("token") || strings.Contains(link, url.QueryEscape(bearer)) {
				t.Fatal("--no-token included a credential")
			}
		} else if values.Get("token") != bearer {
			t.Fatal("bearer did not round-trip")
		}
	}
}

func phoneLinkState(t *testing.T, account string) (*auth.TokenStore, *auth.DeviceToken) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("WINGTHING_DIR", dir)
	// The running wing uses config's machine ID, not the token's device ID.
	for name, contents := range map[string]string{"wing-id": "0123456789abcdef01234567\n", "wing.yaml": "roost: https://home.example:8443\n", "config.yaml": "roost_url: https://unused.example\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	key, err := auth.EnsureKeyPair(dir)
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewTokenStore(dir)
	token := &auth.DeviceToken{Token: "private-test-bearer+&/=", DeviceID: "other-device", PublicKey: key, UserID: account}
	if err := store.Save(token); err != nil {
		t.Fatal(err)
	}
	return store, token
}

func runPhoneLink(t *testing.T, flags ...string) (string, string, error) {
	t.Helper()
	cmd := newRootCommand()
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs(append([]string{"phone", "link"}, flags...))
	err := cmd.Execute()
	return out.String(), stderr.String(), err
}

type phoneLinkTransport func(*http.Request) (*http.Response, error)

func (f phoneLinkTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func phoneLinkHTTP(t *testing.T, f phoneLinkTransport) {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = f
	t.Cleanup(func() { http.DefaultTransport = old })
}

func TestPhoneLinkCLIUsesLocalStateAndNeverLogsToken(t *testing.T) {
	_, token := phoneLinkState(t, "local-account")
	phoneLinkHTTP(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("local identity must not call the network")
		return nil, nil
	})
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(old) })
	for _, flags := range [][]string{nil, {"--json"}, {"--no-token"}, {"--json", "--no-token"}} {
		out, stderr, err := runPhoneLink(t, flags...)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(stderr, "\n") != 1 || !strings.HasPrefix(stderr, "Warning:") {
			t.Fatal("missing one-line stderr warning")
		}
		for _, secret := range []string{token.Token, url.QueryEscape(token.Token)} {
			if strings.Contains(stderr+logs.String(), secret) {
				t.Fatal("credential appeared outside stdout")
			}
		}
		link := strings.TrimSpace(out)
		if strings.Contains(strings.Join(flags, " "), "--json") {
			var result struct{ URL string }
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err)
			}
			link = result.URL
		}
		parsed, err := url.Parse(link)
		if err != nil {
			t.Fatal(err)
		}
		values := parsed.Query()
		if values.Get("origin") != "https://home.example:8443" || values.Get("account") != "local-account" || values.Get("wing") != "0123456789abcdef01234567" || values.Get("key") != token.PublicKey {
			t.Fatal("setup did not use this wing's state")
		}
		if strings.Contains(strings.Join(flags, " "), "--no-token") {
			if values.Has("token") {
				t.Fatal("credential present with --no-token")
			}
		} else if values.Get("token") != token.Token {
			t.Fatal("missing credential on stdout")
		}
	}
}

func TestPhoneLinkLegacyAccountChecksConfiguredRoostAndCachesIdentity(t *testing.T) {
	store, token := phoneLinkState(t, "")
	requests := 0
	phoneLinkHTTP(t, func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.String() != "https://home.example:8443/auth/check" || req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer "+token.Token {
			t.Fatal("account check did not stay at the configured roost")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"user_id":"legacy-account"}`)), Header: make(http.Header), Request: req}, nil
	})
	for range 2 {
		if _, _, err := runPhoneLink(t, "--no-token"); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 1 {
		t.Fatalf("account checks = %d, want 1", requests)
	}
	saved, err := store.Load()
	if err != nil || saved.UserID != "legacy-account" || saved.Token != token.Token {
		t.Fatal("auth-check identity not retained with bearer")
	}
}

func TestPhoneLinkFailuresNeverExposeCredentialOrFollowRedirects(t *testing.T) {
	for _, scenario := range []string{"transport", "redirect", "body", "malformed-token", "output"} {
		t.Run(scenario, func(t *testing.T) {
			store, token := phoneLinkState(t, "")
			calls := 0
			phoneLinkHTTP(t, func(req *http.Request) (*http.Response, error) {
				calls++
				if calls > 1 {
					t.Fatal("followed redirect")
				}
				if scenario == "transport" {
					return nil, errors.New(token.Token)
				}
				status, body := 200, `{"user_id":42,"secret":"`+token.Token+`"}`
				if scenario == "redirect" {
					status = 302
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": {"https://other.example/"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})
			if scenario == "malformed-token" {
				if err := os.WriteFile(filepath.Join(store.Dir, "device_token.yaml"), []byte("device_token: ["+token.Token), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			out, stderr, err := runPhoneLink(t)
			if scenario == "output" {
				token.UserID = "local-account"
				if err := store.Save(token); err != nil {
					t.Fatal(err)
				}
				cmd := phoneCmd()
				cmd.SetArgs([]string{"link"})
				cmd.SetOut(phoneLinkFailWriter{token.Token})
				cmd.SetErr(&logs)
				err = cmd.Execute()
			}
			if err == nil || out != "" {
				t.Fatal("failed setup emitted a link")
			}
			if strings.Contains(stderr+logs.String()+err.Error(), token.Token) {
				t.Fatal("failure exposed credential")
			}
		})
	}
}

type phoneLinkFailWriter struct{ secret string }

func (w phoneLinkFailWriter) Write([]byte) (int, error) { return 0, errors.New(w.secret) }

func TestPhoneLinkRequiresLoginEvenWithoutToken(t *testing.T) {
	for _, scenario := range []string{"missing", "empty", "expired", "wrong-key"} {
		t.Run(scenario, func(t *testing.T) {
			store, token := phoneLinkState(t, "local-account")
			switch scenario {
			case "missing":
				if err := store.Delete(); err != nil {
					t.Fatal(err)
				}
			case "empty":
				token.Token = ""
			case "expired":
				token.ExpiresAt = time.Now().Add(-time.Minute).Unix()
			case "wrong-key":
				token.PublicKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
			}
			if scenario != "missing" {
				if err := store.Save(token); err != nil {
					t.Fatal(err)
				}
			}
			out, _, err := runPhoneLink(t, "--no-token")
			if err == nil || out != "" {
				t.Fatal("invalid login emitted setup link")
			}
		})
	}
}

func TestPhoneLinkRejectsInvalidAndOversizedValues(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	for _, origin := range []string{"http://home.example", "https://home.example/path", "https://user:secret@home.example", "https://home.example/?x=1", "https://home.example/#fragment"} {
		if _, err := buildPhoneLink(origin, "account", "wing", key, "secret", false); err == nil {
			t.Fatal("invalid origin accepted")
		}
	}
	for _, fields := range [][3]string{{"", "wing", key}, {"account", "", key}, {"account", "wing", "bad-key"}} {
		if _, err := buildPhoneLink("https://home.example", fields[0], fields[1], fields[2], "secret", false); err == nil {
			t.Fatal("invalid identity accepted")
		}
	}
	if _, err := buildPhoneLink("https://home.example", "account", "wing", key, strings.Repeat("x", maxPhoneLinkBytes), false); err == nil {
		t.Fatal("oversize link accepted")
	}
}
