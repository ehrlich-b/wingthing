package dashboard

import (
	"fmt"
	"path"
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
	case "failed":
		return "F"
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
	status := statusLetter(r.Status)
	if r.attention == "unread" {
		status += "*"
	}

	// All columns remain available at 40 cells. Smaller terminals truncate the
	// compact row; the status and name are always the first visible fields.
	available := max(0, width-3-runewidth.StringWidth(status))
	ageWidth := min(5, available/6)
	agentWidth := min(12, available/5)
	cwdWidth := available / 3
	cwd := shortCWD(r.CWD)
	if width < 60 {
		ageWidth = min(4, available/6)
		agentWidth = min(6, available/5)
		cwdWidth = available / 4
		if r.CWD != "" {
			cwd = path.Base(path.Clean(r.CWD))
		}
	}
	nameWidth := max(0, available-ageWidth-agentWidth-cwdWidth-3)
	agent := r.Agent
	if agent == "" {
		agent = r.Kind
	}
	return "  " + status + " " + field(r.name(), nameWidth) + " " +
		field(agent, agentWidth) + " " + field(cwd, cwdWidth) + " " + field(age(seconds), ageWidth)
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
	names := s.machineNames()
	attention := 0
	unread := 0
	for name, machine := range s.machines {
		for _, session := range machine.sessions {
			counts[statusLetter(session.Status)]++
			total++
			attentionKind := s.sessionRow(name, session).attention
			if attentionKind == "unread" {
				unread++
			}
			if needsAttention(attentionKind) {
				attention++
			}
		}
	}
	scope := s.machineFilter
	if scope == "" {
		scope = "All"
	}
	if s.attentionOnly {
		scope += " ! attention"
	}
	header := []string{
		fmt.Sprintf("wt | %d sessions | %d machines", total, len(names)),
		fmt.Sprintf("B:%d W:%d I:%d D:%d E:%d F:%d ?:%d U:%d | [%s] !:%d bell:%s", counts["B"], counts["W"], counts["I"], counts["D"], counts["E"], counts["F"], counts["?"], unread, scope, attention, s.bellSetting()),
	}
	var body []viewLine
	selectedLine := 0
	for _, name := range names {
		if s.machineFilter != "" && name != s.machineFilter {
			continue
		}
		machine := s.machines[name]
		label := name + "  " + connection(machine, now)
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
			rowWidth := width
			suffix := ""
			if machine.err != "" {
				suffix = " stale"
				rowWidth -= len(suffix)
			}
			line := viewLine{text: rowText(r, rowWidth, seconds) + suffix, selected: r.key() == s.selected}
			if line.selected {
				selectedLine = len(body)
				line.text = ">" + line.text[1:]
			}
			body = append(body, line)
		}
		if !found {
			text := "  (no sessions)"
			if s.filter != "" || s.attentionOnly {
				text = "  (no matches)"
			}
			if machine.err != "" && machine.updated.IsZero() {
				text = "  (inventory unavailable)"
			}
			body = append(body, viewLine{text: text})
		}
	}
	footer := []string{"arrows/j/k move  Enter attach  v view  t takeover  n new  r rename  x stop",
		"/ filter ! attention b bell m machines i details ? help R refresh q/Ctrl-C quit h recent"}
	if s.firstRun() && !s.recent {
		for _, line := range s.firstRunLines() {
			body = append(body, viewLine{text: line})
		}
	}
	if width < 75 {
		footer = []string{"Enter attach v view t take n r x stop", "/ filter ! attention b bell m i ? R q h"}
	}
	if s.recent {
		header, body, selectedLine, footer = s.runView(now)
	}
	if s.mode == machinePicker {
		body = []viewLine{{text: "Machines (Enter selects)"}}
		choices := append([]string{""}, names...)
		for i, name := range choices {
			text := "All machines"
			if name != "" {
				text = name + "  " + connection(s.machines[name], now)
			}
			prefix := "  "
			if i == s.picker {
				prefix = "> "
				selectedLine = len(body)
			}
			body = append(body, viewLine{text: prefix + text, selected: i == s.picker})
		}
		footer = []string{"j/k move  Enter select  Esc cancel"}
	} else if s.mode == help || s.mode == details || s.mode == runDetails || s.mode == runResult {
		body = nil
		for _, line := range overlayLines(s, now) {
			for _, wrapped := range wrap(line, width) {
				body = append(body, viewLine{text: wrapped})
			}
		}
		footer = []string{"j/k scroll  Esc/Enter back  Ctrl-C quit"}
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
		message = "Stop " + s.target.key() + " (" + s.target.name() + ")? [y/N]"
	case confirmTakeover:
		message = "Take input control of " + s.target.key() + " from " + s.target.WriterOwner + "? [y/N]"
	}
	if s.mode == help || s.mode == details || s.mode == runDetails || s.mode == runResult || s.mode == machinePicker {
		message = ""
	}
	if s.mode != navigate && s.mode != help && s.mode != details && s.mode != machinePicker && s.mode != runFilter && s.mode != runDetails && s.mode != runResult {
		footer = []string{"Enter accept  Esc cancel  Ctrl-C quit", message}
		if s.mode == confirmStop {
			footer[0] = "y stop  Enter/Esc cancel  Ctrl-C quit"
		}
		if s.mode == confirmTakeover {
			footer[0] = "y take control  Enter/Esc cancel  Ctrl-C quit"
		}
	} else if message != "" {
		// Keep the existing sanitization while making bounded launch metadata
		// readable in the message area at ordinary terminal widths.
		footer = append(footer, wrap(message, width)...)
	}
	if s.mode > navigate && s.mode < machinePicker && s.message != "" {
		footer = append(footer, s.message)
	}
	if s.mode >= agentChooser {
		body = nil
		for _, line := range s.onboardingLines() {
			for _, wrapped := range wrap(line, width) {
				body = append(body, viewLine{text: wrapped})
			}
		}
		selectedLine = 0
		footer = []string{"Enter accept  Esc cancel  Ctrl-C quit"}
	}
	// Preserve the prompt and escape guidance at tiny terminal heights.
	if s.mode > navigate && s.mode < machinePicker && height <= 2 {
		footer = []string{message, "Esc cancel Ctrl-C quit"}
		if height == 1 {
			footer = []string{clip(message, max(0, width-11)) + " Esc/Ctrl-C"}
		}
	}
	// Preserve the current prompt even when the terminal is only a few rows.
	if len(footer) > height {
		footer = footer[len(footer)-height:]
	}
	header = header[:min(len(header), height-len(footer))]
	bodyHeight := max(0, height-len(header)-len(footer))
	start := max(0, min(selectedLine-bodyHeight/2, len(body)-bodyHeight))
	if s.mode == details || s.mode == help || s.mode == runDetails || s.mode == runResult {
		start = min(s.overlayOffset, max(0, len(body)-bodyHeight))
	}
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

