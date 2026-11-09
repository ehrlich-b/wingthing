package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/fleet"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
	"github.com/ehrlich-b/wingthing/internal/testssh"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

// This fixture runs two built wings, the actual CLI/MCP socket contracts, and
// fixture SSH Unix forwarding. It calls no provider and changes no live state.
func TestBuiltMultiwingInventorySearchActionsAndOutage(t *testing.T) {
	repo, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	binary := localOnlyFixtureBinary(t, repo, testsocket.Dir(t))
	if _, e := os.Stat(binary); e != nil {
		t.Fatalf("fixture wt: %v", e)
	}
	peerBinary := os.Getenv("WT_TEST_PREVIEW_BINARY")
	if peerBinary == "" {
		peerBinary = filepath.Join(testsocket.Dir(t), "wt-preview")
		build := exec.CommandContext(t.Context(), "nice", "-n", "15", "go", "build", "-p", "2", "-buildvcs=false",
			"-ldflags", "-X github.com/ehrlich-b/wingthing/internal/config.ReleaseChannel=preview", "-o", peerBinary, "./cmd/wt")
		build.Dir = repo
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build fixture wt-preview: %v\n%s", err, output)
		}
	}
	if _, err := os.Stat(peerBinary); err != nil {
		t.Fatalf("fixture wt-preview: %v", err)
	}
	binaryHashes := map[string]string{}
	for _, executable := range []string{binary, peerBinary} {
		file, err := os.Open(executable)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		_, err = io.Copy(hash, file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		binaryHashes[executable] = fmt.Sprintf("%x", hash.Sum(nil))
	}
	h := testssh.New(t)
	home := filepath.Join(h.Root, "home")
	if e = os.Mkdir(home, 0700); e != nil {
		t.Fatal(e)
	}
	macState := testprovider.ShortStateDir(t, testprovider.TempDir(t))
	remoteState := testprovider.ShortStateDir(t, testprovider.TempDir(t))
	previewState := testprovider.ShortStateDir(t, testprovider.TempDir(t))
	path := h.Root + string(os.PathListSeparator) + os.Getenv("PATH")
	// The agent launch uses a fixture executable ahead of all installed provider
	// CLIs. It proves launch argv/runtime routing without any model request.
	if err := os.WriteFile(filepath.Join(h.Root, "codex"), []byte("#!/bin/sh\nprintf 'fixture agent terminal ready\\n'\nexec /bin/cat\n"), 0700); err != nil {
		t.Fatal(err)
	}
	environment := func(state string) []string {
		return []string{"HOME=" + home, "WINGTHING_DIR=" + state, "PATH=" + path, "SHELL=/bin/sh", "TERM=xterm-256color", "WT_FAKE_SSH_ROOT=" + h.Root}
	}
	// Let the preview executable initialize its own empty fixture state through
	// its normal guard before adding configuration. Never hand-write a marker.
	initialize := exec.Command(peerBinary, "session", "inventory", "--json")
	initialize.Env = environment(previewState)
	initialize.Dir = home
	if _, err := initialize.CombinedOutput(); err == nil {
		t.Fatal("empty preview fixture unexpectedly had a running wing")
	}
	if marker, err := os.ReadFile(filepath.Join(previewState, ".release-channel")); err != nil || strings.TrimSpace(string(marker)) != "preview" {
		t.Fatalf("preview fixture was not initialized through its executable: %v", err)
	}
	reloadDone := map[string]chan struct{}{}
	start := func(state, label, executable string) *exec.Cmd {
		t.Helper()
		if e = config.SaveWingConfig(state, &config.WingConfig{Label: label, PhoneFleet: true, AllowUnsandboxed: true, Roost: "http://127.0.0.1:1"}); e != nil {
			t.Fatal(e)
		}
		process := exec.Command(executable, "wing", "start", "--local-only", "--listen", "0.0.0.0", "--foreground")
		process.Env = environment(state)
		process.Dir = home
		logs, e := process.StderrPipe()
		if e != nil {
			t.Fatal(e)
		}
		if e = process.Start(); e != nil {
			t.Fatal(e)
		}
		ready := make(chan struct{})
		reloaded := make(chan struct{}, 1)
		reloadDone[state] = reloaded
		done := make(chan error, 1)
		go func() {
			scanner := bufio.NewScanner(logs)
			announced := false
			var log strings.Builder
			for scanner.Scan() {
				line := scanner.Text()
				log.WriteString(line + "\n")
				if !announced && strings.Contains(line, "wing: local control ready") {
					announced = true
					close(ready)
				}
				if strings.Contains(line, "tools reloaded:") {
					select {
					case reloaded <- struct{}{}:
					default:
					}
				}
			}
			err := process.Wait()
			if err != nil {
				err = fmt.Errorf("%w: %s", err, log.String())
			}
			done <- err
		}()
		t.Cleanup(func() {
			_ = process.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = process.Process.Kill()
				<-done
			}
		})
		select {
		case <-ready:
		case err := <-done:
			done <- err
			t.Fatalf("wing startup: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("wing readiness timeout")
		}
		return process
	}
	gatewayProcess := start(macState, "my-mac", binary)
	start(remoteState, "devbox", binary)
	macMeta, e := sshcontrol.InspectLocal(t.Context(), macState, "")
	if e != nil {
		t.Fatal(e)
	}
	remoteMeta, e := sshcontrol.InspectLocal(t.Context(), remoteState, "")
	if e != nil {
		t.Fatal(e)
	}
	h.Host(t, "remote-fixture", remoteMeta)
	if e = config.SaveRemotes(macState, map[string]config.Remote{"remote-fixture": {SSHTarget: "remote-fixture", WTBinary: binary, WingID: remoteMeta.WingID, WingthingDir: remoteMeta.WingthingDir, ControlSocket: remoteMeta.ControlSocket, ControlVersion: control.ContractVersion}}); e != nil {
		t.Fatal(e)
	}
	seed := func(state, id, name, agent, text string, live, coordinator bool) {
		dir := filepath.Join(state, "eggs", id)
		if e = os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
		owner, e := config.EnsureLocalOwner(state, "")
		if e != nil {
			t.Fatal(e)
		}
		if e = eggclient.WriteEggOwner(dir, owner.ID, ""); e != nil {
			t.Fatal(e)
		}
		if e = eggclient.WriteSessionName(dir, name); e != nil {
			t.Fatal(e)
		}
		providerHome := home
		if state == previewState {
			providerHome = config.PreviewProviderHome(state)
		}
		files := map[string]string{"egg.meta": "agent=" + agent + "\ncwd=/personal/repo\nprovider_home=" + providerHome + "\n", "lifecycle.jsonl": `{"sequence":1,"type":"message","source":"codex_transcript","role":"assistant","text":"` + text + `"}` + "\n"}
		if live {
			files["egg.pid"] = fmt.Sprint(os.Getpid())
		}
		if coordinator {
			files["lifecycle.jsonl"] += `{"sequence":2,"type":"session_ready","state":"working","source":"codex_hook","timestamp":"` + time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano) + `","reason":"native work started"}` + "\n"
			if e = sessionrole.Write(dir, sessionrole.Metadata{Role: "coordinator", Pinned: true}); e != nil {
				t.Fatal(e)
			}
		}
		for file, value := range files {
			if e = os.WriteFile(filepath.Join(dir, file), []byte(value), 0600); e != nil {
				t.Fatal(e)
			}
		}
	}
	seed(macState, "same-short-id", "local-run", "codex", "local task", true, false)
	seed(remoteState, "same-short-id", "wing", "codex", "coordinator needs an owner decision", true, true)
	seed(remoteState, "past-dns-run", "dns-helper-repair", "claude", "fixed the DNS helper on the remote machine", false, false)
	evidence := os.Getenv("WT_MULTI_WING_EVIDENCE")
	if evidence != "" {
		if e = os.MkdirAll(evidence, 0700); e != nil {
			t.Fatal(e)
		}
	}
	run := func(state, shot string, args ...string) ([]byte, error) {
		t.Helper()
		executable := binary
		if state == previewState {
			executable = peerBinary
		}
		cmd := exec.Command(executable, args...)
		cmd.Env = environment(state)
		cmd.Dir = home
		var output []byte
		var err error
		if shot != "" && evidence != "" {
			terminal, e := pty.StartWithSize(cmd, &pty.Winsize{Rows: 36, Cols: 120})
			if e != nil {
				t.Fatal(e)
			}
			output, _ = io.ReadAll(terminal)
			_ = terminal.Close()
			err = cmd.Wait()
			if e = os.WriteFile(filepath.Join(evidence, shot+".ansi"), output, 0600); e != nil {
				t.Fatal(e)
			}
			metadata, _ := json.Marshal(map[string]any{"command": append([]string{filepath.Base(executable)}, args...), "execution_binary": filepath.Base(executable), "binary_sha256": binaryHashes[executable], "surface": "cli", "rows": 36, "cols": 120, "fixture": true})
			_ = os.WriteFile(filepath.Join(evidence, shot+".json"), metadata, 0600)
		} else {
			output, err = cmd.CombinedOutput()
		}
		return output, err
	}
	must := func(state, shot string, args ...string) []byte {
		out, err := run(state, shot, args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return out
	}
	parse := func(data []byte) fleet.Inventory {
		var inv fleet.Inventory
		if e = json.Unmarshal(data, &inv); e != nil {
			t.Fatalf("inventory decode: %v\n%s", e, data)
		}
		return inv
	}
	oldEvidence := parse(must(remoteState, "", "session", "inventory", "--json"))
	for _, row := range oldEvidence.Sessions {
		if row.Role == "coordinator" && (row.Presence == nil || row.Presence.State != "unknown" || !row.Presence.Stale || row.Presence.ObservedAt == "") {
			t.Fatal("built inventory refreshed old native hook")
		}
	}
	must(remoteState, "native-stale", "session", "inventory")
	must(remoteState, "heartbeat", "session", "heartbeat", "same-short-id", "--state", "asleep", "--last-wake", time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), "--next-wake", time.Now().UTC().Add(time.Hour).Format(time.RFC3339), "--last-note", time.Now().UTC().Add(-30*time.Second).Format(time.RFC3339), "--json")
	must(macState, "open", "session", "inventory")
	combined := must(macState, "", "session", "inventory", "--remotes", "--json")
	direct := must(remoteState, "", "session", "inventory", "--json")
	inv := parse(combined)
	peer := parse(direct)
	merged := fleet.Merge([]fleet.Inventory{inv, peer})
	if len(merged.Sessions) != 3 || merged.Sessions[0].OriginWingID != remoteMeta.WingID || len(merged.Sessions[0].Routes) != 2 {
		t.Fatalf("built merge: %+v", merged)
	}
	must(macState, "fleet", "session", "inventory", "--remotes")
	must(macState, "search", "search", "DNS helper")
	searched := parse(must(macState, "", "search", "DNS helper", "--json"))
	if len(searched.Sessions) != 1 || searched.Sessions[0].SessionID != "past-dns-run" || searched.Sessions[0].Live {
		t.Fatalf("archive search: %+v", searched)
	}
	localSearch := parse(must(macState, "", "search", "DNS helper", "--local", "--json"))
	if !localSearch.Complete || len(localSearch.Sessions) != 0 {
		t.Fatalf("local search crossed origin: %+v", localSearch)
	}
	for _, origin := range []string{macState, remoteState} {
		collisions := parse(must(origin, "", "search", "codex", "--json"))
		if origin == macState && (len(collisions.Sessions) != 2 || collisions.Sessions[0].OriginWingID == collisions.Sessions[1].OriginWingID) {
			t.Fatalf("search conflated overlapping IDs: %+v", collisions)
		}
	}

	must(macState, "steer", "session", "action", "--wing", remoteMeta.WingID, "--session", "past-dns-run", "--operation", "session_read", "--arguments", `{"limit":1}`)
	must(macState, "rename", "session", "action", "--wing", remoteMeta.WingID, "--session", "same-short-id", "--operation", "terminal_rename", "--arguments", `{"name":"wing-renamed"}`)
	if eggclient.ReadSessionName(filepath.Join(macState, "eggs", "same-short-id")) != "local-run" || eggclient.ReadSessionName(filepath.Join(remoteState, "eggs", "same-short-id")) != "wing-renamed" {
		t.Fatal("built mutation crossed machines")
	}
	if _, err := run(macState, "ambiguous", "session", "action", "--session", "same-short-id", "--operation", "session_read"); err == nil {
		t.Fatal("built unqualified action accepted")
	}
	// A finite provider-free real egg validates stop delivery and process exit.
	// This fixture explicitly selects the outer host boundary, not a sandbox claim.
	if err := os.WriteFile(filepath.Join(previewState, "egg.yaml"), []byte("isolation: privileged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	start(previewState, "devbox", peerBinary)
	writerMeta, err := sshcontrol.InspectLocal(t.Context(), previewState, "")
	if err != nil {
		t.Fatal(err)
	}
	h.Host(t, "writer-fixture", writerMeta)
	originalRemotes, err := config.LoadRemotes(macState)
	if err != nil {
		t.Fatal(err)
	}
	expanded := map[string]config.Remote{}
	for name, remote := range originalRemotes {
		expanded[name] = remote
	}
	expanded["writer-fixture"] = config.Remote{SSHTarget: "writer-fixture", WTBinary: peerBinary, WingID: writerMeta.WingID, WingthingDir: writerMeta.WingthingDir, ControlSocket: writerMeta.ControlSocket, ControlVersion: writerMeta.Version}
	if err := config.SaveRemotes(macState, expanded); err != nil {
		t.Fatal(err)
	}
	startArgs, _ := json.Marshal(map[string]any{"command": []string{"/bin/cat"}, "cwd": home})
	startOutput := must(macState, "launch", "session", "launch", "--wing", writerMeta.WingID, "--operation", "terminal_start", "--arguments", string(startArgs), "--json")
	var started map[string]any
	if err := json.Unmarshal(startOutput, &started); err != nil {
		t.Fatal(err)
	}
	stopID, ok := started["session_id"].(string)
	if !ok {
		t.Fatal("finite egg launch returned no exact identity")
	}
	if started["origin_wing_id"] != writerMeta.WingID || started["route"].(map[string]any)["kind"] != "via" {
		t.Fatal("launch crossed origin")
	}
	t.Cleanup(func() { _ = eggclient.KillOrphanEggContext(t.Context(), &config.Config{Dir: previewState}, stopID) })
	agentArgs, _ := json.Marshal(map[string]any{"agent": "codex", "cwd": home, "label": "qualified-agent"})
	agentLaunch := must(macState, "agent-launch", "session", "launch", "--wing", writerMeta.WingID, "--operation", "agent_start", "--arguments", string(agentArgs), "--json")
	var agentReceipt map[string]any
	if err := json.Unmarshal(agentLaunch, &agentReceipt); err != nil {
		t.Fatal(err)
	}
	agentID, ok := agentReceipt["session_id"].(string)
	if !ok || agentReceipt["origin_wing_id"] != writerMeta.WingID {
		t.Fatal("qualified agent launch identity missing")
	}
	t.Cleanup(func() { _ = eggclient.KillOrphanEggContext(t.Context(), &config.Config{Dir: previewState}, agentID) })
	if meta := eggclient.ReadEggMetaValues(filepath.Join(previewState, "eggs", agentID)); meta["agent"] != "codex" {
		t.Fatal("qualified agent did not execute on the origin")
	}
	must(macState, "", "session", "action", "--wing", writerMeta.WingID, "--session", agentID, "--operation", "terminal_stop")
	// The same phone gateway bridge reaches a real PTY, without exposing egg
	// tokens or starting anything on a live machine.
	owner, err := config.EnsureLocalOwner(macState, "")
	if err != nil {
		t.Fatal(err)
	}
	wc, err := config.LoadWingConfig(macState)
	if err != nil {
		t.Fatal(err)
	}
	service := &wingsession.Service{Config: &config.Config{Dir: macState, WingID: macMeta.WingID}, Home: home, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wc} }}
	// Fixture forwarding uses PATH only inside this test process.
	t.Setenv("PATH", path)
	t.Setenv("WT_FAKE_SSH_ROOT", h.Root)
	bridge, err := localmcp.OwnerFleetAttach(t.Context(), service, owner.ID, fleet.Identity{OriginWingID: writerMeta.WingID, SessionID: stopID}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	if bridge.Route.Kind != "via" || bridge.Route.Remote != "writer-fixture" {
		t.Fatalf("PTY route: %+v", bridge.Route)
	}
	streamReady := time.AfterFunc(5*time.Second, func() { _ = bridge.Close() })
	if _, err := bridge.Recv(); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Send(&pb.SessionMsg{SessionId: stopID, Payload: &pb.SessionMsg_Input{Input: []byte("qualified live PTY through the gateway\r")}}); err != nil {
		t.Fatal(err)
	}
	var liveOutput strings.Builder
	for !strings.Contains(liveOutput.String(), "qualified live PTY through the gateway") {
		msg, err := bridge.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if data, ok := msg.Payload.(*pb.SessionMsg_Output); ok {
			liveOutput.Write(data.Output)
		}
	}
	streamReady.Stop()
	_ = bridge.Close()
	must(macState, "attach", "session", "action", "--wing", writerMeta.WingID, "--session", stopID, "--operation", "terminal_read")
	// The real built gateway receives encrypted, paired phone requests. Change
	// only its disposable wing.yaml and SIGHUP its own child process; no fixture
	// mutates the in-memory runtime flag on behalf of the reload handler.
	phone := pairBuiltFleetPhone(t, macState)
	phoneLaunch := phone.command("launch", map[string]any{"origin_wing_id": writerMeta.WingID, "operation": "terminal_start", "arguments": map[string]any{"command": []string{"/bin/cat"}, "cwd": home, "label": "phone-launched"}})
	if phoneLaunch["error"] != nil {
		t.Fatalf("built phone launch: %v", phoneLaunch["error"])
	}
	phoneReceipt, ok := phoneLaunch["result"].(map[string]any)
	if !ok || phoneReceipt["origin_wing_id"] != writerMeta.WingID {
		t.Fatal("built phone launch origin mismatch")
	}
	phoneID, ok := phoneReceipt["session_id"].(string)
	if !ok {
		t.Fatal("built phone launch lacks session identity")
	}
	t.Cleanup(func() { _ = eggclient.KillOrphanEggContext(t.Context(), &config.Config{Dir: previewState}, phoneID) })
	attachmentArgs := map[string]any{"origin_wing_id": writerMeta.WingID, "session_id": phoneID, "takeover": true}
	if result := phone.command("fleet_attach", attachmentArgs); result["error"] != nil {
		t.Fatalf("built phone attach: %v", result["error"])
	}
	if result := phone.command("fleet_input", map[string]any{"origin_wing_id": writerMeta.WingID, "session_id": phoneID, "input": "paired phone typed into the exact remote PTY\r"}); result["error"] != nil {
		t.Fatal("paired phone input failed")
	}
	readUntil := time.Now().Add(3 * time.Second)
	for {
		result := phone.command("fleet_read", map[string]any{"origin_wing_id": writerMeta.WingID, "session_id": phoneID})
		if result["error"] != nil {
			t.Fatal("paired phone read failed")
		}
		content, ok := result["result"].(map[string]any)
		if !ok {
			t.Fatal("paired phone result is not structured")
		}
		if screen, ok := content["screen"].(string); ok && strings.Contains(screen, "paired phone typed into the exact remote PTY") {
			break
		}
		if time.Now().After(readUntil) {
			t.Fatal("paired phone input did not reach the kernel PTY")
		}
		time.Sleep(20 * time.Millisecond)
	}
	must(macState, "phone-attach", "session", "action", "--wing", writerMeta.WingID, "--session", phoneID, "--operation", "terminal_read")
	_, observedEgg, err := eggclient.OpenLocalEgg(t.Context(), &config.Config{Dir: previewState}, phoneID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observedEgg.Close() })
	if state, err := observedEgg.Status(t.Context()); err != nil || state.WriterId == "" {
		t.Fatalf("phone writer was not claimed: %v", err)
	}
	reload := func(enabled bool) {
		t.Helper()
		updated, err := config.LoadWingConfig(macState)
		if err != nil {
			t.Fatal(err)
		}
		updated.PhoneFleet = enabled
		if err := config.SaveWingConfig(macState, updated); err != nil {
			t.Fatal(err)
		}
		if err := gatewayProcess.Process.Signal(syscall.SIGHUP); err != nil {
			t.Fatal(err)
		}
		select {
		case <-reloadDone[macState]:
		case <-time.After(5 * time.Second):
			t.Fatal("built gateway reload timeout")
		}
	}
	reload(false)
	// No phone command prompts cleanup. A quiet writer must be released by the
	// policy observer after the production reload commits its revocation.
	until := time.Now().Add(4 * time.Second)
	for {
		state, err := observedEgg.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if state.WriterId == "" {
			break
		}
		if time.Now().After(until) {
			t.Fatal("reloaded revocation retained a quiet phone writer")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if result := phone.command("inventory", map[string]any{"include_remotes": true}); result["error"] == nil {
		t.Fatal("saved/reloaded revocation retained inventory")
	}
	if result := phone.command("action", map[string]any{"origin_wing_id": writerMeta.WingID, "session_id": phoneID, "operation": "terminal_read"}); result["error"] == nil {
		t.Fatal("saved/reloaded revocation retained action")
	}
	if result := phone.command("launch", map[string]any{"origin_wing_id": writerMeta.WingID, "operation": "terminal_start", "arguments": map[string]any{"command": []string{"/bin/cat"}, "cwd": home}}); result["error"] == nil {
		t.Fatal("saved/reloaded revocation retained launch")
	}
	if result := phone.command("fleet_read", map[string]any{"origin_wing_id": writerMeta.WingID, "session_id": phoneID}); result["error"] == nil {
		t.Fatal("saved/reloaded revocation retained attachment content")
	}
	if result := phone.command("fleet_attach", attachmentArgs); result["error"] == nil {
		t.Fatal("saved/reloaded revocation admitted new attach")
	}
	reload(true)
	if result := phone.command("fleet_attach", attachmentArgs); result["error"] != nil {
		t.Fatalf("reloaded enable did not restore explicit attach: %v", result["error"])
	}
	if result := phone.command("detach", map[string]any{}); result["error"] != nil {
		t.Fatal("built phone detach failed")
	}
	if result := phone.command("inventory", map[string]any{"include_remotes": true}); result["error"] != nil {
		t.Fatal("reloaded enable did not restore inventory")
	}
	// Device revocation cuts off an otherwise enabled gateway and releases an
	// active remote writer without requiring another phone command.
	if result := phone.command("fleet_attach", attachmentArgs); result["error"] != nil {
		t.Fatal("reattach before device revocation failed")
	}
	must(macState, "phone-revoked", "devices", "revoke", "fixture-phone")
	until = time.Now().Add(3 * time.Second)
	for {
		state, err := observedEgg.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if state.WriterId == "" {
			break
		}
		if time.Now().After(until) {
			t.Fatal("device revocation retained quiet remote writer")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, op := range []string{"inventory", "search", "launch", "action", "fleet_attach", "fleet_read", "fleet_input"} {
		if result := phone.command(op, attachmentArgs); result["error"] != "revoked" {
			t.Fatalf("revoked phone used %s", op)
		}
	}
	must(macState, "phone-revocation-independent", "session", "action", "--wing", writerMeta.WingID, "--session", phoneID, "--operation", "terminal_read")
	must(macState, "phone-launch", "session", "action", "--wing", writerMeta.WingID, "--session", phoneID, "--operation", "terminal_send", "--arguments", `{"input":"phone launched this exact remote kernel PTY","enter":true}`)
	must(macState, "phone-launch-read", "session", "action", "--wing", writerMeta.WingID, "--session", phoneID, "--operation", "terminal_read")
	must(macState, "", "session", "action", "--wing", writerMeta.WingID, "--session", phoneID, "--operation", "terminal_stop")
	must(macState, "stop", "session", "action", "--wing", writerMeta.WingID, "--session", stopID, "--operation", "terminal_stop")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, alive := eggclient.ReadAliveEggPID(filepath.Join(previewState, "eggs", stopID)); !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stopped egg process survived")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := config.SaveRemotes(macState, originalRemotes); err != nil {
		t.Fatal(err)
	}
	// The phone's direct endpoint is down while this SSH gateway remains up.
	// This is pre-dispatch reachability state, not a dropped mutation replay.
	for i := range peer.Sessions {
		for j := range peer.Sessions[i].Routes {
			peer.Sessions[i].Routes[j].Healthy = false
		}
	}
	peer.Complete = false
	for i := range peer.Sources {
		peer.Sources[i].Healthy = false
		peer.Sources[i].Error = "fixture direct endpoint unreachable"
	}
	merged = fleet.Merge([]fleet.Inventory{inv, peer})
	route, err := fleet.SelectRoute(merged.Sessions[0], time.Now())
	if err != nil || route.Kind != "via" || route.WingID != macMeta.WingID {
		t.Fatalf("built direct-down: %+v %v", route, err)
	}
	if evidence != "" {
		a, b := filepath.Join(evidence, "gateway-inventory.json"), filepath.Join(evidence, "direct-down-inventory.json")
		_ = os.WriteFile(a, combined, 0600)
		data, _ := json.Marshal(peer)
		_ = os.WriteFile(b, data, 0600)
		must(macState, "fallback", "session", "merge", a, b)
		unknown := peer
		unknown.Sessions = append([]fleet.Session(nil), peer.Sessions...)
		for i := range unknown.Sessions {
			unknown.Sessions[i].OriginWingID = ""
		}
		file := filepath.Join(evidence, "unknown-origin.json")
		data, _ = json.Marshal(unknown)
		_ = os.WriteFile(file, data, 0600)
		must(macState, "duplicates", "session", "merge", a, file)
	}
	h.Offline(t, "remote-fixture", true)
	// Drop this fixture's known forward so the next inventory observes the outage.
	select {
	case forward := <-h.Started:
		forward.Drop()
	case <-time.After(2 * time.Second):
		t.Fatal("fixture forwarding not observed")
	}
	offline := parse(must(macState, "", "session", "inventory", "--remotes", "--json"))
	if offline.Complete || len(offline.Sessions) != 1 {
		t.Fatalf("offline coverage: %+v", offline)
	}
	must(macState, "remote-unreachable", "session", "inventory", "--remotes")
	must(macState, "local-continues", "session", "inventory")
	if e = os.Remove(filepath.Join(remoteState, "eggs", "same-short-id", "egg.pid")); e != nil {
		t.Fatal(e)
	}
	must(remoteState, "coordinator-dead", "session", "inventory")
	must(remoteState, "coordinator-evidence", "session", "action", "--wing", remoteMeta.WingID, "--session", "same-short-id", "--operation", "session_read", "--arguments", `{"limit":1}`)
	if e = os.WriteFile(filepath.Join(macState, "clients.yaml"), []byte("require_client: true\nclients: {}\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := run(macState, "revoked", "session", "inventory"); e == nil {
		t.Fatal("revoked implicit client retained access")
	}
	missingState := testprovider.ShortStateDir(t, testprovider.TempDir(t))
	if _, e := run(missingState, "offline", "session", "inventory"); e == nil {
		t.Fatal("unstarted wing accepted")
	}
}
