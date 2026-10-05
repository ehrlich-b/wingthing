package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func TestProtectedWriteTargetArgsEmptySetAddsNothing(t *testing.T) {
	for _, targets := range [][]string{nil, {}} {
		args, err := protectedWriteTargetArgs(targets)
		if err != nil || len(args) != 0 {
			t.Fatalf("protectedWriteTargetArgs(%#v) = %#v, %v; want no argv", targets, args, err)
		}
	}
}

func TestProtectedWriteTargetArgsRoundTripExactly(t *testing.T) {
	targets := []string{
		"/Users/u/.wingthing/eggs/parent",
		"/Users/u/state,with,commas",
		"/Users/u/space and=equals",
		"/Users/u/--looks-like-a-flag",
		"/Users/u/.wingthing/wt.db",
		"/Users/u/.wingthing/wt.db", // duplicates are carried, not collapsed
	}
	args, err := protectedWriteTargetArgs(targets)
	if err != nil {
		t.Fatal(err)
	}
	for i, arg := range args {
		if want := "--protected-write-target=" + targets[i]; arg != want {
			t.Fatalf("argv[%d] = %q, want %q", i, arg, want)
		}
	}

	cmd := eggRunCmd()
	if err := cmd.ParseFlags(append([]string{"--session-id", "s1", "--agent", "claude"}, args...)); err != nil {
		t.Fatalf("egg run rejected protected-target argv: %v", err)
	}
	got, err := cmd.Flags().GetStringArray(protectedWriteTargetArg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, targets) {
		t.Fatalf("round trip = %#v, want %#v", got, targets)
	}
	if flag := cmd.Flags().Lookup(protectedWriteTargetArg); flag == nil || !flag.Hidden {
		t.Fatal("protected-write-target must be a hidden internal flag")
	}
}

func TestEggRunWithoutProtectedTargetsParsesEmpty(t *testing.T) {
	cmd := eggRunCmd()
	if err := cmd.ParseFlags([]string{"--session-id", "s1"}); err != nil {
		t.Fatal(err)
	}
	got, err := cmd.Flags().GetStringArray(protectedWriteTargetArg)
	if err != nil || len(got) != 0 {
		t.Fatalf("default protected targets = %#v, %v; want empty", got, err)
	}
}

func TestProtectedWriteTargetArgsRejectInvalidTargets(t *testing.T) {
	for _, target := range []string{"", "relative/state", "/a\x00b"} {
		_, err := protectedWriteTargetArgs([]string{"/ok", target})
		var pe *sandbox.ProtectedWriteTargetError
		if !errors.As(err, &pe) {
			t.Fatalf("target %q: expected ProtectedWriteTargetError, got %v", target, err)
		}
	}
}
