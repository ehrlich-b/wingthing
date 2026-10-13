package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
)

func TestPreviewReadsRecentMessagesOnExactSavedRoute(t *testing.T) {
	s := fixture()
	r := s.rows()[2]
	r.machine = "Studio"
	r.ID = "exact-id"
	r.OriginWingID = "studio-origin"
	r.identity.profile = config.Remote{SSHTarget: "studio-demo", WingthingDir: "/personal/state", WingID: "studio-origin"}
	calls := 0
	value, err := loadSessionPreview(t.Context(), r, func(_ context.Context, args []string, streams remotepkg.IO) error {
		calls++
		joined := strings.Join(args, " ")
		for _, want := range []string{"--remote studio-demo", "--remote-state /personal/state", "--remote-wing-id studio-origin", "session action --json", "--session exact-id", "--wing studio-origin", "--operation session_read"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("missing %q in %v", want, args)
			}
		}
		view := egg.SessionView{SessionID: r.ID, HeadCursor: 80}
		if calls == 2 {
			if !strings.Contains(joined, `--arguments {"after_cursor":50,"limit":30}`) {
				t.Fatal("did not read bounded recent tail")
			}
			view.Report = &egg.SessionReport{Update: "DNS change ready.", NextAction: "Run the DNS tests."}
			view.Events = []egg.SessionEvent{{Type: "message", Role: "user", Text: "Keep the cached result."}, {Type: "message", Role: "assistant", Text: "The timeout test is ready.\x1b]52;bad\a"}, {Type: "tool_result", Role: "assistant", Text: "RAW_OUTPUT"}, {Type: "message", Role: "assistant", Text: "api_key=syntheticcredential"}}
		}
		return json.NewEncoder(streams.Out).Encode(map[string]any{"origin_wing_id": r.OriginWingID, "session_id": r.ID, "result": map[string]any{"session": r.ID, "lifecycle": view}})
	})
	if err != nil || calls != 2 || len(value.messages) != 3 || value.next != "Run the DNS tests." {
		t.Fatalf("%+v %v", value, err)
	}
	for _, m := range value.messages {
		if strings.ContainsAny(m.text, "\x1b\x07") || strings.Contains(m.text, "RAW_OUTPUT") || strings.Contains(m.text, "syntheticcredential") {
			t.Fatal("unsafe preview", m)
		}
	}
}
func TestPreviewRejectsDifferentSessionAndClearsDeniedText(t *testing.T) {
	s := fixture()
	r := s.rows()[0]
	_, err := loadSessionPreview(t.Context(), r, func(_ context.Context, _ []string, streams remotepkg.IO) error {
		return json.NewEncoder(streams.Out).Encode(map[string]any{"session": "replacement", "lifecycle": egg.SessionView{SessionID: "replacement"}})
	})
	if err == nil {
		t.Fatal("different identity accepted")
	}
	p := newPreviews()
	p.reads[r.identity] = previewRead{serial: 1}
	s.previews = map[attentionIdentity]sessionPreview{r.identity: {update: "Private work"}}
	p.apply(s, previewResult{target: r, serial: 1, err: errors.New("denied")})
	if !s.preview(r).failed || s.latestUpdate(r) != "" {
		t.Fatal("denial kept protected text")
	}
}
func TestPreviewCancelsSelectionAndRejectsObsoleteReply(t *testing.T) {
	s := fixture()
	r := s.rows()[0]
	r.StateCursor = 1
	r.StateSource = "claude_hook"
	m := s.machines[r.machine]
	for i := range m.sessions {
		if m.sessions[i].ID == r.ID {
			m.sessions[i] = r.LocalSession
		}
	}
	s.machines[r.machine] = m
	s.selected = r.key()
	s.selectionTouched = true
	s.reconcile()
	r, _ = s.selection()
	started := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	s.previewReader = func(ctx context.Context, _ row) (sessionPreview, error) {
		started <- struct{}{}
		<-ctx.Done()
		cancelled <- struct{}{}
		return sessionPreview{}, ctx.Err()
	}
	p := newPreviews()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	defer p.workers.Wait()
	defer p.stop()
	p.request(ctx, s, true, time.Now())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("read did not start")
	}
	s.selected = "work:one"
	p.request(ctx, s, true, time.Now())
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("obsolete read not cancelled")
	}
	result := <-p.results
	p.apply(s, result)
	if _, ok := s.previews[r.identity]; ok {
		t.Fatal("obsolete reply restored text")
	}
}
func TestPaperPaneKeepsStateColorAndUnreadSeparate(t *testing.T) {
	s := fixture()
	r, _ := s.selection()
	s.previews = map[attentionIdentity]sessionPreview{r.identity: {messages: []previewMessage{{speaker: "You", text: "Keep the cached result."}, {speaker: "Codex", text: "The timeout test is ready."}}, next: "Run the DNS tests."}}
	s.theme = terminalTheme{colors: 256}
	output := render(s, 132, 36, time.Now())
	lines := renderedLines(t, output, 132, 36)
	text := strings.Join(lines, "\n")
	for _, want := range []string{"┌", "┬", "│", "└", "Run the DNS tests.", "RECENT", "Keep the cached result.", "The timeout test is ready."} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if !strings.Contains(output, "48;5;255") || !strings.Contains(output, "48;5;234") || strings.Contains(text, "[unread]") {
		t.Fatal("row recolored or unread in title")
	}
	if !strings.Contains(output, "\x1b[38;5;217m! Needs you") {
		t.Fatal("selected state lost warning color")
	}
	s.theme.ascii = true
	if strings.Contains(render(s, 132, 36, time.Now()), "│") {
		t.Fatal("ASCII fallback lost")
	}
}
func TestWorklistPositionAccountsForPinnedSession(t *testing.T) {
	s := fixture()
	m := s.machines["local"]
	pin := session("pin", "Coordinator", "/app", "idle")
	pin.Metadata = sessionrole.Metadata{Role: "coordinator", Pinned: true}
	m.sessions = append(m.sessions, pin)
	for i := 0; i < 24; i++ {
		item := session(string(rune('a'+i)), "Update parser", "/app", "working")
		m.sessions = append(m.sessions, item)
	}
	s.machines["local"] = m
	s.reconcile()
	text := strings.Join(renderedLines(t, render(s, 80, 24, time.Now()), 80, 24), "\n")
	if !strings.Contains(text, "27 sessions   ") || !strings.Contains(text, "of 27 sessions") || strings.Contains(text, "pinned") {
		t.Fatal("header and position disagree", text)
	}
}

