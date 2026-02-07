package memory

// Index returns the body of index.md (Layer 1, always loaded).
// Returns empty string if index.md is missing.
func (s *MemoryStore) Index() string {
	return s.Load("index")
}
