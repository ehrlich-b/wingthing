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
	if width < 3 {
		return runewidth.Truncate(clean(text), max(0, width), strings.Repeat(".", max(0, width)))
	}
	return runewidth.Truncate(clean(text), width, "...")
}

func field(text string, width int) string {
	text = clip(text, width)
	return text + strings.Repeat(" ", max(0, width-runewidth.StringWidth(text)))
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

type viewLine struct {
	text                  string
	selected              bool
	tone                  tone
	statusAt, statusWidth int
	statusTone            tone
	unreadAt, unreadWidth int
	rightTone             tone
	split                 int
	right                 string
	frame                 bool
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func machineLabel(name string) string {
	if name == "local" {
		return "This machine"
	}
	return clean(name)
}

func ownerLabel(value string) string {
	switch value {
	case "browser", "agent", "terminal":
		return value
	default:
		return "Another client"
	}
}

func isolationLabel(value string) string {
	switch value {
	case "standard":
		return "Standard"
	case "strict":
		return "Strict"
	case "network":
		return "Network"
	case "privileged":
		return "Sandbox off"
	default:
		return "Not reported"
	}
}

func agentLabel(agent string) string {
	switch agent {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "":
		return "Terminal"
	}
	return clean(agent)
}

// At 80 cells the columns fit exactly; below 80 drop age, then agent.
func columns(width int) (title, state, agent, machine, elapsed int) {
	state, machine = 13, 15
	if width >= 75 {
		return width - 2 - unreadWidth(width) - state - 9 - machine - 8 - columnGap(width), state, 9, machine, 8
	}
	if width >= 65 {
		return width - 2 - unreadWidth(width) - state - 9 - machine, state, 9, machine, 0
	}
	state = 12
	machine = min(12, max(8, width/5))
	return max(1, width-2-3-state-machine), state, 0, machine, 0
}

func columnGap(width int) int { return min(2, max(0, width-78)) }
func unreadWidth(width int) int {
	if width >= 65 {
		return 6
	}
	return 3
}

func ageField(text string, width, padding int) string {
	if width == 0 {
		return ""
	}
	return strings.Repeat(" ", padding) + field(text, width)
}

func tableLabel(width int) string {
	t, st, a, m, e := columns(width)
	return "  " + field("State", st) + field("Unread", unreadWidth(width)) + field("  Title", t) + field("Agent", a) + field("Machine", m) + ageField("Age", e, columnGap(width))
}

func sessionLine(s *state, r row, width int, now time.Time) viewLine {
	t, st, a, m, e := columns(width)
	unread := ""
	if r.attention == "unread" {
		unread = " *"
	}
	label, role := stateLabel(r)
	if s.stale(r.machine) {
		label, role = "? Last seen", warning
	}
	elapsed := r.UptimeSecs
	if updated := s.machines[r.machine].updated; !updated.IsZero() {
		elapsed += max(0, int64(now.Sub(updated)/time.Second))
	}
	selected := r.key() == s.selected
	prefix := "  "
	if selected {
		prefix = "> "
	}
	return viewLine{text: "  " + field(label, st) + field(unread, unreadWidth(width)) + field(prefix+r.name(), t) + field(agentLabel(r.Agent), a) + field(machineLabel(r.machine), m) + ageField(age(elapsed), e, columnGap(width)), selected: selected, statusAt: 2, statusWidth: st, statusTone: role, unreadAt: 2 + st, unreadWidth: unreadWidth(width)}
}

func (s *state) stale(name string) bool {
	m := s.machines[name]
	return m.err != "" || (name != "local" && s.registryError != "")
}

func (s *state) scopeLine(now time.Time) string {
	scope := "All machines"
	if s.registryError != "" {
		return "Saved machines could not be loaded. Local sessions remain available."
	}
	if s.machineFilter != "" {
		scope = machineLabel(s.machineFilter)
	}
	if s.attentionOnly {
		scope += " / Needs attention"
	}
	var trouble []string
	connected, loading := 0, 0
	for _, name := range s.machineNames() {
		if s.machineFilter != "" && name != s.machineFilter {
			continue
		}
		m := s.machines[name]
		if s.stale(name) {
			cause := " unreachable"
			if accessDenied(m.err) {
				cause = " access denied"
			}
			trouble = append(trouble, machineLabel(name)+cause)
		} else if m.updated.IsZero() {
			loading++
		} else {
			connected++
		}
	}
	if len(trouble) > 0 {
		return scope + "   " + strings.Join(trouble, "; ")
	}
	if loading > 0 {
		return scope + "   Loading sessions..."
	}
	return scope + "   " + plural(connected, "machine") + " connected"
}

func (s *state) selectedLines(r row, now time.Time, width int) []string {
	label, _ := stateLabel(r)
	title := r.name()
	metadata := agentLabel(r.Agent) + " / " + machineLabel(r.machine) + " / " + r.project() + " / " + label
	action := "Enter open   v read only   i full details"
	switch {
	case r.attention == "unread":
		action = "Unread response. Enter opens the terminal. i full details."
	case r.Status == "blocked":
		action = "Waiting for input. Enter opens the terminal. i full details."
		if next := s.preview(r).next; next != "" {
			action = next + " Enter opens the terminal. i full details."
		}
	case r.Status == "failed":
		action = "Session failed. i full details."
	case r.Status == "exited" || r.Status == "done":
		action = "Session ended. h recent runs. i full details."
	case r.attention == "unknown":
		action = "Activity not reported. Enter checks the session. i full details."
	}
	if s.stale(r.machine) {
		metadata = machineLabel(r.machine) + " / " + connection(s.machines[r.machine], now)
		action = routeCause(s.machines[r.machine].err) + " R retry."
	}
	// A normal title fits in full. Longer remote titles retain identity and an
	// explicit route to their complete, scrollable detail instead of taking over.
	if width == 0 {
		return nil
	}
	return []string{title, metadata, action}
}

// Only classify diagnostics here; arbitrary remote payloads remain outside
// ordinary UI chrome. The identity and compatibility failures stay distinct.
func routeCause(err string) string {
	text := strings.ToLower(err)
	switch {
	case strings.Contains(text, "host key"), strings.Contains(text, "host identification"), strings.Contains(text, "identity"), strings.Contains(text, "pinned"):
		return "Identity changed. Verify the machine before reconnecting."
	case strings.Contains(text, "version mismatch"), strings.Contains(text, "too old"), strings.Contains(text, "incompatible"):
		return "Update wt on this machine before reconnecting."
	case strings.Contains(text, "permission denied"), strings.Contains(text, "authentication"):
		return "SSH access was denied. Check the saved SSH profile."
	case strings.Contains(text, "timed out"), strings.Contains(text, "timeout"):
		return "Connection timed out. Check the machine and network."
	case strings.Contains(text, "refused"):
		return "Connection refused. Check that wt is running on this machine."
	default:
		return "Sessions could not be loaded. Retry the machine."
	}
}

func accessDenied(err string) bool {
	text := strings.ToLower(err)
	for _, cause := range []string{"denied", "authentication", "unauthenticated", "unauthorized", "not authorized", "not owned", "forbidden", "revoked", "access ended", "credential expired", "grant expired"} {
		if strings.Contains(text, cause) {
			return true
		}
	}
	return false
}

func keyHints(s *state) []string {
	switch s.mode {
	case runFilter:
		return []string{"Enter keep search   Ctrl-U clear", "Esc back   Ctrl-C quit"}
	case actionMenu:
		return []string{"r rename   x review stop   t review input control", "Esc back   Ctrl-C quit"}
	case filter:
		return []string{"Type to search   Enter keep search   Ctrl-U clear", "Esc restore selection   Ctrl-C quit"}
	case help:
		return []string{"j/k scroll   PgUp/PgDn page", "Esc back   q back   Ctrl-C quit"}
	case machinePicker:
		return []string{"j/k move   Enter select   i details", "Esc back   Ctrl-C quit"}
	case confirmStop:
		return []string{"Enter keep running   y stop session", "j/k scroll   Esc back   Ctrl-C quit"}
	case confirmTakeover:
		return []string{"Enter keep agent control   y take control", "j/k scroll   Esc back   Ctrl-C quit"}
	case rename:
		return []string{"Enter save name   Ctrl-U clear", "Esc back   Ctrl-C quit"}
	case newAgent:
		return []string{"Enter choose agent   Ctrl-U clear", "Esc back   Ctrl-C quit"}
	case newCWD:
		return []string{"Enter start session   Ctrl-U clear", "Esc back   Ctrl-C quit"}
	case agentSetup:
		return []string{"1 Claude Code   2 Codex   R rescan", "Esc back   Ctrl-C quit"}
	case installConsent:
		return []string{"Enter install " + s.setupAgent.Name, "Esc back   Ctrl-C quit"}
	case providerReady:
		return []string{"v verify   l provider login   Enter new session   R rescan", "Esc back   Ctrl-C quit"}
	case setupFailure:
		return []string{"r review " + s.setupStage + "   R rescan   a choose agent", "Esc back   Ctrl-C quit"}
	case diagnostics:
		return []string{"1 launch readiness   2 session reach   3 saved result", "Esc back   Ctrl-C quit"}
	case diagnosticInput:
		return []string{"Enter accept input   Ctrl-U clear", "Esc back   Ctrl-C quit"}
	}
	if s.isOverlay() {
		return []string{"j/k scroll   PgUp/PgDn page", "Esc back   Enter back   Ctrl-C quit"}
	}
	if s.recent {
		return []string{"Enter open   o result   / search   N next page", "j/k move   h sessions   i details   ? help   q quit"}
	}
	if len(s.rows()) == 0 && s.filter == "" {
		for name, m := range s.machines {
			if (s.machineFilter == "" || name == s.machineFilter) && (s.stale(name) || m.updated.IsZero()) {
				return []string{"R refresh   a agents   m machines   h runs", "? help   q quit"}
			}
		}
	}
	if len(s.rows()) == 0 && s.filter == "" && !s.hasAgent() && (s.machineFilter == "" || s.machineFilter == "local") {
		return []string{"a install an agent   m machines   h runs", "? help   q quit"}
	}
	if len(s.rows()) == 0 && s.filter == "" {
		return []string{"n new session   a agents   m machines   h runs", "? help   q quit"}
	}
	return []string{"Enter open   / search   n new   ! needs attention", "j/k move   m machines   h runs   . actions   R refresh   ? help   q quit"}
}

func (s *state) hasAgent() bool {
	for _, a := range s.agents {
		if a.Path != "" {
			return true
		}
	}
	return false
}

// render uses a fixed cell grid. Search and notices never move the footer or
// the pinned coordinator, and the selected detail has a dedicated region.
func render(s *state, width, height int, now time.Time) string {
	width, height = max(1, width), max(1, height)
	lines := make([]viewLine, height)
	set := func(y int, text string, role tone) {
		if y >= 0 && y < height {
			lines[y] = viewLine{text: text, tone: role}
		}
	}
	total, blocked, unread, cached := 0, 0, 0, 0
	for name, m := range s.machines {
		for _, session := range m.sessions {
			total++
			r := s.sessionRow(name, session)
			if s.stale(name) {
				if r.attention == "unread" {
					unread++
				}
				cached++
				continue
			}
			if r.attention == "blocked" {
				blocked++
			}
			if r.attention == "unread" {
				unread++
			}
		}
	}
	counts := fmt.Sprintf("%d needs you", blocked)
	counts += fmt.Sprintf("   %d unread", unread)
	if cached > 0 {
		counts += fmt.Sprintf("   %d cached", cached)
	}
	unknownSources := s.registryError != ""
	loading := false
	for _, m := range s.machines {
		if m.err != "" || (m.updated.IsZero() && len(m.sessions) == 0) {
			unknownSources = true
		}
		if m.loading {
			loading = true
		}
	}
	countLabel := plural(total, "session")
	if unknownSources {
		countLabel = fmt.Sprintf("%d known sessions", total)
	}
	if total == 0 && unknownSources && !loading {
		countLabel = "Sessions unavailable"
	}
	if total == 0 && unknownSources && loading {
		countLabel = "Loading sessions..."
	}
	title := "wt   " + countLabel + "   " + plural(len(s.machines), "machine")
	if width >= 120 {
		title = "wt   " + now.Local().Format("Mon Jan 2 · 15:04") + "   " + countLabel + "   " + plural(len(s.machines), "machine")
	}
	if width < 65 {
		title = "wt   " + countLabel
	}
	set(0, title+strings.Repeat(" ", max(2, width-runewidth.StringWidth(title)-len(counts)))+counts, plain)
	set(1, s.scopeLine(now)+"   "+s.machineStrip(), secondary)
	rows := s.rows()
	var work []row
	var pin *row
	for i := range rows {
		if rows[i].Pinned && pin == nil {
			r := rows[i]
			pin = &r
		} else {
			work = append(work, rows[i])
		}
	}
	listWidth := width
	_, hasSelection := s.selection()
	wide := width >= 120 && height >= 30 && hasSelection && (s.mode == navigate || s.mode == filter) && !s.recent
	if wide {
		listWidth = 78
	}
	if pin != nil {
		set(3, "PINNED", secondary)
		if height > 4 {
			lines[4] = sessionLine(s, *pin, listWidth-boolCell(wide), now)
		}
		label, _ := stateLabel(*pin)
		if s.stale(pin.machine) {
			label = "? Last seen"
		}
		summary := "  " + label + ". Open wing to read the latest update."
		if update := s.latestUpdate(*pin); update != "" {
			summary = "  " + update
		}
		if s.stale(pin.machine) {
			summary = "  Cached wing. Open after reconnecting " + machineLabel(pin.machine) + "."
		}
		set(5, summary, secondary)
		prefix := "  "
		if pin.key() == s.selected {
			prefix = "> "
		}
		pinText := prefix + field("wing", 10) + field(machineLabel(pin.machine), 15) + field(age(pin.UptimeSecs), 5)
		if wide {
			pinText += strings.TrimSpace(summary)
		} else {
			pinText += label
		}
		lines[4] = viewLine{text: pinText, selected: pin.key() == s.selected, statusAt: 2, statusWidth: 10, statusTone: warning}

		if wide {
			set(5, "", plain)
		}

	} else {
		set(3, "", secondary)
		set(4, "", plain)
	}
	set(6, "SESSIONS", secondary)
	set(7, tableLabel(listWidth-boolCell(wide)), secondary)
	listStart, listEnd := 8, height-12
	if height >= 18 {
		listEnd = max(13, listEnd)
	}
	if wide {
		listEnd = listStart + min(7, max(0, (height-13)/2))
	}
	if height < 18 {
		listStart = 2
		if pin != nil {
			listStart = 5
		}
		listEnd = height - 3
		set(7, "", plain)
	}
	count := max(0, listEnd-listStart)
	selectedIndex := 0
	for i, r := range work {
		if r.key() == s.selected {
			selectedIndex = i
		}
	}
	start := max(0, min(selectedIndex-count/2, len(work)-count))
	for i := 0; i < count && start+i < len(work); i++ {
		lines[listStart+i] = sessionLine(s, work[start+i], listWidth-boolCell(wide), now)
	}
	if len(work) > count && count > 0 {
		position := fmt.Sprintf("%d-%d of %d sessions", start+1, min(start+count, len(work)), len(work))
		if pin != nil {
			position += " + 1 pinned"
		}
		set(listEnd, position, secondary)
	}
	if r, ok := s.selection(); ok {
		detailWidth := width
		if wide {
			detailWidth = width - listWidth - 4
		}
		details := s.selectedLines(r, now, detailWidth)
		if wide {
			for y := 3; y < height-4; y++ {
				lines[y].split, lines[y].frame = listWidth, true
			}
			pane := s.paperPane(r, now, detailWidth, height-7)
			for i, line := range pane {
				if i+3 < height-4 {
					lines[i+3].right = line
					switch {
					case line == "SELECTED" || line == "MACHINES" || line == "TODAY" || line == "RECENT":
						lines[i+3].rightTone = secondary
					case strings.HasPrefix(line, "! Needs you"):
						lines[i+3].rightTone = warning
					case strings.HasPrefix(line, "* Working"):
						lines[i+3].rightTone = accent
					case strings.HasPrefix(line, "+ Done"):
						lines[i+3].rightTone = success
					}
				}
			}

		} else if height >= 18 {
			for i, line := range details {
				if i >= 3 {
					break
				}
				set(height-6+i, line, plain)
			}
		}
	} else if len(rows) == 0 && s.filter == "" && !s.attentionOnly {
		unavailable := false
		for name, m := range s.machines {
			if s.machineFilter != "" && name != s.machineFilter {
				continue
			}
			if m.err != "" || m.updated.IsZero() {
				unavailable = true
			}
		}
		if unavailable {
			set(3, "Sessions unavailable", warning)
			if loading {
				set(3, "Loading sessions...", plain)
			}
			set(5, "R retry   m machines", plain)
			set(6, "", plain)
			set(7, "", plain)
		} else if !s.hasAgent() && (s.machineFilter == "" || s.machineFilter == "local") {
			set(3, "Install an agent", plain)
			set(5, "a choose Claude Code or Codex", accent)
			set(6, "", plain)
			set(7, "", plain)
			for i, a := range s.agents {
				set(7+i, a.Name+"   Missing", secondary)
			}
		} else {
			emptyMachine := s.machineFilter
			if emptyMachine == "" {
				emptyMachine = "local"
			}
			set(3, "No sessions yet on "+machineLabel(emptyMachine), plain)
			set(5, "n new session", accent)
			set(6, "", plain)
			set(7, "", plain)
			for _, a := range s.agents {
				if a.Path != "" {
					if emptyMachine != "local" {
						set(7, "Agent not checked on "+machineLabel(emptyMachine), secondary)
					} else {
						set(7, agentLabel(a.ID)+" / "+machineLabel(emptyMachine)+" / "+projectName(s.defaultCWD), secondary)
					}
					break
				}
			}
		}
	} else {
		set(8, "No matching sessions", plain)
		set(10, "/ edit search   Ctrl-U clears the query", secondary)
	}
	if (s.filter != "" || s.mode == filter) && len(work) == 0 {
		set(8, "No matching sessions", plain)
		set(10, "Edit the query or clear it with Ctrl-U while searching.", secondary)
	}
	if s.filter != "" || s.mode == filter {
		query := s.filter
		if s.mode == filter {
			query = s.input + "_"
		}
		set(1, "Search sessions /"+query+fmt.Sprintf("   %d matches", len(work)), plain)
		for _, name := range s.machineNames() {
			if s.stale(name) {
				set(height-3, machineLabel(name)+" could not be searched. R retry.", warning)
				break
			}
		}
	}
	if height >= 18 && (s.mode == navigate || s.mode == filter) && !s.recent && hasSelection {
		s.paperReports(lines, work, pin, start, count, listEnd, listWidth-boolCell(wide), wide, now)
	}
	if wide {
		set(2, paneBorder(s.theme, width, listWidth, true), separator)
		set(height-4, paneBorder(s.theme, width, listWidth, false), separator)
	}
	if s.mode == machinePicker {
		body := []viewLine{{text: "Machines", tone: plain}, {}}
		choices := append([]string{""}, s.machineNames()...)
		for i, name := range choices {
			text := "All machines"
			if name != "" {
				text = machineLabel(name) + "   " + connection(s.machines[name], now)
			}
			prefix := "  "
			if i == s.picker {
				prefix = "> "
			}
			body = append(body, viewLine{text: prefix + text, selected: i == s.picker})
		}
		fillBody(lines, body, max(0, min(s.picker+2-max(0, height-5)/2, len(body)-max(0, height-5))))
	} else if s.isOverlay() || s.isSetupMode() || s.mode == confirmStop || s.mode == confirmTakeover || s.mode == rename || s.mode == newAgent || s.mode == newCWD || s.mode == actionMenu {
		body := overlayLines(s, now)
		if s.isSetupMode() {
			body = s.setupLines()
		}

		var content []viewLine
		for _, line := range body {
			for _, line := range wrap(line, width) {
				content = append(content, viewLine{text: line})
			}
		}
		offset := min(s.overlayOffset, max(0, len(content)-max(0, height-5)))
		fillBody(lines, content, offset)
	}
	if s.recent && !s.isSetupMode() && (s.mode == navigate || s.mode == runFilter) {
		header, body, selected, _ := s.runView(now, width)
		for i, text := range header {
			set(i, text, plain)
		}
		offset := max(0, min(selected-(height-5)/2, len(body)-(height-5)))
		fillBody(lines, body, offset)
	}
	if s.mode == runFilter {
		set(1, "Search runs /"+s.input+"_", plain)
	}
	if s.message != "" && s.message != "Done" && s.mode != help {
		noticeRow := height - 4
		if wide {
			noticeRow = height - 3
		}
		set(noticeRow, safeNotice(s.message), warning)
	}
	hints := keyHints(s)
	if width < 65 && s.mode == navigate && !s.recent {
		hints = []string{"Enter open  / search  ! attention  n new", "j/k move m scope R refresh ? help q quit"}
	}
	for i, text := range hints {
		set(height-2+i, text, secondary)
	}
	if height < 4 {
		lines[0] = viewLine{text: "Resize terminal. Esc back; q quit"}
	}
	var output strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&output, "\x1b[%d;1H\x1b[2K", i+1)
		output.WriteString(paintLine(s.theme, line, width))
		output.WriteString("\x1b[0m")
	}
	return output.String()
}

