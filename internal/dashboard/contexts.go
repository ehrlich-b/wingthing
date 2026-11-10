package dashboard

import (
	"fmt"
	"sort"
	"time"
	"unicode"
)

func (s *state) machineContext(name string) string {
	if name == "local" {
		return s.localContext
	}
	return s.remotes[name].Context
}
func contextLabel(name string) string {
	if name == "" {
		return "Other"
	}
	runes := []rune(name)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}
func (s *state) contexts() []string {
	seen := map[string]bool{}
	for name := range s.machines {
		seen[s.machineContext(name)] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func (s *state) hasContexts() bool {
	for name := range s.machines {
		if s.machineContext(name) != "" {
			return true
		}
	}
	return false
}
func (s *state) cycleContext() {
	if !s.hasContexts() && !s.contextFiltered {
		return
	}
	names := s.contexts()
	next := 0
	if s.contextFiltered {
		next = len(names)
		for i, name := range names {
			if name == s.contextFilter {
				next = i + 1
				break
			}
		}
	}
	s.contextFiltered = next < len(names)
	s.contextFilter = ""
	s.machineFilter = ""
	if s.contextFiltered {
		s.contextFilter = names[next]
	}
	s.selectionLost, s.selected, s.message = false, "", ""
	s.selectionTouched = true
	s.reconcile()
}
func (s *state) contextNeedsYou(context string) int {
	count := 0
	for name, machine := range s.machines {
		if s.machineContext(name) != context || s.stale(name) {
			continue
		}
		for _, session := range machine.sessions {
			if s.sessionRow(name, session).attention == "blocked" {
				count++
			}
		}
	}
	return count
}

// Headings participate in the viewport, never in selection or action routing.
func (s *state) renderContexts(lines []viewLine, work []row, top, bottom, width int, now time.Time) {
	type item struct {
		line         viewLine
		key, context string
		session      bool
	}
	var items []item
	for _, context := range s.contexts() {
		if s.contextFiltered && context != s.contextFilter {
			continue
		}
		var group []item
		for _, name := range s.machineNames() {
			if s.machineContext(name) != context || s.machineFilter != "" && s.machineFilter != name {
				continue
			}
			found := false
			for _, r := range work {
				if r.machine != name {
					continue
				}
				found = true
				group = append(group, item{sessionLine(s, r, width, now), r.key(), context, true})
			}
			if !found && s.filter == "" && !s.attentionOnly {
				text := machineLabel(name) + " · No sessions"
				if m := s.machines[name]; m.err != "" || m.updated.IsZero() {
					text = machineLabel(name) + " · " + connection(m, now)
				}
				group = append(group, item{line: viewLine{text: "  " + text, tone: secondary}, context: context})
			}
		}
		if len(group) == 0 {
			continue
		}
		items = append(items, item{line: viewLine{text: fmt.Sprintf("%s · %d needs you", clean(contextLabel(context)), s.contextNeedsYou(context)), tone: secondary}, context: context})
		items = append(items, group...)
	}
	if len(items) == 0 {
		return
	}
	set := func(y int, line viewLine) {
		if y < 0 || y >= len(lines) {
			return
		}
		old := lines[y]
		line.split, line.frame, line.right, line.rightTone = old.split, old.frame, old.right, old.rightTone
		lines[y] = line
	}
	capacity := max(0, bottom-top)
	if capacity == 0 {
		return
	}
	for y := top; y <= bottom && y < len(lines); y++ {
		if y >= 0 {
			set(y, viewLine{})
		}
	}
	if len(items) > capacity {
		capacity--
	}
	if capacity <= 0 {
		return
	}
	selected := 0
	for i, item := range items {
		if item.key == s.selected && item.key != "" {
			selected = i
		}
	}
	start := max(0, min(selected-capacity/2, len(items)-max(1, capacity-1)))
	// Repeat the heading when the viewport starts inside a context.
	sticky := capacity > 1 && start > 0 && items[start].session
	used := 0
	if sticky {
		set(top, viewLine{text: fmt.Sprintf("%s · %d needs you", clean(contextLabel(items[start].context)), s.contextNeedsYou(items[start].context)), tone: secondary})
		used++
	}
	end := min(len(items), start+capacity-used)
	for i := start; i < end; i++ {
		set(top+used+i-start, items[i].line)
	}
	hidden := 0
	for i, item := range items {
		if item.session && (i < start || i >= end) {
			hidden++
		}
	}
	if hidden > 0 && bottom < len(lines) {
		set(bottom, viewLine{text: fmt.Sprintf("  +%d more · j/k move", hidden), tone: secondary})
	}
}
