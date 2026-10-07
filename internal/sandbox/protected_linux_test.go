//go:build linux

package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Linux write policy is not a closed list of emitted rules (paths outside HOME
// stay writable and UseRegex mounts overlay HOME), so a nonempty protected set
// must be refused explicitly instead of being reported as enforced.
func TestLinuxRefusesProtectedWriteTargets(t *testing.T) {
	_, err := newPlatform(Config{ProtectedWriteTargets: []string{"/home/u/.wingthing/parent"}})
	requireProtectedError(t, err, "")

	_, err = New(Config{ProtectedWriteTargets: []string{"/home/u/.wingthing/parent"}})
	requireProtectedError(t, err, "")
	var ee *EnforcementError
	if errors.As(err, &ee) {
		t.Fatalf("protected refusal must not be reported as missing isolation: %v", err)
	}
}

func TestLinuxJailVerifiesProtectedWriteTargets(t *testing.T) {
	for _, test := range []struct {
		name, writable string
		refused        bool
	}{
		{"workspace", "/workspace", false},
		{"parent", "/state", true},
		{"child", "/state/broker/journal", true},
		{"exact", "/state/broker", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{Deny: []string{"/"}, ProtectedWriteTargets: []string{"/state/broker"}, Mounts: []Mount{{Source: "/usr", ReadOnly: true}, {Source: test.writable}}}
			if err := verifyLinuxProtectedWriteTargets(cfg); (err != nil) != test.refused {
				t.Fatalf("protected jail: %v", err)
			}
		})
	}
}

func TestLinuxJailRefusesPhysicalProtectedTargetAliases(t *testing.T) {
	root := t.TempDir()
	state, alias := filepath.Join(root, "state"), filepath.Join(root, "alias")
	if err := os.MkdirAll(filepath.Join(state, "broker"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(state, alias); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Deny: []string{"/"}, ProtectedWriteTargets: []string{state}, Mounts: []Mount{{Source: filepath.Join(alias, "broker")}}}
	if err := verifyLinuxProtectedWriteTargets(cfg); err == nil {
		t.Fatal("physical alias reopened protected state")
	}
}
