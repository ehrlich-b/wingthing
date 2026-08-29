package promptmgr

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	namePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	variablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	revisionPattern = regexp.MustCompile(`^[a-f0-9]{12}$`)
)

var ErrNotFound = errors.New("prompt not found")

// Asset is a named, versioned prompt template. Revision is content-addressed;
// timestamps are metadata and do not change the revision.
type Asset struct {
	Name        string   `json:"name" yaml:"name"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Template    string   `json:"template" yaml:"template"`
	Variables   []string `json:"variables,omitempty" yaml:"variables,omitempty"`
	Agent       string   `json:"agent,omitempty" yaml:"agent,omitempty"`
	CWD         string   `json:"cwd,omitempty" yaml:"cwd,omitempty"`
	Revision    string   `json:"revision" yaml:"revision"`
	CreatedAt   string   `json:"created_at" yaml:"created_at"`
	UpdatedAt   string   `json:"updated_at" yaml:"updated_at"`
}

type Store struct {
	dir string
}

func New(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) List() ([]Asset, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Asset{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list prompts: %w", err)
	}
	assets := make([]Asset, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		asset, getErr := s.Get(name, "")
		if getErr != nil {
			return nil, getErr
		}
		assets = append(assets, *asset)
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	return assets, nil
}

func (s *Store) Get(name, revision string) (*Asset, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir, name+".yaml")
	if revision != "" {
		if !revisionPattern.MatchString(revision) {
			return nil, fmt.Errorf("invalid prompt revision %q", revision)
		}
		path = filepath.Join(s.dir, "_history", name, revision+".yaml")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if revision == "" {
			return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
		}
		return nil, fmt.Errorf("%w: %q revision %q", ErrNotFound, name, revision)
	}
	if err != nil {
		return nil, fmt.Errorf("read prompt %q: %w", name, err)
	}
	asset, err := decodeAsset(data)
	if err != nil {
		return nil, fmt.Errorf("decode prompt %q: %w", name, err)
	}
	if asset.Name != name {
		return nil, fmt.Errorf("prompt file %q declares name %q", name, asset.Name)
	}
	computed, err := contentRevision(asset)
	if err != nil {
		return nil, err
	}
	if asset.Revision != computed {
		return nil, fmt.Errorf("prompt %q revision mismatch: stored %q computed %q", name, asset.Revision, computed)
	}
	if revision != "" && asset.Revision != revision {
		return nil, fmt.Errorf("prompt %q history file contains revision %q", name, asset.Revision)
	}
	return asset, nil
}

// Save atomically updates the current asset and preserves the content-addressed
// revision in _history. expectedRevision prevents one client from silently
// overwriting a concurrent edit; omit it to create or force an update.
func (s *Store) Save(asset Asset, expectedRevision string) (*Asset, error) {
	if err := validateAsset(&asset); err != nil {
		return nil, err
	}
	var existing *Asset
	existing, err := s.Get(asset.Name, "")
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if expectedRevision != "" {
		if existing == nil || existing.Revision != expectedRevision {
			actual := ""
			if existing != nil {
				actual = existing.Revision
			}
			return nil, fmt.Errorf("prompt %q revision conflict: expected %q, current %q", asset.Name, expectedRevision, actual)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if existing != nil {
		asset.CreatedAt = existing.CreatedAt
	} else {
		asset.CreatedAt = now
	}
	asset.UpdatedAt = now
	asset.Variables = append([]string(nil), asset.Variables...)
	sort.Strings(asset.Variables)
	asset.Revision, err = contentRevision(&asset)
	if err != nil {
		return nil, err
	}
	encoded, err := yaml.Marshal(&asset)
	if err != nil {
		return nil, fmt.Errorf("encode prompt: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return nil, fmt.Errorf("create prompt directory: %w", err)
	}
	historyDir := filepath.Join(s.dir, "_history", asset.Name)
	if err := os.MkdirAll(historyDir, 0700); err != nil {
		return nil, fmt.Errorf("create prompt history: %w", err)
	}
	historyPath := filepath.Join(historyDir, asset.Revision+".yaml")
	if _, statErr := os.Stat(historyPath); errors.Is(statErr, os.ErrNotExist) {
		if err := writeAtomic(historyPath, encoded); err != nil {
			return nil, err
		}
	}
	if err := writeAtomic(filepath.Join(s.dir, asset.Name+".yaml"), encoded); err != nil {
		return nil, err
	}
	return &asset, nil
}

func Render(asset *Asset, values map[string]string) (string, error) {
	if asset == nil {
		return "", errors.New("prompt asset is required")
	}
	declared := make(map[string]bool, len(asset.Variables))
	for _, name := range asset.Variables {
		declared[name] = true
		if _, ok := values[name]; !ok {
			return "", fmt.Errorf("prompt variable %q is required", name)
		}
	}
	for name := range values {
		if !declared[name] {
			return "", fmt.Errorf("prompt variable %q is not declared", name)
		}
	}
	tmpl, err := template.New(asset.Name).Option("missingkey=error").Parse(asset.Template)
	if err != nil {
		return "", fmt.Errorf("parse prompt template: %w", err)
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, values); err != nil {
		return "", fmt.Errorf("render prompt template: %w", err)
	}
	return rendered.String(), nil
}

func validateAsset(asset *Asset) error {
	if err := validateName(asset.Name); err != nil {
		return err
	}
	if strings.TrimSpace(asset.Template) == "" {
		return errors.New("prompt template is required")
	}
	if asset.CWD != "" && !filepath.IsAbs(asset.CWD) {
		return errors.New("prompt cwd must be absolute when set")
	}
	seen := make(map[string]bool, len(asset.Variables))
	for _, name := range asset.Variables {
		if !variablePattern.MatchString(name) {
			return fmt.Errorf("invalid prompt variable %q", name)
		}
		if seen[name] {
			return fmt.Errorf("duplicate prompt variable %q", name)
		}
		seen[name] = true
	}
	return nil
}

func validateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("invalid prompt name %q", name)
	}
	return nil
}

func contentRevision(asset *Asset) (string, error) {
	content := struct {
		Name, Description, Template, Agent, CWD string
		Variables                               []string
	}{asset.Name, asset.Description, asset.Template, asset.Agent, asset.CWD, append([]string(nil), asset.Variables...)}
	sort.Strings(content.Variables)
	encoded, err := json.Marshal(content)
	if err != nil {
		return "", fmt.Errorf("hash prompt: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])[:12], nil
}

func decodeAsset(data []byte) (*Asset, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var asset Asset
	if err := decoder.Decode(&asset); err != nil {
		return nil, err
	}
	if err := validateAsset(&asset); err != nil {
		return nil, err
	}
	return &asset, nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".prompt-*.tmp")
	if err != nil {
		return fmt.Errorf("create prompt temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install prompt file: %w", err)
	}
	return nil
}
