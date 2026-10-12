package sandbox

import (
	"fmt"
	"path/filepath"
	"strings"
)

// checkRequiredWriteDirs uses the existing parser for the actual emitted policy.
// Check the baseline and every rule boundary within each spool, so a descendant
// ban cannot hide behind a writable state-directory parent. A literal pin on the
// spool entry itself does not restrict writes to its contents.
func checkRequiredWriteDirs(profile string, dirs []string) error {
	return checkDirectoryWrites(profile, dirs, true)
}

func checkDirectoryWrites(profile string, dirs []string, subtree bool) error {
	if len(dirs) == 0 {
		return nil
	}
	rules, err := parseWriteRules(profile)
	if err != nil {
		return fmt.Errorf("required callback policy cannot be verified: %w", err)
	}
	for _, dir := range dirs {
		dir, err = CanonicalPath(dir)
		if err != nil {
			return err
		}
		candidates := []string{filepath.Join(dir, "wt-required-output")}
		for _, rule := range rules {
			if subtree && rule.path != dir && pathWithin(rule.path, dir) {
				candidates = append(candidates, rule.path, filepath.Join(rule.path, "wt-required-output"))
			}
		}
		for _, path := range candidates {
			allowed := false
			var deciding string
			for _, rule := range rules {
				matches := false
				switch rule.kind {
				case writeRuleAll:
					matches = true
				case writeRuleSubpath:
					matches = pathWithin(path, rule.path)
				case writeRuleLiteral:
					matches = path == rule.path
				case writeRulePrefix:
					matches = strings.HasPrefix(path, rule.path)
				}
				if matches {
					allowed, deciding = rule.allow, rule.line
				}
			}
			if !allowed {
				return fmt.Errorf("required callback spool %s is denied by final policy: %s", dir, deciding)
			}
		}
	}
	return nil
}
