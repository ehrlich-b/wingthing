package dashboard

import (
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

func (s *state) machineStrip() string {
	var parts []string
	for _, name := range s.machineNames() {
		mark := "■"
		if s.stale(name) {
			mark = "□"
		} else if s.machines[name].updated.IsZero() {
			mark = "?"
		}
		if s.theme.ascii || s.theme.dumb {
			if mark == "■" {
				mark = "+"
			} else if mark == "□" {
				mark = "-"
			}
		}
		parts = append(parts, mark+" "+machineLabel(name))
	}
	return strings.Join(parts, "  ")
}

func lastLine(s *state, r row) string {
	if value := s.latestUpdate(r); value != "" {
		return value
	}
	value := s.preview(r)
	if value.failed {
		return "Messages unavailable. Enter open."
	}
	if value.loading {
		return "Loading messages..."
	}
	if s.stale(r.machine) {
		return "Last seen. R reconnect."
	}
	return "Nothing new yet."
}

// Reports follow the visible work rows. At compact sizes the selected row gets
// the available line, while wing remains outside the scrollable session list.
func (s *state) paperReports(lines []viewLine, work []row, pin *row, start, count, end, width int, wide bool, now time.Time) {
	// Compact layouts keep the selected block and a blank row above the footer.
	limit := len(lines) - 8
	if wide {
		limit = len(lines) - 5
	}
	set := func(y int, text string, role tone) {
		if y >= 0 && y < limit {
			lines[y].text, lines[y].tone = text, role
		}
	}
	y := end + 1
	// The compact selected block already carries the selected session's last line.
	if wide && len(work) > 0 {
		set(y, "LAST LINE", secondary)
		y++
		for i := start; i < min(start+count, len(work)); i++ {
			r := work[i]
			titleWidth := min(24, max(12, width/3))
			set(y, field(r.name(), titleWidth)+" "+lastLine(s, r), plain)
			y++
		}
		y++
	}
	set(y, "FROM WING", secondary)
	y++
	if pin == nil {
		set(y, "No wing session reported.", secondary)
		return
	}
	value := s.preview(*pin)
	if len(value.messages) == 0 {
		set(y, lastLine(s, *pin), plain)
		return
	}
	for i := len(value.messages) - 1; i >= 0; i-- {
		msg := value.messages[i]
		if msg.speaker == "You" {
			continue
		}
		stamp, parts := wingLines(msg, width, now)
		for j, part := range parts {
			if j == 0 {
				set(y, stamp+part, plain)
				if y < limit {
					lines[y].statusAt = 0
					lines[y].statusWidth = len(stamp)
					lines[y].statusTone = secondary
				}
			} else {
				set(y, strings.Repeat(" ", len(stamp))+part, plain)
			}
			y++
		}
		if !wide {
			break
		}
	}
}

// wingLines wraps one wing message under its time so a request is never cut
// mid-sentence; past two lines the rest stays in the session.
func wingLines(msg previewMessage, width int, now time.Time) (string, []string) {
	stamp := paperStamp(msg.timestamp, now)
	if stamp != "" {
		stamp += "  "
	}
	room := max(1, width-len(stamp))
	parts := wrap(msg.text, room)
	if len(parts) > 2 {
		parts = parts[:2]
		parts[1] = clip(parts[1]+" ...", room)
	}
	return stamp, parts
}

// newestWingLines is how many rows wing's newest message needs in a compact list.
func (s *state) newestWingLines(pin *row, width int, now time.Time) int {
	if pin == nil {
		return 1
	}
	messages := s.preview(*pin).messages
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].speaker != "You" {
			_, parts := wingLines(messages[i], width, now)
			return max(1, len(parts))
		}
	}
	return 1
}
func paperStamp(value string, now time.Time) string {
	stamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return ""
	}
	stamp = stamp.Local()
	if stamp.Format("2006-01-02") == now.Local().Format("2006-01-02") {
		return stamp.Format("15:04")
	}
	return stamp.Format("Jan 2 15:04")
}
func (s *state) paperPane(r row, now time.Time, width, height int) []string {
	label, _ := stateLabel(r)
	if s.stale(r.machine) {
		label = "? Last seen"
	}
	status := label
	// wing reports through its messages; an idle cell beside them misleads.
	if r.Pinned && r.Status != "blocked" && !s.stale(r.machine) {
		status = "pinned"
	}
	if !s.stale(r.machine) {
		status += " · " + age(r.UptimeSecs)
	}
	if r.attention == "unread" {
		status += " · * unread"
	}
	lines := []string{"SELECTED", r.name(), agentLabel(r.Agent) + " · " + machineLabel(r.machine) + " · " + r.project(), status}
	appendText := func(text string, limit int) {
		parts := wrap(text, width)
		if len(parts) > limit {
			parts = parts[:limit]
			parts[limit-1] = clip(parts[limit-1]+" ...", width)
		}
		lines = append(lines, parts...)
	}
	// Speaker and time sit in fixed columns; text wraps under itself.
	appendMessage := func(speaker, stamp, text string, limit int) {
		parts := wrap(text, max(1, width-13))
		if len(parts) > limit {
			parts = parts[:limit]
			parts[limit-1] = clip(parts[limit-1]+" ...", width-13)
		}
		for i, part := range parts {
			if i == 0 {
				lines = append(lines, field(speaker, 7)+field(stamp, 6)+part)
			} else {
				lines = append(lines, strings.Repeat(" ", 13)+part)
			}
		}
	}
	value := s.preview(r)
	if s.stale(r.machine) {
		lines = append(lines, "")
		appendText(connection(s.machines[r.machine], now), 3)
		appendText(routeCause(s.machines[r.machine].err), 3)
	} else if len(value.messages) > 0 {
		lines = append(lines, "", "RECENT")
		messages := value.messages
		if len(messages) > 3 {
			messages = messages[len(messages)-3:]
		}
		for i := len(messages) - 1; i >= 0; i-- {
			msg := messages[i]
			speaker := strings.ToLower(msg.speaker)
			if r.Pinned && msg.speaker != "You" {
				speaker = "wing"
			}
			appendMessage(speaker, paperStamp(msg.timestamp, now), msg.text, 3)
		}
	} else {
		lines = append(lines, "")
		appendText(lastLine(s, r), 3)
	}
	action := "Enter open   i details"
	switch {
	case s.stale(r.machine):
		action = "R reconnect   i details"
	case r.Status == "blocked":
		if p, ok := s.livePermission(r); ok {
			lines = append(lines, "", "WILL RUN")
			appendExact := func(text string) {
				parts := permissionWrap(text, width)
				if len(parts) > 4 {
					parts = parts[:4]
					parts[3] = clip(parts[3]+" ...", width)
				}
				lines = append(lines, parts...)
			}
			appendExact(fmt.Sprint(p.Arguments["command"]))
			appendExact(permissionWhere(p))
			action = "a review   d decline   Enter open"
			break
		}
		lines = append(lines, "", "ASKING")
		appendText(s.askingText(r), 3)
		appendText(s.askingWhere(r), 3)
		action = "Answer in terminal · Enter open"
	}
	lines = append(lines, "", action, "", "MACHINES")
	names := s.machineNames()
	for i, name := range names {
		if i >= 4 {
			lines = append(lines, fmt.Sprintf("%d more · m machines", len(names)-i))
			break
		}
		m := s.machines[name]
		state := "connected"
		if name == "local" {
			state = "this computer"
		}
		if s.stale(name) {
			state = "unreachable"
			if accessDenied(m.err) {
				state = accessState(m.err)
			}
			if !m.updated.IsZero() {
				state = "last seen " + paperStamp(m.updated.Format(time.RFC3339), now)
			}
		} else if m.updated.IsZero() {
			state = "loading"
		}
		entry := field(machineLabel(name), 14) + field(state, 17) + s.machineCount(name)
		if runewidth.StringWidth(entry) > width {
			entry = machineLabel(name) + " · " + state + " · " + s.machineCount(name)
		}
		appendText(entry, 2)
	}
	done, failed := 0, 0
	var unknown []string
	for name, m := range s.machines {
		if m.err != "" || m.updated.IsZero() {
			unknown = append(unknown, machineLabel(name))
		}
		if s.stale(name) {
			continue
		}
		for _, session := range m.sessions {
			stamp, err := time.Parse(time.RFC3339Nano, session.StateObservedAt)
			if err != nil || stamp.Local().Format("2006-01-02") != now.Local().Format("2006-01-02") {
				continue
			}
			switch s.sessionRow(name, session).Status {
			case "done":
				done++
			case "failed":
				failed++
			}
		}
	}
	today := fmt.Sprintf("%d done · %d failed", done, failed)
	switch {
	case s.registryError != "" || len(unknown) > 1:
		today += " · some machines unknown"
	case len(unknown) == 1:
		today += " · " + unknown[0] + " unknown"
	}
	lines = append(lines, "", "TODAY", today)
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines
}

// Match the bounded table viewport used by render; scrolling cancels old reads.
func (s *state) visibleReports(width, height int) []row {
	if height < 18 || s.recent || (s.mode != navigate && s.mode != filter) {
		return nil
	}
	var work []row
	pin := false
	for _, r := range s.rows() {
		if r.Pinned && !pin {
			pin = true
			continue
		}
		work = append(work, r)
	}
	count := max(0, max(13, height-12)-8)
	if width >= 120 && height >= 30 {
		count = min(7, max(0, (height-13)/2))
	}
	if len(work) > count && count > 2 {
		count--
	}
	index := 0
	for i, r := range work {
		if r.key() == s.selected {
			index = i
			break
		}
	}
	start := max(0, min(index-count/2, len(work)-count))
	return work[start:min(start+count, len(work))]
}
