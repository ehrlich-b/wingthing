package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestCursorModelSelectionIsNotIgnored(t *testing.T) {
	const model = "selected-fixture-model"
	var argv []string
	stream, err := NewCursor(0).Run(context.Background(), "fixture", RunOpts{
		Model: model,
		CmdFactory: func(ctx context.Context, _ string, args []string) (*exec.Cmd, error) {
			argv = append([]string(nil), args...)
			return exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"fixture output"}]}}'`), nil
		},
	})
	// Explicit rejection is acceptable when model selection is unsupported.
	if err != nil {
		if stream != nil || !strings.Contains(err.Error(), "does not support model selection") {
			t.Fatalf("expected explicit model rejection, got stream=%v, err=%v", stream, err)
		}
		return
	}
	for {
		if _, ok := stream.Next(); !ok {
			break
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(argv, model) {
		t.Fatalf("selected model %q silently ignored: argv = %q", model, argv)
	}
}

func TestCursorRejectsModelBeforeCommandCreation(t *testing.T) {
	for _, model := range []string{"sonnet-4", "selected-fixture-model", "--help", " "} {
		t.Run(model, func(t *testing.T) {
			cursor := NewCursor(0)
			// Direct execution must reject the model before attempting this command.
			cursor.command = filepath.Join(testsocket.Dir(t), "missing-agent")
			for _, useFactory := range []bool{false, true} {
				called := false
				opts := RunOpts{Model: model}
				if useFactory {
					opts.CmdFactory = func(ctx context.Context, _ string, _ []string) (*exec.Cmd, error) {
						called = true
						return exec.CommandContext(ctx, cursor.command), nil
					}
				}
				stream, err := cursor.Run(context.Background(), "fixture", opts)
				if err == nil || err.Error() != "cursor headless adapter does not support model selection" {
					t.Fatalf("factory=%v: expected model rejection, got %v", useFactory, err)
				}
				if stream != nil || called {
					t.Fatalf("factory=%v: rejected model created command or stream", useFactory)
				}
			}
		})
	}
}

func TestCursorDefaultModelExecutesProvider(t *testing.T) {
	dir := testsocket.Dir(t)
	command := filepath.Join(dir, "agent")
	const script = `#!/bin/sh
printf '%s\n' "$@" > argv
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"default model output"}]}}'
`
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cursor := NewCursor(0)
	cursor.command = command
	stream, err := cursor.Run(context.Background(), "default model fixture", RunOpts{WorkDir: dir})
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
	if got := stream.Text(); got != "default model output" {
		t.Fatalf("output = %q", got)
	}
	argv, err := os.ReadFile(filepath.Join(dir, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-p", "default model fixture", "--output-format", "stream-json"}
	if got := strings.Split(strings.TrimSuffix(string(argv), "\n"), "\n"); !reflect.DeepEqual(got, want) {
		t.Fatalf("default argv = %q, want %q", got, want)
	}
}
