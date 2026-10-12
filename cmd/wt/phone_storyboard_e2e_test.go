//go:build cgo

package main

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/phonepair"
)

type storyboardPeer struct {
	in  io.WriteCloser
	out *bufio.Reader
}

func newStoryboardPeer(t *testing.T, f *northstarFixture) *storyboardPeer {
	t.Helper()
	binary := filepath.Join(f.root, "testpeer")
	build := exec.CommandContext(t.Context(), "nice", "-n", "15", "go", "build", "-buildvcs=false", "-o", binary, "./internal/phonepair/testpeer")
	build.Dir = f.repo
	build.Env = append(os.Environ(), "GOFLAGS=-p=4", "GOCACHE="+filepath.Join(f.repo, ".scratch/northstar/go-build"), "GOMODCACHE="+filepath.Join(f.repo, ".scratch/northstar/go-mod"))
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("testpeer build: %v\n%s", e, out)
	}
	cmd := exec.Command(binary, "--client")
	cmd.Env = f.env
	cmd.Dir = f.work
	in, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = in.Close()
		select {
		case e := <-done:
			if e != nil {
				t.Errorf("peer exit: %v %s", e, stderr.String())
			}
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("peer did not stop")
		}
	})
	return &storyboardPeer{in: in, out: bufio.NewReader(out)}
}
func (p *storyboardPeer) call(t *testing.T, cmd any) map[string]any {
	t.Helper()
	if e := json.NewEncoder(p.in).Encode(cmd); e != nil {
		t.Fatal(e)
	}
	type response struct {
		b []byte
		e error
	}
	done := make(chan response, 1)
	go func() { b, e := p.out.ReadBytes('\n'); done <- response{b, e} }()
	var r response
	select {
	case r = <-done:
	case <-time.After(12 * time.Second):
		t.Fatal("peer response timeout")
	}
	if r.e != nil {
		t.Fatal(r.e)
	}
	var result map[string]any
	if e := json.Unmarshal(r.b, &result); e != nil {
		t.Fatal(e)
	}
	return result
}
func (p *storyboardPeer) ok(t *testing.T, cmd any) map[string]any {
	t.Helper()
	r := p.call(t, cmd)
	if r["error"] != nil {
		t.Fatalf("phone command failed: %v", r)
	}
	return r
}
func phoneLinkInvitation(t *testing.T, h *northstarPTY) phonepair.Invitation {
	t.Helper()
	h.mu.Lock()
	out := h.transcript.String()
	h.mu.Unlock()
	start := strings.Index(out, "wingthing://pair#v1z.")
	if start < 0 {
		t.Fatal("CLI did not display requested fallback link")
	}
	packed := strings.Fields(out[start:])[0]
	b, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(packed, "wingthing://pair#v1z."))
	if e != nil {
		t.Fatal(e)
	}
	reader := flate.NewReader(bytes.NewReader(b))
	defer reader.Close()
	var parts []json.RawMessage
	if e = json.NewDecoder(io.LimitReader(reader, 4096)).Decode(&parts); e != nil || len(parts) != 9 {
		t.Fatal("invalid invitation", e)
	}
	var q phonepair.Invitation
	for i, target := range []any{&q.Version, &q.WingID, &q.Name, &q.Key} {
		if e = json.Unmarshal(parts[i], target); e != nil {
			t.Fatal(e)
		}
	}
	if e = json.Unmarshal(parts[4], &q.Local); e != nil {
		var endpoints []string
		if e = json.Unmarshal(parts[4], &endpoints); e != nil || len(endpoints) == 0 {
			t.Fatal("invalid direct endpoints")
		}
		q.Local, q.Endpoints = endpoints[0], endpoints[1:]
	}
	for i, target := range []any{&q.Relay, &q.ID, &q.Secret} {
		if e = json.Unmarshal(parts[i+5], target); e != nil {
			t.Fatal(e)
		}
	}
	var seconds int64
	if e = json.Unmarshal(parts[8], &seconds); e != nil {
		t.Fatal(e)
	}
	q.ExpiresAt = time.Unix(seconds, 0)
	return q
}

