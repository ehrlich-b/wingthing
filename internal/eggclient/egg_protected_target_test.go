package eggclient

import (
	"errors"

	"testing"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func TestProtectedWriteTargetArgsEmptySetAddsNothing(t *testing.T) {
	for _, targets := range [][]string{nil, {}} {
		args, err := ProtectedWriteTargetArgs(targets)
		if err != nil || len(args) != 0 {
			t.Fatalf("protectedWriteTargetArgs(%#v) = %#v, %v; want no argv", targets, args, err)
		}
	}
}

func TestProtectedWriteTargetArgsRejectInvalidTargets(t *testing.T) {
	for _, target := range []string{"", "relative/state", "/a\x00b"} {
		_, err := ProtectedWriteTargetArgs([]string{"/ok", target})
		var pe *sandbox.ProtectedWriteTargetError
		if !errors.As(err, &pe) {
			t.Fatalf("target %q: expected ProtectedWriteTargetError, got %v", target, err)
		}
	}
}
