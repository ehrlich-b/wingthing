package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

func TestDashboardNoTTYHelpUnchanged(t *testing.T) {
	previous := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = previous })
	want, err := os.ReadFile("testdata/root-help.golden")
	if err != nil {
		t.Fatal(err)
	}
	for _, tty := range [][2]bool{{false, false}, {true, false}, {false, true}} {
		var out bytes.Buffer
		if err := executeCLI(context.Background(), nil, remotepkg.IO{
			Out: &out, ErrOut: io.Discard, StdinTTY: tty[0], StdoutTTY: tty[1],
		}); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out.Bytes(), want) {
			t.Fatalf("TTY %v changed help bytes:\n%s", tty, &out)
		}
	}
}

func TestDashboardInteractiveRootRoutesToDashboard(t *testing.T) {
	err := executeCLI(context.Background(), nil, remotepkg.IO{
		In: bytes.NewReader(nil), Out: io.Discard, ErrOut: io.Discard, StdinTTY: true, StdoutTTY: true,
	})
	if err == nil || err.Error() != "dashboard requires interactive stdin and stdout" {
		t.Fatalf("interactive root did not invoke dashboard: %v", err)
	}
}
