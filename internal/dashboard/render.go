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
	text = clean(text)
	if runewidth.StringWidth(text) <= width {
		return text
	}
	return strings.TrimRight(runewidth.Truncate(text, width-3, ""), " .,;:") + "..."
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

// machinePhrase is machineLabel for the middle of a sentence.
func machinePhrase(name string) string {
	if name == "local" {
		return "this machine"
	}
	return machineLabel(name)
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
		label, role = "? Last seen", caution
	}
	elapsed := s.rowAge(r, now)
	selected := r.key() == s.selected && !s.selectionLost
	lead := "  "
	if selected {
		lead = "> "
	}
	return viewLine{text: lead + field(label, st) + field(unread, unreadWidth(width)) + field("  "+r.name(), t) + field(agentLabel(r.Agent), a) + field(machineLabel(r.machine), m) + ageField(age(elapsed), e, columnGap(width)), selected: selected, statusAt: 2, statusWidth: st, statusTone: role, unreadAt: 2 + st, unreadWidth: unreadWidth(width)}
}

// rowAge counts the time since the machine last reported on top of the
// reported age, so a cached row never looks newer than its machine.
func (s *state) rowAge(r row, now time.Time) int64 {
	elapsed := r.UptimeSecs
	if updated := s.machines[r.machine].updated; !updated.IsZero() {
		elapsed += max(0, int64(now.Sub(updated)/time.Second))
	}
	return elapsed
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
	if s.contextFiltered {
		scope = contextLabel(s.contextFilter)
	}
	if s.attentionOnly {
		scope += " · needs you"
	}
	var trouble []string
	connected, loading := 0, 0
	for _, name := range s.machineNames() {
		if s.machineFilter != "" && name != s.machineFilter {
			continue
		}
		if s.contextFiltered && s.machineContext(name) != s.contextFilter {
			continue
		}
		m := s.machines[name]
		if s.stale(name) {
			cause := " unreachable"
			if accessDenied(m.err) {
				cause = " " + accessState(m.err)
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
	// The name gives way first so agent, machine and project stay visible.
	suffix := " · " + agentLabel(r.Agent) + " · " + machineLabel(r.machine) + " · " + r.project()
	title := clip(r.name(), max(12, width-runewidth.StringWidth(suffix))) + suffix
	metadata := label + " · " + lastLine(s, r)
	// wing reports through its messages; an idle cell beside them misleads.
	if r.Pinned && r.Status != "blocked" && !s.stale(r.machine) {
		metadata = lastLine(s, r)
	}
	action := "v read only   i details"
	switch {
	case r.Status == "blocked":
		metadata = label + " · Waiting for your answer."
		if next := s.preview(r).next; next != "" {
			metadata = label + " · " + next
		}
		action = s.permissionPosition(r) + "Answer in terminal · Enter open   i details"
	case r.Status == "failed":
		action = "i details   h runs"
	case r.Status == "exited" || r.Status == "done":
		action = "Enter open   h runs   i details"
	case r.attention == "unknown":
		metadata = label + " · " + agentLabel(r.Agent) + " does not say what it is doing."
		action = "Enter open to check   i details"
	}
	if s.stale(r.machine) {
		metadata = "? Last seen · " + routeCause(s.machines[r.machine].err)
		if updated := s.machines[r.machine].updated; !updated.IsZero() {
			metadata = "? Last seen " + age(int64(now.Sub(updated)/time.Second)) + " ago · " + routeCause(s.machines[r.machine].err)
		}
		action = "R reconnect   i details"
	}
	// A normal title fits in full. Longer remote titles retain identity and an
	// explicit route to their complete, scrollable detail instead of taking over.
	if width == 0 {
		return nil
	}
	if p, ok := s.livePermission(r); ok {
		keys := s.permissionKeys(r)
		if r.PermissionRespond {
			// The footer already carries a approve and d decline.
			keys = strings.TrimSpace(s.permissionPosition(r))
		}
		return []string{"WILL RUN " + fmt.Sprint(p.Arguments["command"]), permissionWhere(p), keys}
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

// setupRetryLabel names what r returns to after a failed setup step.
func setupRetryLabel(stage string) string {
	switch stage {
	case "install":
		return "try install again"
	case "login":
		return "try sign-in again"
	}
	return "try again"
}

func keyHints(s *state) []string {
	switch s.mode {
	case permissionReview:
		if !s.reviewPermissionLive() {
			return []string{"Answer in terminal   Esc back", "j/k scroll   PgUp/PgDn page"}
		}
		if s.overlayOffset < s.overlayLimit {
			return []string{"End read to the last line   Esc cancel", "j/k scroll   PgUp/PgDn page   Ctrl-C quit"}
		}
		return []string{"Enter allow once   Esc cancel", "j/k scroll   PgUp/PgDn page   Ctrl-C quit"}
	case runFilter:
		return []string{"Enter keep search   Ctrl-U clear", "Esc back   Ctrl-C quit"}
	case actionMenu:
		if takeoverAvailable(s.target) {
			return []string{"r rename   x stop   t take control", "Esc back   Ctrl-C quit"}
		}
		return []string{"r rename   x stop", "Esc back   Ctrl-C quit"}
	case filter:
		return []string{"Type to search   Enter keep search   Ctrl-U clear", "Esc restore selection   Ctrl-C quit"}
	case help:
		return []string{"j/k scroll   PgUp/PgDn page", "Esc back   Ctrl-C quit"}
	case machinePicker:
		return []string{"j/k move   Enter select   i details", "Esc back   Ctrl-C quit"}
	case confirmStop:
		if accessDenied(s.machines[s.target.machine].err) {
			return []string{"Enter back", "Esc back   Ctrl-C quit"}
		}
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
		return []string{"v verify   l sign in   Enter new session   R rescan", "Esc back   Ctrl-C quit"}
	case setupFailure:
		return []string{"r " + setupRetryLabel(s.setupStage) + "   R rescan   A choose agent", "Esc back   Ctrl-C quit"}
	case diagnostics:
		return []string{"1 launch readiness   2 session reach   3 saved result", "Esc back   Ctrl-C quit"}
	case diagnosticInput:
		return []string{"Enter accept input   Ctrl-U clear", "Esc back   Ctrl-C quit"}
	}
	if s.mode == details && !s.stale(s.target.machine) {
		return []string{"j/k scroll   PgUp/PgDn page", "Enter open   Esc back   Ctrl-C quit"}
	}
	if s.isOverlay() {
		return []string{"j/k scroll   PgUp/PgDn page", "Esc back   Enter back   Ctrl-C quit"}
	}
	if s.recent {
		// N only pages when the wing returned a cursor for this page.
		keys := "Enter open   o result   / search"
		if s.history().pagingPage().Next != "" {
			keys += "   N next page"
		}
		return []string{keys, "j/k move   h sessions   i details   ? help   q quit"}
	}
	if len(s.rows()) == 0 && s.filter == "" {
		for name, m := range s.machines {
			if (s.machineFilter == "" || name == s.machineFilter) && (s.stale(name) || m.updated.IsZero()) {
				return []string{s.refreshKey() + "   A agents   c context   m machines   h runs", "? help   q quit"}
			}
		}
	}
	if len(s.rows()) == 0 && s.filter == "" && !s.hasAgent() && (s.machineFilter == "" || s.machineFilter == "local") {
		return []string{"A install an agent   c context   m machines   h runs", "? help   q quit"}
	}
	if len(s.rows()) == 0 && s.filter == "" {
		return []string{"n new session   A agents   c context   m machines   h runs", "? help   q quit"}
	}
	if r, ok := s.selection(); ok {
		if _, live := s.livePermission(r); live && r.PermissionRespond {
			return []string{s.permissionKeys(r) + "   / search", "j/k move   A agents   D diagnostics   ? help   q quit   Ctrl+B then q detaches"}
		}
	}
	return []string{"Enter open   v read only   / search   n new   ! needs you   . actions", "j/k move   c context   m machines   h runs   " + s.refreshKey() + "   ? help   q quit   Ctrl+B then q detaches"}
}

// refreshKey names R for what it does now: reconnect when a machine is
// unreachable, refresh otherwise.
func (s *state) refreshKey() string {
	for name := range s.machines {
		if s.stale(name) {
			return "R reconnect"
		}
	}
	return "R refresh"
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
			old := lines[y]
			lines[y] = viewLine{text: text, tone: role, split: old.split, frame: old.frame, right: old.right, rightTone: old.rightTone}
		}
	}
	// In the ungrouped view wing sits outside the table and its session count.
	grouped := s.hasContexts()
	total, blocked, unread, cached := 0, 0, 0, 0
	pinCounted := false
	for name, m := range s.machines {
		for _, session := range m.sessions {
			r := s.sessionRow(name, session)
			pin := !grouped && r.Pinned && !pinCounted
			if pin {
				pinCounted = true
			} else {
				total++
			}
			if s.stale(name) {
				if r.attention == "unread" {
					unread++
				}
				// Machine counts leave the pinned wing out of cached too.
				if !pin {
					cached++
				}
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
	// Grouped pins stay with their machines so display and navigation agree.
	for i := range rows {
		if !grouped && rows[i].Pinned && pin == nil {
			r := rows[i]
			pin = &r
		} else {
			work = append(work, rows[i])
		}
	}
	// On very short screens, give the selected session the pinned block’s space.
	if pin != nil && height < 9 && pin.key() != s.selected {
		work = append([]row{*pin}, work...)
		pin = nil
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
			lines[4] = sessionLine(s, *pin, listWidth-3*boolCell(wide), now)
		}
		summary := "No report yet. Enter opens wing."
		if update := s.latestUpdate(*pin); update != "" {
			summary = update
		}
		if pin.Status == "blocked" {
			summary = "Needs you. " + summary
		}
		if s.stale(pin.machine) {
			stamp := "time unknown"
			if updated := s.machines[pin.machine].updated; !updated.IsZero() {
				stamp = paperStamp(updated.Format(time.RFC3339), now)
			}
			summary = "Last seen " + stamp + ". Reconnect " + machineLabel(pin.machine) + "."
		}
		prefix := "  "
		if pin.key() == s.selected {
			prefix = "> "
		}
		pinText := prefix + field("wing", 10) + field(machineLabel(pin.machine), 15) + field(age(s.rowAge(*pin, now)), 5) + summary
		pinTone := plain
		if pin.Status == "blocked" {
			pinTone = warning
		}
		lines[4] = viewLine{text: pinText, selected: pin.key() == s.selected, statusAt: 2, statusWidth: 10, statusTone: pinTone}
		set(5, "", plain)
	} else {
		set(3, "", secondary)
		set(4, "", plain)
	}
	// Without wing the table moves up instead of leaving an empty pinned block.
	shift := 0
	if pin == nil && len(work) > 0 && height >= 18 {
		shift = 3
	}
	set(6-shift, "SESSIONS", secondary)
	set(7-shift, tableLabel(listWidth-3*boolCell(wide)), secondary)
	listStart, listEnd := 8, height-12
	if height >= 18 {
		listEnd = max(13, listEnd)
	}
	if wide {
		listEnd = listStart + min(12, max(0, (height-13)/2))
	}
	listStart, listEnd = listStart-shift, listEnd-shift
	// A compact list gives up a row when wing's newest message wraps.
	if height >= 18 && !wide {
		listEnd -= s.newestWingLines(pin, listWidth, now) - 1
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
	// The position line and a blank row before the next heading both fit in the window.
	positionAt := listEnd
	if height >= 18 && len(work) > count && count > 2 {
		count--
		positionAt = listEnd - 1
	}
	selectedIndex := 0
	for i, r := range work {
		if r.key() == s.selected {
			selectedIndex = i
		}
	}
	start := max(0, min(selectedIndex-count/2, len(work)-count))
	for i := 0; i < count && start+i < len(work); i++ {
		lines[listStart+i] = sessionLine(s, work[start+i], listWidth-3*boolCell(wide), now)
	}
	if len(work) > count && count > 0 {
		set(positionAt, fmt.Sprintf("  %d-%d of %d sessions · +%d more", start+1, min(start+count, len(work)), len(work), len(work)-count), secondary)
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
			pane := s.paperPane(r, now, detailWidth-1, height-7)
			if s.preview(r).output != nil {
				pane = s.terminalPane(r, now, detailWidth-1, height-7)
			}
			for i, line := range pane {
				if i+3 < height-4 {
					lines[i+3].right = line
					switch {
					case line == "SELECTED" || line == "MACHINES" || line == "TODAY" || line == "RECENT" || line == "ASKING":
						lines[i+3].rightTone = secondary
					case strings.HasPrefix(line, "! Needs you"):
						lines[i+3].rightTone = warning
					case strings.HasPrefix(line, "? Last seen"):
						lines[i+3].rightTone = caution
					case strings.HasPrefix(line, "+ Done"):
						lines[i+3].rightTone = success
					}
				}
			}

		} else if height >= 18 {
			set(height-7, "SELECTED", secondary)
			for i, line := range details {
				if i >= 3 {
					break
				}
				role := plain
				if i == 2 {
					role = secondary
				}
				set(height-6+i, line, role)
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
			set(5, "R reconnect   m machines", plain)
			set(6, "", plain)
			set(7, "", plain)
		} else if !s.hasAgent() && (s.machineFilter == "" || s.machineFilter == "local") {
			set(3, "Install an agent", plain)
			set(5, "A choose Claude Code or Codex", accent)
			set(6, "", plain)
			set(7, "", plain)
			nameWidth := agentNameWidth(s.agents)
			for i, a := range s.agents {
				set(7+i, field(a.Name, nameWidth)+"   Missing", secondary)
			}
		} else {
			emptyMachine := s.machineFilter
			if emptyMachine == "" {
				emptyMachine = "local"
			}
			where := "this machine"
			if emptyMachine != "local" {
				where = machineLabel(emptyMachine)
			}
			set(3, "No sessions yet on "+where, plain)
			set(5, "n new session", accent)
			set(6, "", plain)
			set(7, "", plain)
			for _, a := range s.agents {
				if a.Path != "" {
					if emptyMachine != "local" {
						set(7, "Agent not checked on "+machineLabel(emptyMachine), secondary)
					} else {
						set(7, agentLabel(a.ID)+" · "+machineLabel(emptyMachine)+" · "+projectName(s.defaultCWD), secondary)
					}
					break
				}
			}
		}
	} else {
		set(8, "  No matching sessions", plain)
		set(10, "  / edit search   Ctrl-U clear", secondary)
	}
	if (s.filter != "" || s.mode == filter) && len(work) == 0 {
		set(8, "  No matching sessions", plain)
		set(10, "  Ctrl-U clear", secondary)
	}
	if s.hasContexts() {
		s.renderContexts(lines, work, listStart, listEnd, listWidth-3*boolCell(wide), now)
	}
	if s.mode != help && (s.filter != "" || s.mode == filter) {
		query := s.filter
		if s.mode == filter {
			query = s.input + "_"
		}
		var search string
		if len(work) == 1 {
			search = "Search sessions /" + query + "   1 match"
		} else {
			search = "Search sessions /" + query + fmt.Sprintf("   %d matches", len(work))
		}
		for _, name := range s.machineNames() {
			if s.stale(name) {
				search += "   " + machineLabel(name) + " not searched, R reconnect"
				break
			}
		}
		set(1, search, plain)
	}
	if height >= 18 && (s.mode == navigate || s.mode == filter) && !s.recent && hasSelection && !s.hasContexts() {
		// Reports follow a short table directly instead of the reserved window.
		reportsAt := listEnd
		if shown := len(work) - start; shown < count {
			reportsAt = listStart + max(0, shown)
		}
		if len(work) == 0 {
			reportsAt = listStart + 3
		}
		s.paperReports(lines, work, pin, start, count, reportsAt, listWidth-3*boolCell(wide), wide, now)
	}
	if wide {
		set(2, paneBorder(s.theme, width, listWidth, true), separator)
		set(height-4, paneBorder(s.theme, width, listWidth, false), separator)
	}
	if s.mode == machinePicker {
		body := []viewLine{{text: "Machines", tone: plain}, {}}
		choices := append([]string{""}, s.machineNames()...)
		nameWidth := runewidth.StringWidth("All machines")
		for _, name := range choices[1:] {
			nameWidth = max(nameWidth, runewidth.StringWidth(machineLabel(name)))
		}
		for i, name := range choices {
			text := "All machines"
			if name != "" {
				text = field(machineLabel(name), nameWidth) + "   " + connection(s.machines[name], now)
			}
			prefix := "  "
			if i == s.picker {
				prefix = "> "
			}
			body = append(body, viewLine{text: prefix + text, selected: i == s.picker})
		}
		fillBody(lines, body, max(0, min(s.picker+2-max(0, height-6)/2, len(body)-max(0, height-6))))
	} else if s.isOverlay() || s.isSetupMode() || s.mode == confirmStop || s.mode == confirmTakeover || s.mode == rename || s.mode == newAgent || s.mode == newCWD || s.mode == actionMenu {
		body := overlayLines(s, now)
		if s.isSetupMode() {
			body = s.setupLines()
		}

		var content []viewLine
		for _, line := range body {
			role := plain
			if line == "ASKING" || line == "RECENT" {
				role = secondary
			}
			for _, line := range s.overlayWrap(line, width) {
				content = append(content, viewLine{text: line, tone: role})
			}
		}
		offset := min(s.overlayOffset, max(0, len(content)-max(0, height-6)))
		fillBody(lines, content, offset)
	}
	if s.recent && !s.isSetupMode() && (s.mode == navigate || s.mode == runFilter) {
		header, body, selected, _ := s.runView(now, width)
		for i, text := range header {
			set(i, text, plain)
		}
		offset := max(0, min(selected-(height-6)/2, len(body)-(height-6)))
		fillBody(lines, body, offset)
	}
	if s.mode == runFilter {
		set(1, "Search runs /"+s.input+"_", plain)
	}
	notice := s.message
	if s.permissionNotice != "" {
		notice = s.permissionNotice
	}
	if notice != "" && notice != "Done" && s.mode != help {
		noticeRow := height - 4
		if wide {
			noticeRow = height - 3
		}
		set(noticeRow, safeNotice(notice), warning)
	}
	if notice == "" && !grouped && s.mode == navigate && !s.recent && height >= 8 {
		hint := "Group Home/Work: wt context set local home"
		if position := strings.TrimSpace(lines[height-3].text); position != "" {
			hint = "wt context set · " + position
		}
		lines[height-3] = viewLine{text: hint, tone: secondary}
	}
	hints := keyHints(s)
	for i := range hints {
		if runewidth.StringWidth(hints[i]) > width {
			hints[i] = strings.TrimSuffix(hints[i], "   Ctrl+B then q detaches")
		}
	}
	if width < 65 && s.mode == navigate && !s.recent {
		hints = []string{"Enter open  / search  ! attention  n new", "j/k move m scope " + s.refreshKey() + " ? help q quit"}
		if s.hasContexts() {
			hints[1] = "j/k c context m scope ? help q quit"
		}
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
	// Row height-3 stays blank so the footer never touches the body.
	for i := 0; i < len(lines)-6 && offset+i < len(body); i++ {
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
	// Framed rows keep one blank cell inside each border.
	if line.frame {
		leftWidth -= 2
	}
	text := field(line.text, leftWidth)
	base := theme.style(line.tone)
	if line.selected {
		base = theme.style(selection)
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
		rule := theme.style(separator) + vertical
		if line.frame {
			out = rule + base + " " + out + base + " "
		}
		out += rule + theme.style(line.rightTone) + "  " + field(line.right, width-line.split-3-boolCell(line.frame))
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
		return "Session did not start. Policy refused launch. D diagnostics."
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
	case "Allow once: delivered. Delivery does not confirm execution.",
		"Decline: delivered. Delivery does not confirm execution.",
		"Allow once: unconfirmed. Answer in terminal; this decision will not be resent.",
		"Decline: unconfirmed. Answer in terminal; this decision will not be resent.",
		"Decision not confirmed. Answer in terminal.", "Answer in terminal", "Sending decision…",
		"Review every line of the current request, or Answer in terminal",
		"Selected session unavailable. j/k choose another.", "Selected machine changed. j/k choose another.",
		"Access denied. R refresh.", "Done", "Running...", "Running…", "Name cannot be empty", "Working directory cannot be empty",
		"Bell on for this dashboard", "Bell off for this dashboard", "Waiting for your answer.",
		"New reply. Background work may still run.", "Input control is not available for this session":
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
			text += " · last seen " + age(int64(now.Sub(m.updated)/time.Second)) + " ago"
		} else {
			text += " · sessions unavailable"
		}
		return text
	}
	if m.updated.IsZero() {
		return "Loading sessions..."
	}
	if m.loading {
		return "Connected · refreshing sessions..."
	}
	return "Connected · updated " + age(int64(now.Sub(m.updated)/time.Second)) + " ago"
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
		lines := []string{"Session actions", "", s.target.name() + " on " + machinePhrase(s.target.machine), agentLabel(s.target.Agent) + " · " + s.target.project(), "", "r  Rename session", "x  Stop session"}
		if takeoverAvailable(s.target) {
			lines = append(lines, "t  Take control and type here")
		}
		return lines
	case confirmStop:
		if err := s.machines[s.target.machine].err; accessDenied(err) {
			return []string{"Stop session", "", accessLine(s.target.machine, err)}
		}
		return []string{"Stop session", "", s.target.name() + " on " + machinePhrase(s.target.machine), agentLabel(s.target.Agent) + " · " + s.target.project(), "", "The agent will stop. Unsaved work may be lost.", "Keep running is selected."}
	case confirmTakeover:
		return []string{"Take control", "Current input owner: " + ownerLabel(s.target.WriterOwner), "", s.target.name() + " on " + machinePhrase(s.target.machine), "", "Typing will go to this terminal. The agent will lose input control.", "Keep agent control is selected."}
	case rename:
		return []string{"Rename session", s.target.name() + " on " + machinePhrase(s.target.machine), "", "Name: " + s.input + "_"}
	case newAgent:
		return []string{"New session", "Machine: " + machineLabel(s.target.machine), "", "Agent (blank = shell): " + s.input + "_"}
	case newCWD:
		return []string{"New session", "Agent: " + agentLabel(s.agent), "Machine: " + machineLabel(s.target.machine), "Project: " + projectName(s.input), "", "Working directory", s.input + "_", "", "Permissions follow this project's settings."}
	}
	return nil
}

func overlayLines(s *state, now time.Time) []string {
	if s.mode == permissionReview {
		return s.permissionReviewLines()
	}
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
		lines := []string{"Help", "Move", "  j/k or arrows move   PgUp/PgDn page   Home/End first/last", "",
			"Open", "  Enter open terminal   v read only   i session details"}
		if r, selected := s.selection(); selected && r.PermissionRespond {
			if _, live := s.livePermission(r); live {
				lines = append(lines, "  a approve: read the command, then Enter allows it once   d decline")
			}
		}
		return append(lines, "  Ctrl+B then q returns here; Ctrl+B twice sends Ctrl+B.", "", "Find", "  / search sessions   ! needs you   c context   m machine scope", "",
			"Manage", "  n new   A agents   . actions   h recent runs",
			"  M machine details   D diagnostics   R refresh   b bell "+s.bellSetting(), "",
			"Leave", "  q or Ctrl-C quits. Leaving keeps sessions running.", "* marks a new reply. Unknown means the agent does not say if it is working.")
	}

	r := s.target
	m := s.machines[r.machine]
	label, _ := stateLabel(r)
	isolation := isolationLabel(r.Isolation)
	if accessDenied(m.err) {
		return []string{machineLabel(r.machine) + " session", "", accessLine(r.machine, m.err)}
	}
	lines := []string{r.name(), "", "Machine: " + machineLabel(r.machine), "Agent: " + agentLabel(r.Agent), "Project: " + r.project(), "State: " + label}
	if s.stale(r.machine) {
		lines = append(lines, "Connection: "+connection(m, now))
	}
	lines = append(lines, "", "Permissions: "+isolation)
	if r.Isolation == "privileged" {
		lines = append(lines, "! Sandbox off. This session has full user access.")
	}
	if r.attention == "unread" {
		lines = append(lines, "Unread response.")
	}
	if s.stale(r.machine) {
		lines = append(lines, "", routeCause(m.err), "Esc, then R to reconnect.")
	} else {
		// ASKING already says the agent is waiting; RECENT needs the rows.
		if r.Status == "blocked" {
			if p, ok := s.livePermission(r); ok {
				lines = append(lines, "", "WILL RUN", fmt.Sprint(p.Arguments["command"]), permissionWhere(p), "", s.permissionKeys(r))
			} else {
				lines = append(lines, "", "ASKING", s.askingText(r), s.askingWhere(r), s.permissionPosition(r)+"Answer in terminal")
			}
		} else {
			lines = append(lines, "", lifecycleDescription(r))
		}
		// The footer already offers Enter; RECENT gets the rows on short screens.
		if s.mode != details {
			lines = append(lines, "", "Enter opens this session. Ctrl+B then q comes back here.")
		}
	}
	if s.mode == details {
		lines = append(lines, "", "RECENT")
		lines = append(lines, s.messageLines(r, 72)...)
	}

	return lines
}

// Remote optional metadata is presentation input too: render descriptions
// from an allowlist instead of forwarding any provider-supplied reason.
// askingText is the request a Needs-you session reported, in its own words.
func (s *state) askingText(r row) string {
	if next := s.preview(r).next; next != "" {
		return next
	}
	return agentLabel(r.Agent) + " has not said what it is asking. Enter opens the session to see."
}

// askingWhere names the machine and project the request would run in.
func (s *state) askingWhere(r row) string {
	return "on " + machinePhrase(r.machine) + ", in " + r.project()
}

func lifecycleDescription(r row) string {
	agent := agentLabel(r.Agent)
	if r.attention == "unknown" {
		return agent + " does not report whether it is working."
	}
	if r.CompletionCursor > 0 || r.attention == "unread" {
		return agent + " replied. Background work may still run."
	}
	if r.StateSource == "" && r.State == "" {
		return "Update wt on " + machinePhrase(r.machine) + " to see live state."
	}
	switch r.State {
	case "needs_input":
		return agent + " is waiting for your answer."
	case "working", "starting":
		return agent + " is working."
	case "idle":
		return agent + " is idle."
	case "completed":
		return agent + " finished."
	case "failed", "stopped":
		return agent + " failed or was stopped."
	default:
		return agent + " has not reported its state."
	}
}
