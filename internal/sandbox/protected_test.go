package sandbox

import (
	"errors"
	"strings"
	"testing"
)

const protectedTestTarget = "/Users/u/.wingthing/ctl"

func requireProtectedError(t *testing.T, err error, wantRule string) *ProtectedWriteTargetError {
	t.Helper()
	var pe *ProtectedWriteTargetError
	if !errors.As(err, &pe) {
		t.Fatalf("expected ProtectedWriteTargetError, got %T: %v", err, err)
	}
	if wantRule != "" && pe.Rule != wantRule {
		t.Fatalf("conflicting rule = %q, want %q (%v)", pe.Rule, wantRule, err)
	}
	return pe
}

func sealedProfile(rules ...string) string {
	lines := append([]string{"(version 1)", "(allow default)"}, rules...)
	lines = append(lines,
		`(deny file-write* (literal "`+protectedTestTarget+`"))`,
		`(deny file-write* (subpath "`+protectedTestTarget+`"))`,
	)
	return strings.Join(lines, "\n") + "\n"
}

func TestProtectedWriteTargetsEmptySetIsNoContract(t *testing.T) {
	if err := checkProtectedWriteTargets("(not a policy", nil, true); err != nil {
		t.Fatalf("empty protected set must not inspect policy: %v", err)
	}
	if err := ValidateProtectedWriteTargets(nil); err != nil {
		t.Fatal(err)
	}
}

