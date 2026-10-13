package dashboard

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Semantic terminal roles share the app palette without painting the user's
// base foreground or background. Labels and ASCII marks also carry meaning.
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
)

type terminalTheme struct {
	light  bool
	colors int
	dumb   bool
}

func detectTheme() terminalTheme {
	if os.Getenv("TERM") == "dumb" {
		return terminalTheme{dumb: true}
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return terminalTheme{}
	}
	t := terminalTheme{colors: 8}
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
	if t.dumb || role == plain {
		return ""
	}
	if t.colors == 0 {
		if role == selection {
			return "\x1b[7m"
		}
		return ""
	}
	dark := []int{0, 250, 153, 151, 222, 217, 242, 24}
	light := []int{0, 240, 24, 22, 94, 124, 244, 153}
	ansi := []int{0, 39, 36, 32, 33, 31, 39, 7}
	if t.colors == 8 {
		if t.light && role == accent {
			return "\x1b[34m"
		}
		return fmt.Sprintf("\x1b[%dm", ansi[role])
	}
	colors := dark
	if t.light {
		colors = light
	}
	if role == selection {
		fg := 255
		if t.light {
			fg = 235
		}
		return fmt.Sprintf("\x1b[48;5;%d;38;5;%dm", colors[role], fg)
	}
	if t.colors == 1<<24 {
		darkRGB := [][3]int{{}, {178, 189, 197}, {173, 204, 224}, {156, 213, 179}, {243, 202, 120}, {255, 169, 174}, {60, 69, 77}}
		lightRGB := [][3]int{{}, {89, 99, 106}, {49, 91, 118}, {36, 98, 70}, {128, 80, 0}, {164, 45, 50}, {216, 222, 217}}
		rgb := darkRGB[role]
		if t.light {
			rgb = lightRGB[role]
		}
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", rgb[0], rgb[1], rgb[2])
	}
	return fmt.Sprintf("\x1b[38;5;%dm", colors[role])
}

func stateLabel(r row) (string, tone) {
	switch r.Status {
	case "blocked":
		return "! Needs you", warning
	case "working":
		return "* Working", accent
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
