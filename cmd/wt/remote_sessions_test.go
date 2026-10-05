package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/ehrlich-b/wingthing/internal/config"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

func TestRemoteSessionOutputLimits(t *testing.T) {
	for _, phase := range []string{"version", "inventory"} {
		for _, stream := range []struct {
			name, redirect string
			limit          int
		}{{"stdout", "", 4 << 20}, {"stderr", " >&2", 64 << 10}} {
			t.Run(phase+"/"+stream.name, func(t *testing.T) {
				cfg := &config.Config{Dir: t.TempDir()}
				seedRemoteListSession(t, cfg, "local-session", "")
				if err := config.SaveRemotes(cfg.Dir, map[string]config.Remote{"flood": {SSHTarget: "host"}}); err != nil {
					t.Fatal(err)
				}
				script := ""
				if phase == "inventory" {
					script = "case \"$3\" in *\"'--version'\"*) echo 'wt version test'; exit 0 ;; esac\n"
				}
				script += fmt.Sprintf("head -c %d /dev/zero%s\nexec sleep 30\n", stream.limit+1, stream.redirect)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				rows, err := discoverMachineSessions(ctx, cfg, remotepkg.IO{SSHPath: writeFakeRemoteSSH(t, script)})
				if err != nil || len(rows) != 2 || rows[0].ID != "local-session" || rows[1].Machine != "flood" || rows[1].ID != "" {
					t.Fatalf("overflow rows = %#v, %v", rows, err)
				}
				want := fmt.Sprintf("%s exceeded %d-byte limit", stream.name, stream.limit)
				if !strings.Contains(rows[1].Error, want) {
					t.Fatalf("overflow error prefix = %q, want %q", rows[1].Error[:min(len(rows[1].Error), 200)], want)
				}
				if ctx.Err() != nil {
					t.Fatal("overflow waited for the query deadline instead of canceling")
				}
			})
		}
	}
}

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

func TestMachineSessionsEscapeControlsOnlyForHumanOutput(t *testing.T) {
	controls := "\n\r\t\x1b\x00\x07\x08\x0b\x0c\x7f\u0085\u009b"
	escaped := `\n\r\t\x1b\x00\a\b\v\f\x7f\u0085\u009b`
	rows := []machineSession{
		{Machine: "machine" + controls, localSession: localSession{
			ID: "id" + controls, Name: "name" + controls, Kind: "kind" + controls,
			Agent: "agent" + controls, Status: "status" + controls, Isolation: "isolation" + controls, CWD: "/cwd" + controls,
		}},
		{Machine: "command-machine", localSession: localSession{ID: "command-id", Kind: "command", Command: "command" + controls}},
		{Machine: "diagnostic-machine" + controls, Error: "diagnostic" + controls},
	}
	var human bytes.Buffer
	if err := writeMachineSessions(&human, rows, false); err != nil {
		t.Fatal(err)
	}
	for _, r := range human.String() {
		if unicode.IsControl(r) && r != '\n' {
			t.Fatalf("raw control %U in table: %q", r, human.String())
		}
	}
	if strings.Count(human.String(), "\n") != len(rows)+1 {
		t.Fatalf("remote field forged a table row: %q", human.String())
	}
	for _, field := range []string{"machine", "id", "name", "kind", "agent", "status", "isolation", "/cwd", "command", "diagnostic-machine", "diagnostic"} {
		if !strings.Contains(human.String(), field+escaped) {
			t.Errorf("table missing escaped %q: %q", field, human.String())
		}
	}
	var output bytes.Buffer
	if err := writeMachineSessions(&output, rows, true); err != nil {
		t.Fatal(err)
	}
	var decoded []machineSession
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil || !reflect.DeepEqual(decoded, rows) {
		t.Fatalf("JSON changed remote fields: %#v, %v", decoded, err)
	}
}
