package dashboard

import (
	"os"
	"strings"
	"testing"
)

func TestTerminalPaletteFallbacks(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	os.Unsetenv("NO_COLOR")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "")
	t.Setenv("WT_TUI_THEME", "light")
	// Explicit theme changes roles, never the terminal's base canvas.
	p := detectTheme()
	if p.colors != 256 || !p.light || p.style(warning) != "\x1b[38;5;94m" {
		t.Fatal(p)
	}
	t.Setenv("COLORTERM", "truecolor")
	if !strings.Contains(detectTheme().style(failure), "164;45;50") {
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

func TestTerminalANSIRolesRespectTheme(t *testing.T) {
	if (terminalTheme{colors: 8}).style(accent) != "\x1b[36m" || (terminalTheme{colors: 8, light: true}).style(accent) != "\x1b[34m" {
		t.Fatal("ANSI accent must follow theme")
	}
}

func TestDumbTerminalHasNoStyles(t *testing.T) {
	t.Setenv("TERM", "dumb")
	if detectTheme().style(selection) != "" {
		t.Fatal("dumb terminal received styles")
	}
}
