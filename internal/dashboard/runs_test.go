package dashboard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func historyPage(wing string) map[string]any {
	return map[string]any{"wing_id": wing, "count": 1, "snapshot_revision": "revision", "runs": []any{map[string]any{"wing_id": wing, "run_id": "same", "session_id": "egg", "status": "running", "agent": "fixture", "cwd": "/work", "created_at": "2026-10-11T12:00:00Z", "cleanup": "unknown", "output_availability": "unavailable"}}}
}
func TestRecentRunsProjectionAndPagingFailClosed(t *testing.T) {
	for _, defect := range []string{"none", "missing rows", "missing revision", "wrong wing", "count", "foreign row", "duplicate", "looping cursor", "changed revision", "too many rows"} {
		t.Run(defect, func(t *testing.T) {
			data := historyPage("one")
			cursor, revision := "", ""
			switch defect {
			case "missing rows":
				delete(data, "runs")
			case "missing revision":
				delete(data, "snapshot_revision")
			case "wrong wing":
				data["wing_id"] = "two"
			case "count":
				data["count"] = 0
			case "foreign row":
				data["runs"].([]any)[0].(map[string]any)["wing_id"] = "two"
			case "duplicate":
				data["runs"] = append(data["runs"].([]any), data["runs"].([]any)[0])
				data["count"] = 2
			case "looping cursor":
				data["next_cursor"] = "cursor"
				cursor, revision = "cursor", "revision"
			case "changed revision":
				cursor, revision = "cursor", "old-revision"
			case "too many rows":
				data["count"] = 201
				data["runs"] = make([]any, 201)
			}
			lists := 0
			page, err := readRunPage(t.Context(), func(_ context.Context, tool string, args map[string]any) (map[string]any, error) {
				if args["wing_id"] != "one" {
					t.Fatal("lost qualified wing")
				}
				if tool == "agent_list" {
					lists++
					if args["limit"] != 100 || args["cursor"] != cursor {
						t.Fatal(args)
					}
					return data, nil
				}
				if tool != "terminal_list" {
					t.Fatal("unexpected read", tool)
				}
				return map[string]any{"wing_id": "one", "sessions": []any{map[string]any{"id": "egg", "pid": 42, "status": "working"}}}, nil
			}, runRoute{name: "local", wingID: "one"}, cursor, revision)
			if lists != 1 {
				t.Fatal("page reader scanned multiple pages")
			}
			if defect == "none" {
				if err != nil || !page.Runs[0].live {
					t.Fatal(page, err)
				}
			} else if err == nil {
				t.Fatal("accepted defective projection")
			}
		})
	}
}
func TestRecentRunsLiveDenialRetainsMetadata(t *testing.T) {
	page, err := readRunPage(t.Context(), func(_ context.Context, tool string, _ map[string]any) (map[string]any, error) {
		if tool == "agent_list" {
			return historyPage("one"), nil
		}
		return nil, errors.New("policy denied")
	}, runRoute{name: "local", wingID: "one"}, "", "")
	if err != nil || len(page.Runs) != 1 || page.Runs[0].live || !strings.Contains(page.liveErr, "denied") {
		t.Fatal(page, err)
	}
}
func TestRecentRunsQualifiedSelectionStaleAndBoundedRetention(t *testing.T) {
	s := fixture()
	h := s.history()
	for _, id := range []string{"one", "two"} {
		p := runPage{WingID: id, route: runRoute{name: id, wingID: id}, Runs: []runRow{{WingID: id, ID: "same", SessionID: "egg", Status: "running", live: true, route: runRoute{name: id, wingID: id}}}}
		h.pages[id] = p
	}
	h.reconcile()
	s.selected = "local:two"
	s.filter = "session query"
	s.handle("h")
	h.selected = "two:same"
	act, _ := s.handle("enter")
	if act == nil || act.kind != runAttach || act.run.WingID != "two" {
		t.Fatal(act)
	}
	h.apply(runsResult{pages: []runPage{{WingID: "two", route: runRoute{name: "two", wingID: "two"}, err: "corrupt discovery"}}})
	if len(h.rows()) != 2 || h.selected != "two:same" {
		t.Fatal("stale identities lost")
	}
	act, _ = s.handle("enter")
	if act != nil || s.mode != runDetails {
		t.Fatal("stale row attached")
	}
	s.handle("escape")
	s.handle("/")
	s.handle("x")
	s.handle("escape")
	if h.filter != "" {
		t.Fatal("history filter cancellation failed")
	}
	s.handle("h")
	if s.selected != "local:two" || s.filter != "session query" {
		t.Fatal("history changed session navigation")
	}
	s.recent = true
	s.mode = navigate
	h.apply(runsResult{err: errors.New("policy revoked")})
	if len(h.rows()) != 2 || h.err == "" {
		t.Fatal("denial replaced stale known rows with success empty")
	}
}
func TestRecentRunsDirectoryUsesVerifiedRoutesAndLabelsLegacy(t *testing.T) {
	remote := config.Remote{SSHTarget: "fixture", WingID: "two", WingthingDir: "/state", WTBinary: "/bin/wt"}
	call := func(_ context.Context, tool string, _ map[string]any) (map[string]any, error) {
		if tool != "wing_list" {
			t.Fatal(tool)
		}
		return map[string]any{"count": 3, "wings": []any{map[string]any{"name": "local", "wing_id": "one", "mcp_control": true}, map[string]any{"name": "forge", "wing_id": "two", "mcp_control": true, "hostname": "fixture", "wingthing_dir": "/state"}, map[string]any{"name": "old", "mcp_control": false}}}, nil
	}
	routes, unavailable, err := readRunDirectory(t.Context(), call, map[string]config.Remote{"forge": remote, "old": {SSHTarget: "legacy"}})
	if err != nil || len(routes) != 2 || routes[1].remote != remote || len(unavailable) != 1 || !strings.Contains(unavailable[0], "legacy SSH-only") {
		t.Fatal(routes, unavailable, err)
	}
	remote.WingID = "changed"
	routes, unavailable, err = readRunDirectory(t.Context(), call, map[string]config.Remote{"forge": remote})
	if err != nil || len(routes) != 1 || len(unavailable) != 2 {
		t.Fatal(routes, unavailable, err)
	}
}
func TestRecentRunsResultAuthorizationIdentitySanitationAndErasure(t *testing.T) {
	r := runRow{WingID: "one", ID: "same", Status: "stopped"}
	for _, outcome := range []string{"ok", "denied", "wrong", "not ready", "too large"} {
		t.Run(outcome, func(t *testing.T) {
			lines, err := readRunResult(t.Context(), func(_ context.Context, tool string, args map[string]any) (map[string]any, error) {
				if tool != "agent_result" || args["wing_id"] != "one" || args["run_id"] != "same" || args["max_chars"] != 8192 {
					t.Fatal(tool, args)
				}
				if outcome == "denied" {
					return nil, errors.New("policy denied")
				}
				d := map[string]any{"wing_id": "one", "run_id": "same", "ready": true, "status": "stopped", "output": "metadata\n\x1b]52;c;fake\a\u202e"}
				if outcome == "wrong" {
					d["wing_id"] = "two"
				}
				if outcome == "not ready" {
					d["ready"] = false
				}
				if outcome == "too large" {
					d["output"] = strings.Repeat("x", 32769)
				}
				return d, nil
			}, r)
			if outcome == "denied" || outcome == "wrong" || outcome == "too large" {
				if err == nil {
					t.Fatal("accepted invalid result")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			text := strings.Join(lines, "\n")
			if strings.ContainsAny(text, "\x1b\a\u202e") {
				t.Fatal("terminal injection")
			}
			if outcome == "ok" && !strings.Contains(text, "cancellation is not completion") {
				t.Fatal(text)
			}
		})
	}
	s := fixture()
	s.recent = true
	s.mode = runResult
	s.history().content = []string{"explicit result"}
	s.handle("escape")
	if s.history().content != nil {
		t.Fatal("result retained after closing")
	}
}
func TestRecentRunsLayoutAndTruthfulUnknown(t *testing.T) {
	s := fixture()
	s.recent = true
	h := s.history()
	r := runRow{WingID: "one", ID: "same", SessionID: "egg", Status: "done", FailureKind: "unknown_outcome", CWD: "/work\x1b[2J", Cleanup: "unknown", OutputAvailability: "unavailable", route: runRoute{name: "local", wingID: "one"}}
	h.pages["one"] = runPage{WingID: "one", Runs: []runRow{r}, Next: "cursor", route: r.route}
	h.reconcile()
	for _, dim := range [][2]int{{120, 24}, {40, 12}, {1, 1}} {
		t.Run(fmt.Sprint(dim), func(t *testing.T) { renderedLines(t, render(s, dim[0], dim[1], time.Now()), dim[0], dim[1]) })
	}
	text := render(s, 120, 24, time.Now())
	for _, want := range []string{"Recent runs", "? Unknown", "N next page", "Missing", "Unknown"} {
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}
	act, _ := s.handle("enter")
	if act != nil || s.mode != runDetails {
		t.Fatal("unknown outcome caused action")
	}
	text = strings.Join(overlayLines(s, time.Now()), "\n")
	for _, want := range []string{"wt agent recover", "--snapshot PATH", "completed, hash-verified", "Never resend automatically"} {
		if !strings.Contains(text, want) {
			t.Fatal(want)
		}
	}
}

func TestRecentRunsNoMatchesStillAllowsExplicitNextPage(t *testing.T) {
	s := fixture()
	s.recent = true
	h := s.history()
	h.pages["one"] = runPage{WingID: "one", Next: "cursor", route: runRoute{name: "local", wingID: "one"}, Runs: []runRow{{WingID: "one", ID: "same"}}}
	h.filter = "absent-from-this-page"
	h.reconcile()
	s.handle("N")
	if !h.requested || !h.next || h.pagingPage().WingID != "one" {
		t.Fatal("filter hid continuation")
	}
}

func TestRecentRunDetailsUseLatestLivenessAfterRefresh(t *testing.T) {
	s := fixture()
	s.recent, s.mode = true, runDetails
	h := s.history()
	r := runRow{WingID: "one", ID: "same", SessionID: "egg", live: true, route: runRoute{name: "local", wingID: "one"}}
	h.target = r
	r.live = false
	h.pages["one"] = runPage{WingID: "one", Runs: []runRow{r}}
	if !strings.Contains(strings.Join(s.runOverlay(), "\n"), "Live session not verified") {
		t.Fatal("details retained stale live flag")
	}
}
