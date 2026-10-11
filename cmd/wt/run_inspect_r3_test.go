package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/runinspect"
)

func TestRunInspectR3CaseAliasedOutput(t *testing.T) {
	binary, workspace, snapshot := inspectBinaryFixture(t)
	alias := filepath.Join(workspace, "SNAPSHOT")
	original, err := os.Stat(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	aliased, err := os.Stat(alias)
	if os.IsNotExist(err) || (err == nil && !os.SameFile(original, aliased)) {
		t.Skip("filesystem does not expose case-insensitive directory aliases")
	}
	if err != nil {
		t.Fatal(err)
	}
	// Exercise equality and both ancestor directions through case aliases.
	child := filepath.Join(snapshot, "reports")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	original, err = os.Stat(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	before := inspectTree(t, snapshot)
	for _, tc := range []struct{ input, output string }{
		{snapshot, alias},
		{snapshot, filepath.Join(alias, "reports")},
		{filepath.Join(alias, "reports"), snapshot},
	} {
		wire, err := runInspectBinary(t, binary, workspace, "run-inspect", "--snapshot", tc.input, "--output-dir", tc.output, "--json")
		if err == nil || !strings.Contains(string(wire), "must not overlap") {
			t.Fatalf("case-aliased overlap accepted: %v %s", err, wire)
		}
		if !reflect.DeepEqual(before, inspectTree(t, snapshot)) {
			t.Fatal("case-aliased output changed snapshot inventory or bytes")
		}
		after, err := os.Stat(snapshot)
		if err != nil || !after.ModTime().Equal(original.ModTime()) {
			t.Fatalf("snapshot directory mtime changed: %v", err)
		}
	}
}

type inspectReplacedDirectoryContext struct {
	context.Context
	mutate func()
	called bool
}

func (c *inspectReplacedDirectoryContext) Err() error {
	if !c.called {
		c.called = true
		c.mutate()
	}
	return c.Context.Err()
}

func TestRunInspectR3ReplacedOutputDirectory(t *testing.T) {
	_, workspace, snapshot := inspectBinaryFixture(t)
	destination := filepath.Join(workspace, "out")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	before := inspectTree(t, snapshot)
	info, err := os.Stat(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspace)
	ctx := &inspectReplacedDirectoryContext{Context: t.Context(), mutate: func() {
		if err := os.Rename(destination, destination+"-original"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(snapshot, destination); err != nil {
			t.Fatal(err)
		}
	}}
	cmd := runInspectCmd()
	cmd.SetArgs([]string{"--snapshot", snapshot, "--output-dir", destination, "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err = cmd.ExecuteContext(ctx)
	if !ctx.called {
		t.Fatal("replacement callback did not run")
	}
	if err == nil || !reflect.DeepEqual(before, inspectTree(t, snapshot)) {
		t.Fatalf("replacement redirected report into snapshot: %v", err)
	}
	after, statErr := os.Stat(snapshot)
	if statErr != nil || !after.ModTime().Equal(info.ModTime()) {
		t.Fatalf("output replacement changed snapshot directory mtime: %v", statErr)
	}
	if _, err := runinspect.Inspect(t.Context(), snapshot, runinspect.Options{WorkspaceRoot: workspace}); err != nil {
		t.Fatalf("output replacement invalidated snapshot: %v", err)
	}
}