func TestValidateProtectedWriteTargetsRejectsNonAbsolute(t *testing.T) {
	for _, target := range []string{"", "relative/state", "./state", "/a\x00b"} {
		requireProtectedError(t, ValidateProtectedWriteTargets([]string{target}), "")
	}
	if err := ValidateProtectedWriteTargets([]string{"/abs/state", "/"}); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedWriteTargetsRejectOverlappingAllowRules(t *testing.T) {
	for _, rule := range []string{
		`(allow file-write* (subpath "/Users/u/.wingthing"))`,           // ancestor
		`(allow file-write* (subpath "/Users/u/.wingthing/ctl"))`,       // exact
		`(allow file-write* (subpath "/Users/u/.wingthing/ctl/sock"))`,  // descendant
		`(allow file-write* (literal "/Users/u/.wingthing/ctl/state"))`, // literal inside
		`(allow file-write* (literal "/Users/u/.wingthing"))`,           // literal ancestor (rename)
		`(allow file-write* (subpath "/"))`,
		`(allow file-write* (regex #"^/Users/u/\.wing"))`,               // prefix of an ancestor name
		`(allow file-write* (regex #"^/Users/u/\.wingthing/ctl"))`,      // exact prefix
		`(allow file-write* (regex #"^/Users/u/\.wingthing/ctl/x"))`,    // prefix inside target
		`(allow file-write-create (subpath "/Users/u/.wingthing/ctl"))`, // narrower write op
		`(allow file* (subpath "/Users/u/.wingthing/ctl"))`,
		`(allow file-read* file-write* (subpath "/Users/u/.wingthing"))`,
	} {
		t.Run(rule, func(t *testing.T) {
			err := checkProtectedWriteTargets(sealedProfile(rule), []string{protectedTestTarget}, false)
			pe := requireProtectedError(t, err, rule)
			if pe.Target != protectedTestTarget || !strings.Contains(err.Error(), "overlaps") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestProtectedWriteTargetsAllowNonOverlappingRules(t *testing.T) {
	profile := sealedProfile(
		`(deny file-write* (subpath "/Users/u"))`,
		`(allow file-write* (subpath "/Users/u/project"))`,
		`(allow file-write* (regex #"^/Users/u/\.claude"))`,
		`(allow file-write* (subpath "/Users/u/.wingthing/ctl2"))`,
		`(allow file-write* (regex #"^/Users/u/\.wingthing/ctlx"))`,
		`(allow file-write* (subpath "/Users/u/.wingthing/eggs/child/browser-requests"))`,
		`(allow file-write* (subpath "/private/tmp"))`,
		`(allow file-read* (literal "/Users/u/.wingthing/ctl"))`,
		`(allow network-outbound (literal "/Users/u/.wingthing/ctl/sock"))`,
		`(deny network*)`,
		`(deny file-read* file-write* (literal "/Users/u/.ssh"))`,
	)
	if err := checkProtectedWriteTargets(profile, []string{protectedTestTarget}, true); err != nil {
		t.Fatalf("non-overlapping policy refused: %v", err)
	}
}

func TestProtectedWriteTargetsCaseFoldWidensOverlap(t *testing.T) {
	rule := `(allow file-write* (subpath "/users/U/.WingThing"))`
	if err := checkProtectedWriteTargets(sealedProfile(rule), []string{protectedTestTarget}, false); err != nil {
		t.Fatalf("case-sensitive comparison should not overlap: %v", err)
	}
	requireProtectedError(t, checkProtectedWriteTargets(sealedProfile(rule), []string{protectedTestTarget}, true), rule)
}

func TestProtectedWriteTargetsRequireFinalDenyCoverage(t *testing.T) {
	literal := `(deny file-write* (literal "` + protectedTestTarget + `"))`
	subpath := `(deny file-write* (subpath "` + protectedTestTarget + `"))`
	for name, tc := range map[string]struct {
		profile string
		ok      bool
	}{
		"allow default only":            {"(allow default)\n", false},
		"literal only misses children":  {"(allow default)\n" + literal + "\n", false},
		"subpath only misses creation":  {"(allow default)\n" + subpath + "\n", false},
		"literal and subpath":           {"(allow default)\n" + literal + "\n" + subpath + "\n", true},
		"ancestor subpath deny":         {"(allow default)\n(deny file-write* (subpath \"/Users/u\"))\n", true},
		"ancestor regex deny":           {"(allow default)\n(deny file-write* (regex #\"^/Users/u/\\.wing\"))\n", true},
		"unfiltered write deny":         {"(allow default)\n(deny file-write*)\n", true},
		"baseline reopened after deny":  {literal + "\n" + subpath + "\n(allow default)\n", false},
		"no allow baseline denies all":  {"(version 1)\n(deny network*)\n", true},
		"read deny does not seal write": {"(allow default)\n(deny file-read* (subpath \"/Users/u\"))\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkProtectedWriteTargets(tc.profile, []string{protectedTestTarget}, true)
			if tc.ok && err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
			if !tc.ok {
				pe := requireProtectedError(t, err, "")
				if !strings.Contains(pe.Reason, "does not deny") {
					t.Fatalf("unexpected reason: %v", err)
				}
			}
		})
	}
}

func TestProtectedWriteTargetsFailClosedOnUnverifiableRules(t *testing.T) {
	for _, rule := range []string{
		`(allow file-write* (regex #"^/Users/u/.*"))`,              // non-literal regex
		`(allow file-write* (regex #"^/Users/u/\.claude\"))`,       // escaped quote
		`(allow file-write* (subpath "/a") (subpath "/b"))`,        // multiple filters
		`(allow file-write* (subpath "Library/Keychains"))`,        // relative path
		`(allow file-write* (subpath "/Users/u/we\"ird"))`,         // escaped path
		`(allow file-write* (vnode-type REGULAR-FILE))`,            // unknown filter
		`(import "system.sb")`,                                     // unknown action
		`allow file-write*`,                                        // not an s-expression
		`(allow file-write* (require-not (subpath "/Users/u/x")))`, // unknown combinator
	} {
		t.Run(rule, func(t *testing.T) {
			err := checkProtectedWriteTargets(sealedProfile(rule), []string{protectedTestTarget}, true)
			pe := requireProtectedError(t, err, "")
			if !strings.Contains(pe.Reason, "cannot be verified") {
				t.Fatalf("unexpected reason: %v", err)
			}
		})
	}
}

func TestProtectedWriteTargetErrorMessage(t *testing.T) {
	err := &ProtectedWriteTargetError{Target: "/s", Rule: `(allow file-write* (subpath "/"))`, Reason: "writable sandbox rule overlaps protected target"}
	want := `protected write target "/s": writable sandbox rule overlaps protected target: (allow file-write* (subpath "/"))`
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestFallbackRefusesProtectedWriteTargets(t *testing.T) {
	if _, err := newFallback(Config{ProtectedWriteTargets: []string{"/state"}}); err == nil {
		t.Fatal("fallback accepted a protected set it cannot enforce")
	} else {
		requireProtectedError(t, err, "")
	}
	sb, err := newFallback(Config{})
	if err != nil {
		t.Fatalf("empty protected set must keep fallback behavior: %v", err)
	}
	if err := sb.Destroy(); err != nil {
		t.Fatal(err)
	}
}