// Freshness describes the query, independently of the provider lifecycle state.
func connection(m machine, now time.Time) string {
	text := "connecting"
	if m.err != "" {
		text = "offline"
		if !m.updated.IsZero() {
			text += "  last success " + age(int64(now.Sub(m.updated)/time.Second)) + " ago; cached rows stale"
		}
	} else if !m.updated.IsZero() {
		text = "connected  updated " + age(int64(now.Sub(m.updated)/time.Second)) + " ago"
	}
	if m.loading {
		text += " (refreshing)"
	}
	return text
}

// Wrap sanitized metadata by display cells so details expose complete values
// without allowing terminal controls or splitting wide characters at an edge.
func wrap(text string, width int) []string {
	var lines []string
	var line strings.Builder
	cells := 0
	for _, r := range clean(text) {
		size := runewidth.RuneWidth(r)
		if cells+size > width && line.Len() > 0 {
			lines = append(lines, line.String())
			line.Reset()
			cells = 0
		}
		line.WriteRune(r)
		cells += size
	}
	return append(lines, line.String())
}

func overlayLines(s *state, now time.Time) []string {
	if s.mode == runDetails || s.mode == runResult {
		return s.runOverlay()
	}
	if s.mode == help {
		return []string{
			"Help / status legend",
			"arrows or j/k: move; Enter: attach",
			"v: read-only (no input or resize); t: confirm preview takeover when ownership is reported",
			"Bracketed pastes are ignored in the navigator; attached pastes pass through literally.",
			"n: new on selected machine; r: rename; x: confirm stop",
			"/: filter machine, name, ID, agent, cwd or status",
			"!: attention only (native blocked, unread response or failed)",
			"b: bell " + s.bellSetting() + " (off by default; this dashboard only)",
			"Attached: Ctrl+B then q returns here; Ctrl+B twice sends Ctrl+B.",
			"Leaving wt keeps sessions running.",
			"m: All, local, or a saved machine (including empty/offline)",
			"i: full details; R: refresh; q or Ctrl-C: quit",
			"W working  B blocked  I idle  D done  E exited  F failed  ? unknown",
			"* unread foreground response; U counts unread. Details/attach acknowledge that response.",
			"Blocked clears on native evidence; unknown: missing evidence or disabled hooks.",
			"Connection/freshness is separate from session status; stale means a retained query result.",
			"Remote setup: wt remote add NAME SSH_TARGET; wt remote list",
			"h: recent runs; / filter; N: next page; o: bounded result",
			"Esc/Enter: back; j/k or PageUp/PageDown: scroll",
		}
	}
	r := s.target
	m := s.machines[r.machine]
	elapsed := r.UptimeSecs
	if !m.updated.IsZero() {
		elapsed += max(0, int64(now.Sub(m.updated)/time.Second))
	}
	isolation := r.Isolation
	if isolation == "" {
		isolation = "not reported"
	}
	agent := r.Agent
	if agent == "" {
		agent = r.Kind
	}
	lines := []string{"Details: " + r.key(), "Name: " + r.name(), "Status: " + r.Status,
		"Machine: " + r.machine + "  " + connection(m, now), "Cwd: " + r.CWD,
		"Attention: " + r.attention,
		fmt.Sprintf("Lifecycle source: %s  State cursor: %d", lifecycleSource(r.StateSource), r.StateCursor),
		"Reason: " + lifecycleDescription(r),
		"Agent/kind: " + agent + " / " + r.Kind, "Age: " + age(elapsed), "Isolation: " + isolation,
		fmt.Sprintf("Readers: %d", r.Readers),
	}
	if takeoverAvailable(r) {
		lines = append(lines, fmt.Sprintf("Writer: %s  Owner: %s  Epoch: %d", r.WriterID, r.WriterOwner, r.InputEpoch))
	} else {
		lines = append(lines, "Writer: ownership not reported for this channel; v observes without input or resize")
	}
	if r.completion > 0 {
		lines = append(lines, fmt.Sprintf("Completion cursor shown: %d", r.completion))
	}
	if r.machine == "local" {
		lines = append(lines, "Route: local; state: "+s.localDir)
	} else if remote, ok := s.remotes[r.machine]; ok {
		state := remote.WingthingDir
		if state == "" {
			state = "default remote state"
		}
		lines = append(lines, "SSH: "+remote.SSHTarget, "Executable: "+remote.Binary(), "State: "+state)
	}
	lines = append(lines, sessionPolicyLines(r)...)
	if m.err != "" {
		lines = append(lines, "Query error: "+m.err, "This row is stale; refresh before acting.")
	}
	ref := r.key()
	if r.machine == "local" {
		ref = r.ID
	}
	lines = append(lines, "Controls: Enter attach; v view; t confirm takeover; r rename; x confirm stop; n new", "Attach: wt attach -- "+ref)
	return lines
}

func lifecycleSource(source string) string {
	switch source {
	case "claude_hook", "codex_hook", "egg_process", "native_import", "unsupported":
		return source
	default:
		return "not reported"
	}
}

// Remote optional metadata is presentation input too: render descriptions
// from an allowlist instead of forwarding any provider-supplied reason.
func lifecycleDescription(r row) string {
	if r.attention == "unknown" {
		return "Native lifecycle evidence is missing, unknown or hooks are disabled; no screen inference."
	}
	if r.CompletionCursor > 0 || r.attention == "unread" {
		return "Foreground response completed; provider or descendants may continue."
	}
	if r.StateSource == "" && r.State == "" {
		return "Older inventory reports " + r.attention + "; lifecycle source and cursor are not reported."
	}
	switch r.State {
	case "needs_input":
		return "Native provider requested input; acknowledgement does not clear blocking."
	case "working", "starting":
		return "Native provider is working."
	case "idle":
		return "Native provider is idle."
	case "completed":
		return "Native provider session ended; descendant success is not certified."
	case "failed", "stopped":
		return "Native evidence reports a failure or stop."
	default:
		return "Native lifecycle evidence is unavailable."
	}
}
