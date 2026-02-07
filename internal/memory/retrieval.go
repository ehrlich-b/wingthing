package memory

import "strings"

// MemoryEntry represents a retrieved memory file with its name and body content.
type MemoryEntry struct {
	Name string
	Body string
}

// Retrieve returns memory file contents in priority order:
//   - Layer 1: index.md (always)
//   - Layer 2: skill-declared deps (always, additive floor)
//   - Layer 3: keyword match task prompt against frontmatter tags and headings
//   - Layer 4: placeholder — thread injection handled by orchestrator
//
// All returned content has frontmatter stripped. Missing files produce empty body + warning.
func (s *MemoryStore) Retrieve(taskPrompt string, skillDeps []string) []MemoryEntry {
	seen := make(map[string]bool)
	var entries []MemoryEntry

	// Layer 1: index.md always loaded
	entries = append(entries, MemoryEntry{Name: "index", Body: s.Index()})
	seen["index"] = true

	// Layer 2: skill-declared deps
	for _, dep := range skillDeps {
		if seen[dep] {
			continue
		}
		seen[dep] = true
		entries = append(entries, MemoryEntry{Name: dep, Body: s.Load(dep)})
	}

	// Layer 3: keyword match against tags and headings
	if taskPrompt != "" {
		s.LoadAll() // ensure all files are cached for matching
		keywords := tokenize(taskPrompt)
		for name, mf := range s.cache {
			if seen[name] {
				continue
			}
			if matchKeywords(keywords, mf.tags, mf.headings) {
				seen[name] = true
				entries = append(entries, MemoryEntry{Name: name, Body: mf.body})
			}
		}
	}

	return entries
}

// tokenize splits a prompt into lowercase words for keyword matching.
func tokenize(s string) []string {
	words := strings.Fields(strings.ToLower(s))
	unique := make(map[string]bool, len(words))
	var result []string
	for _, w := range words {
		// Strip common punctuation
		w = strings.Trim(w, ".,;:!?\"'()[]{}#")
		if len(w) < 2 {
			continue
		}
		if unique[w] {
			continue
		}
		unique[w] = true
		result = append(result, w)
	}
	return result
}

// matchKeywords returns true if any keyword appears in tags or headings.
func matchKeywords(keywords, tags, headings []string) bool {
	for _, kw := range keywords {
		for _, tag := range tags {
			if tag == kw {
				return true
			}
		}
		for _, heading := range headings {
			// Check if keyword appears as a word in the heading
			for _, hw := range strings.Fields(heading) {
				hw = strings.Trim(hw, ".,;:!?\"'()[]{}#")
				if hw == kw {
					return true
				}
			}
		}
	}
	return false
}
