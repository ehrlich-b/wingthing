package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

func TestContextCLI(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WINGTHING_DIR", dir)
	t.Setenv("WINGTHING_PREVIEW_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "remotes.yaml"), []byte("remotes:\n  studio:\n    ssh_target: fixture-studio\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"set", "local", "home"}, {"set", "studio", "work"}, {"list"}} {
		var out bytes.Buffer
		if err := executeCLI(context.Background(), append([]string{"context"}, args...), remotepkg.IO{Out: &out, ErrOut: &out}); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out.String())
		}
		if args[0] == "list" {
			fields := strings.Fields(out.String())
			if strings.Join(fields, " ") != "MACHINE CONTEXT local home studio work" {
				t.Fatalf("list = %s", out.String())
			}
		}
	}
	for _, args := range [][]string{{"set", "missing", "home"}, {"set", "local", "invalid"}, {"set", "local"}, {"list", "extra"}} {
		var out bytes.Buffer
		if err := executeCLI(context.Background(), append([]string{"context"}, args...), remotepkg.IO{Out: &out, ErrOut: &out}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
