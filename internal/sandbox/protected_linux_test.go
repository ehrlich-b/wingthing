//go:build linux

package sandbox

import (
	"errors"
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
