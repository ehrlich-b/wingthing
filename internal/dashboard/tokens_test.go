package dashboard

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestTerminalPaletteFallbacks(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	os.Unsetenv("NO_COLOR")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "")
	t.Setenv("WT_TUI_THEME", "light")
	// Extended palettes keep the terminal's canvas and use mid-tone indexes.
	p := detectTheme()
	if p.colors != 256 || !p.light || p.style(warning) != "\x1b[0;38;5;136m" || p.style(plain) != "\x1b[0m" {
		t.Fatal(p)
	}
	t.Setenv("COLORTERM", "truecolor")
	if detectTheme().style(failure) != "\x1b[0;38;5;131m" {
		t.Fatal("truecolor role lost")
	}
	for role := plain; role <= caution; role++ {
		if strings.Contains(detectTheme().style(role), "48;") {
			t.Fatal("role painted the terminal canvas")
		}
	}
	t.Setenv("NO_COLOR", "")
	if p = detectTheme(); p.style(warning) != "\x1b[0m" || p.style(selection) != "\x1b[0;1m" {
		t.Fatal("NO_COLOR lost structural selection")
	}
}
func TestTerminalStatusHasShapeAndName(t *testing.T) {
	for _, s := range []string{"working", "blocked", "idle", "done", "exited", "failed", "unknown"} {
		label, _ := stateLabel(row{LocalSession: session("a", "Task", "/project", s)})
		if len(strings.Fields(label)) < 2 || strings.Contains(label, "\x1b") {
			t.Fatal(label)
		}
	}
}

func TestBasicTerminalKeepsLabelsInNeutralInk(t *testing.T) {
	if (terminalTheme{colors: 8}).style(accent) != "\x1b[0m" || (terminalTheme{colors: 8, light: true}).style(accent) != "\x1b[0m" {
		t.Fatal("ANSI accent must remain readable")
	}
}

func TestDumbTerminalHasNoStyles(t *testing.T) {
	t.Setenv("TERM", "dumb")
	if detectTheme().style(selection) != "" {
		t.Fatal("dumb terminal received styles")
	}
}

func TestBasicSelectionCannotSpreadStateColorAcrossRow(t *testing.T) {
	s := fixture()
	r, _ := s.selection()
	s.selected = r.key()
	line := sessionLine(s, r, 80, time.Now())
	output := paintLine(terminalTheme{colors: 8}, line, 80)
	if strings.Contains(output, "7m") || !strings.Contains(output, "\x1b[0;1;33m! Needs you") || !strings.HasPrefix(output, "\x1b[0;1m> ") {
		t.Fatalf("ANSI selection spread the warning color, filled the row or lost its label: %q", output)
	}
}

func TestNoColorSelectionStopsAtPaneBorder(t *testing.T) {
	s := fixture()
	r, _ := s.selection()
	s.selected = r.key()
	s.theme = terminalTheme{}
	for _, line := range strings.Split(render(s, 132, 36, time.Now()), "\n") {
		at := strings.Index(line, "\x1b[0;1m> ")
		if at < 0 {
			continue
		}
		rest := line[at:]
		border := strings.Index(rest, "│")
		if border < 0 || !strings.Contains(rest[:border], "\x1b[0m") {
			t.Fatalf("selection ran into the right pane: %q", rest)
		}
		return
	}
	t.Fatal("selected row not found")
}
