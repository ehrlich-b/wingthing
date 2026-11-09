package egg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestInitialCodexPromptLimit(t *testing.T) {
	for _, tc := range []struct {
		name, prompt string
		tooLarge     bool
	}{
		{"small", "small startup prompt", false},
		{"at limit", strings.Repeat("x", MaxInitialCodexPromptBytes), false},
		{"multibyte at limit", strings.Repeat("Ω", MaxInitialCodexPromptBytes/2), false},
		{"100 KiB ASCII", strings.Repeat("x", 100*1024), false},
		{"escaped at limit", strings.Repeat("\x01", MaxInitialCodexPromptBytes/6) + strings.Repeat("x", MaxInitialCodexPromptBytes%6), false},
		{"escaped over", strings.Repeat("\x01", MaxInitialCodexPromptBytes/6+1), true},
		{"HTML escaped over", strings.Repeat("<", MaxInitialCodexPromptBytes/6+1), true},
		{"newline at limit", strings.Repeat("\n", MaxInitialCodexPromptBytes/2), false},
		{"quote over", strings.Repeat("\"", MaxInitialCodexPromptBytes/2+1), true},
		{"one byte over", strings.Repeat("x", MaxInitialCodexPromptBytes) + " ", true},
		{"140 KiB", "private-prompt-canary" + strings.Repeat("x", 140*1024-len("private-prompt-canary")), true},
		{"multibyte over", strings.Repeat("Ω", MaxInitialCodexPromptBytes/2+1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := testsocket.Dir(t)
			rc := RunConfig{Agent: "codex", CWD: home, OuterBoundary: true, InitialRun: &RunTurnRequest{RunID: "fixture", Prompt: tc.prompt}}
			opts := LaunchPreparationOptions{Home: home, HostHome: home, EggDir: filepath.Join(home, "egg"), CodexCapability: SavedCodexCapability("codex-cli 0.159.3")}
			prepared, err := PrepareLaunch(rc, opts, NativeLaunchState())
			if tc.tooLarge {
				var sizeErr *InitialCodexPromptTooLargeError
				want := fmt.Sprintf("initial native Codex prompt requires %d encoded argv bytes; limit is %d bytes", InitialCodexPromptArgBytes(tc.prompt), MaxInitialCodexPromptBytes)
				if !errors.As(err, &sizeErr) || sizeErr.SizeBytes != InitialCodexPromptArgBytes(tc.prompt) || sizeErr.LimitBytes != MaxInitialCodexPromptBytes || err.Error() != want {
					t.Fatalf("unsafe or incorrect size error: %v", err)
				}
				// RunSession also rejects before recording startup failure or
				// touching state, probing a binary, reserving a run or spawning.
				s := &Server{dir: home}
				if err := s.RunSession(t.Context(), rc); !errors.As(err, &sizeErr) {
					t.Fatalf("direct session: %v", err)
				}
				entries, err := os.ReadDir(home)
				if err != nil || len(entries) != 0 {
					t.Fatalf("rejection wrote launch state: %v %v", entries, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !prepared.NativeRun || !prepared.CodexRun || prepared.Args[len(prepared.Args)-1] != tc.prompt || prepared.Args[len(prepared.Args)-2] != "--" {
				t.Fatal("initial prompt was changed or lost")
			}
			wire, err := json.Marshal(rc.InitialRun)
			if err != nil || len("--initial-run=")+len(wire)+1 >= 128*1024 {
				t.Fatalf("egg argv exceeds per-argument budget: %d %v", len(wire), err)
			}
		})
	}
}

func TestCodexLaterPromptUnaffectedByInitialLimit(t *testing.T) {
	rt, home, request, sent := codexRunFixture(t)
	request.Prompt = strings.Repeat("<", 64*1024)
	if _, err := rt.submit(request); err != nil {
		t.Fatal(err)
	}
	<-sent
	publishCodexNotify(t, home, filepath.Base(rt.dir), "complete", "thread-exact", "turn-exact", request.Prompt, "later turn result")
	if _, err := rt.wait(t.Context(), request.RunID); err != nil {
		t.Fatal(err)
	}
	result, err := rt.get(request.RunID, true)
	if err != nil || result.Status != "done" || result.Text != "later turn result" {
		t.Fatalf("later prompt transport changed: %+v %v", result, err)
	}
}
