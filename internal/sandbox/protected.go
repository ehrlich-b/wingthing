package sandbox

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// ProtectedWriteTargetError reports that the final sandbox policy cannot keep a
// host-protected write target unwritable, or that the platform cannot enforce
// the protected-target contract at all. It is additive to EnforcementError: New
// returns it unchanged so callers can tell a policy conflict apart from missing
// OS isolation. It is never produced for an empty protected set.
type ProtectedWriteTargetError struct {
	Target string // protected target as resolved for the emitted policy ("" when not target-specific)
	Rule   string // exact emitted policy rule that conflicts ("" when not rule-specific)
	Reason string
}

func (e *ProtectedWriteTargetError) Error() string {
	msg := "protected write target"
	if e.Target != "" {
		msg += " " + strconv.Quote(e.Target)
	}
	msg += ": " + e.Reason
	if e.Rule != "" {
		msg += ": " + e.Rule
	}
	return msg
}

// ValidateProtectedWriteTargets checks host-supplied targets syntactically so
// callers can refuse bad input before spawning anything. It is not enforcement:
// the platform policy builder re-checks every target against the exact rules it
// emits.
func ValidateProtectedWriteTargets(targets []string) error {
	for _, target := range targets {
		if target == "" || strings.IndexByte(target, 0) >= 0 || !filepath.IsAbs(target) {
			return &ProtectedWriteTargetError{Target: target, Reason: "must be a non-empty absolute path without NUL bytes"}
		}
	}
	return nil
}

// refuseProtectedWriteTargets is used by backends whose final policy is not a
// closed list of write rules this package can check. They must refuse rather
// than silently accept a protected set they cannot enforce.
func refuseProtectedWriteTargets(cfg Config, backend string) error {
	if len(cfg.ProtectedWriteTargets) == 0 {
		return nil
	}
	return &ProtectedWriteTargetError{
		Reason: fmt.Sprintf("%s sandbox cannot enforce protected write targets; refusing nonempty protected set (%d)", backend, len(cfg.ProtectedWriteTargets)),
	}
}

type writeRuleKind int

const (
	writeRuleAll     writeRuleKind = iota // (allow default) or an unfiltered file-write op
	writeRuleSubpath                      // (subpath "P"): P and descendants
	writeRuleLiteral                      // (literal "P"): exactly P
	writeRulePrefix                       // (regex #"^P"): any path string starting with P
)

// writeRule is one file-write decision parsed back from an emitted SBPL line.
type writeRule struct {
	allow bool
	kind  writeRuleKind
	path  string
	line  string
}

