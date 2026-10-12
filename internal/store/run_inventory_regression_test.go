package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunInventoryRegressionPartialLegacyTimeMustNotDisappear(t *testing.T) {
	for _, missing := range []string{"created_at", "finished_at"} {
		t.Run(missing, func(t *testing.T) {
			parent := t.TempDir()
			real := filepath.Join(parent, "real")
			alias := filepath.Join(parent, "alias")
			if e := os.Mkdir(real, 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.Symlink(real, alias); e != nil {
				t.Fatal(e)
			}
			s, e := Open(filepath.Join(alias, "db"))
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			now := time.Now().UTC()
			if e = s.CreateTask(&Task{ID: "legacy", Type: "agent_run", Agent: "claude", Principal: "reader", CWD: "/work", Status: "done", RunAt: now}); e != nil {
				t.Fatal(e)
			}
			// Nullable creation time is a valid schema state; invalid finish text can
			// occur in a partial legacy record. Neither proves an expired run.
			if missing == "created_at" {
				_, e = s.DB().Exec("UPDATE tasks SET created_at=NULL WHERE id='legacy'")
			} else {
				_, e = s.DB().Exec("UPDATE tasks SET finished_at='' WHERE id='legacy'")
			}
			if e != nil {
				t.Fatal(e)
			}
			task, e := s.GetTask("legacy")
			if e != nil && missing != "created_at" {
				t.Fatal(e)
			}
			if missing == "finished_at" && task == nil {
				t.Fatal("known ID missing")
			}
			page, e := s.ListRunInventory(t.Context(), RunInventoryQuery{Scope: RunInventoryScope{Principal: "reader"}, Limit: 100, TerminalSince: now.Add(-time.Hour)})
			if e != nil && !strings.Contains(e.Error(), "discovery is incomplete") {
				t.Fatalf("expected an explicit incomplete-state error: %v", e)
			}
			if e == nil && (len(page.Runs) != 1 || page.Runs[0].ID != "legacy") {
				t.Fatalf("partial legacy %s silently dropped; want run or explicit incomplete-state error", missing)
			}
		})
	}
}

func TestRunInventoryDeniedMalformedMetadataDoesNotBlockDiscovery(t *testing.T) {
	for _, damage := range []string{"invalid-json", "legacy-time", "unknown-principal"} {
		t.Run(damage, func(t *testing.T) {
			s := inventoryTestStore(t)
			now := time.Now().UTC()
			inventoryFixture(t, s, "mine", "reader", "user", "/allowed", "done", "", now)
			switch damage {
			case "invalid-json":
				inventoryFixture(t, s, "denied", "reader", "user", "/forbidden", "done", "", now)
				if _, err := s.DB().Exec("UPDATE agent_runs SET record='invalid JSON' WHERE id='denied'"); err != nil {
					t.Fatal(err)
				}
			case "legacy-time":
				if err := s.CreateTask(&Task{ID: "denied", Type: "agent_run", Agent: "claude", Principal: "reader", CWD: "/forbidden", Status: "done", RunAt: now}); err != nil {
					t.Fatal(err)
				}
				if _, err := s.DB().Exec("UPDATE tasks SET created_at=NULL WHERE id='denied'"); err != nil {
					t.Fatal(err)
				}
			case "unknown-principal":
				inventoryFixture(t, s, "denied", "reader", "other-user", "/allowed", "done", "", now)
				if _, err := s.DB().Exec("UPDATE agent_runs SET record=json_remove(record,'$.launch.Authority.Principal') WHERE id='denied'"); err != nil {
					t.Fatal(err)
				}
			}
			q := RunInventoryQuery{Scope: RunInventoryScope{Principal: "reader", UserID: "user", EnforcePaths: true, AllowedPaths: []string{"/allowed"}}, Limit: 1, TerminalSince: now.Add(-time.Hour)}
			page, err := s.ListRunInventory(t.Context(), q)
			if err != nil || len(page.Runs) != 1 || page.Runs[0].ID != "mine" || page.More {
				t.Fatalf("denied metadata affected authorized discovery: %+v %v", page, err)
			}
		})
	}
}

func TestRunInventoryModernProjectionDamageFailsClosed(t *testing.T) {
	for _, damage := range []string{"principal", "type", "created_at", "finished_at"} {
		t.Run(damage, func(t *testing.T) {
			s := inventoryTestStore(t)
			now := time.Now().UTC()
			inventoryFixture(t, s, "durable", "reader", "user", "/allowed", "done", "key", now)
			updates := map[string]string{
				"principal":   "UPDATE tasks SET principal='other' WHERE id='durable'",
				"type":        "UPDATE tasks SET type='prompt' WHERE id='durable'",
				"created_at":  "UPDATE tasks SET created_at=NULL WHERE id='durable'",
				"finished_at": "UPDATE tasks SET finished_at='' WHERE id='durable'",
			}
			if _, err := s.DB().Exec(updates[damage]); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"", "key"} {
				q := RunInventoryQuery{Scope: RunInventoryScope{Principal: "reader", UserID: "user"}, Limit: 1, RequestKey: key, TerminalSince: now.Add(-time.Hour)}
				if _, err := s.ListRunInventory(t.Context(), q); err == nil || !strings.Contains(err.Error(), "discovery is incomplete") {
					t.Fatalf("partial %s with key %q was not explicitly incomplete: %v", damage, key, err)
				}
			}
		})
	}
}
