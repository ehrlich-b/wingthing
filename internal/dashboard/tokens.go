package dashboard

import (
	"os"
	"strconv"
	"strings"
)

// Text keeps the terminal's own ink and canvas. Secondary text is dim gray,
// one muted blue marks focus and primary keys, and status colors stay on
// status cells. Basic terminals rely on labels, marks and bold.
type tone uint8

const (
	plain tone = iota
	secondary
	accent
	success
	warning
	failure
	separator
	selection
	caution
)

type terminalTheme struct {
	light  bool
	colors int
	dumb   bool
	ascii  bool
}

func detectTheme() terminalTheme {
	locale := os.Getenv("LC_ALL")
	if locale == "" {
		locale = os.Getenv("LC_CTYPE")
	}
	if locale == "" {
		locale = os.Getenv("LANG")
	}
	ascii := os.Getenv("WT_TUI_ASCII") == "1" || !strings.Contains(strings.ToLower(locale), "utf")
	if os.Getenv("TERM") == "dumb" {
		return terminalTheme{dumb: true, ascii: true}
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return terminalTheme{ascii: ascii}
	}
	t := terminalTheme{colors: 8, ascii: ascii}
	if strings.Contains(os.Getenv("TERM"), "256color") {
		t.colors = 256
	}
	if c := os.Getenv("COLORTERM"); c == "truecolor" || c == "24bit" {
		t.colors = 1 << 24
	}
	// COLORFGBG is supplied by terminals that expose their base palette.
	parts := strings.Split(os.Getenv("COLORFGBG"), ";")
	if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
		t.light = n == 7 || n == 15
	}
	switch os.Getenv("WT_TUI_THEME") {
	case "light":
		t.light = true
	case "dark":
		t.light = false
	}
	return t
}

func (t terminalTheme) style(role tone) string {
	if t.dumb {
		return ""
	}
	// NO_COLOR keeps attributes; every role resets so bold cannot bleed past a cell.
	if t.colors == 0 {
		if role == selection {
			return "\x1b[0;1m"
		}
		return "\x1b[0m"
	}
	if t.colors == 8 {
		return "\x1b[" + ansiRoles[role] + "m"
	}
	return "\x1b[" + extendedRoles[role] + "m"
}

// The selected row keeps each status cell's color and adds bold.
func (t terminalTheme) selectedState(role tone) string {
	if t.colors == 0 {
		return t.style(selection)
	}
	return strings.Replace(t.style(role), "\x1b[0", "\x1b[0;1", 1)
}

// Basic terminals: needs you and last seen yellow, failed red, done green.
// The accent stays in the terminal's own ink; blue is unreadable on some canvases.
var ansiRoles = []string{"0", "0;2", "0", "0;32", "0;33", "0;31", "0;2", "0;1", "0;33"}

// Extended palettes pick mid-tone indexes that read on light and dark canvases.
var extendedRoles = []string{"0", "0;38;5;244", "0;38;5;67", "0;38;5;65", "0;38;5;136", "0;38;5;131", "0;38;5;244", "0;1;38;5;67", "0;38;5;136"}

func stateLabel(r row) (string, tone) {
	switch r.Status {
	case "blocked":
		return "! Needs you", warning
	case "working":
		return "* Working", plain
	case "idle":
		return "o Idle", secondary
	case "done":
		return "+ Done", success
	case "exited":
		return "- Ended", secondary
	case "failed":
		return "x Failed", failure
	default:
		return "? Unknown", secondary
	}
}
