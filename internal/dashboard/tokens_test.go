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
	// Extended palettes use the Paper sheet on either host theme.
	p := detectTheme()
	if p.colors != 256 || !p.light || p.style(warning) != "\x1b[38;5;130m" {
		t.Fatal(p)
	}
	t.Setenv("COLORTERM", "truecolor")
	if !strings.Contains(detectTheme().style(failure), "155;58;29") {
		t.Fatal("truecolor role lost")
	}
	t.Setenv("NO_COLOR", "")
	if p = detectTheme(); p.style(warning) != "" || p.style(selection) != "\x1b[7m" {
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
	if (terminalTheme{colors: 8}).style(accent) != "\x1b[39m" || (terminalTheme{colors: 8, light: true}).style(accent) != "\x1b[39m" {
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
	if strings.Contains(output, "[31m") || !strings.Contains(output, "[39;7m") || !strings.Contains(output, "! Needs you") {
		t.Fatal("ANSI selection spread the warning color or lost its label")
	}
}