func fillBody(lines []viewLine, body []viewLine, offset int) {
	for y := 3; y < len(lines)-2; y++ {
		lines[y] = viewLine{}
	}
	for i := 0; i < len(lines)-5 && offset+i < len(body); i++ {
		lines[3+i] = body[offset+i]
	}
}

func boolCell(value bool) int {
	if value {
		return 1
	}
	return 0
}

func paneGlyphs(theme terminalTheme) (string, string, string, string, string, string) {
	if theme.dumb || theme.ascii || runewidth.StringWidth("┌┬┐└┴┘│─") != 8 {
		return "+", "+", "+", "+", "+", "+"
	}
	return "┌", "┬", "┐", "└", "┴", "┘"
}
func paneBorder(theme terminalTheme, width, split int, top bool) string {
	a, b, c, d, e, f := paneGlyphs(theme)
	horizontal := "─"
	if a == "+" {
		horizontal = "-"
	}
	if !top {
		a, b, c = d, e, f
	}
	return a + strings.Repeat(horizontal, split-1) + b + strings.Repeat(horizontal, max(0, width-split-2)) + c
}
func paintLine(theme terminalTheme, line viewLine, width int) string {
	leftWidth := width
	if line.split > 0 {
		leftWidth = line.split - boolCell(line.frame)
	}
	text := field(line.text, leftWidth)
	base := theme.sheet() + theme.style(line.tone)
	if line.selected {
		base = theme.sheet() + theme.style(selection)
	}
	out := base
	if line.statusWidth > 0 {
		before := runewidth.Truncate(text, line.statusAt, "")
		rest := strings.TrimPrefix(text, before)
		status := runewidth.Truncate(rest, line.statusWidth, "")
		after := strings.TrimPrefix(rest, status)
		role := theme.style(line.statusTone)
		if line.selected {
			role = theme.selectedState(line.statusTone)
		}
		out += before + role + status + base
		if line.unreadWidth > 0 {
			gap := runewidth.Truncate(after, max(0, line.unreadAt-line.statusAt-line.statusWidth), "")
			rest := strings.TrimPrefix(after, gap)
			cell := runewidth.Truncate(rest, line.unreadWidth, "")
			unreadRole := theme.style(warning)
			if line.selected {
				unreadRole = theme.selectedState(warning)
			}
			out += gap + unreadRole + cell + base + strings.TrimPrefix(rest, cell)
		} else {
			out += after
		}
	} else {
		out += text
	}
	if line.split > 0 {
		vertical := "│"
		a, _, _, _, _, _ := paneGlyphs(theme)
		if a == "+" {
			vertical = "|"
		}
		rule := "\x1b[0m" + theme.sheet() + theme.style(separator) + vertical
		if line.frame {
			out = rule + out
		}
		out += rule + theme.sheet() + theme.style(line.rightTone) + "  " + field(line.right, width-line.split-3-boolCell(line.frame))
		if line.frame {
			out += rule
		}
	}
	return out
}

