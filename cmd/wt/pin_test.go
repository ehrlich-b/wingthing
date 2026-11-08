package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
)

func TestPinCLIAndWingToolPersistWithoutChangingRole(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	t.Setenv("WINGTHING_DIR", cfg.Dir)
	t.Setenv("WT_MCP_CLIENT", "")
	seedRemoteListSession(t, cfg, "ours", "")
	dir := filepath.Join(cfg.Dir, "eggs", "ours")
	if err := sessionrole.Write(dir, sessionrole.Metadata{Role: "coordinator", Pinned: true}); err != nil {
		t.Fatal(err)
	}
	call := testWingTool(t, cfg, "owner")
	for _, pinned := range []bool{false, true} {
		wire, _ := json.Marshal(map[string]any{"session": "ours", "pinned": pinned})
		if _, err := call(t.Context(), "session_pin", wire); err != nil {
			t.Fatal(err)
		}
		// A fresh inventory uses persisted files, not the wing connection.
		rows, err := eggclient.DiscoverSessionRefs(cfg)
		if err != nil || len(rows) != 1 || rows[0].Pinned != pinned || rows[0].Role != "coordinator" {
			t.Fatalf("reopen=%v %v", rows, err)
		}
	}
	var output bytes.Buffer
	if err := executeCLI(t.Context(), []string{"unpin", "ours", "--json"}, remotepkg.IO{Out: &output, ErrOut: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"pinned":false`) {
		t.Fatal(output.String())
	}
	metadata, err := sessionrole.Read(dir)
	if err != nil || metadata.Pinned || metadata.Role != "coordinator" {
		t.Fatalf("unpin=%v %v", metadata, err)
	}
}

func TestRoleStandaloneResumeRefusesBeforeHistoryRestore(t *testing.T) {
	scratch := testprovider.TempDir(t)
	cfg := &config.Config{Dir: testprovider.ShortStateDir(t, scratch)}
	home, work := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", cfg.Dir)
	dir := filepath.Join(cfg.Dir, "eggs", "source")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := sessionrole.Write(dir, sessionrole.Metadata{Role: "coordinator", Pinned: true}); err != nil {
		t.Fatal(err)
	}
	err := eggSpawn(t.Context(), "claude", "", false, "source", "", work, true, true, false, false, nil)
	if err == nil || !strings.Contains(err.Error(), "standalone --resume cannot preserve") {
		t.Fatalf("role resumed without protected MCP: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "projects")); !os.IsNotExist(err) {
		t.Fatal("role refusal restored provider history")
	}
	if err := sessionrole.Write(dir, sessionrole.Metadata{}); err != nil {
		t.Fatal(err)
	}
	err = eggSpawn(t.Context(), "claude", "", false, "source", "", work, true, true, false, false, nil)
	if err == nil || !strings.Contains(err.Error(), "restore session:") {
		t.Fatalf("ordinary resume did not reach its existing restore path: %v", err)
	}
}

func TestPinnedCoordinatorFleetSelectionDoesNotGuess(t *testing.T) {
	ordinary := eggclient.MachineSession{Machine: "local", LocalSession: eggclient.LocalSession{ID: "egg", Name: "coordinator", PID: 42}}
	pin := eggclient.MachineSession{Machine: "remote", LocalSession: eggclient.LocalSession{ID: "parent", Name: "renamed", PID: 42, Metadata: sessionrole.Metadata{Pinned: true, Role: "coordinator"}}}
	for _, rows := range [][]eggclient.MachineSession{{ordinary}, {ordinary, {Machine: "offline", Error: "unreachable"}}} {
		if _, found, err := pinnedCoordinator(rows); found || err != nil {
			t.Fatalf("ordinary selected: %v", err)
		}
	}
	selected, found, err := pinnedCoordinator([]eggclient.MachineSession{ordinary, pin, {Machine: "offline", Error: "unreachable"}})
	if err != nil || !found || selected.Machine != "remote" || selected.ID != "parent" {
		t.Fatalf("remote=%v %v %v", selected, found, err)
	}
	duplicate := pin
	duplicate.Machine = "another"
	if _, _, err := pinnedCoordinator([]eggclient.MachineSession{pin, duplicate}); err == nil || !strings.Contains(err.Error(), "remote:parent") || !strings.Contains(err.Error(), "another:parent") {
		t.Fatalf("ambiguity=%v", err)
	}
}

func TestSessionPSPinnedRemoteFirstWithUnreachablePeer(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	t.Setenv("WINGTHING_DIR", cfg.Dir)
	for i := 0; i < 36; i++ {
		seedRemoteListSession(t, cfg, fmt.Sprintf("egg-%02d", i), "")
	}
	if err := config.SaveRemotes(cfg.Dir, map[string]config.Remote{"remote": {SSHTarget: "online"}, "offline": {SSHTarget: "offline"}}); err != nil {
		t.Fatal(err)
	}
	script := `case "$2" in offline) exit 255 ;; esac
printf '%s\n' '{"version":"fixture","contract_version":"v1","sessions":[{"id":"parent","name":"coordinator","kind":"agent","agent":"claude","status":"idle","pid":42,"pinned":true,"role":"coordinator"}]}'
`
	var out bytes.Buffer
	err := executeCLI(context.Background(), []string{"session", "ps", "--json"}, remotepkg.IO{Out: &out, ErrOut: io.Discard, SSHPath: writeFakeRemoteSSH(t, script)})
	if err != nil {
		t.Fatal(err)
	}
	var rows []eggclient.MachineSession
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 38 || rows[0].ID != "parent" || rows[0].Machine != "remote" || rows[0].Role != "coordinator" {
		t.Fatalf("rows=%v", rows)
	}
	if rows[len(rows)-1].Error == "" || rows[len(rows)-1].Machine != "offline" {
		t.Fatalf("offline absent: %v", rows[len(rows)-1])
	}
}

func TestPinStandaloneSessionAndMissingRoleRemainOrdinary(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	t.Setenv("WINGTHING_DIR", cfg.Dir)
	t.Setenv("WT_MCP_CLIENT", "")
	seedRemoteListSession(t, cfg, "plain", "")
	for _, verb := range []string{"pin", "unpin"} {
		cmd := pinCmd(verb == "pin")
		cmd.SetArgs([]string{"plain", "--json"})
		cmd.SetOut(io.Discard)
		if err := cmd.ExecuteContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		m, err := sessionrole.Read(filepath.Join(cfg.Dir, "eggs", "plain"))
		if err != nil || m.Role != "" || m.Pinned != (verb == "pin") {
			t.Fatalf("%s: %v %v", verb, m, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.Dir, "roles")); !os.IsNotExist(err) {
		t.Fatal("created role directory for non-role session")
	}
}

func TestAttachCoordinatorDispatchesQualifiedRemoteID(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	t.Setenv("WINGTHING_DIR", cfg.Dir)
	t.Setenv("WT_MCP_CLIENT", "")
	if err := config.SaveRemotes(cfg.Dir, map[string]config.Remote{"work": {SSHTarget: "fixture-peer"}}); err != nil {
		t.Fatal(err)
	}
	script := `case "$3" in
*"'session' 'ps' '--json' '--remote-inventory'"*) printf '%s\n' '{"version":"fixture","contract_version":"v1","sessions":[{"id":"remote-parent","name":"renamed","pid":42,"pinned":true,"role":"coordinator"}]}' ;;
*"'attach' '--read-only' '--' 'remote-parent'"*) printf 'attached remote-parent\n' ;;
*) printf 'wrong route\n' >&2; exit 91 ;;
esac
`
	var out bytes.Buffer
	if err := executeCLI(t.Context(), []string{"attach", "coordinator", "--read-only"}, remotepkg.IO{In: strings.NewReader(""), Out: &out, ErrOut: io.Discard, SSHPath: writeFakeRemoteSSH(t, script)}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "attached remote-parent\n" {
		t.Fatal(out.String())
	}
}

func TestPinConfiguredRemoteRoutesWithoutInventoryFanout(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	t.Setenv("WINGTHING_DIR", cfg.Dir)
	if err := config.SaveRemotes(cfg.Dir, map[string]config.Remote{"work": {SSHTarget: "fixture-peer"}}); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"pin", "unpin"} {
		script := `case "$3" in *"'` + verb + `' '--json' '--' 'remote-parent'"*) printf '%s\n' '{"session":"remote-parent","pinned":` + fmt.Sprint(verb == "pin") + `}' ;; *) exit 91 ;; esac`
		var out bytes.Buffer
		if err := executeCLI(t.Context(), []string{verb, "work:remote-parent", "--json"}, remotepkg.IO{Out: &out, ErrOut: io.Discard, SSHPath: writeFakeRemoteSSH(t, script)}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), `"session":"remote-parent"`) {
			t.Fatal(out.String())
		}
	}
}
