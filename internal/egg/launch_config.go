package egg

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/fsutil"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

// PolicyRuleSource describes effective declared rules, including inherited
// denials. Automatic provider holes are attributed separately by ResolvePolicy.
// Source retains the cleaned input filename for display; canonical filenames
// are used separately for file access and cycle detection.
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
// use the same resolver as runtime discovery. The home argument is the merge-time
// host HOME, independently of execution HOME.
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
	resolver := configResolver{bases: basesDir, home: home, active: map[string]bool{}, visited: map[string]bool{}}
	result, err := resolver.resolve(path, 0)
	return result.cfg, result.rules, err
}

type configResolver struct {
	bases, home string
	runtime     bool
	active      map[string]bool
	visited     map[string]bool
	reads       int
}

func (r *configResolver) basePath(name, dir string) (string, error) {
	if r.runtime {
		return resolveBasePath(name, dir), nil
	}
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
func (r *configResolver) resolve(path string, depth int) (explicitPolicy, error) {
	var zero explicitPolicy
	r.reads++
	if !r.runtime && r.reads > 32 {
		return zero, errors.New("policy resolution exceeds 32 file reads")
	}
	if depth > maxBaseDepth {
		return zero, errors.New("egg config base chain too deep")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return zero, err
	}
	lexical := filepath.Clean(path)
	if r.visited[lexical] {
		return zero, errors.New("egg config circular or repeated base reference: runtime policy resolver rejects repeated base references")
	}
	r.visited[lexical] = true
	var child *EggConfig
	if r.runtime {
		child, err = LoadEggConfig(path)
	} else {
		var real string
		real, err = filepath.EvalSymlinks(path)
		if err != nil {
			return zero, fmt.Errorf("policy file unavailable: %s", lexical)
		}
		if r.active[real] {
			return zero, errors.New("egg config circular base reference")
		}
		r.active[real] = true
		defer delete(r.active, real)
		child, err = loadLaunchPolicy(real)
	}
	if err != nil {
		return zero, err
	}
	childRules := configRuleSources(child, lexical)
	if child.Base.Name == "none" {
		if child.Base.HasMasks() {
			return zero, errors.New("base masks invalid with base: none (nothing to mask)")
		}
		return explicitPolicy{child, childRules}, nil
	}
	parent := explicitPolicy{cfg: DefaultEggConfig()}
	parent.rules = configRuleSources(parent.cfg, "built-in defaults")
	if child.Base.Name != "" {
		path, err := r.basePath(child.Base.Name, filepath.Dir(path))
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
			path, err := r.basePath(mask.name, filepath.Dir(path))
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

// loadLaunchPolicy adds bounded, regular-file reads to the shared resolver.
func loadLaunchPolicy(path string) (*EggConfig, error) {
	file, err := fsutil.OpenBoundedRegular(path, 64<<10)
	if err != nil {
		return nil, errors.New("policy file cannot be opened")
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil, errors.New("policy read failed or exceeded 64 KiB")
	}
	child, err := LoadEggConfigFromYAML(string(data))
	if err != nil {
		return nil, errors.New("invalid egg policy; check its YAML and network fields")
	}
	return child, nil
}

// RebaseLaunchConfig anchors relative FS grants and denials at the selected
// configured root. It does not change the caller's config or expand execution HOME.
func RebaseLaunchConfig(cfg *EggConfig, policyBase string) *EggConfig {
	copyCfg := *cfg
	copyCfg.FS = append([]string(nil), cfg.FS...)
	for i, rule := range copyCfg.FS {
		mode, path, hasMode := strings.Cut(rule, ":")
		if !hasMode {
			path = rule
		}
		if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~") {
			copyCfg.FS[i] = filepath.Join(policyBase, path)
			if hasMode {
				copyCfg.FS[i] = mode + ":" + copyCfg.FS[i]
			}
		}
	}
	return &copyCfg
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
		if strings.ContainsAny(entry, "=:\t\r\n ") {
			result = append(result, PolicyRuleSource{"env_invalid", "[selector contents redacted]", source})
			continue
		}
		result = append(result, PolicyRuleSource{"env", entry, source})
	}
	return result
}

// PolicyDenialSources attributes a resolved denial using the shared path expansion.
func PolicyDenialSources(prefix, deny string, rules []PolicyRuleSource, cwd, home string) string {
	var sources []string
	for _, rule := range rules {
		path, ok := strings.CutPrefix(rule.Rule, prefix)
		if rule.Section != "fs" || !ok {
			continue
		}
		if path == "~" {
			path = home
		} else if strings.HasPrefix(path, "~/") {
			path = filepath.Join(home, path[2:])
		} else if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		real, err := sandbox.CanonicalPath(path)
		if err == nil && real == deny && !containsExact(sources, rule.Source) {
			sources = append(sources, rule.Source)
		}
	}
	if len(sources) == 0 {
		return "effective policy"
	}
	return strings.Join(sources, ", ")
}