func TestDeniedInventoryClearsProtectedPreviewAndSessionCache(t *testing.T) {
	s := fixture()
	r := s.rows()[2]
	s.previews = map[attentionIdentity]sessionPreview{r.identity: {update: "Private remote work", observed: time.Now()}}
	inv := newInventory(nil)
	inv.apply(s, inventoryResult{name: r.machine, generation: r.generation, err: errors.New("permission denied"), updated: time.Now()})
	if len(s.machines[r.machine].sessions) != 0 || len(s.previews) != 0 {
		t.Fatal("authorization denial kept protected cache")
	}
}
func TestEmptyWideNavigatorHasNoUnusedDetailFrame(t *testing.T) {
	s := &state{machines: map[string]machine{"local": {updated: time.Now()}}}
	text := render(s, 132, 36, time.Now())
	if strings.ContainsAny(text, "┌│└") || !strings.Contains(text, "Install an agent") {
		t.Fatal("empty navigator has unused pane")
	}
}

func TestTwoPinnedSessionsCannotDisplaceSelectedPreview(t *testing.T) {
	s := fixture()
	for _, name := range []string{"local", "work"} {
		m := s.machines[name]
		pin := session("pin", "Coordinator", "/project", "idle")
		pin.Metadata = sessionrole.Metadata{Role: "coordinator", Pinned: true}
		pin.StateSource = "claude_hook"
		pin.StateCursor = 1
		m.sessions = append(m.sessions, pin)
		for i := range m.sessions {
			m.sessions[i].StateSource = "claude_hook"
			m.sessions[i].StateCursor = 1
		}
		s.machines[name] = m
	}
	s.selected = "local:one"
	s.selectionTouched = true
	s.reconcile()
	got := make(chan string, 2)
	s.previewReader = func(_ context.Context, r row) (sessionPreview, error) { got <- r.key(); return sessionPreview{}, nil }
	p := newPreviews()
	defer p.workers.Wait()
	defer p.stop()
	p.request(t.Context(), s, true, time.Now())
	selected := false
	for i := 0; i < 2; i++ {
		select {
		case key := <-got:
			selected = selected || key == s.selected
		case <-time.After(time.Second):
			t.Fatal("preview did not finish")
		}
	}
	if !selected {
		t.Fatal("pinned rows consumed selected preview slot")
	}
}

func TestRemoteOriginChangeCannotRestoreOldMessages(t *testing.T) {
	s := fixture()
	r := s.rows()[2]
	r.OriginWingID = "old-origin"
	m := s.machines[r.machine]
	for i := range m.sessions {
		if m.sessions[i].ID == r.ID {
			m.sessions[i].OriginWingID = "new-origin"
		}
	}
	s.machines[r.machine] = m
	s.previews = map[attentionIdentity]sessionPreview{r.identity: {origin: "old-origin", update: "Old origin's work"}}
	p := newPreviews()
	p.reads[r.identity] = previewRead{serial: 1, origin: "old-origin"}
	p.apply(s, previewResult{target: r, serial: 1, value: sessionPreview{origin: "old-origin", update: "Late old text"}})
	current := s.sessionRow(r.machine, m.sessions[0])
	if s.latestUpdate(current) != "" {
		t.Fatal("old origin text appeared under replacement session")
	}
}
