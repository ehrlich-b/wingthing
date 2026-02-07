package thread

import (
	"fmt"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/store"
)

// Render renders thread entries to markdown.
// When entries come from multiple machines, the machine origin is shown in the header.
func Render(entries []*store.ThreadEntry) string {
	if len(entries) == 0 {
		return ""
	}

	machines := make(map[string]bool)
	for _, e := range entries {
		machines[e.MachineID] = true
	}
	multiMachine := len(machines) > 1

	var b strings.Builder
	for i, e := range entries {
		if i > 0 {
			b.WriteString("\n")
		}
		skill := "ad-hoc"
		if e.Skill != nil {
			skill = *e.Skill
		}
		agent := "unknown"
		if e.Agent != nil {
			agent = *e.Agent
		}
		ts := e.Timestamp.Format("15:04")
		if multiMachine {
			fmt.Fprintf(&b, "## %s — %s [%s, %s, %s]\n", ts, e.Summary, agent, skill, e.MachineID)
		} else {
			fmt.Fprintf(&b, "## %s — %s [%s, %s]\n", ts, e.Summary, agent, skill)
		}
		if e.UserInput != nil {
			fmt.Fprintf(&b, "> User: %q\n", *e.UserInput)
		}
		fmt.Fprintf(&b, "- %s\n", e.Summary)
	}
	return b.String()
}

// RenderDay fetches entries for a given day and renders them within budget.
func RenderDay(s *store.Store, date time.Time, budget int) (string, error) {
	entries, err := s.ListThreadByDate(date)
	if err != nil {
		return "", err
	}
	return RenderWithBudget(entries, budget), nil
}
