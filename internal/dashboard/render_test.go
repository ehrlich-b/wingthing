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
var styling = regexp.MustCompile(`\x1b\[(?:0|7)m`)

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
	for _, dimensions := range [][2]int{{80, 24}, {40, 12}} {
		t.Run(fmt.Sprint(dimensions), func(t *testing.T) {
			s := fixture()
			width, height := dimensions[0], dimensions[1]
			output := render(s, width, height, time.Unix(1000, 0))
			lines := renderedLines(t, output, width, height)
			if lines[0] != "wt | 3 sessions | 2 machines" || !strings.HasPrefix(lines[1], "B:1 W:1 I:1 D:0 E:0 F:0 ?:0") {
				t.Fatalf("counts = %v", lines[:2])
			}
			text := strings.Join(lines, "\n")
			for _, want := range []string{"local", "/src/app", "B review", "codex", "src/app", "2m", "work", "/home/me/app", "Enter attach", "x stop", "/ filter"} {
				if !strings.Contains(text, want) {
					t.Errorf("layout missing %q:\n%s", want, text)
				}
			}
			if !strings.Contains(output, "\x1b[7m> B") || strings.Index(text, "review") > strings.Index(text, "build") {
				t.Fatal("selection or blocked-first order was lost")
			}
		})
	}
}

func TestDashboardNarrowUnicodeAndControlFields(t *testing.T) {
	s := fixture()
	s.machines["local"] = machine{sessions: []eggclient.LocalSession{
		session("unsafe", "界界界\x1b[2J\n", "/界/项目\t", "working"),
	}, err: "failure\x1b]52;forged\a\n"}
	s.reconcile()
	for _, dimensions := range [][2]int{{40, 12}, {12, 7}, {1, 1}, {0, 0}} {
		width, height := max(1, dimensions[0]), max(1, dimensions[1])
		output := render(s, dimensions[0], dimensions[1], time.Now())
		renderedLines(t, output, width, height)
	}
}

func TestDashboardScrollKeepsSelectionVisible(t *testing.T) {
	s := fixture()
	local := s.machines["local"]
	for i := 0; i < 50; i++ {
		local.sessions = append(local.sessions, session(fmt.Sprintf("id-%02d", i), fmt.Sprintf("name-%02d", i), "/src/app", "idle"))
	}
	s.machines["local"] = local
	s.selected = "local:id-49"
	text := strings.Join(renderedLines(t, render(s, 40, 12, time.Now()), 40, 12), "\n")
	if !strings.Contains(text, "> I name-49") {
		t.Fatalf("selected row scrolled offscreen:\n%s", text)
	}
}

func TestDashboardRendersAgeStatusAndPrompts(t *testing.T) {
	s := fixture()
	s.machines["local"] = machine{sessions: []eggclient.LocalSession{session("aged", "old", "/src/app", "done")}, updated: time.Unix(100, 0)}
	s.reconcile()
	text := render(s, 80, 24, time.Unix(220, 0))
	if !strings.Contains(text, "> D old") || !strings.Contains(text, "4m") {
		t.Fatal("cached uptime did not advance")
	}
	for _, test := range []struct {
		mode mode
		want string
	}{{filter, "Filter: /"}, {rename, "Name: "}, {newAgent, "Agent (blank = shell): "}, {newCWD, "Cwd on local:"}, {confirmStop, "Stop local:aged (old)? [y/N]"}} {
		s.mode, s.target, s.input = test.mode, s.rows()[0], "input"
		if !strings.Contains(render(s, 80, 24, time.Now()), test.want) {
			t.Errorf("mode %v missing prompt %q", test.mode, test.want)
		}
	}
	s.mode = confirmStop
	if !strings.Contains(render(s, 80, 24, time.Now()), "y stop  Enter/Esc cancel") {
		t.Fatal("stop guidance implied Enter accepts")
	}
	if age(-1) != "0s" || age(3600) != "1h" || age(86400) != "1d" {
		t.Fatal("age boundaries changed")
	}
}

func TestDashboardAllStatusesAreVisible(t *testing.T) {
	s := &state{machines: map[string]machine{"local": {sessions: []eggclient.LocalSession{
		session("1", "working", "/src", "working"), session("2", "blocked", "/src", "blocked"),
		session("3", "idle", "/src", "idle"), session("4", "done", "/src", "done"),
		session("5", "exited", "/src", "exited"), session("6", "unknown", "/src", "future-status"), session("7", "failure", "/src", "failed"),
	}}}}
	s.reconcile()
	text := render(s, 80, 24, time.Now())
	for _, want := range []string{"B:1 W:1 I:1 D:1 E:1 F:1 ?:1", "W working", "B blocked", "I idle", "D done", "E exited", "F failure", "? unknown"} {
		if !strings.Contains(text, want) {
			t.Errorf("inventory missing %q", want)
		}
	}
}

func TestDashboardFreshnessDetailsHelpAndAttention(t *testing.T) {
	s := fixture()
	now := time.Unix(500, 0)
	m := s.machines["work"]
	m.updated, m.err = now.Add(-5*time.Minute), "query failed\x1b]52;c;secret\a"
	s.machines["work"] = m
	s.remotes = map[string]config.Remote{"work": {SSHTarget: "fake-forge", WTBinary: "/opt/wing thing/wt", WingthingDir: "/remote/state"}}
	s.selected = "work:one"
	text := render(s, 80, 24, now)
	for _, want := range []string{"offline", "last success 5m ago", "stale", "> I remote", "! attention", "i details", "? help"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
	drive(s, "i")
	detail := strings.Join(renderedLines(t, render(s, 80, 24, now), 80, 24), "\n")
	for _, want := range []string{"work:one", "/home/me/app", "fake-forge", "/opt/wing thing/wt", "/remote/state", "Isolation:", "Readers:", "Writer:"} {
		if !strings.Contains(detail, want) {
			t.Errorf("details missing %q:\n%s", want, detail)
		}
	}
	drive(s, "\x1b?")
	text = render(s, 80, 24, now)
	for _, want := range []string{"Ctrl+B then q", "Ctrl+B twice", "B blocked", "F failed", "wt remote add", "keeps sessions running"} {
		if !strings.Contains(text, want) {
			t.Errorf("help missing %q", want)
		}
	}
	for _, unsupported := range []string{"v observe", "t take control", "h recent", "unread"} {
		if strings.Contains(text, unsupported) {
			t.Errorf("unsupported control %q", unsupported)
		}
	}
}

func TestDashboardDetailsWrapUnicodeAndTinyPrompts(t *testing.T) {
	s := fixture()
	s.target = row{LocalSession: session("full-id", "界\u202e\x1bname", "/very/long/路径/with/a/complete/project/location", "blocked"), machine: "local"}
	s.mode = details
	lines := renderedLines(t, render(s, 40, 12, time.Now()), 40, 12)
	text := strings.Join(lines, "")
	if !strings.Contains(text, s.target.CWD) {
		t.Fatalf("full cwd lost: %s", text)
	}
	if strings.Contains(text, "\u202e") {
		t.Fatal("bidi control escaped sanitation")
	}
	s.mode, s.input = rename, "edited"
	tiny := render(s, 40, 1, time.Now())
	if !strings.Contains(tiny, "Name: edited") || !strings.Contains(tiny, "Esc/Ctrl-C") {
		t.Fatal("tiny prompt lost escape guidance")
	}
	for _, dimensions := range [][2]int{{40, 12}, {20, 3}, {1, 1}} {
		renderedLines(t, render(s, dimensions[0], dimensions[1], time.Now()), dimensions[0], dimensions[1])
	}
}
