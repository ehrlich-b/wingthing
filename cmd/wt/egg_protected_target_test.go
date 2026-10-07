package main

import (
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"reflect"
	"testing"
)

func TestProtectedWriteTargetArgsRoundTripExactly(t *testing.T) {
	targets := []string{
		"/Users/u/.wingthing/eggs/parent",
		"/Users/u/state,with,commas",
		"/Users/u/space and=equals",
		"/Users/u/--looks-like-a-flag",
		"/Users/u/.wingthing/wt.db",
		"/Users/u/.wingthing/wt.db", // duplicates are carried, not collapsed
	}
	args, err := eggclient.ProtectedWriteTargetArgs(targets)
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
	got, err := cmd.Flags().GetStringArray(eggclient.ProtectedWriteTargetArg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, targets) {
		t.Fatalf("round trip = %#v, want %#v", got, targets)
	}
	if flag := cmd.Flags().Lookup(eggclient.ProtectedWriteTargetArg); flag == nil || !flag.Hidden {
		t.Fatal("protected-write-target must be a hidden internal flag")
	}
}

func TestEggRunWithoutProtectedTargetsParsesEmpty(t *testing.T) {
	cmd := eggRunCmd()
	if err := cmd.ParseFlags([]string{"--session-id", "s1"}); err != nil {
		t.Fatal(err)
	}
	got, err := cmd.Flags().GetStringArray(eggclient.ProtectedWriteTargetArg)
	if err != nil || len(got) != 0 {
		t.Fatalf("default protected targets = %#v, %v; want empty", got, err)
	}
}

func TestEggRunRoostPolicyPathsAreExplicit(t *testing.T) {
	paths := []string{"/work/a/egg.yaml", "/work/spaces and,commas/egg.yaml"}
	for _, roost := range []bool{false, true} {
		cmd := eggRunCmd()
		args := []string{"--session-id", "s1", "--fs", "deny-write:./egg.yaml"}
		if roost {
			for _, path := range paths {
				args = append(args, "--roost-policy-path", path)
			}
		}
		if err := cmd.ParseFlags(args); err != nil {
			t.Fatal(err)
		}
		got, err := cmd.Flags().GetStringArray("roost-policy-path")
		if err != nil || (roost && !reflect.DeepEqual(got, paths)) || (!roost && len(got) != 0) {
			t.Fatalf("roost=%v: policy paths = %v, %v", roost, got, err)
		}
		if !cmd.Flags().Lookup("roost-policy-path").Hidden {
			t.Fatal("runtime roost policy paths must be internal")
		}
	}
}
