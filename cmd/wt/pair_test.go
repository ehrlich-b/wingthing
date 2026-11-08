package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/pake"
	"github.com/ehrlich-b/wingthing/internal/phonepair"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
	"github.com/skip2/go-qrcode"
)

func TestPairTerminalHasRealScannableLayoutAndPermissionCopy(t *testing.T) {
	q := phonepair.Invitation{Version: 1, WingID: "8761234abcef8761234abc", Name: "my-mac", Key: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), Local: "http://192.168.1.40:6078", Relay: "https://wingthing.ai", ID: strings.Repeat("a", 43), Secret: "apple bridge cedar delta ember frost", ExpiresAt: time.Now().Add(5 * time.Minute)}
	var out bytes.Buffer
	if e := showInvitation(&out, q, ""); e != nil {
		t.Fatal(e)
	}
	place := "this machine"
	if runtime.GOOS == "darwin" {
		place = "this Mac"
	}
	for _, copy := range []string{"Pair a device with wing my-mac (" + place + "). Access will be shown here before you approve.", q.Secret, "5:00 · Waiting for scan...", "Same Wi-Fi: no account needed. Away from home: needs " + place + " registered once (`wt login`)."} {
		if !strings.Contains(out.String(), copy) {
			t.Fatal("missing required terminal copy", copy)
		}
	}
	if strings.Contains(out.String(), q.Link()) {
		t.Fatal("normal terminal printed hidden fallback link")
	}
	qr, e := qrcode.New(q.Link(), qrcode.Low)
	if e != nil {
		t.Fatal(e)
	}
	matrix := qr.Bitmap()
	if len(matrix) > 80 {
		t.Fatalf("QR wraps an ordinary terminal: %d columns", len(matrix))
	}
	// Decode the upper/lower block representation back into the exact QR bitmap.
	lines := strings.Split(out.String(), "\n")
	var rendered []string
	for _, line := range lines {
		if strings.HasPrefix(line, "\x1b[30;47m") {
			rendered = append(rendered, strings.TrimSuffix(strings.TrimPrefix(line, "\x1b[30;47m"), "\x1b[0m"))
		}
	}
	for y, row := range matrix {
		runes := []rune(rendered[y/2])
		for x, black := range row {
			r := runes[x]
			got := r == '█' || (y%2 == 0 && r == '▀') || (y%2 == 1 && r == '▄')
			if got != black {
				t.Fatal("terminal changed QR module")
			}
		}
	}
}
func TestPhoneLocalOnlyTerminalExplainsNoAccount(t *testing.T) {
	q := phonepair.Invitation{Version: 1, WingID: "main-wing", Name: "my-mac", Key: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), Local: "http://192.168.1.40:6078", Endpoints: []string{"http://100.91.2.3:6078", "http://my-mac.fixture.ts.net:6078"}, ID: strings.Repeat("a", 43), Secret: "apple bridge cedar delta ember frost", ExpiresAt: time.Now().Add(5 * time.Minute)}
	var out bytes.Buffer
	if e := showInvitation(&out, q, ""); e != nil {
		t.Fatal(e)
	}
	qr, err := qrcode.New(q.Link(), qrcode.Low)
	if err != nil {
		t.Fatal(err)
	}
	if len(qr.Bitmap()) > 80 {
		t.Fatalf("tailnet QR wraps: %d", len(qr.Bitmap()))
	}
	if !strings.Contains(out.String(), "Local network only. That's fine; nothing else needed.") || !strings.Contains(out.String(), q.Endpoints[1]) {
		t.Fatal("local-only/tailnet copy missing")
	}
}
func TestPhoneCommandsAreRegistered(t *testing.T) {
	for _, path := range [][]string{{"pair"}, {"devices"}, {"devices", "revoke"}} {
		command, _, e := newRootCommand().Find(path)
		if e != nil || command == nil {
			t.Fatal("missing command", path)
		}
	}
}

func TestPairGuidanceNamesLANListenFlag(t *testing.T) {
	var out bytes.Buffer
	cmd := pairCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "wt wing start --local-only --listen 0.0.0.0") || !strings.Contains(out.String(), "loopback only") {
		t.Fatalf("missing LAN guidance in help: %s", out.String())
	}
	if !pake.Available {
		t.Skip("PAKE unavailable on this build")
	}
	t.Setenv("WINGTHING_DIR", testsocket.Dir(t))
	err := runPair(t.Context(), strings.NewReader(""), &out)
	if err == nil || !strings.Contains(err.Error(), "wt wing start --local-only --listen 0.0.0.0") || !strings.Contains(err.Error(), "unavailable or ambiguous") || !strings.Contains(err.Error(), "wt wing start --listen 0.0.0.0") {
		t.Fatalf("missing LAN guidance when wing is absent: %v", err)
	}
}

