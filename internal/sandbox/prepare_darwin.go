//go:build darwin

package sandbox

import "strings"

// ValidateLaunchPreparation runs the exact final policy builder without creating
// a sandbox or starting a native process. New rechecks the same requirements.
func ValidateLaunchPreparation(cfg Config) (bool, error) {
	_, err := buildCheckedProfile(cfg)
	return true, err
}

func checkSeatbeltDirectoryWrites(cfg Config, path string) error {
	return checkDirectoryWrites(buildProfile(cfg), []string{path}, false)
}

func checkSeatbeltFileReads(cfg Config, path string) DirectoryWriteCheck {
	rules, err := parseFileRules(buildProfile(cfg), "file-read")
	if err != nil {
		return DirectoryWriteCheck{Reason: "final read policy cannot be verified: " + err.Error()}
	}
	allowed := false
	deciding := writeRule{}
	for _, rule := range rules {
		matches := rule.kind == writeRuleAll || rule.kind == writeRuleSubpath && pathWithin(path, rule.path) || rule.kind == writeRuleLiteral && path == rule.path || rule.kind == writeRulePrefix && strings.HasPrefix(path, rule.path)
		if matches {
			allowed, deciding = rule.allow, rule
		}
	}
	result := DirectoryWriteCheck{Allowed: allowed, Verified: true}
	if !allowed {
		result.Reason, result.DenialKind, result.DenialPath = deciding.line, "deny", deciding.path
	}
	return result
}
