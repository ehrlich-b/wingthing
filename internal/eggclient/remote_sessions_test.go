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

	"io"

	"testing"
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
			scratch, err := filepath.Abs("../../.scratch")
			if err != nil {
				t.Fatal(err)
			}
			home, _, state := testprovider.ScopedHomeDirs(t, scratch)
			root := t.TempDir()
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
			err = ValidateRemoteSession(t.Context(), "forge", config.Remote{SSHTarget: "fixture-peer", WTBinary: binary, WingthingDir: state}, test.id, remotepkg.IO{In: strings.NewReader("foreground must not be consumed"), StdinTTY: true, StdoutTTY: true, SSHPath: ssh})
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
			if strings.Count(string(route), "BatchMode=yes") != 2 || strings.Count(string(route), remotepkg.ShellQuote(binary)) != 2 || !strings.Contains(string(route), "'session' 'ps' '--json' '--remote-inventory'") {
				t.Fatalf("inventory lost literal/batch route: %s", route)
			}
		})
	}
}
