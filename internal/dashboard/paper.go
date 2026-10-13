package dashboard

import (
	"fmt"
	"strings"
	"time"
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
	return "No recent message reported. Enter open."
}

// Reports follow the visible work rows. At compact sizes the selected row gets
// the available line, while wing remains outside the scrollable session list.
func (s *state) paperReports(lines []viewLine, work []row, pin *row, start, count, end, width int, wide bool, now time.Time) {
	set := func(y int, text string, role tone) {
		if y >= 0 && (y < len(lines)-6 || (wide && y < len(lines)-5)) {
			lines[y].text, lines[y].tone = text, role
		}
	}
	y := end + 1
	set(y, " LAST LINE", secondary)
	y++
	if wide {
		for i := start; i < min(start+count, len(work)); i++ {
			r := work[i]
			titleWidth := min(24, max(12, width/3))
			set(y, " "+field(r.name(), titleWidth)+" "+lastLine(s, r), plain)
			y++
		}
	} else if r, ok := s.selection(); ok {
		set(y, " "+lastLine(s, r), plain)
		y++
	}
	if wide {
		y++
	}
	set(y, " FROM WING", secondary)
	y++
	if pin == nil {
		set(y, " No wing session reported.", secondary)
		return
	}
	value := s.preview(*pin)
	if len(value.messages) == 0 {
		set(y, " "+lastLine(s, *pin), plain)
		return
	}
	for i := len(value.messages) - 1; i >= 0; i-- {
		msg := value.messages[i]
		if msg.speaker == "You" {
			continue
		}
		stamp := paperStamp(msg.timestamp, now)
		if stamp != "" {
			stamp += "  "
		}
		set(y, " "+stamp+msg.text, plain)
		if y < len(lines)-6 || (wide && y < len(lines)-5) {
			lines[y].statusAt = 1
			lines[y].statusWidth = len(stamp)
			lines[y].statusTone = warning
		}
		y++
		if !wide {
			break
		}
	}
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
	lines := []string{"SELECTED", r.name(), agentLabel(r.Agent) + " · " + machineLabel(r.machine) + " · " + r.project(), label}
	if r.attention == "unread" {
		lines[3] += " · * unread"
	}
	appendText := func(text string, limit int) {
		parts := wrap(text, width)
		if len(parts) > limit {
			parts = parts[:limit]
			parts[limit-1] = clip(parts[limit-1], max(0, width-3)) + "..."
		}
		lines = append(lines, parts...)
	}
	lines = append(lines, "")
	if s.stale(r.machine) {
		appendText(connection(s.machines[r.machine], now), 3)
		appendText(routeCause(s.machines[r.machine].err)+" R retry.", 3)
	} else if value := s.preview(r); value.next != "" {
		appendText(value.next, 3)
	} else {
		appendText(lastLine(s, r), 3)
	}
	action := "Enter open   i full details"
	if s.stale(r.machine) {
		action = "R reconnect   i full details"
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
				state = "access denied"
			}
		} else if m.updated.IsZero() {
			state = "loading"
		}
		count := machineCount(m)
		appendText(machineLabel(name)+" · "+state+" · "+count, 2)
		if s.stale(name) && !m.updated.IsZero() {
			appendText("Last seen "+paperStamp(m.updated.Format(time.RFC3339), now), 1)
		}
	}
	done, failed := 0, 0
	for name, m := range s.machines {
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
	lines = append(lines, "", "TODAY", fmt.Sprintf("%d done · %d failed reported", done, failed))
	for _, m := range s.machines {
		if m.err != "" || m.updated.IsZero() || s.registryError != "" {
			lines = append(lines, "Some machines unavailable.")
			break
		}
	}
	lines = append(lines, "", "RECENT")
	for _, msg := range s.preview(r).messages {
		stamp := paperStamp(msg.timestamp, now)
		header := msg.speaker
		if stamp != "" {
			header += " · " + stamp
		}
		appendText(header, 1)
		appendText(msg.text, 2)
	}
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