// parseWriteRules reads the final SBPL profile and returns every rule that can
// decide a file write, in order. It only understands the shapes buildProfile
// emits; any other rule that could affect writes is an error so a future
// profile change cannot silently bypass the protected-target check.
func parseWriteRules(profile string) ([]writeRule, error) {
	var rules []writeRule
	for _, raw := range strings.Split(profile, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line == "(version 1)" {
			continue
		}
		if !strings.HasPrefix(line, "(") || !strings.HasSuffix(line, ")") {
			return nil, fmt.Errorf("unrecognized policy line %q", line)
		}
		inner := line[1 : len(line)-1]
		action, rest, _ := strings.Cut(inner, " ")
		if action != "allow" && action != "deny" {
			return nil, fmt.Errorf("unrecognized policy action in %q", line)
		}
		opsPart, filter := rest, ""
		if i := strings.IndexByte(rest, '('); i >= 0 {
			opsPart, filter = rest[:i], strings.TrimSpace(rest[i:])
		}
		ops := strings.Fields(opsPart)
		if len(ops) == 0 {
			return nil, fmt.Errorf("policy rule without operation %q", line)
		}
		affectsWrite := false
		for _, op := range ops {
			if op == "default" || op == "file" || op == "file*" || strings.HasPrefix(op, "file-write") {
				affectsWrite = true
			}
		}
		if !affectsWrite {
			continue
		}
		rule := writeRule{allow: action == "allow", line: line}
		if filter != "" {
			kind, path, err := parseWriteFilter(filter)
			if err != nil {
				return nil, fmt.Errorf("%v in %q", err, line)
			}
			rule.kind, rule.path = kind, path
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func parseWriteFilter(filter string) (writeRuleKind, string, error) {
	for _, f := range []struct {
		prefix string
		kind   writeRuleKind
	}{{"(subpath ", writeRuleSubpath}, {"(literal ", writeRuleLiteral}} {
		if strings.HasPrefix(filter, f.prefix) && strings.HasSuffix(filter, ")") {
			path, err := plainSBPLString(filter[len(f.prefix) : len(filter)-1])
			return f.kind, path, err
		}
	}
	const regexPrefix = `(regex #"^`
	if strings.HasPrefix(filter, regexPrefix) && strings.HasSuffix(filter, `")`) {
		prefix, err := literalSBPLRegexPrefix(filter[len(regexPrefix) : len(filter)-2])
		return writeRulePrefix, prefix, err
	}
	return 0, "", fmt.Errorf("unrecognized write filter %q", filter)
}

// plainSBPLString accepts only a quoted absolute path with no escapes. Go's %q
// and SBPL disagree on some escapes, so an escaped path is not trusted here.
func plainSBPLString(quoted string) (string, error) {
	if len(quoted) < 2 || quoted[0] != '"' || quoted[len(quoted)-1] != '"' {
		return "", fmt.Errorf("unrecognized quoted path %s", quoted)
	}
	path := quoted[1 : len(quoted)-1]
	if strings.ContainsAny(path, "\"\\") {
		return "", fmt.Errorf("escaped path %s", quoted)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("relative path %s", quoted)
	}
	return path, nil
}

// literalSBPLRegexPrefix inverts sbplRegexEscape. A regex body containing an
// unescaped metacharacter is not a literal prefix and cannot be reasoned about.
func literalSBPLRegexPrefix(body string) (string, error) {
	var sb strings.Builder
	escaped := false
	for _, c := range body {
		if escaped {
			sb.WriteRune(c)
			escaped = false
			continue
		}
		switch c {
		case '\\':
			escaped = true
		case '.', '*', '+', '?', '(', ')', '[', ']', '{', '}', '|', '^', '$', '"':
			return "", fmt.Errorf("non-literal regex %q", body)
		default:
			sb.WriteRune(c)
		}
	}
	if escaped {
		return "", fmt.Errorf("dangling regex escape %q", body)
	}
	return sb.String(), nil
}

// pathWithin reports whether child is parent or one of its descendants.
func pathWithin(child, parent string) bool {
	if parent == "/" {
		return strings.HasPrefix(child, "/")
	}
	return child == parent || strings.HasPrefix(child, parent+"/")
}

func descendantPrefix(target string) string {
	if strings.HasSuffix(target, "/") {
		return target
	}
	return target + "/"
}

// overlapsTarget reports whether an explicit allow rule could grant a write to
// the target, any descendant, or an ancestor (renaming an ancestor relocates
// the target). foldCase compares case-insensitively, which only widens overlap.
func (r writeRule) overlapsTarget(target string, foldCase bool) bool {
	path := r.path
	if foldCase {
		path, target = strings.ToLower(path), strings.ToLower(target)
	}
	switch r.kind {
	case writeRuleSubpath, writeRuleLiteral:
		return pathWithin(target, path) || pathWithin(path, target)
	case writeRulePrefix:
		return strings.HasPrefix(target, path) || strings.HasPrefix(path, descendantPrefix(target))
	default:
		return true
	}
}

// checkProtectedWriteTargets validates the exact emitted profile against
// already-canonical protected targets:
//
//  1. No explicit allow-write rule may overlap a target. This covers writable
//     mounts (subpath and regex prefix), keychain, and temp-dir allows alike;
//     a later deny is not accepted as a repair for an overlapping allow.
//  2. After the last allow-everything baseline, deny rules must cover both the
//     exact target (a subpath on the target itself does not cover creating a
//     missing target) and every descendant.
func checkProtectedWriteTargets(profile string, targets []string, foldCase bool) error {
	if len(targets) == 0 {
		return nil
	}
	rules, err := parseWriteRules(profile)
	if err != nil {
		return &ProtectedWriteTargetError{Reason: "final sandbox policy cannot be verified: " + err.Error()}
	}
	lastAllowAll := -1
	for i, r := range rules {
		if r.allow && r.kind == writeRuleAll {
			lastAllowAll = i
		}
	}
	for _, target := range targets {
		for _, r := range rules {
			if r.allow && r.kind != writeRuleAll && r.overlapsTarget(target, foldCase) {
				return &ProtectedWriteTargetError{Target: target, Rule: r.line, Reason: "writable sandbox rule overlaps protected target"}
			}
		}
		exact, descendants := lastAllowAll < 0, lastAllowAll < 0
		for _, r := range rules[lastAllowAll+1:] {
			if r.allow {
				continue // only non-overlapping explicit allows remain
			}
			switch r.kind {
			case writeRuleAll:
				exact, descendants = true, true
			case writeRuleSubpath:
				if pathWithin(target, r.path) {
					descendants = true
					exact = exact || target != r.path
				}
			case writeRuleLiteral:
				exact = exact || target == r.path
			case writeRulePrefix:
				exact = exact || strings.HasPrefix(target, r.path)
				descendants = descendants || strings.HasPrefix(descendantPrefix(target), r.path)
			}
		}
		if !exact || !descendants {
			return &ProtectedWriteTargetError{Target: target, Reason: "final sandbox policy does not deny writes to protected target"}
		}
	}
	return nil
}