func safeNotice(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "attach-after-admission"):
		return "Session started. Attachment failed. Check it before retrying."
	case strings.Contains(lower, "admission/policy"):
		return "Session did not start. Policy refused launch. d diagnostics."
	case strings.Contains(lower, "unknown") && strings.Contains(lower, "admission"):
		return "Start not confirmed. Check the session before starting another."
	case strings.Contains(lower, "cannot validate"), strings.Contains(lower, "changed or was removed"), strings.Contains(lower, "registry error"):
		return "Saved machine changed or could not be loaded. R refresh."
	case strings.Contains(lower, "enter retries"):
		return "Attachment failed. Enter retries after checking the session."
	}
	// Action errors can contain arbitrary remote payloads. Only known local
	// notices may supply literal chrome; everything else has a fixed safe label.
	switch message {
	case "Access denied. R refresh.", "Done", "Running...", "Running…", "Name cannot be empty", "Working directory cannot be empty",
		"Bell on for this dashboard", "Bell off for this dashboard", "Native provider requested input.",
		"New foreground response; provider or descendants may continue.", "Input control is not available for this session":
		return message
	}
	if strings.Contains(lower, "unreachable") {
		return "Machine unreachable. R retries its saved route."
	}
	return "Action could not finish. Check the session before retrying."
}