// Drive the real interactive pair command to completion. storyboardCommand
// alone waits for an exit, but pairing waits for a phone and terminal approval.
// The testpeer is the phone; neither a registry entry nor a CLI screen is faked.
func storyboardPhoneCommand(t *testing.T, f *northstarFixture, binary string, args ...string) (string, error) {
	t.Helper()
	pid, err := os.ReadFile(filepath.Join(f.local, "wing.pid"))
	if err != nil {
		t.Fatal(err)
	}
	h := northstarStartPTY(t, f, binary, 160, 60, "pair", "--show-link")
	h.expect("phone fixture invitation", 0, "Pair a phone", "5:00", "Waiting for scan", "wingthing://pair#v1z.")
	q := phoneLinkInvitation(t, h)
	peer := newStoryboardPeer(t, f)
	peer.ok(t, map[string]any{"Op": "init", "Invitation": q, "Endpoint": q.Local})
	peer.ok(t, map[string]string{"Op": "start"})
	proof := peer.ok(t, map[string]string{"Op": "prove", "Name": "Bryan's iPhone"})
	emoji, ok := proof["emoji"].(string)
	if !ok || emoji == "" {
		t.Fatal("phone fixture did not receive transcript-bound emoji")
	}
	h.expect("phone fixture confirmation", 0, emoji, "Bryan's iPhone wants to pair")
	pending := peer.ok(t, map[string]string{"Op": "confirm"})
	if pending["state"] != "confirming" || pending["device_id"] != nil {
		t.Fatal("phone fixture gained access without terminal approval")
	}
	h.send("\r")
	h.expect("phone fixture paired", 0, "Paired Bryan's iPhone")
	var exitErr error
	select {
	case exitErr = <-h.done:
	case <-time.After(20 * time.Second):
		t.Fatal("phone fixture pair did not exit")
	}
	if exitErr != nil {
		t.Fatalf("phone fixture pair failed: %v", exitErr)
	}
	if err := h.slave.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.readDone:
	case <-time.After(3 * time.Second):
		t.Fatal("phone fixture pair PTY did not drain")
	}
	h.mu.Lock()
	out := h.transcript.String()
	h.mu.Unlock()
	grant := peer.ok(t, map[string]string{"Op": "status"})
	if grant["device_id"] == nil {
		t.Fatal("phone fixture did not obtain a device grant")
	}
	peer.ok(t, map[string]string{"Op": "connect"})
	inventory := peer.ok(t, map[string]string{"Op": "sessions"})
	rows, ok := inventory["result"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("phone fixture saw unexpected wing inventory: %v", inventory)
	}
	row, ok := rows[0].(map[string]any)
	if !ok || row["id"] != "phone-session" || row["blocked"] != true {
		t.Fatalf("phone fixture lost session identity/attention: %v", inventory)
	}
	peer.ok(t, map[string]string{"Op": "attach", "SessionID": "phone-session"})
	read := peer.ok(t, map[string]string{"Op": "read", "SessionID": "phone-session"})
	result, ok := read["result"].(map[string]any)
	screen, _ := result["screen"].(string)
	if !ok || !strings.Contains(screen, "PHONE_SESSION_UNCHANGED") {
		t.Fatalf("phone fixture read unexpected session: %v", read)
	}
	peer.ok(t, map[string]string{"Op": "detach"})
	after, err := os.ReadFile(filepath.Join(f.local, "wing.pid"))
	if err != nil || !bytes.Equal(pid, after) {
		t.Fatal("phone fixture pairing replaced the main wing")
	}
	if args[0] == "pair" {
		return out, exitErr
	}
	if args[0] == "devices" && len(args) > 1 && args[1] == "revoke" {
		// Card 11's unchanged prerequisite expects an unpaired-device error.
		// Exercise a real grant and revocation first, then retain that state for
		// the original command. No missing-device copy is fabricated.
		revokedOut, err := storyboardCommand(t, f, binary, args...)
		if err != nil {
			t.Fatalf("phone fixture revoke: %v\n%s", err, revokedOut)
		}
		storyboardContains(t, revokedOut, "Revoked Bryan's iPhone", "Live access closed")
		revoked := peer.call(t, map[string]string{"Op": "connect"})
		if revoked["error"] != "revoked" {
			t.Fatal("phone fixture revocation allowed reconnect", revoked)
		}
	}
	return storyboardCommand(t, f, binary, args...)
}

// This checks the repaired prerequisites separately from the original cards'
// unconditional physical-device fatal assertion. It does not replace a card.
func TestPhoneStoryboardHarnessPrerequisites(t *testing.T) {
	for _, command := range [][]string{{"pair"}, {"devices"}, {"devices", "revoke", "Bryan's iPhone"}} {
		t.Run(strings.Join(command, "-"), func(t *testing.T) {
			f, binary := storyboardFixture(t)
			if command[0] == "pair" {
				// Delay endpoint discovery beyond general wing readiness to
				// exercise the separate authenticated phone-readiness wait.
				northstarWrite(t, filepath.Join(f.bin, "tailscale"), `#!/bin/sh
[ "$1 $2" = "status --json" ] || exit 1
sleep 1
printf '%s\n' '{"BackendState":"Stopped"}'
`, 0700)
			}
			northstarStandaloneEgg(t, f, f.local, "phone-session", "pairing", "needs_input", "PHONE_SESSION_UNCHANGED")
			storyboardPhoneMainWing(t, f, binary)
			out, err := storyboardPhoneCommand(t, f, binary, command...)
			switch {
			case command[0] == "pair":
				if err != nil {
					t.Fatal(err)
				}
				storyboardContains(t, out, "Pair a phone", "5:00", "Waiting for scan")
			case len(command) == 1:
				if err != nil {
					t.Fatal(err)
				}
				storyboardContains(t, out, "NAME", "PAIRED AT", "LAST SEEN", "Bryan's iPhone")
			default:
				if err == nil {
					t.Fatal("unpaired revoke unexpectedly succeeded")
				}
				storyboardContains(t, out, "device", "not paired")
			}
		})
	}
}

