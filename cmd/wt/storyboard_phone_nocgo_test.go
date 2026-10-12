//go:build !cgo

package main

import "testing"

// Non-CGO builds explicitly reject pairing; retain that observable failure.
// The executable Go phone fixture requires the protocol's native PAKE build.
func storyboardPhoneCommand(t *testing.T, f *northstarFixture, binary string, args ...string) (string, error) {
	t.Helper()
	return storyboardCommand(t, f, binary, args...)
}
