package dashboard

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

// Drive the real key/reply select loop with a held history reply. The missing
// state directory makes dispatched reads return locally, without IPC, providers
// or a PTY. Worker replies still carry the exact target through the real loop.
func TestRecentRunResultRefreshEventDispatch(t *testing.T) {
	for _, scenario := range []string{"complete", "cancel", "second-target", "late-result"} {
		t.Run(scenario, func(t *testing.T) {
			s := fixture()
			s.machines = map[string]machine{}
			s.localDir = filepath.Join(t.TempDir(), "absent-state")
			s.recent = true
			h := s.history()
			first := runRow{WingID: "one", ID: "same", SessionID: "egg", Status: "stopped", route: runRoute{name: "local", wingID: "one"}}
			second := first
			second.WingID, second.route = "two", runRoute{name: "remote", wingID: "two"}
			h.pages["one"] = runPage{WingID: "one", Runs: []runRow{first}}
			h.pages["two"] = runPage{WingID: "two", Runs: []runRow{second}}
			h.reconcile()
			h.loading = true // The inventory request has started; its reply is held.

			type frame struct {
				mode    mode
				loading bool
				target  runRow
				content string
			}
			frames := make(chan frame, 16)
			output := writerFunc(func(p []byte) (int, error) {
				frames <- frame{s.mode, h.loading, h.target, strings.Join(h.content, "\n")}
				return len(p), nil
			})
			input, writer := pipeInput(t)
			ctx, cancel := context.WithCancel(t.Context())
			var restored atomic.Int32
			done := make(chan error, 1)
			go func() {
				done <- run(ctx, s, emptyInventory(), nil, fakeTerminal(io.Discard, &restored), remotepkg.IO{In: input, Out: output}, nil, nil)
			}()
			t.Cleanup(func() {
				cancel()
				if err := receive(t, done); err != nil {
					t.Error(err)
				}
			})
			receive(t, frames)
			send := func(key string) frame {
				t.Helper()
				if _, err := io.WriteString(writer, key); err != nil {
					t.Fatal(err)
				}
				return receive(t, frames)
			}
			opened := send("o")
			if opened.mode != runResult || !opened.loading || opened.target != first || opened.content != "" {
				t.Fatalf("explicit result request dropped during refresh: %+v", opened)
			}
			want := first
			if scenario != "complete" {
				closed := send("q")
				if closed.mode != navigate || closed.content != "" {
					t.Fatalf("result close: %+v", closed)
				}
				if scenario != "cancel" {
					send("j")
					want = second
					opened = send("o")
					if opened.mode != runResult || opened.target != second || !opened.loading {
						t.Fatalf("second qualified target lost: %+v", opened)
					}
				}
			}
			// Discovery now lists only the other target: a saved explicit request
			// must not follow the refreshed selection or reuse a cancelled one.
			other := first
			if want == first {
				other = second
			}
			reply := runsResult{pages: []runPage{{WingID: other.WingID, Runs: []runRow{other}}}}
			if scenario == "late-result" {
				reply = runsResult{target: &first, content: []string{"STALE_FIRST_RESULT"}}
			}
			select {
			case h.results <- reply:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			finished := receive(t, frames)
			if scenario == "cancel" {
				if finished.mode != navigate || finished.loading || finished.content != "" {
					t.Fatalf("cancelled request dispatched or retained output: %+v", finished)
				}
				return
			}
			if strings.Contains(finished.content, "STALE_FIRST_RESULT") {
				t.Fatal("late result exposed the previous target")
			}
			if finished.loading {
				finished = receive(t, frames)
			}
			if finished.mode != runResult || finished.loading || finished.target != want || !strings.Contains(finished.content, "Result unavailable. Retry this exact run.") {
				t.Fatalf("deferred result was not dispatched for the exact target: %+v", finished)
			}
			t.Logf("delayed reply dispatched wing=%s run=%s route=%s; scenario=%s", finished.target.WingID, finished.target.ID, finished.target.route.name, scenario)
		})
	}
}
