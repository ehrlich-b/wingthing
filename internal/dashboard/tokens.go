package dashboard

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Paper is a cream sheet with ink, red pencil, slate and moss. Basic
// terminals retain their canvas; labels and marks carry the same meaning.
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

// Extended-color terminals render the same paper sheet in either host theme.
func (t terminalTheme) sheet() string {
	if t.dumb || t.colors < 256 {
		return ""
	}
	if t.colors == 1<<24 {
		return "\x1b[48;2;243;237;224;38;2;31;26;21m"
	}
	return "\x1b[48;5;230;38;5;234m"
}
func (t terminalTheme) style(role tone) string {
	if t.dumb {
		return ""
	}
	if t.colors == 0 {
		if role == selection {
			return "\x1b[7m"
		}
		return ""
	}
	if t.colors == 8 {
		if role == selection {
			return "\x1b[39;7m"
		}
		return "\x1b[39m"
	}
	if role == selection {
		if t.colors == 1<<24 {
			return "\x1b[48;2;31;26;21;38;2;243;237;224m"
		}
		return "\x1b[48;5;234;38;5;230m"
	}
	colors := []int{234, 239, 24, 65, 130, 130, 246}
	rgb := [][3]int{{31, 26, 21}, {90, 80, 71}, {47, 76, 107}, {78, 106, 72}, {155, 58, 29}, {155, 58, 29}, {168, 156, 134}}
	if t.colors == 1<<24 {
		c := rgb[role]
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c[0], c[1], c[2])
	}
	return fmt.Sprintf("\x1b[38;5;%dm", colors[role])
}
func (t terminalTheme) selectedState(role tone) string {
	if t.colors == 8 {
		return "\x1b[39m"
	}
	if t.colors < 256 {
		return t.style(role)
	}
	rgb := [][3]int{{243, 237, 224}, {203, 192, 173}, {199, 214, 230}, {188, 211, 175}, {243, 198, 181}, {243, 198, 181}, {200, 188, 166}}
	colors := []int{230, 250, 153, 151, 217, 217, 250}
	if t.colors == 1<<24 {
		c := rgb[role]
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c[0], c[1], c[2])
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
