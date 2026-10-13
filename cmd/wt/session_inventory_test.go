package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/fleet"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
	"github.com/ehrlich-b/wingthing/internal/testssh"
)

// This fixture runs two built wings, the actual CLI/MCP socket contracts, and
// fixture SSH Unix forwarding. It calls no provider and changes no live state.
func TestBuiltMultiwingInventorySearchActionsAndOutage(t *testing.T) {
	repo, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	binary := os.Getenv("WT_TEST_BINARY")
	if binary == "" {
		binary = filepath.Join(repo, "wt")
	}
	if _, e := os.Stat(binary); e != nil {
		t.Fatal("build with make ops ACTION=build before this fixture")
	}
	h := testssh.New(t)
	home := filepath.Join(h.Root, "home")
	if e = os.Mkdir(home, 0700); e != nil {
		t.Fatal(e)
	}
	macState := testprovider.ShortStateDir(t, filepath.Join(repo, ".scratch"))
	remoteState := testprovider.ShortStateDir(t, filepath.Join(repo, ".scratch"))
	path := h.Root + string(os.PathListSeparator) + os.Getenv("PATH")
	environment := func(state string) []string {
		return []string{"HOME=" + home, "WINGTHING_DIR=" + state, "PATH=" + path, "SHELL=/bin/sh", "TERM=xterm-256color", "WT_FAKE_SSH_ROOT=" + h.Root}
	}
	start := func(state, label string) {
		t.Helper()
		if e = config.SaveWingConfig(state, &config.WingConfig{Label: label, PhoneFleet: true, AllowUnsandboxed: true, Roost: "http://127.0.0.1:1"}); e != nil {
			t.Fatal(e)
		}
		process := exec.Command(binary, "wing", "start", "--local-only", "--foreground")
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
	}
	start(macState, "EhrlichMac")
	start(remoteState, "forge")
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
		files := map[string]string{"egg.meta": "agent=" + agent + "\ncwd=/personal/repo\nprovider_home=" + home + "\n", "lifecycle.jsonl": `{"sequence":1,"type":"message","source":"codex_transcript","role":"assistant","text":"` + text + `"}` + "\n"}
		if live {
			files["egg.pid"] = fmt.Sprint(os.Getpid())
		}
		if coordinator {
			files["lifecycle.jsonl"] += `{"sequence":2,"type":"session_ready","state":"needs_input","source":"codex_hook","reason":"input needed"}` + "\n"
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
		cmd := exec.Command(binary, args...)
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
			metadata, _ := json.Marshal(map[string]any{"command": append([]string{"wt"}, args...), "surface": "cli", "rows": 36, "cols": 120, "fixture": true})
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
	must(remoteState, "heartbeat", "session", "heartbeat", "same-short-id", "--state", "asleep", "--last-wake", time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), "--next-wake", time.Now().UTC().Add(time.Hour).Format(time.RFC3339), "--json")
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
	must(macState, "search", "session", "inventory", "--remotes", "--query", "DNS helper")
	searched := parse(must(macState, "", "session", "inventory", "--remotes", "--query", "DNS helper", "--json"))
	if len(searched.Sessions) != 1 || searched.Sessions[0].SessionID != "past-dns-run" || searched.Sessions[0].Live {
		t.Fatalf("archive search: %+v", searched)
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
	controlClient, err := controlsocket.Dial(t.Context(), remoteState, controlsocket.Hello{Unsandboxed: true})
	if err != nil {
		t.Fatal(err)
	}
	startArgs, _ := json.Marshal(map[string]any{"command": []string{"/bin/cat"}, "cwd": home})
	started, denied, err := controlClient.Call(t.Context(), "terminal_start", startArgs)
	_ = controlClient.Close()
	if err != nil || denied {
		t.Fatalf("finite egg start: %+v %v", started, err)
	}
	stopID, ok := started["session"].(string)
	if !ok {
		t.Fatalf("finite egg identity: %+v", started)
	}
	t.Cleanup(func() { _ = eggclient.KillOrphanEggContext(t.Context(), &config.Config{Dir: remoteState}, stopID) })
	must(macState, "stop", "session", "action", "--wing", remoteMeta.WingID, "--session", stopID, "--operation", "terminal_stop")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, alive := eggclient.ReadAliveEggPID(filepath.Join(remoteState, "eggs", stopID)); !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stopped egg process survived")
		}
		time.Sleep(10 * time.Millisecond)
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
	if e = os.WriteFile(filepath.Join(macState, "clients.yaml"), []byte("require_client: true\nclients: {}\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := run(macState, "revoked", "session", "inventory"); e == nil {
		t.Fatal("revoked implicit client retained access")
	}
	missingState := testprovider.ShortStateDir(t, filepath.Join(repo, ".scratch"))
	if _, e := run(missingState, "offline", "session", "inventory"); e == nil {
		t.Fatal("unstarted wing accepted")
	}
}
