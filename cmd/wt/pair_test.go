package main

import (
	"bytes"
	"encoding/base64"
	"github.com/ehrlich-b/wingthing/internal/phonepair"
	"github.com/skip2/go-qrcode"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPairTerminalHasRealScannableLayoutAndPermissionCopy(t *testing.T) {
	q := phonepair.Invitation{Version: 1, WingID: "8761234abcef8761234abc", Name: "my-mac", Key: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), Local: "http://192.168.1.40:6078", Relay: "https://wingthing.ai", ID: strings.Repeat("a", 43), Secret: "apple bridge cedar delta ember frost", ExpiresAt: time.Now().Add(5 * time.Minute)}
	var out bytes.Buffer
	if e := showInvitation(&out, q); e != nil {
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
	if e := showInvitation(&out, q); e != nil {
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
