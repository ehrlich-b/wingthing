package egg

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
)

func TestRecoveryExitDistinguishesShutdownFromDeliberateExit(t *testing.T) {
	for _, tc := range []struct {
		name               string
		code               int
		cancelled, stopped bool
	}{
		{"exit", 0, false, true}, {"shutdown", 0, true, false}, {"crash", -1, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := recordSessionProcessExit(dir, tc.code, tc.cancelled); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(filepath.Join(dir, DeliberateStopFile))
			if (err == nil) != tc.stopped {
				t.Fatalf("stop marker: %v", err)
			}
		})
	}
}

func TestRecoveryKillPersistsDeliberateStop(t *testing.T) {
	dir := t.TempDir()
	s := &Server{dir: dir, session: &Session{}}
	if _, err := s.Kill(context.Background(), &pb.KillRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, DeliberateStopFile)); err != nil {
		t.Fatal(err)
	}
	if !s.session.cancelled {
		t.Fatal("kill not recorded as cancelled")
	}
}
