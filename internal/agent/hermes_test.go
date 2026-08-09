package agent

import (
	"context"
	"os/exec"
	"reflect"
	"testing"
)

func TestHermesRunUsesPureOneShotContract(t *testing.T) {
	t.Setenv("WT_HERMES_TOOLSETS", "")
	hermes := NewHermes(0)
	var gotName string
	var gotArgs []string
	stream, err := hermes.Run(context.Background(), "test prompt", RunOpts{
		CmdFactory: func(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
			gotName = name
			gotArgs = append([]string(nil), args...)
			return exec.CommandContext(ctx, "sh", "-c", "printf 'HERMES_OK\\n'"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, ok := stream.Next(); !ok {
			break
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if gotName != "hermes" || !reflect.DeepEqual(gotArgs, []string{"-z", "test prompt"}) {
		t.Fatalf("invocation = %q %q", gotName, gotArgs)
	}
	if stream.Text() != "HERMES_OK\n" {
		t.Fatalf("output = %q", stream.Text())
	}
}

func TestHermesRunSupportsCanaryToolsetRestriction(t *testing.T) {
	t.Setenv("WT_HERMES_TOOLSETS", "terminal")
	hermes := NewHermes(0)
	var gotArgs []string
	stream, err := hermes.Run(context.Background(), "test prompt", RunOpts{
		CmdFactory: func(ctx context.Context, _ string, args []string) (*exec.Cmd, error) {
			gotArgs = append([]string(nil), args...)
			return exec.CommandContext(ctx, "sh", "-c", "printf ok"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, ok := stream.Next(); !ok {
			break
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"-t", "terminal", "-z", "test prompt"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args = %q, want %q", gotArgs, want)
	}
}
