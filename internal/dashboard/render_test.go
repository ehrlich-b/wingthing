package dashboard

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/mattn/go-runewidth"
)

var cursorLine = regexp.MustCompile(`\x1b\[\d+;1H\x1b\[2K`)
var styling = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func renderedLines(t *testing.T, output string, width, height int) []string {
	t.Helper()
	lines := cursorLine.Split(output, -1)[1:]
	if len(lines) != height {
		t.Fatalf("got %d lines, want %d", len(lines), height)
	}
	for i := range lines {
		lines[i] = styling.ReplaceAllString(lines[i], "")
		if goWidth := runewidth.StringWidth(lines[i]); goWidth > width {
			t.Fatalf("line %d uses %d/%d cells: %q", i+1, goWidth, width, lines[i])
		}
		if !strings.Contains(output, fmt.Sprintf("\x1b[%d;1H", i+1)) {
			t.Fatal("missing row address")
		}
		for _, r := range lines[i] {
			if unicode.IsControl(r) {
				t.Fatalf("unescaped terminal control in line: %q", lines[i])
			}
		}
	}
	return lines
}

func TestDashboardLayout80x24And40x12(t *testing.T) {
	for _, dim := range [][2]int{{80, 24}, {40, 12}, {132, 36}} {
		t.Run(fmt.Sprint(dim), func(t *testing.T) {
			s := fixture()
			w, h := dim[0], dim[1]
			output := render(s, w, h, time.Unix(1000, 0))
			lines := renderedLines(t, output, w, h)
			text := strings.Join(lines, "\n")
			for _, want := range []string{"3 sessions", "1 needs you", "Needs you", "work", "Enter open", "/ search", "? help"} {
				if !strings.Contains(text, want) {
					t.Errorf("missing %q:\n%s", want, text)
				}
			}
			for _, bad := range []string{"B:", "W:", "/src/", "/home/", "local:two"} {
				if strings.Contains(text, bad) {
					t.Errorf("internal chrome %q", bad)
				}
			}
			if !regexp.MustCompile(`(?m)> .*review`).MatchString(text) || strings.Index(text, "review") > strings.Index(text, "build") {
				t.Fatal("selection or initial attention order lost")
			}
			if !strings.Contains(lines[h-2], "Enter open") || !strings.Contains(lines[h-1], "q quit") {
				t.Fatal("footer moved")
			}
		})
	}
}
func TestDashboardNarrowUnicodeAndControlFields(t *testing.T) {
	s := fixture()
	s.machines["local"] = machine{sessions: []eggclient.LocalSession{session("unsafe", "界界界\x1b[2J\n", "/界/项目\t", "working")}, err: "failure\x1b]52;forged\a\n"}
	s.reconcile()
	for _, dim := range [][2]int{{40, 12}, {12, 7}, {1, 1}, {0, 0}} {
		renderedLines(t, render(s, dim[0], dim[1], time.Now()), max(1, dim[0]), max(1, dim[1]))
	}
}
func TestDashboardScrollKeepsSelectionVisible(t *testing.T) {
	s := fixture()
	m := s.machines["local"]
	for i := 0; i < 50; i++ {
		m.sessions = append(m.sessions, session(fmt.Sprintf("id-%02d", i), fmt.Sprintf("name-%02d", i), "/src/app", "idle"))
	}
	s.machines["local"] = m
	s.selected = "local:id-49"
	for _, dim := range [][2]int{{40, 12}, {80, 24}, {132, 36}} {
		text := strings.Join(renderedLines(t, render(s, dim[0], dim[1], time.Now()), dim[0], dim[1]), "\n")
		if !regexp.MustCompile(`(?m)> .*name 49`).MatchString(text) || !strings.Contains(text, "of 53 sessions") {
			t.Fatalf("selection or position lost:\n%s", text)
		}
	}
}
func TestDashboardRendersAgeStatusAndPrompts(t *testing.T) {
	s := fixture()
	s.machines["local"] = machine{sessions: []eggclient.LocalSession{session("aged", "old", "/src/app", "done")}, updated: time.Unix(100, 0)}
	s.reconcile()
	s.selected = "local:aged"
	text := render(s, 80, 24, time.Unix(220, 0))
	if !regexp.MustCompile(`> .{0,120}old`).MatchString(text) || !strings.Contains(text, "4m") {
		t.Fatal("cached uptime lost")
	}
	for _, test := range []struct {
		mode mode
		want string
	}{{filter, "Search sessions /"}, {rename, "Name: "}, {newAgent, "Agent (blank = shell): "}, {newCWD, "Working directory"}, {confirmStop, "old on This machine"}} {
		s.mode, s.target, s.input = test.mode, row{LocalSession: session("aged", "old", "/src/app", "done"), machine: "local"}, "input"
		if !strings.Contains(render(s, 80, 24, time.Now()), test.want) {
			t.Errorf("missing %q", test.want)
		}
	}
	s.mode = confirmStop
	if text = render(s, 80, 24, time.Now()); !strings.Contains(text, "Enter keep running") || !strings.Contains(text, "y stop session") || !strings.Contains(text, "ID: aged") || !strings.Contains(text, "app") || strings.Contains(text, "local:aged") {
		t.Fatal("confirmation target or default lost")
	}
	if age(-1) != "0s" || age(3600) != "1h" || age(86400) != "1d" {
		t.Fatal("age boundaries changed")
	}
}
func TestDashboardAllStatusesAreVisible(t *testing.T) {
	s := &state{machines: map[string]machine{"local": {sessions: []eggclient.LocalSession{session("1", "working", "/src", "working"), session("2", "blocked", "/src", "blocked"), session("3", "idle", "/src", "idle"), session("4", "done", "/src", "done"), session("5", "exited", "/src", "exited"), session("6", "unknown", "/src", "future-status"), session("7", "failure", "/src", "failed")}}}}
	s.reconcile()
	text := ""
	for _, r := range s.rows() {
		s.selected = r.key()
		text += render(s, 80, 24, time.Now())
	}
	for _, want := range []string{"! Needs you", "* Working", "o Idle", "+ Done", "- Ended", "x Failed", "? Unknown"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
}
func TestDashboardFreshnessDetailsHelpAndAttention(t *testing.T) {
	s := fixture()
	now := time.Unix(500, 0)
	m := s.machines["work"]
	m.updated, m.attempted, m.err = now.Add(-5*time.Minute), now, "query failed\x1b]52;c;secret\a"
	s.machines["work"] = m
	s.remotes = map[string]config.Remote{"work": {SSHTarget: "fake-forge", WTBinary: "/opt/wing thing/wt", WingthingDir: "/remote/state"}}
	s.selected = "work:one"
	text := render(s, 80, 24, now)
	for _, want := range []string{"work unreachable", "Last seen 5m ago", "remote · ", "? Last seen", "R reconnect", "? help"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
	drive(s, "i")
	detail := strings.Join(renderedLines(t, render(s, 80, 24, now), 80, 24), "\n")
	for _, want := range []string{"Machine: work", "Project: app", "Permissions:", "Esc, then R to reconnect."} {
		if !strings.Contains(detail, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{"work:one", "/home/me/app", "fake-forge", "/remote/state", "secret"} {
		if strings.Contains(detail, bad) {
			t.Errorf("internal field %q", bad)
		}
	}
	drive(s, "\x1b?")
	text = render(s, 80, 24, now)
	for _, want := range []string{"Move", "Open", "Find", "Manage", "Leave", "Ctrl+B then q", "Ctrl+B twice", "keeps sessions running", "b bell off", "h recent runs"} {
		if !strings.Contains(text, want) {
			t.Errorf("help missing %q", want)
		}
	}
}
func TestDashboardUnreadAndContentFreeLifecycleDetails(t *testing.T) {
	s := &state{machines: map[string]machine{"local": {}}}
	attentionApply(s, "local", attentionSession("working", 1, 0))
	r := attentionSession("completed", 2, 2)
	r.StateReason = "PRIVATE_TRANSCRIPT_TOKEN\x1b]52;payload\a"
	attentionApply(s, "local", r)
	for _, dim := range [][2]int{{80, 24}, {40, 12}} {
		text := strings.Join(renderedLines(t, render(s, dim[0], dim[1], time.Now()), dim[0], dim[1]), "\n")
		if !regexp.MustCompile(`(?m)^> o Idle +\* +agent`).MatchString(text) || !strings.Contains(text, "1 unread") || strings.Contains(text, "PRIVATE") {
			t.Fatalf("unread projection: %s", text)
		}
	}
	drive(s, "i")
	text := render(s, 100, 24, time.Now())
	for _, want := range []string{"State: o Idle", "Unread response", "replied", "Background work may still run"} {
		if !strings.Contains(text, want) {
			t.Errorf("details missing %q", want)
		}
	}
	if strings.Contains(text, "PRIVATE") || strings.Contains(s.message, "PRIVATE") {
		t.Fatal("native reason leaked provider content")
	}
}
func TestDashboardDetailsWrapUnicodeAndTinyPrompts(t *testing.T) {
	s := fixture()
	s.target = row{LocalSession: session("full-id", "界\u202e\x1bname", "/very/long/路径/with/a/complete/project/location", "blocked"), machine: "local"}
	s.mode = details
	text := strings.Join(renderedLines(t, render(s, 40, 12, time.Now()), 40, 12), "")
	if !strings.Contains(text, "location") || strings.Contains(text, s.target.CWD) || strings.Contains(text, "\u202e") {
		t.Fatal("unsafe or raw project field")
	}
	s.mode, s.input = rename, "edited"
	tiny := render(s, 40, 1, time.Now())
	if !strings.Contains(tiny, "Resize terminal") || !strings.Contains(tiny, "Esc back") {
		t.Fatal("tiny terminal recovery hint lost")
	}
	for _, dim := range [][2]int{{40, 12}, {20, 3}, {1, 1}} {
		renderedLines(t, render(s, dim[0], dim[1], time.Now()), dim[0], dim[1])
	}
}
func TestDashboardHelpAdvertisesRecentRuns(t *testing.T) {
	s := fixture()
	s.mode = help
	if !strings.Contains(render(s, 120, 32, time.Now()), "h recent runs") {
		t.Fatal("history help missing")
	}
}