func projectName(cwd string) string {
	if cwd == "" {
		return "No project selected"
	}
	name := path.Base(path.Clean(cwd))
	if name == "/" || name == "." {
		return "Workspace"
	}
	return clean(name)
}

// Freshness describes the query, independently of the provider lifecycle state.
func connection(m machine, now time.Time) string {
	if m.err != "" {
		text := "Unreachable"
		if accessDenied(m.err) {
			text = "Access denied"
		}
		if !m.updated.IsZero() {
			text += " / Last seen " + age(int64(now.Sub(m.updated)/time.Second)) + " ago"
		} else {
			text += " / Sessions unavailable"
		}
		if !m.attempted.IsZero() {
			text += " / Retried " + age(int64(now.Sub(m.attempted)/time.Second)) + " ago"
		}
		return text
	}
	if m.updated.IsZero() {
		return "Loading sessions..."
	}
	if m.loading {
		return "Connected / Refreshing sessions..."
	}
	return "Connected / Updated " + age(int64(now.Sub(m.updated)/time.Second)) + " ago"
}

// Wrap sanitized metadata by display cells so details expose complete values
// without allowing terminal controls or splitting wide characters at an edge.
func wrap(text string, width int) []string {
	width = max(1, width)
	text = clean(text)
	var lines []string
	for runewidth.StringWidth(text) > width {
		prefix := runewidth.Truncate(text, width, "")
		if prefix == "" {
			prefix = string([]rune(text)[0])
		}
		split := strings.LastIndex(prefix, " ")
		if split > 0 {
			lines = append(lines, prefix[:split])
			text = strings.TrimLeft(text[split+1:], " ")
		} else {
			lines = append(lines, prefix)
			text = strings.TrimPrefix(text, prefix)
		}
	}
	return append(lines, text)
}

