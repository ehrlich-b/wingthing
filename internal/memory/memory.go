package memory

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// MemoryStore holds the path to the memory directory and caches parsed files.
type MemoryStore struct {
	dir   string
	cache map[string]*memoryFile
}

type memoryFile struct {
	frontmatter map[string]any
	body        string
	tags        []string
	headings    []string
}

// New creates a MemoryStore for the given directory.
func New(dir string) *MemoryStore {
	return &MemoryStore{
		dir:   dir,
		cache: make(map[string]*memoryFile),
	}
}

// Dir returns the memory directory path.
func (s *MemoryStore) Dir() string {
	return s.dir
}

// Load reads and caches a memory file by name (without .md extension).
// Returns the body with frontmatter stripped. Missing files return empty string + warning.
func (s *MemoryStore) Load(name string) string {
	if mf, ok := s.cache[name]; ok {
		return mf.body
	}

	mf, err := s.parseFile(name)
	if err != nil {
		log.Printf("memory: warning: %s: %v", name, err)
		return ""
	}
	s.cache[name] = mf
	return mf.body
}

// LoadAll reads all .md files in the memory directory into cache.
func (s *MemoryStore) LoadAll() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		log.Printf("memory: warning: read dir %s: %v", s.dir, err)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		if _, ok := s.cache[name]; !ok {
			if mf, err := s.parseFile(name); err == nil {
				s.cache[name] = mf
			}
		}
	}
}

func (s *MemoryStore) parseFile(name string) (*memoryFile, error) {
	path := filepath.Join(s.dir, name+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	fm, body := parseFrontmatter(data)

	mf := &memoryFile{
		frontmatter: fm,
		body:        body,
		tags:        extractTags(fm),
		headings:    extractHeadings(body),
	}
	return mf, nil
}

// parseFrontmatter splits YAML frontmatter (between --- fences) from the body.
func parseFrontmatter(data []byte) (map[string]any, string) {
	content := string(data)

	if !strings.HasPrefix(content, "---\n") {
		return nil, content
	}

	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return nil, content
	}

	yamlBlock := content[4 : 4+end]
	body := content[4+end+4:] // skip past closing "\n---"
	// Strip the leading newline(s) after the closing fence
	body = strings.TrimLeft(body, "\n")

	var fm map[string]any
	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
		return nil, string(data)
	}
	return fm, body
}

// parseFrontmatterBytes is used by tests that want both parts.
func parseFrontmatterBytes(data []byte) (map[string]any, string) {
	return parseFrontmatter(data)
}

func extractTags(fm map[string]any) []string {
	if fm == nil {
		return nil
	}
	raw, ok := fm["tags"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []any:
		tags := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				tags = append(tags, strings.ToLower(s))
			}
		}
		return tags
	}
	return nil
}

func extractHeadings(body string) []string {
	var headings []string
	scanner := bytes.NewBufferString(body)
	for {
		line, err := scanner.ReadString('\n')
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			// Strip leading # and whitespace
			heading := strings.TrimLeft(trimmed, "#")
			heading = strings.TrimSpace(heading)
			if heading != "" {
				headings = append(headings, strings.ToLower(heading))
			}
		}
		if err != nil {
			break
		}
	}
	return headings
}

// Frontmatter returns the parsed frontmatter for a file, loading it if needed.
func (s *MemoryStore) Frontmatter(name string) map[string]any {
	if _, ok := s.cache[name]; !ok {
		s.Load(name) // populates cache or warns
	}
	mf, ok := s.cache[name]
	if !ok {
		return nil
	}
	return mf.frontmatter
}

func (s *MemoryStore) tags(name string) []string {
	mf, ok := s.cache[name]
	if !ok {
		return nil
	}
	return mf.tags
}

func (s *MemoryStore) headings(name string) []string {
	mf, ok := s.cache[name]
	if !ok {
		return nil
	}
	return mf.headings
}