func TestPairLoopbackOnlyWarning(t *testing.T) {
	t.Setenv("WINGTHING_DIR", testsocket.Dir(t))
	for _, endpoint := range []string{"http://127.0.0.1:6078", "http://127.0.0.2:6078", "http://[::1]:6078", "http://[::ffff:127.0.0.1]:6078", "http://localhost:6078"} {
		t.Run(endpoint, func(t *testing.T) {
			q := phonepair.Invitation{Local: endpoint, Endpoints: []string{endpoint}, Secret: "private test secret"}
			var out bytes.Buffer
			err := showInvitation(&out, q, "")
			if err == nil || !strings.Contains(err.Error(), "listens on loopback only") || !strings.Contains(err.Error(), "a phone cannot connect over the LAN") || !strings.Contains(err.Error(), lanPairStartCommand) {
				t.Fatalf("missing loopback warning: %v", err)
			}
			for _, want := range []string{lanPairStartCommand, "Other flags the wing was started with must be added by hand."} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("restart guidance missing %q: %v", want, err)
				}
			}
			if !strings.Contains(err.Error(), "unavailable or ambiguous") || !strings.Contains(err.Error(), "wt wing start --listen 0.0.0.0") {
				t.Fatalf("unqueried mode must offer both variants: %v", err)
			}
			if out.Len() != 0 {
				t.Fatalf("unusable invitation was displayed: %s", out.String())
			}
			q.Relay = "https://wingthing.ai"
			if err := showInvitation(&out, q, ""); err != nil {
				t.Fatalf("registered relay is usable: %v", err)
			}
			if !strings.Contains(out.String(), "unavailable or ambiguous") || !strings.Contains(out.String(), "wt wing start --listen 0.0.0.0") {
				t.Fatalf("relay restart guidance changes mode: %s", out.String())
			}
			if !strings.Contains(out.String(), "listens on loopback only") || !strings.Contains(out.String(), "Pair through the registered relay") || !strings.Contains(out.String(), "\x1b[30;47m") || strings.Contains(out.String(), "Direct endpoint:") || strings.Contains(out.String(), "Same Wi-Fi: no account needed") {
				t.Fatalf("relay invitation misrepresents LAN access: %s", out.String())
			}
		})
	}
}

func TestPairNoLANInterfaceDoesNotSuggestRebinding(t *testing.T) {
	q := phonepair.Invitation{Local: "http://127.0.0.1:6078", Secret: "private test secret"}
	for _, relay := range []string{"", "https://wingthing.ai"} {
		q.Relay = relay
		var out bytes.Buffer
		err := showInvitation(&out, q, "0.0.0.0:6078")
		message := out.String()
		if relay == "" {
			if err == nil || out.Len() != 0 {
				t.Fatalf("unusable invitation displayed: %v %s", err, &out)
			}
			message = err.Error()
		} else if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(message, "no advertised LAN endpoint") || strings.Contains(message, "--listen 0.0.0.0") || strings.Contains(message, "listens on loopback only") {
			t.Fatalf("incorrect missing-interface guidance: %s", message)
		}
	}
}

func TestLANPairRestartGuidanceUsesLiveMode(t *testing.T) {
	for _, mode := range []string{phonepair.ModeLocalOnly, phonepair.ModeRelay, "", "ambiguous"} {
		t.Run("mode="+mode, func(t *testing.T) {
			dir := testsocket.Dir(t)
			t.Setenv("WINGTHING_DIR", dir)
			// Deliberately stale, conflicting files and flag-looking string values.
			for name, data := range map[string]string{
				"wing.args":   "wing\nstart\n--foreground\n--roost\nhttps://dead.example\n--labels=--listen",
				"wing.status": `{"state":"connected","roost_url":"https://dead.example"}`,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "" {
				private, err := ecdh.X25519().GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				server, err := phonepair.Listen(t.Context(), dir, "fixture-wing", "Fixture", nil, private, nil, "127.0.0.1", mode)
				if err != nil {
					t.Fatal(err)
				}
				if server == nil {
					t.Skip("PAKE unavailable")
				}
				defer server.Close()
			}
			got := lanPairRestartGuidance()
			// An empty invitation relay must not override the live mode.
			var out bytes.Buffer
			err := showInvitation(&out, phonepair.Invitation{Local: "http://127.0.0.1:6078"}, "")
			if err == nil || !strings.Contains(err.Error(), got) {
				t.Fatalf("invitation changed live restart guidance: %v", err)
			}
			local, relay := strings.Contains(got, lanPairStartCommand), strings.Contains(got, "wt wing start --listen 0.0.0.0")
			if local != (mode != phonepair.ModeRelay) || relay != (mode != phonepair.ModeLocalOnly) {
				t.Fatalf("wrong commands for mode %q: %s", mode, got)
			}
			if strings.Contains(got, "--roost") || strings.Contains(got, "--labels") || !strings.Contains(got, "must be added by hand") {
				t.Fatalf("reconstructed stale arguments or missing note: %s", got)
			}
		})
	}
}
