package taskrun

import (
	"bytes"
	"context"
	"errors"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
	"io"
	"strings"
	"testing"
	"time"
)

type warningFailWriter struct{}

func (warningFailWriter) Write([]byte) (int, error) { return 0, errors.New("closed destination") }

func TestUnsandboxedWarningSurvivesLogFiltering(t *testing.T) {
	for _, completed := range []bool{false, true} {
		var out bytes.Buffer
		if err := writeUnsandboxedWarning(&out, completed); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "WARNING:") || !strings.Contains(out.String(), "SANDBOX") && !strings.Contains(out.String(), "sandbox enforcement") {
			t.Fatal(out.String())
		}
		if err := writeUnsandboxedWarning(warningFailWriter{}, completed); err == nil {
			t.Fatal("lost warning ignored")
		}
		if err := writeUnsandboxedWarning(io.Discard, completed); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunTaskRefusesImplicitPrivilegedIsolation(t *testing.T) {
	t.Setenv("PATH", testsocket.Dir(t))
	cfg := &config.Config{Dir: testsocket.Dir(t), DefaultAgent: "claude", WingID: "sample"}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.UpsertAgent(&store.Agent{Name: "claude", DefaultIsolation: "privileged"}); err != nil {
		t.Fatal(err)
	}
	task := &store.Task{ID: "implicit-privileged", Type: "prompt", What: "Run tests", RunAt: time.Now(), Agent: "claude", CWD: testsocket.Dir(t)}
	if err := db.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	err = RunTaskTo(context.Background(), cfg, db, task, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unsandboxed execution requires wt run --unsandboxed") {
		t.Fatalf("implicit privilege not refused: %v", err)
	}
	saved, err := db.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != "failed" {
		t.Fatalf("status: %s", saved.Status)
	}
}