// Complement the unchanged card tests with Linux-executable backend evidence.
// No result here establishes camera, Keychain, cellular, or phone UI acceptance.
func TestPhoneStoryboardWingJourney(t *testing.T) {
	f, binary := storyboardFixture(t)
	northstarWrite(t, filepath.Join(f.bin, "tailscale"), `#!/bin/sh
[ "$1 $2" = "status --json" ] || exit 1
printf '%s\n' '{"BackendState":"Running","Self":{"TailscaleIPs":["100.91.2.3"],"DNSName":"EhrlichMac.fixture.ts.net."}}'
`, 0700)
	local := northstarStandaloneEgg(t, f, f.local, "phone-session", "pairing", "needs_input", "PHONE_SESSION_UNCHANGED")
	remote := northstarStandaloneEgg(t, f, f.remote, "phone-session", "other-wing", "working", "OTHER_WING_UNCHANGED")
	out, e := storyboardCommand(t, f, binary, "wing", "start", "--paths", f.work)
	if e != nil {
		t.Fatalf("main wing startup: %v\n%s", e, out)
	}
	storyboardContains(t, out, "local control: ready", "Local network only. That's fine; nothing else needed.")
	t.Cleanup(func() {
		cmd := exec.Command(binary, "wing", "stop")
		cmd.Env = f.env
		cmd.Dir = f.work
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Errorf("stop isolated wing: %v\n%s", e, out)
		}
	})
	storyboardPhoneReady(t, f)
	pid, e := os.ReadFile(filepath.Join(f.local, "wing.pid"))
	if e != nil {
		t.Fatal(e)
	}
	var control phonepair.Control
	b, e := os.ReadFile(filepath.Join(f.local, "phone-control.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &control); e != nil {
		t.Fatal(e)
	}
	h := northstarStartPTY(t, f, binary, 160, 60, "pair", "--show-link")
	h.expect("live invitation", 0, "Pair a phone", "5:00", "Waiting for scan", "wingthing://pair#v1z.")
	q := phoneLinkInvitation(t, h)
	if q.WingID != control.WingID || q.Key != control.Key || len(strings.Fields(q.Secret)) != 6 || q.Relay != "" || time.Until(q.ExpiresAt) > 5*time.Minute {
		t.Fatal("invitation lost main-wing identity/lifetime/local-only binding")
	}
	if direct := strings.Join(q.Endpoints, " "); !strings.Contains(direct, "100.91.2.3") || !strings.Contains(direct, "ehrlichmac.fixture.ts.net") {
		t.Fatal("tailnet IP/MagicDNS absent from main wing invitation")
	}
	peer := newStoryboardPeer(t, f)
	peer.ok(t, map[string]any{"Op": "init", "Invitation": q, "Endpoint": q.Local})
	peer.ok(t, map[string]string{"Op": "start"})
	proof := peer.ok(t, map[string]string{"Op": "prove", "Name": "Bryan's iPhone"})
	emoji, ok := proof["emoji"].(string)
	if !ok || emoji == "" {
		t.Fatal("no transcript-bound emoji")
	}
	h.expect("two-sided confirmation", 0, emoji, "Bryan's iPhone wants to pair")
	pending := peer.ok(t, map[string]string{"Op": "confirm"})
	if pending["state"] != "confirming" || pending["device_id"] != nil {
		t.Fatal("phone approval alone granted access")
	}
	h.send("\r")
	h.expect("paired receipt", 0, "Paired Bryan's iPhone")
	select {
	case e := <-h.done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pair did not exit")
	}
	grant := peer.ok(t, map[string]string{"Op": "status"})
	again := peer.ok(t, map[string]string{"Op": "confirm"})
	if grant["device_id"] == nil || grant["device_id"] != again["device_id"] {
		t.Fatal("dropped final reply created a different grant")
	}
	out, e = storyboardCommand(t, f, binary, "devices")
	if e != nil {
		t.Fatal(e)
	}
	storyboardContains(t, out, "NAME", "PAIRED AT", "LAST SEEN", "Bryan's iPhone")
	if strings.Count(out, "Bryan's iPhone") != 1 {
		t.Fatal("duplicate phone registry entry")
	}
	out, e = storyboardCommand(t, f, binary, "devices", "revoke", "unpaired-phone")
	if e == nil {
		t.Fatal("revoked an unpaired phone")
	}
	storyboardContains(t, out, "device", "not paired with this wing")
	peer.ok(t, map[string]string{"Op": "connect"})
	inventory := peer.ok(t, map[string]string{"Op": "sessions"})
	rows, ok := inventory["result"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("wrong wing inventory: %v", inventory)
	}
	row := rows[0].(map[string]any)
	if row["id"] != "phone-session" || row["name"] != "pairing" || row["blocked"] != true {
		t.Fatalf("missing session attention: %v", row)
	}
	peer.ok(t, map[string]string{"Op": "attach", "SessionID": "phone-session"})
	read := peer.ok(t, map[string]string{"Op": "read", "SessionID": "phone-session"})
	if !strings.Contains(read["result"].(map[string]any)["screen"].(string), "PHONE_SESSION_UNCHANGED") {
		t.Fatalf("wrong attachment screen: %v", read)
	}
	peer.ok(t, map[string]string{"Op": "input", "SessionID": "phone-session", "Input": "Run the pairing tests first.\r"})
	deadline := time.Now().Add(3 * time.Second)
	for {
		r := peer.ok(t, map[string]string{"Op": "read", "SessionID": "phone-session"})
		if strings.Contains(r["result"].(map[string]any)["screen"].(string), "Run the pairing tests first.") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("input not delivered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	denied := peer.call(t, map[string]string{"Op": "config"})
	if denied["error"] != "denied" {
		t.Fatal("phone gained configuration authority", denied)
	}
	peer.ok(t, map[string]string{"Op": "detach"})
	if local.killed.Load() != 0 || remote.killed.Load() != 0 {
		t.Fatal("detach stopped a session")
	}
	stopped := northstarStandaloneEgg(t, f, f.local, "phone-stop", "stop-fixture", "working", "STOP_FIXTURE")
	peer.ok(t, map[string]string{"Op": "stop", "SessionID": "phone-stop"})
	if stopped.killed.Load() != 1 || local.killed.Load() != 0 || remote.killed.Load() != 0 {
		t.Fatal("phone stop affected the wrong session")
	}
	peer.ok(t, map[string]string{"Op": "attach", "SessionID": "phone-session"})
	out, e = storyboardCommand(t, f, binary, "devices", "revoke", "Bryan's iPhone")
	if e != nil {
		t.Fatal(e)
	}
	storyboardContains(t, out, "Revoked Bryan's iPhone", "Live access closed")
	revoked := peer.call(t, map[string]string{"Op": "read", "SessionID": "phone-session"})
	if revoked["error"] != "revoked" {
		t.Fatal("revocation retained live output", revoked)
	}
	revoked = peer.call(t, map[string]string{"Op": "connect"})
	if revoked["error"] != "revoked" {
		t.Fatal("revocation allowed reconnect", revoked)
	}
	out, e = storyboardCommand(t, f, binary, "devices")
	if e != nil || strings.Contains(out, "Bryan's iPhone") {
		t.Fatal("revocation registry still active", e)
	}
	after, e := os.ReadFile(filepath.Join(f.local, "wing.pid"))
	if e != nil || !bytes.Equal(pid, after) {
		t.Fatal("pairing replaced main daemon")
	}
	if local.killed.Load() != 0 || remote.killed.Load() != 0 {
		t.Fatal("pairing or revocation killed a session")
	}
	select {
	case <-remote.attached:
		t.Fatal("paired phone attached another wing")
	default:
	}
	// Terminal cancellation must invalidate this precise invitation without
	// interrupting the main wing or admitting a second device.
	cancelPTY := northstarStartPTY(t, f, binary, 160, 60, "pair", "--show-link")
	cancelPTY.expect("fresh invitation after revoke", 0, "5:00", "wingthing://pair#v1z.")
	fresh := phoneLinkInvitation(t, cancelPTY)
	if fresh.ID == q.ID || fresh.Secret == q.Secret {
		t.Fatal("fresh invitation reused consumed secret")
	}
	cancelPTY.send("\x03")
	select {
	case <-cancelPTY.done:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal cancellation did not exit")
	}
	peer.ok(t, map[string]any{"Op": "init", "Invitation": fresh, "Endpoint": fresh.Local})
	cancelled := peer.call(t, map[string]string{"Op": "start"})
	if cancelled["error"] != "cancelled" {
		t.Fatal("terminal exit left invitation usable", cancelled)
	}
	t.Log("05 cards 0–6, 11–12, 17: CLI invitation, two approvals, recovery, inventory, attach/input/detach and live revoke PASS; physical phone checks REQUIRED-UNRUN")
}
