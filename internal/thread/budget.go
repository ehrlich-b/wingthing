package thread

import (
	"github.com/ehrlich-b/wingthing/internal/store"
)

// RenderWithBudget renders entries newest-first, dropping oldest entries
// until the rendered output fits within budget characters.
// If budget <= 0, all entries are rendered.
func RenderWithBudget(entries []*store.ThreadEntry, budget int) string {
	if len(entries) == 0 {
		return ""
	}
	if budget <= 0 {
		return Render(entries)
	}

	// Try all entries first.
	rendered := Render(entries)
	if len(rendered) <= budget {
		return rendered
	}

	// Drop oldest entries (front of slice) until it fits.
	for start := 1; start < len(entries); start++ {
		rendered = Render(entries[start:])
		if len(rendered) <= budget {
			return rendered
		}
	}

	// Even a single newest entry exceeds budget — return it truncated.
	return rendered
}
