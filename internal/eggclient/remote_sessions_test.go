package eggclient

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
	"github.com/ehrlich-b/wingthing/internal/testsocket"

	"io"

	"testing"
	"time"
)

func TestRemoteSessionBufferBoundsLargeWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buffer := remoteSessionBuffer{limit: 64 << 10, stream: "stderr", cancel: cancel}
	input := bytes.Repeat([]byte("x"), buffer.limit+1)
	// io.Copy must not find a promoted bytes.Buffer.ReadFrom that bypasses Write.
	n, err := io.Copy(&buffer, bytes.NewReader(input))
	if n != int64(buffer.limit) || err == nil || buffer.buffer.Len() != buffer.limit || ctx.Err() == nil {
		t.Fatalf("copy = %d, %v; buffer length = %d; context = %v", n, err, buffer.buffer.Len(), ctx.Err())
	}
	if n, err := buffer.Write(input); n != 0 || err == nil || buffer.buffer.Len() != buffer.limit {
		t.Fatalf("write after overflow = %d, %v; length = %d", n, err, buffer.buffer.Len())
	}
}

func TestRemoteSessionInventoryFirstCompatibilityAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name, response, want string
		calls                int
	}{
		{"compatible", "printf '%s' '{\"version\":\"fixture\",\"contract_version\":\"v1\",\"sessions\":[]}'", "", 1},
		{"different contract", "printf '%s' '{\"version\":\"old\",\"contract_version\":\"v2\",\"sessions\":[]}'", "remote wt version old", 1},
		{"old receiver", "echo 'unknown flag: --remote-inventory' >&2; exit 1", "remote wt version old", 2},
		{"invalid JSON", "echo '[]'", "invalid session ps", 2},
		{"offline", "exit 255", "status 255", 1},
		{"canceled", "exec /bin/sleep 30", "deadline exceeded", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := testsocket.Dir(t)
			real, alias := filepath.Join(root, "tmp-real"), filepath.Join(root, "tmp-alias")
			if err := os.Mkdir(real, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, alias); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMPDIR", alias)
			log, ssh := filepath.Join(root, "calls"), filepath.Join(alias, "ssh")
			script := "#!/bin/sh\n[ \"$1\" = -T ] && [ \"$2\" = -o ] && [ \"$3\" = BatchMode=yes ] || exit 90\nprintf '%s\\n' \"$5\" >> " + remotepkg.ShellQuote(log) + "\ncase \"$5\" in *\"'--version'\"*) echo 'wt version old' ;; *) " + test.response + " ;; esac\n"
			if err := os.WriteFile(ssh, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()
			rows, err := QueryRemoteSessions("fixture-local", ctx, "fixture", config.Remote{SSHTarget: "fixture-only"}, remotepkg.IO{SSHPath: ssh})
			if test.want == "" {
				if err != nil || rows == nil {
					t.Fatalf("compatible inventory: %v, %v", rows, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("diagnostic = %v, want %q", err, test.want)
			}
			data, readErr := os.ReadFile(log)
			if readErr != nil {
				t.Fatal(readErr)
			}
			commands := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(commands) != test.calls || !strings.Contains(commands[0], "'--remote-inventory'") {
				t.Fatalf("inventory-first subprocesses: %q", commands)
			}
			if test.calls == 2 && !strings.Contains(commands[1], "'--version'") {
				t.Fatal("missing compatibility diagnostic")
			}
		})
	}
}

func TestRemoteSessionFreshIdentityUsesExactBatchRoute(t *testing.T) {
	for _, test := range []struct {
		name, id string
		sessions []LocalSession
		fail     bool
	}{
		{"exact ID", "full-id", []LocalSession{{ID: "full-id", Name: "display-name", PID: 42}}, false},
		{"name collision", "full-id", []LocalSession{{ID: "another-id", Name: "full-id", PID: 42}}, true},
		{"abbreviation", "full", []LocalSession{{ID: "full-id", PID: 42}}, true},
		{"no process identity", "full-id", []LocalSession{{ID: "full-id"}}, true},
		{"duplicate identity", "full-id", []LocalSession{{ID: "full-id", PID: 42}, {ID: "full-id", PID: 43}}, true},
		{"duplicate without PID", "full-id", []LocalSession{{ID: "full-id", PID: 42}, {ID: "full-id"}}, true},
		{"gone", "full-id", []LocalSession{}, true},
		{"empty ID", "", []LocalSession{{ID: "", PID: 42}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home, _, state := testprovider.ScopedHomeDirs(t, testprovider.TempDir(t))
			home, _ = filepath.Abs(home)
			state, _ = filepath.Abs(state)
			root := testsocket.Dir(t)
			real, alias := filepath.Join(root, "tmp-real"), filepath.Join(root, "tmp-alias")
			if err := os.Mkdir(real, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, alias); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMPDIR", alias)
			t.Setenv("HOME", home)
			log := filepath.Join(home, "ssh-route")
			ssh := filepath.Join(alias, "ssh")
			binary := filepath.Join(alias, "custom wt")
			data, err := json.Marshal(RemoteSessionInventory{Version: "fixture", ContractVersion: RemoteSessionContractVersion, Sessions: test.sessions})
			if err != nil {
				t.Fatal(err)
			}
			receiver := "#!/bin/sh\n[ \"$WINGTHING_DIR\" = " + remotepkg.ShellQuote(state) + " ] || exit 93\n" +
				"[ \"$WINGTHING_PREVIEW_DIR\" = \"$WINGTHING_DIR\" ] || exit 94\n" +
				"[ \"$1\" = --expected-channel ] && [ \"$2\" = stable ] || exit 95\nshift 2\n" +
				"case \"$1\" in --version) echo 'wt version fixture' ;; session) printf '%s\\n' " + remotepkg.ShellQuote(string(data)) + " ;; *) exit 96 ;; esac\n"
			transport := "#!/bin/sh\n[ \"$1\" = -T ] && [ \"$2\" = -o ] && [ \"$3\" = BatchMode=yes ] && [ \"$4\" = fixture-peer ] || exit 91\n" +
				"printf '%s\\n' \"$@\" >> " + remotepkg.ShellQuote(log) + "\nexec /bin/sh -c \"$5\"\n"
			for path, content := range map[string]string{ssh: transport, binary: receiver} {
				if err := os.WriteFile(path, []byte(content), 0700); err != nil {
					t.Fatal(err)
				}
			}
			err = ValidateRemoteSession(t.Context(), "devbox", config.Remote{SSHTarget: "fixture-peer", WTBinary: binary, WingthingDir: state}, test.id, remotepkg.IO{In: strings.NewReader("foreground must not be consumed"), StdinTTY: true, StdoutTTY: true, SSHPath: ssh})
			if (err != nil) != test.fail {
				t.Fatalf("validate %q => %v; fail=%v", test.id, err, test.fail)
			}
			if err != nil && !strings.Contains(err.Error(), "no unique fresh process identity") {
				t.Fatal(err)
			}
			route, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("SSH subprocesses per validation: %d", strings.Count(string(route), "BatchMode=yes"))
			if strings.Count(string(route), "BatchMode=yes") != 1 || strings.Count(string(route), remotepkg.ShellQuote(binary)) != 1 || !strings.Contains(string(route), "'session' 'ps' '--json' '--remote-inventory'") {
				t.Fatalf("inventory lost literal/batch route: %s", route)
			}
		})
	}
}

func TestRemoteInventoryOriginIdentityAndLegacyUnknown(t *testing.T) {
	for _, tc := range []struct {
		wing, pin string
		fail      bool
		// Pinned routes must prove identity; only unpinned legacy rows stay unknown.
	}{{"origin", "origin", false}, {"changed", "origin", true}, {"", "origin", true}, {"", "", false}} {
		root := testsocket.Dir(t)
		ssh := filepath.Join(root, "ssh")
		payload, _ := json.Marshal(RemoteSessionInventory{WingID: tc.wing, MachineLabel: "devbox", Version: "fixture", ContractVersion: RemoteSessionContractVersion, Sessions: []LocalSession{{ID: "same-short-id", OriginWingID: "forged-row-origin"}}})
		if e := os.WriteFile(ssh, []byte("#!/bin/sh\nprintf '%s' "+remotepkg.ShellQuote(string(payload))+"\n"), 0700); e != nil {
			t.Fatal(e)
		}
		rows, e := QueryRemoteSessions("fixture", t.Context(), "fixture", config.Remote{SSHTarget: "fixture-host", WingID: tc.pin}, remotepkg.IO{SSHPath: ssh})
		if tc.fail {
			if e == nil || !strings.Contains(e.Error(), "pinned wing identity mismatch") {
				t.Fatalf("missing or changed wing pin was not rejected by identity: %v", e)
			}
			continue
		}
		if e != nil || len(rows) != 1 || rows[0].OriginWingID != tc.wing || rows[0].MachineLabel != "devbox" {
			t.Fatalf("origin guessed or row spoof accepted: %+v %v", rows, e)
		}
	}
}
