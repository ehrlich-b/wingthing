package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/fsutil"
	"gopkg.in/yaml.v3"
)

// contextDocument retains fields unknown to this version, comments, and scalar
// types. Context changes must not round-trip the whole config through a struct.
func contextDocument(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		data = []byte("{}\n")
	} else if err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err == io.EOF {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	} else if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("%s must contain one YAML document", path)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s must contain a YAML mapping", path)
	}
	// Decode solely to reject duplicate keys before editing anything.
	var checked map[string]any
	if err := doc.Decode(&checked); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &doc, nil
}

func contextField(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func localContextKey(node *yaml.Node) string {
	if contextField(node, "machine_context") != nil {
		return "machine_context"
	}
	if field := contextField(node, "context"); field != nil && field.Kind != yaml.ScalarNode {
		return "machine_context" // Preserve the existing context service block.
	}
	return "context"
}

func contextValue(node *yaml.Node, key string) (string, error) {
	field := contextField(node, key)
	if field == nil {
		return "", nil
	}
	if field.Kind != yaml.ScalarNode || field.Tag != "!!str" {
		return "", fmt.Errorf("%s must be a string", key)
	}
	if err := ValidateMachineContext(field.Value); err != nil {
		return "", err
	}
	return field.Value, nil
}

func localContextDocument(dir string) (*yaml.Node, error) {
	// Apply the loader's validation before selecting or changing a context key.
	// Keep the original YAML nodes so unrelated settings survive the edit.
	if _, err := LoadWingConfig(dir); err != nil {
		return nil, err
	}
	return contextDocument(filepath.Join(dir, "wing.yaml"))
}

// MachineContexts lists saved metadata without contacting any machine or
// creating local state. An unset context is represented by the empty string.
func MachineContexts(dir string) (map[string]string, error) {
	wing, err := localContextDocument(dir)
	if err != nil {
		return nil, err
	}
	local, err := contextValue(wing.Content[0], localContextKey(wing.Content[0]))
	if err != nil {
		return nil, err
	}
	result := map[string]string{"local": local}
	doc, err := contextDocument(filepath.Join(dir, "remotes.yaml"))
	if err != nil {
		return nil, err
	}
	remotes := contextField(doc.Content[0], "remotes")
	if remotes == nil {
		return result, nil
	}
	if remotes.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("remotes must be a mapping")
	}
	for i := 0; i+1 < len(remotes.Content); i += 2 {
		name, remote := remotes.Content[i].Value, remotes.Content[i+1]
		if err := ValidateRemoteName(name); err != nil {
			return nil, err
		}
		if remote.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("remote %q must be a mapping", name)
		}
		value, err := contextValue(remote, "context")
		if err != nil {
			return nil, fmt.Errorf("remote %q: %w", name, err)
		}
		result[name] = value
	}
	return result, nil
}

// SetMachineContext changes only the chosen context key. none removes it.
func SetMachineContext(dir, machine, value string) error {
	if value != "home" && value != "work" && value != "none" {
		return fmt.Errorf("context must be home, work, or none")
	}
	filename := "wing.yaml"
	if machine != "local" {
		if err := ValidateRemoteName(machine); err != nil {
			return err
		}
		filename = "remotes.yaml"
	}
	path := filepath.Join(dir, filename)
	load := func() (*yaml.Node, *yaml.Node, error) {
		var doc *yaml.Node
		var err error
		if machine == "local" {
			doc, err = localContextDocument(dir)
		} else {
			doc, err = contextDocument(path)
		}
		if err != nil {
			return nil, nil, err
		}
		target := doc.Content[0]
		if machine != "local" {
			remotes := contextField(target, "remotes")
			if remotes == nil || remotes.Kind != yaml.MappingNode {
				return nil, nil, fmt.Errorf("unknown machine %q", machine)
			}
			target = contextField(remotes, machine)
			if target == nil {
				return nil, nil, fmt.Errorf("unknown machine %q", machine)
			}
		}
		if target.Kind != yaml.MappingNode {
			return nil, nil, fmt.Errorf("machine %q must be a mapping", machine)
		}
		if target.Anchor != "" {
			return nil, nil, fmt.Errorf("machine %q uses a YAML anchor; use explicit keys before setting context", machine)
		}
		// Inherited keys cannot be removed without changing their source or
		// materializing unrelated keys. Refuse rather than change their meaning.
		if contextField(target, "<<") != nil {
			return nil, nil, fmt.Errorf("machine %q uses YAML merges; use explicit keys before setting context", machine)
		}
		return doc, target, nil
	}
	if _, _, err := load(); err != nil {
		return err // Unknown machines never create a directory or lock file.
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lockName := ".wing.yaml.lock"
	if machine != "local" {
		lockName = ".remotes.lock"
	}
	lock, err := acquireConfigLock(dir, lockName)
	if err != nil {
		return err
	}
	defer lock.Close()
	doc, target, err := load()
	if err != nil {
		return err
	}
	key := "context"
	if machine == "local" {
		key = localContextKey(target)
		// Creating wing.yaml switches LoadWingConfig away from the legacy
		// wing-id fallback. Carry that effective identity into the new file,
		// under the same lock as the context edit, without changing existing IDs.
		if _, err := os.Stat(path); os.IsNotExist(err) {
			cfg, err := LoadWingConfig(dir)
			if err != nil {
				return err
			}
			if cfg.WingID != "" {
				target.Content = append(target.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "wing_id"},
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: cfg.WingID})
			}
		} else if err != nil {
			return err
		}
	}
	for i := 0; i+1 < len(target.Content); i += 2 {
		if target.Content[i].Value == key {
			if target.Content[i+1].Anchor != "" {
				return fmt.Errorf("context uses a YAML anchor; use an explicit value before setting context")
			}
			if value == "none" {
				target.Content = append(target.Content[:i], target.Content[i+2:]...)
			} else {
				field := target.Content[i+1]
				field.Kind, field.Tag, field.Value = yaml.ScalarNode, "!!str", value
				field.Content, field.Alias = nil, nil
			}
			return saveContextDocument(dir, path, doc)
		}
	}
	if value == "none" {
		return nil
	}
	target.Content = append(target.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
	return saveContextDocument(dir, path, doc)
}

func saveContextDocument(dir, path string, doc *yaml.Node) error {
	data, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".context-*")
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return fsutil.SyncDirectory(dir)
}
