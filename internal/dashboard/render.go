package dashboard

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/mattn/go-runewidth"
)

func clean(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return '?'
		}
		return r
	}, text)
}

func clip(text string, width int) string {
	return runewidth.Truncate(clean(text), max(0, width), "…")
}

func field(text string, width int) string {
	text = clip(text, width)
	return text + strings.Repeat(" ", max(0, width-runewidth.StringWidth(text)))
}

func statusLetter(status string) string {
	switch status {
	case "working":
		return "W"
	case "blocked":
		return "B"
	case "idle":
		return "I"
	case "done":
		return "D"
	case "exited":
		return "E"
	default:
		return "?"
	}
}

func shortCWD(cwd string) string {
	if cwd == "" {
		return "-"
	}
	cwd = path.Clean(cwd)
	parent := path.Base(path.Dir(cwd))
	if parent == "/" || parent == "." {
		return cwd
	}
	return parent + "/" + path.Base(cwd)
}

func age(seconds int64) string {
	seconds = max(0, seconds)
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm", seconds/60)
	case seconds < 86400:
		return fmt.Sprintf("%dh", seconds/3600)
	default:
		return fmt.Sprintf("%dd", seconds/86400)
	}
}

func rowText(r row, width int, seconds int64) string {
	// All columns remain available at 40 cells. Smaller terminals truncate the
	// compact row; the status and name are always the first visible fields.
	available := max(0, width-4)
	ageWidth := min(5, available/6)
	agentWidth := min(12, available/5)
	cwdWidth := available / 3
	nameWidth := max(0, available-ageWidth-agentWidth-cwdWidth-3)
	agent := r.Agent
	if agent == "" {
		agent = r.Kind
	}
	return "  " + statusLetter(r.Status) + " " + field(r.name(), nameWidth) + " " +
		field(agent, agentWidth) + " " + field(shortCWD(r.CWD), cwdWidth) + " " + field(age(seconds), ageWidth)
}

type viewLine struct {
	text     string
	selected bool
}

// render is pure: it only uses the inventory snapshot and a supplied clock.
// Cursor addressing avoids newline-driven scrolling at the bottom edge.
func render(s *state, width, height int, now time.Time) string {
	width, height = max(1, width), max(1, height)
	rows := s.rows()
	counts := make(map[string]int)
	total := 0
	names := make([]string, 0, len(s.machines))
	for name, machine := range s.machines {
		names = append(names, name)
		for _, session := range machine.sessions {
			counts[statusLetter(session.Status)]++
			total++
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == "local" || names[j] == "local" {
			return names[i] == "local"
		}
		return names[i] < names[j]
	})
	header := []string{
		fmt.Sprintf("wt | %d sessions | %d machines", total, len(names)),
		fmt.Sprintf("B:%d W:%d I:%d D:%d E:%d ?:%d", counts["B"], counts["W"], counts["I"], counts["D"], counts["E"], counts["?"]),
	}
	var body []viewLine
	selectedLine := 0
	for _, name := range names {
		machine := s.machines[name]
		label := name
		if machine.loading {
			label += " (refreshing)"
		}
		body = append(body, viewLine{text: label})
		if machine.err != "" {
			body = append(body, viewLine{text: "  ! " + machine.err})
		}
		project, found := "", false
		for _, r := range rows {
			if r.machine != name {
				continue
			}
			found = true
			if r.project() != project {
				project = r.project()
				body = append(body, viewLine{text: "  " + project})
			}
			seconds := r.UptimeSecs
			if !machine.updated.IsZero() {
				seconds += max(0, int64(now.Sub(machine.updated)/time.Second))
			}
			line := viewLine{text: rowText(r, width, seconds), selected: r.key() == s.selected}
			if line.selected {
				selectedLine = len(body)
				line.text = ">" + line.text[1:]
			}
			body = append(body, line)
		}
		if !found && machine.err == "" {
			body = append(body, viewLine{text: "  (no sessions)"})
		}
	}
	footer := []string{"arrows/j/k move  Enter attach  n new  r rename  x stop  / filter  q/Ctrl-C quit"}
	if width < 75 {
		footer = []string{"arrows/j/k move Enter attach n new", "r rename x stop / filter q/Ctrl-C quit"}
	}
	message := s.message
	if s.filter != "" && s.mode != filter && message == "" {
		message = "/" + s.filter + fmt.Sprintf(" (%d/%d)", len(rows), total)
	}
	switch s.mode {
	case filter:
		message = "Filter: /" + s.input + "_"
	case rename:
		message = "Name: " + s.input + "_"
	case newAgent:
		message = "Agent (blank = shell): " + s.input + "_"
	case newCWD:
		message = "Cwd on " + s.target.machine + ": " + s.input + "_"
	case confirmStop:
		message = "Stop " + s.target.key() + "? [y/N]"
	}
	footer = append(footer, message)
	if s.mode != navigate && s.message != "" {
		footer = append(footer, s.message)
	}
	// Preserve the current prompt even when the terminal is only a few rows.
	if len(footer) > height {
		footer = footer[len(footer)-height:]
	}
	header = header[:min(len(header), height-len(footer))]
	bodyHeight := max(0, height-len(header)-len(footer))
	start := max(0, min(selectedLine-bodyHeight/2, len(body)-bodyHeight))
	lines := make([]viewLine, 0, height)
	for _, text := range header {
		lines = append(lines, viewLine{text: text})
	}
	for i := 0; i < bodyHeight; i++ {
		line := viewLine{}
		if start+i < len(body) {
			line = body[start+i]
		}
		lines = append(lines, line)
	}
	for _, text := range footer {
		lines = append(lines, viewLine{text: text})
	}
	var output strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&output, "\x1b[%d;1H\x1b[2K", i+1)
		if line.selected {
			output.WriteString("\x1b[7m")
		}
		output.WriteString(clip(line.text, width))
		output.WriteString("\x1b[0m")
	}
	return output.String()
}