func promptLines(s *state) []string {
	switch s.mode {
	case actionMenu:
		return []string{"Session actions", "", s.target.name() + " on " + machineLabel(s.target.machine), agentLabel(s.target.Agent) + " / " + s.target.project(), "", "Rename session", "Stop session", "Take input control (when available)"}
	case confirmStop:
		return []string{"Stop session", "", s.target.name() + " on " + machineLabel(s.target.machine), agentLabel(s.target.Agent) + " / " + s.target.project(), "", "The agent will stop. Unsaved work may be lost.", "Keep running is selected."}
	case confirmTakeover:
		return []string{"Take control", "Current input owner: " + ownerLabel(s.target.WriterOwner), "", s.target.name() + " on " + machineLabel(s.target.machine), "", "Typing will go to this terminal. The agent will lose input control.", "Keep agent control is selected."}
	case rename:
		return []string{"Rename session", s.target.name() + " on " + machineLabel(s.target.machine), "", "Name: " + s.input + "_"}
	case newAgent:
		return []string{"New session", "Machine: " + machineLabel(s.target.machine), "", "Agent (blank = shell): " + s.input + "_"}
	case newCWD:
		return []string{"New session", "Agent: " + agentLabel(s.agent), "Machine: " + machineLabel(s.target.machine), "Project: " + projectName(s.input), "", "Working directory", s.input + "_", "", "Session permissions follow this project's policy.", "The start command checks admission before launch."}
	}
	return nil
}

