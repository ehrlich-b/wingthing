//go:build cgo

package main

import (
	"bufio"
	"context"
	"debug/buildinfo"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/pake"
	"github.com/ehrlich-b/wingthing/internal/phonepair"
)

// TestPhonePeerPairsThenAttaches deliberately builds the server without CGO,
// while this cgo-tagged test peer uses native BoringSSL (internal/pake/pake.go).
// The peer mirrors mobile/ios/WingthingCore/Sources/WingthingCore/PhonePairing.swift
// lines 114-159 (SPAKE2 identities, base64 messages, transcript/HKDF and AES-GCM)
// and 215-241 (phone.box carrier and POST /phone). Pairing/device payloads mirror
// PhoneDevices.swift lines 90-130, 158-172 and 181-197, including session_id and
// raw string input; fleet commands use a different input envelope.
func TestPhonePeerPairsThenAttaches(t *testing.T) {
	root, err := os.MkdirTemp(os.TempDir(), "pp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	state, home := filepath.Join(root, "s"), filepath.Join(root, "h")
	for _, dir := range []string{state, home} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(root, "wt")
	build := exec.CommandContext(t.Context(), "go", "build", "-p", "2", "-o", binary, "./cmd/wt")
	build.Dir = "../.."
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("CGO-free wt build: %v\n%s", err, out)
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	cgoOff := false
	for _, setting := range info.Settings {
		if setting.Key == "CGO_ENABLED" && setting.Value == "0" {
			cgoOff = true
		}
	}
	if !cgoOff {
		t.Fatal("server executable was not built with CGO_ENABLED=0")
	}
	t.Log("built CGO_ENABLED=0 wt; peer uses native BoringSSL SPAKE2")
	if err := config.SaveWingConfig(state, &config.WingConfig{AllowUnsandboxed: true, Roost: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "egg.yaml"), []byte("isolation: privileged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	audit := filepath.Join(root, "input.txt")
	if err := os.WriteFile(audit, nil, 0600); err != nil {
		t.Fatal(err)
	}
	// A provider response prefix and independent input audit distinguish actual
	// provider execution from the PTY's input echo or a fabricated phone result.
	provider := fmt.Sprintf(`#!/usr/bin/python3
import os, sys, tty
if sys.argv[1:]==['--version']:
 print('fake-codex 1.0'); sys.exit(0)
tty.setraw(0)
os.write(1,b'phone-provider-ready\r\n')
buffer=b''
while True:
 chunk=os.read(0,1)
 if not chunk: break
 if chunk!=b'\r': buffer+=chunk; continue
 text=buffer.decode().removeprefix('\x1b[200~').removesuffix('\x1b[201~'); buffer=b''
 with open(%q,'a') as f: f.write(text+'\n')
 os.write(1,('phone-provider:'+text+'\r\n').encode())
`, audit)
	if err := os.WriteFile(filepath.Join(root, "codex"), []byte(provider), 0700); err != nil {
		t.Fatal(err)
	}
	wing := exec.Command(binary, "wing", "start", "--local-only", "--listen", "127.0.0.1", "--foreground")
	wing.Dir = home
	wing.Env = []string{"HOME=" + home, "WINGTHING_DIR=" + state, "PATH=" + root + string(os.PathListSeparator) + os.Getenv("PATH"), "SHELL=/bin/sh", "TERM=xterm-256color", "TMPDIR=" + os.TempDir()}
	stderr, err := wing.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	ready, done := make(chan struct{}), make(chan error, 1)
	if err := wing.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		scanner := bufio.NewScanner(stderr)
		announced := false
		var logs strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			logs.WriteString(line + "\n")
			if !announced && strings.Contains(line, "wing: local control ready") {
				close(ready)
				announced = true
			}
		}
		err := wing.Wait()
		done <- fmt.Errorf("wing exit: %v\n%s", err, logs.String())
	}()
	t.Cleanup(func() {
		_ = wing.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = wing.Process.Kill()
			<-done
		}
	})
	select {
	case <-ready:
	case err := <-done:
		done <- err
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("local wing startup deadline")
	}
	launch, _ := json.Marshal(map[string]any{"agent": "codex", "cwd": home, "label": "phone-pair-fixture"})
	started, err := localmcp.CallLocalWingTool(t.Context(), state, "", "agent_start", launch)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := started["session"].(string)
	if id == "" {
		t.Fatalf("fake-provider session was not started: %v", started)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = eggclient.KillOrphanEggContext(ctx, &config.Config{Dir: state}, id)
	})

	wrong, control, key := newBuiltFleetPhone(t, state)
	var q phonepair.Invitation
	wrong.post("/admin", map[string]string{"Op": "create"}, &q, control.Token)
	// Keep the invitation identity intact, changing only the six-word code.
	words := strings.Fields(q.Secret)
	if words[0] == "wrong" {
		words[0] = "incorrect"
	} else {
		words[0] = "wrong"
	}
	exchange, first, err := pake.New(true, strings.Join(words, " "), phonepair.Identity(q))
	if err != nil {
		t.Fatal(err)
	}
	defer exchange.Close()
	alice := base64.StdEncoding.EncodeToString(first)
	response := wrong.wire(phonepair.Request{Op: "pair.start", ID: q.ID, Message: alice})
	if response.Error != "" {
		t.Fatalf("wrong-code exchange start: %s", response.Error)
	}
	bob, err := base64.StdEncoding.DecodeString(response.Message)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := exchange.Finish(bob)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := phonepair.PairKey(q, shared, alice, response.Message)
	if err != nil {
		t.Fatal(err)
	}
	refused := wrong.encrypted("pair.prove", map[string]any{"op": "prove", "id": q.ID, "name": "wrong-phone"}, pair)
	if refused["error"] != "bad_proof" {
		t.Fatalf("wrong code was not refused: %v", refused["error"])
	}
	wrong.session, err = auth.DeriveSharedKey(key, control.Key, "wt-phone-v1")
	if err != nil {
		t.Fatal(err)
	}
	hello := wrong.wire(phonepair.Request{Op: "device.hello", SenderPub: wrong.pub})
	raw, err := auth.Decrypt(wrong.session, hello.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var challenge map[string]any
	if err := json.Unmarshal(raw, &challenge); err != nil {
		t.Fatal(err)
	}
	wrong.id = stringField(challenge, "connection_id")
	for _, op := range []string{"attach", "input"} {
		wrong.sequence++
		denied := wrong.encrypted("device.command", map[string]any{"connection_id": wrong.id, "sequence": wrong.sequence, "op": op, "session_id": id, "input": "WRONG-CODE-MUST-NOT-ARRIVE\r"}, wrong.session)
		if denied["error"] != "revoked" {
			t.Fatalf("unpaired %s was not denied: %v", op, denied["error"])
		}
	}
	t.Log("wrong pairing code refused; unauthenticated attach and input denied")

	phone := pairBuiltFleetPhone(t, state)
	listed := phone.swiftCommand("sessions", "", "")
	sessions, ok := listed.([]any)
	if !ok {
		t.Fatal("phone sessions did not return a list")
	}
	found := false
	for _, value := range sessions {
		row, ok := value.(map[string]any)
		if ok && row["id"] == id && row["agent"] == "codex" {
			found = true
		}
	}
	if !found {
		t.Fatal("phone list omitted the fake-provider session")
	}
	readUntil := func(marker string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		var output strings.Builder
		for time.Now().Before(deadline) {
			read, ok := phone.swiftCommand("read", id, "").(map[string]any)
			if !ok || read["ended"] != false {
				t.Fatal("phone terminal ended or returned an invalid read")
			}
			output.WriteString(stringField(read, "output"))
			if strings.Contains(output.String(), marker) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("phone did not read provider terminal marker %q", marker)
	}
	mustOK := func(op, input string) {
		t.Helper()
		result, ok := phone.swiftCommand(op, id, input).(map[string]any)
		if !ok || result["ok"] != true {
			t.Fatalf("phone %s did not succeed", op)
		}
	}
	assertRunning := func() {
		t.Helper()
		session, err := eggclient.ResolveLifecycleSession(&config.Config{Dir: state}, id)
		if err != nil || session.PID <= 0 {
			t.Fatalf("session not live after detach: %v", err)
		}
		if err := syscall.Kill(session.PID, 0); err != nil {
			t.Fatalf("session process did not survive detach: %v", err)
		}
	}
	mustOK("attach", "")
	readUntil("phone-provider-ready")
	if data, err := os.ReadFile(audit); err != nil || len(data) != 0 {
		t.Fatalf("input reached provider before valid pairing: %v, %q", err, data)
	}
	for i, marker := range []string{"PAIR-ATTACH-FIRST", "PAIR-ATTACH-SECOND"} {
		if i > 0 {
			mustOK("attach", "")
		} // Same authenticated peer, no pair.* request.
		mustOK("input", marker+"\r")
		readUntil("phone-provider:" + marker)
		mustOK("detach", "")
		assertRunning()
		t.Logf("attach %d: provider marker read through phone terminal; detach preserved live session", i+1)
	}
	data, err := os.ReadFile(audit)
	if err != nil || string(data) != "PAIR-ATTACH-FIRST\nPAIR-ATTACH-SECOND\n" {
		t.Fatalf("provider input audit: %v, %q", err, data)
	}
	t.Log("provider audit contains exactly both authorized markers; second attach reused paired peer")
}

// swiftCommand preserves PhoneDevices.swift:188-197's raw input and verifies
// the authenticated connection and monotonically increasing response sequence.
func (p *builtFleetPhone) swiftCommand(op, id, input string) any {
	p.t.Helper()
	p.sequence++
	reply := p.encrypted("device.command", map[string]any{"connection_id": p.id, "sequence": p.sequence, "op": op, "session_id": id, "input": input}, p.session)
	if reply["error"] != nil || reply["connection_id"] != p.id || reply["sequence"] != float64(p.sequence) {
		p.t.Fatalf("phone %s failed or mismatched response: %v", op, reply["error"])
	}
	return reply["result"]
}
