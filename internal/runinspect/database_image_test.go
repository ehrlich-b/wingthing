package runinspect

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

type databaseChangeContext struct {
	context.Context
	change func()
	once   sync.Once
	called atomic.Bool
}

func (c *databaseChangeContext) Done() <-chan struct{} {
	c.once.Do(func() {
		c.change()
		c.called.Store(true)
	})
	return c.Context.Done()
}

func TestRunInspectDatabaseQueriesUseVerifiedImage(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, _ string) {
		addWing(t, db, "run", "execution", "", "admitted", "pending", nil)
	})
	_, alternate := fixture(t, func(db *sql.DB, _ string) {
		addWing(t, db, "run", "execution", "", "terminal", "done", str("unmanifested completion"))
	})
	file := filepath.Join(path, "wt.db")
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := os.ReadFile(filepath.Join(alternate, "wt.db"))
	if err != nil || bytes.Equal(original, replacement) {
		t.Fatalf("replacement must differ: %v", err)
	}
	s, err := openSnapshot(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	defer s.root.Close()
	restore := func() {
		if err := os.WriteFile(file, original, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(restore)
	ctx := &databaseChangeContext{Context: context.Background(), change: func() {
		// QueryContext checks Done before opening its first connection, after
		// database() has prepared its input. Change the same inode: retaining
		// a descriptor alone does not freeze SQLite's subsequent reads.
		// Background's nil Done avoids asynchronous row-cancellation callbacks.
		if err := os.WriteFile(file, replacement, 0600); err != nil {
			t.Fatal(err)
		}
	}}
	r := records{tables: map[string]bool{}}
	if err := s.database(ctx, &r); err != nil {
		t.Fatal(err)
	}
	if !ctx.called.Load() {
		t.Fatal("database replacement callback did not run")
	}
	restore()
	if err := s.verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(r.wings) != 1 || !r.wings[0].valid || r.wings[0].record.Result.Status != "pending" {
		t.Fatalf("queries consumed changed supplier bytes: %+v", r.wings)
	}
}
