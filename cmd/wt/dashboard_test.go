package main

import (
	"bytes"
	"context"
	_ "embed"
	"io"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

// Embedded so compiled test binaries run outside the source tree.
//
//go:embed testdata/root-help.golden
var rootHelpGolden []byte

func TestDashboardNoTTYHelpUnchanged(t *testing.T) {
	previous := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = previous })
	want := rootHelpGolden
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