func overlayLines(s *state, now time.Time) []string {
	if lines := promptLines(s); lines != nil {
		return lines
	}
	if s.mode == machineDetails {
		return s.machineLines(now)
	}
	if s.mode == diagnostics || s.mode == diagnosticInput || s.mode == diagnosticReport {
		return s.diagnosticLines()
	}
	if s.mode == runDetails || s.mode == runResult {
		return s.runOverlay()
	}
	if s.mode == help {
		return []string{"Help", "Move", "  j/k or arrows move   PgUp/PgDn page   Home/End first/last", "",
			"Open", "  Enter open terminal   v read only   i session details",
			"  Ctrl+B then q returns here; Ctrl+B twice sends Ctrl+B.", "",
			"Find", "  / search sessions   ! needs attention   m machine scope", "",
			"Manage", "  n new   a agents   . actions   r rename   x review stop",
			"  h recent runs   M machine details   d diagnostics   R refresh",
			"  b bell " + s.bellSetting() + " (this navigator only)", "",
			"Leave", "  q or Ctrl-C quits. Leaving keeps sessions running.", "Unread * marks a new response. Unknown means activity is not reported."}
	}

	r := s.target
	m := s.machines[r.machine]
	label, _ := stateLabel(r)
	isolation := isolationLabel(r.Isolation)
	lines := []string{r.name(), "", "Machine: " + machineLabel(r.machine), "Agent: " + agentLabel(r.Agent), "Project: " + r.project(), "State: " + label, "Connection: " + connection(m, now), "", "Permissions: " + isolation}
	if r.Isolation == "privileged" {
		lines = append(lines, "! Sandbox off. This session has full user access.")
	}
	if r.attention == "unread" {
		lines = append(lines, "Unread response.")
	}
	if s.stale(r.machine) {
		lines = append(lines, "", routeCause(m.err), "R retry from the sessions list. Verify the machine before attaching.")
	} else {
		lines = append(lines, "", lifecycleDescription(r), "", "Return to the list to open this exact session.", "Ctrl+B then q returns from the terminal.")
	}
	if s.mode == details {
		lines = append(lines, "", "RECENT")
		lines = append(lines, s.messageLines(r, 72)...)
	}

	return lines
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
