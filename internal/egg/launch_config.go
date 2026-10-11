package egg

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PolicyRuleSource describes effective declared rules, including inherited
// denials. Automatic provider holes are attributed separately by ResolvePolicy.
type PolicyRuleSource struct {
	Section string `json:"section"`
	Rule    string `json:"rule"`
	Source  string `json:"source"`
}

type explicitPolicy struct {
	cfg   *EggConfig
	rules []PolicyRuleSource
}

// ResolveLaunchConfig is an explicit, read-only alternative to discovery. Empty
// path means built-in defaults; named bases require basesDir. It never reads a
// wing, HOME, environment variables, or credential contents. Parsing and merging
// use the runtime's production implementations. Relative FS rules remain rooted
// at the proposed launch CWD, just as in the runtime.
func ResolveLaunchConfig(path, basesDir, home string) (*EggConfig, []PolicyRuleSource, error) {
	if !filepath.IsAbs(home) {
		return nil, nil, errors.New("explicit absolute home required")
	}
	if path == "" {
		cfg := DefaultEggConfig()
		return cfg, configRuleSources(cfg, "built-in defaults"), nil
	}
	if !filepath.IsAbs(path) || basesDir != "" && !filepath.IsAbs(basesDir) {
		return nil, nil, errors.New("absolute policy and bases paths required")
	}
	resolver := launchConfigResolver{bases: basesDir, home: home, active: map[string]bool{}}
	result, err := resolver.resolve(path, 0)
	return result.cfg, result.rules, err
}

type launchConfigResolver struct {
	bases, home string
	active      map[string]bool
}

func (r *launchConfigResolver) basePath(name, dir string) (string, error) {
	if filepath.IsAbs(name) {
		return name, nil
	}
	if strings.HasPrefix(name, "./") || strings.HasPrefix(name, "../") {
		return filepath.Join(dir, name), nil
	}
	if r.bases == "" {
		return "", errors.New("named base requires an explicit bases directory")
	}
	if name == "" || filepath.Base(name) != name {
		return "", errors.New("invalid named base")
	}
	return filepath.Join(r.bases, name+".yaml"), nil
}
func (r *launchConfigResolver) resolve(path string, depth int) (explicitPolicy, error) {
	var zero explicitPolicy
	if depth > maxBaseDepth {
		return zero, errors.New("egg config base chain too deep")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return zero, fmt.Errorf("policy file unavailable: %s", filepath.Clean(path))
	}
	if r.active[real] {
		return zero, errors.New("egg config circular base reference")
	}
	r.active[real] = true
	defer delete(r.active, real)
	file, err := os.Open(real)
	if err != nil {
		return zero, errors.New("policy file cannot be opened")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return zero, errors.New("policy must be a regular file at most 64 KiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return zero, errors.New("policy read failed or exceeded 64 KiB")
	}
	child, err := LoadEggConfigFromYAML(string(data))
	if err != nil {
		return zero, errors.New("invalid egg policy; check its YAML and network fields")
	}
	childRules := configRuleSources(child, real)
	if child.Base.Name == "none" {
		if child.Base.HasMasks() {
			return zero, errors.New("base masks invalid with base: none")
		}
		return explicitPolicy{child, childRules}, nil
	}
	parent := explicitPolicy{cfg: DefaultEggConfig()}
	parent.rules = configRuleSources(parent.cfg, "built-in defaults")
	if child.Base.Name != "" {
		path, err := r.basePath(child.Base.Name, filepath.Dir(real))
		if err != nil {
			return zero, err
		}
		parent, err = r.resolve(path, depth+1)
		if err != nil {
			return zero, err
		}
	}
	for _, mask := range []struct{ section, name string }{{"fs", child.Base.FS}, {"network", child.Base.Network}, {"env", child.Base.Env}} {
		if mask.name == "" {
			continue
		}
		replacement := explicitPolicy{cfg: &EggConfig{}}
		if mask.name != "none" {
			path, err := r.basePath(mask.name, filepath.Dir(real))
			if err != nil {
				return zero, err
			}
			replacement, err = r.resolve(path, depth+1)
			if err != nil {
				return zero, err
			}
		}
		switch mask.section {
		case "fs":
			parent.cfg.FS = replacement.cfg.FS
		case "network":
			parent.cfg.Network = replacement.cfg.Network
		case "env":
			parent.cfg.Env = replacement.cfg.Env
		}
		retained := []PolicyRuleSource{}
		for _, source := range parent.rules {
			if source.Section != mask.section {
				retained = append(retained, source)
			}
		}
		for _, source := range replacement.rules {
			if source.Section == mask.section {
				retained = append(retained, source)
			}
		}
		parent.rules = retained
	}
	merged := mergeEggConfigForHome(parent.cfg, child, r.home)
	// Filter by effective membership; removed denies and section masks disappear.
	// Duplicate declarations retain each source so the denial remains explainable.
	rules := append(parent.rules, childRules...)
	effective := configRuleSources(merged, "")
	retained := []PolicyRuleSource{}
	for _, source := range rules {
		for _, rule := range effective {
			if source.Section == rule.Section && source.Rule == rule.Rule {
				retained = append(retained, source)
				break
			}
		}
	}
	return explicitPolicy{merged, retained}, nil
}
func configRuleSources(cfg *EggConfig, source string) []PolicyRuleSource {
	var result []PolicyRuleSource
	for _, entry := range cfg.FS {
		result = append(result, PolicyRuleSource{"fs", entry, source})
	}
	for _, entry := range cfg.Network.Domains {
		result = append(result, PolicyRuleSource{"network", entry, source})
	}
	if cfg.Network.AgentDomains != "" {
		result = append(result, PolicyRuleSource{"network", "agent_domains=" + cfg.Network.AgentDomains, source})
	}
	if cfg.Network.Mode != "" {
		result = append(result, PolicyRuleSource{"network", "mode=" + cfg.Network.Mode, source})
	}
	for _, entry := range cfg.Env {
		result = append(result, PolicyRuleSource{"env", entry, source})
	}
	return result
}
