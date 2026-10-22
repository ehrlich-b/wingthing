package dashboard

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

func navigatorFixture(n int) *state {
	s := &state{machines: map[string]machine{"local": {updated: time.Now()}}, previews: map[attentionIdentity]sessionPreview{}}
	m := s.machines["local"]
	for i := 0; i < n; i++ {
		m.sessions = append(m.sessions, session(fmt.Sprintf("full-id-%02d", i), fmt.Sprintf("Task %02d repair long running deployment and verify rollback", i), "/project/application", "working"))
	}
	s.machines["local"] = m
	s.reconcile()
	return s
}

func navigatorCapture(t *testing.T, name, data string) {
	t.Helper()
	if root := os.Getenv("WT_NAVIGATOR_EVIDENCE"); root != "" {
		if !filepath.IsAbs(root) {
			t.Fatal("capture directory must be absolute")
		}
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNavigatorPreviewEightRows160x44(t *testing.T) {
	s := navigatorFixture(8)
	for i, r := range s.rows() {
		s.selected = r.key()
		s.selectionTouched = true
		marker := fmt.Sprintf("OUTPUT_FOR_%s", r.ID)
		s.previews[r.identity] = sessionPreview{output: []string{marker, "Tests passed. Ready for the next step."}, observed: time.Now()}
		output := render(s, 160, 44, time.Unix(1000, 0))
		lines := renderedLines(t, output, 160, 44)
		text := strings.Join(lines, "\n")
		if !strings.Contains(text, marker) {
			t.Fatal("selected output absent", text)
		}
		for j := 0; j < 8; j++ {
			if !strings.Contains(text, fmt.Sprintf("Task %02d", j)) {
				t.Fatalf("row %d disappeared for selection %d", j, i)
			}
		}
		navigatorCapture(t, fmt.Sprintf("160x44-selected-%02d.ansi", i), output)
		navigatorCapture(t, fmt.Sprintf("160x44-selected-%02d.txt", i), text)
	}
}

func TestNavigatorPreviewCompactAndVolume(t *testing.T) {
	for _, size := range [][2]int{{80, 44}, {160, 44}, {43, 44}} {
		for _, n := range []int{0, 1, 8, 12, 13} {
			s := navigatorFixture(n)
			if r, ok := s.selection(); ok {
				s.previews[r.identity] = sessionPreview{output: []string{"Selected terminal output"}}
			}
			output := render(s, size[0], size[1], time.Unix(1000, 0))
			text := strings.Join(renderedLines(t, output, size[0], size[1]), "\n")
			if size[0] == 80 && strings.Contains(text, "OUTPUT · read only") {
				t.Fatal("compact navigator became a split pane")
			}
			if size[0] == 160 && n == 13 && !strings.Contains(text, "of 13 sessions") {
				t.Fatal("missing overflow count")
			}
			navigatorCapture(t, fmt.Sprintf("%dx%d-count-%02d.txt", size[0], size[1], n), text)
		}
	}
}

func TestNavigatorPreviewReadOnlyTransport(t *testing.T) {
	for _, remote := range []bool{false, true} {
		s := fixture()
		r := s.rows()[0]
		if remote {
			r = s.rows()[2]
			r.OriginWingID = "exact-origin"
			r.identity.profile = config.Remote{SSHTarget: "preview-host", WingthingDir: "/state/demo", WingID: r.OriginWingID}
		}
		calls := []string{}
		value, err := loadTerminalPreview(t.Context(), r, func(_ context.Context, args []string, streams remotepkg.IO) error {
			calls = append(calls, strings.Join(args, " "))
			if streams.In != nil {
				t.Fatal("preview connected input")
			}
			if remote {
				for _, want := range []string{"--remote preview-host", "--remote-state /state/demo", "--wing exact-origin", "--session " + r.ID} {
					if !strings.Contains(calls[len(calls)-1], want) {
						t.Fatal("preview changed its saved route", args)
					}
				}
				if !strings.Contains(calls[len(calls)-1], "--operation terminal_read") {
					t.Fatal(args)
				}
			} else if strings.Join(args, " ") != "session read --json -- "+r.ID {
				t.Fatal(args)
			}
			result := map[string]any{"session": r.ID, "base64": base64.StdEncoding.EncodeToString([]byte("\x1b[2J\x1b[HTERMINAL_OUTPUT\r\n\x1b]52;c;Y2xpcGJvYXJk\a\x1b[6n"))}
			if remote {
				result = map[string]any{"origin_wing_id": r.OriginWingID, "session_id": r.ID, "result": result}
			}
			return json.NewEncoder(streams.Out).Encode(result)
		})
		if err != nil || !strings.Contains(strings.Join(value.output, "\n"), "TERMINAL_OUTPUT") {
			t.Fatal(value, err)
		}
		if len(calls) != 1 {
			t.Fatal(calls)
		}
		navigatorCapture(t, fmt.Sprintf("transport-remote-%t.json", remote), fmt.Sprintf("{\"calls\":%q,\"mutation_calls\":0,\"input_connected\":false}\n", calls))
	}
}

func TestNavigatorPreviewIdentityAndDelayedResponse(t *testing.T) {
	s := navigatorFixture(8)
	p := newPreviews()
	r, _ := s.selection()
	s.selectionTouched = true
	s.previewReader = func(context.Context, row) (sessionPreview, error) { return sessionPreview{}, nil }
	p.reads[r.identity] = previewRead{serial: 1}
	m := s.machines["local"]
	m.sessions[0].Name = "Renamed session with a very different display name"
	s.machines["local"] = m
	newInventory(nil).apply(s, inventoryResult{name: "local", sessions: m.sessions, updated: time.Now()})
	if s.selected != r.key() {
		t.Fatal("rename or refresh switched target")
	}
	p.apply(s, previewResult{target: r, serial: 1, value: sessionPreview{output: []string{"same target"}}})
	if s.selected != r.key() {
		t.Fatal("preview changed selection")
	}
	s.move(1)
	selected := s.selected
	delete(p.reads, r.identity)
	p.apply(s, previewResult{target: r, serial: 1, value: sessionPreview{output: []string{"LATE_WRONG_OUTPUT"}}})
	if s.selected != selected || strings.Contains(render(s, 160, 44, time.Now()), "LATE_WRONG_OUTPUT") {
		t.Fatal("delayed output switched target")
	}
	newInventory(nil).apply(s, inventoryResult{name: "local", err: errors.New("remote lost")})
	if s.selected != selected {
		t.Fatal("loss switched target")
	}
	// Replacement output must be rejected even when a display name collides.
	_, err := loadTerminalPreview(t.Context(), r, func(_ context.Context, _ []string, streams remotepkg.IO) error {
		return json.NewEncoder(streams.Out).Encode(map[string]any{"session": "replacement", "base64": ""})
	})
	if err == nil {
		t.Fatal("accepted another target's output")
	}
}

func TestNavigatorPreviewDisappearingSelectionNeverFallsThrough(t *testing.T) {
	s := navigatorFixture(8)
	s.selectionTouched = true
	r, _ := s.selection()
	m := s.machines["local"]
	newInventory(nil).apply(s, inventoryResult{name: "local", sessions: m.sessions[1:], updated: time.Now()})
	if s.selected != r.key() {
		t.Fatal("removed selection switched target")
	}
	if _, ok := s.selection(); ok {
		t.Fatal("replacement row became actionable")
	}
	if act, _ := s.handle("v"); act != nil {
		t.Fatal("viewer opened a replacement")
	}
	s.handle("j")
	if _, ok := s.selection(); !ok {
		t.Fatal("explicit selection could not recover")
	}
}

func TestNavigatorPreviewSavedRouteReplacementRequiresSelection(t *testing.T) {
	s := fixture()
	s.selected = "work:one"
	s.selectionTouched = true
	r, _ := s.selection()
	inv := newInventory(nil)
	inv.reload(s, map[string]config.Remote{"work": {SSHTarget: "old-host", WingID: "old-wing"}}, nil)
	// A new saved host under the same label must not inherit the old selection.
	inv.reload(s, map[string]config.Remote{"work": {SSHTarget: "new-host", WingID: "new-wing"}}, nil)
	if s.selected != r.key() || !s.selectionLost {
		t.Fatal("saved route replacement switched selection")
	}
	if act, _ := s.handle("v"); act != nil {
		t.Fatal("old selection opened replacement host")
	}
	p := newPreviews()
	p.reads[r.identity] = previewRead{serial: 1}
	p.apply(s, previewResult{target: r, serial: 1, value: sessionPreview{output: []string{"OLD_HOST_TEXT"}}})
	if strings.Contains(render(s, 160, 44, time.Now()), "OLD_HOST_TEXT") {
		t.Fatal("retired host restored output")
	}
}

func TestNavigatorPreviewLostSelectionRecoveryNotice(t *testing.T) {
	for _, message := range []string{"Selected session unavailable. j/k choose another.", "Selected machine changed. j/k choose another."} {
		s := navigatorFixture(8)
		s.message = message
		text := strings.Join(renderedLines(t, render(s, 160, 44, time.Now()), 160, 44), "\n")
		if !strings.Contains(text, message) {
			t.Fatal("selection recovery hint was hidden")
		}
	}
}

func TestNavigatorPreviewInitialSelectionCannotDisappearIntoAnotherTarget(t *testing.T) {
	s := navigatorFixture(8)
	r, _ := s.selection()
	m := s.machines["local"]
	newInventory(nil).apply(s, inventoryResult{name: "local", sessions: m.sessions[1:], updated: time.Now()})
	if s.selected != r.key() || !s.selectionLost {
		t.Fatal("initial selection fell through to another target")
	}
	if act, _ := s.handle("v"); act != nil {
		t.Fatal("initial selection opened another target")
	}
}

func TestNavigatorPreviewRemoteOutputIdentity(t *testing.T) {
	s := fixture()
	r := s.rows()[2]
	r.OriginWingID = "expected-origin"
	for _, field := range []string{"origin", "outer-session", "inner-session", "missing-snapshot", "invalid-snapshot"} {
		t.Run(field, func(t *testing.T) {
			_, err := loadTerminalPreview(t.Context(), r, func(_ context.Context, _ []string, streams remotepkg.IO) error {
				inner := map[string]any{"session": r.ID, "base64": base64.StdEncoding.EncodeToString([]byte("PRIVATE_OUTPUT"))}
				outer := map[string]any{"origin_wing_id": r.OriginWingID, "session_id": r.ID, "result": inner}
				switch field {
				case "origin":
					outer["origin_wing_id"] = "replacement-origin"
				case "outer-session":
					outer["session_id"] = "replacement-session"
				case "inner-session":
					inner["session"] = "replacement-session"
				case "missing-snapshot":
					delete(inner, "base64")
				case "invalid-snapshot":
					inner["base64"] = "not valid base64!"
				}
				return json.NewEncoder(streams.Out).Encode(outer)
			})
			if err == nil {
				t.Fatal("invalid or differently scoped output accepted")
			}
		})
	}
}
