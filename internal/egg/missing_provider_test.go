package egg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/control"
)

func TestMissingProviderDiagnosticBeforeLaunch(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "mp-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dir) })
			server, err := NewServer(dir)
			if err != nil {
				t.Fatal(err)
			}
			err = server.RunSession(context.Background(), RunConfig{Agent: agent, Cols: 80, Rows: 24, AgentArgs: []string{"SECRET_PROMPT_SENTINEL"}})
			diagnostic, ok := control.LaunchDiagnosticOf(err)
			if !ok || diagnostic.Reason != agent+"_missing" {
				t.Fatalf("missing diagnostic: %+v %v", diagnostic, err)
			}
			message := control.FormatLaunchDiagnostic(diagnostic)
			if !strings.Contains(message, agent) || !strings.Contains(message, "wt setup") || strings.Contains(message, "SECRET_PROMPT_SENTINEL") {
				t.Fatal(message)
			}
			if _, err := os.Stat(filepath.Join(dir, "egg.sock")); !os.IsNotExist(err) {
				t.Fatalf("provider control started: %v", err)
			}
		})
	}
}
