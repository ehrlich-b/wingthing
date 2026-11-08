package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestSessionPSLocalFormatGolden(t *testing.T) {
	// Taken from the format before configured remotes (204bce9^).
	// Status and optional native lifecycle metadata are additive to the existing format.
	const human = "NAME    ID             KIND     PROCESS  STATUS   ISOLATION  READERS  UPTIME  IDLE  CWD\n" +
		"review  local-session  command  /bin/sh  unknown  unknown    0        0s      0s    /tmp/work\n"
	const jsonGolden = `[
  {
    "id": "local-session",
    "name": "review",
    "kind": "command",
    "status": "unknown",
    "state": "unknown",
    "state_source": "unsupported",
    "state_reason": "Native lifecycle evidence is missing or hooks are disabled; status is unknown.",
    "command": "/bin/sh",
    "cwd": "/tmp/work",
    "pid": %d,
    "readers": 0,
    "uptime_seconds": 0,
    "idle_seconds": 0,
    "buffer_bytes": 0,
    "total_written": 0
  }
]
`
	for _, registry := range []string{"missing", "empty"} {
		for _, active := range []bool{false, true} {
			for _, jsonOutput := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/active=%t/json=%t", registry, active, jsonOutput), func(t *testing.T) {
					cfg := &config.Config{Dir: testsocket.Dir(t)}
					t.Setenv("WINGTHING_DIR", cfg.Dir)
					if registry == "empty" {
						if err := config.SaveRemotes(cfg.Dir, map[string]config.Remote{}); err != nil {
							t.Fatal(err)
						}
					}
					if active {
						seedRemoteListSession(t, cfg, "local-session", "")
						if err := eggclient.WriteSessionName(filepath.Join(cfg.Dir, "eggs", "local-session"), "review"); err != nil {
							t.Fatal(err)
						}
					}
					args := []string{"session", "ps"}
					want := "no active sessions\n"
					if active {
						want = human
					}
					if jsonOutput {
						args = append(args, "--json")
						want = "[]\n"
						if active {
							want = fmt.Sprintf(jsonGolden, os.Getpid())
						}
					}
					var output bytes.Buffer
					if err := executeCLI(context.Background(), args, remotepkg.IO{Out: &output, ErrOut: io.Discard, SSHPath: filepath.Join(cfg.Dir, "must-not-run-ssh")}); err != nil {
						t.Fatal(err)
					}
					if output.String() != want {
						t.Fatalf("local output differs from golden\ngot:  %q\nwant: %q", output.String(), want)
					}
				})
			}
		}
	}
}
