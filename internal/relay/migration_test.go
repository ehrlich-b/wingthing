package relay

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenRelayMigratesConcurrentFirstUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	const attempts = 8
	start := make(chan struct{})
	stores := make(chan *RelayStore, attempts)
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			store, err := OpenRelay(path)
			stores <- store
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(stores)
	close(errs)
	for store := range stores {
		if store != nil {
			t.Cleanup(func() { _ = store.Close() })
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent first open: %v", err)
		}
	}

	store, err := OpenRelay(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var applied int
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			want++
		}
	}
	if applied != want {
		t.Fatalf("applied migrations = %d, want %d", applied, want)
	}
}

func TestUpgradeEveryRelayMigrationBaselinePreservesRelationships(t *testing.T) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var migrations []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			migrations = append(migrations, entry.Name())
		}
	}
	sort.Strings(migrations)
	if len(migrations) == 0 {
		t.Fatal("no embedded relay migrations")
	}

	for baseline := 1; baseline <= len(migrations); baseline++ {
		baseline := baseline
		t.Run(fmt.Sprintf("through_%s", migrations[baseline-1]), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "relay.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE schema_migrations (
				version TEXT PRIMARY KEY,
				applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
			)`); err != nil {
				t.Fatal(err)
			}
			for index := 0; index < baseline; index++ {
				contents, err := migrationsFS.ReadFile("migrations/" + migrations[index])
				if err != nil {
					t.Fatal(err)
				}
				tx, err := db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(string(contents)); err != nil {
					_ = tx.Rollback()
					t.Fatalf("apply %s: %v", migrations[index], err)
				}
				if _, err := tx.Exec("INSERT INTO schema_migrations(version) VALUES (?)", migrations[index]); err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}

			for _, user := range []string{"upgrade-owner", "upgrade-member"} {
				if _, err := db.Exec(
					"INSERT INTO users(id, provider, provider_id, display_name) VALUES (?, 'google', ?, ?)",
					user, user, user,
				); err != nil {
					t.Fatal(err)
				}
			}
			mustMigrationExec(t, db, "INSERT INTO sessions(token, user_id, expires_at) VALUES ('upgrade-session', 'upgrade-owner', '2030-01-01 00:00:00')")
			mustMigrationExec(t, db, "INSERT INTO device_tokens(token, user_id, device_id) VALUES ('upgrade-device-token', 'upgrade-owner', 'upgrade-wing')")
			mustMigrationExec(t, db, "INSERT INTO audit_log(user_id, event, detail) VALUES ('upgrade-owner', 'upgrade-event', 'durable audit')")
			mustMigrationExec(t, db, "INSERT INTO orgs(id, name, slug, owner_user_id, max_seats) VALUES ('upgrade-org', 'Upgrade Org', 'shared-slug', 'upgrade-owner', 3)")
			mustMigrationExec(t, db, "INSERT INTO org_members(org_id, user_id, role) VALUES ('upgrade-org', 'upgrade-owner', 'owner'), ('upgrade-org', 'upgrade-member', 'admin')")
			if baseline >= 3 {
				mustMigrationExec(t, db, "INSERT INTO org_invites(id, org_id, email, token, invited_by, role) VALUES ('upgrade-invite', 'upgrade-org', 'invite@example.test', 'upgrade-invite-token', 'upgrade-owner', 'admin')")
			} else {
				mustMigrationExec(t, db, "INSERT INTO org_invites(id, org_id, email, token, invited_by) VALUES ('upgrade-invite', 'upgrade-org', 'invite@example.test', 'upgrade-invite-token', 'upgrade-owner')")
			}
			mustMigrationExec(t, db, "INSERT INTO subscriptions(id, org_id, plan, status, seats) VALUES ('upgrade-sub', 'upgrade-org', 'team_monthly', 'active', 3)")
			mustMigrationExec(t, db, "INSERT INTO entitlements(id, user_id, subscription_id) VALUES ('upgrade-owner-ent', 'upgrade-owner', 'upgrade-sub'), ('upgrade-member-ent', 'upgrade-member', 'upgrade-sub')")
			if baseline >= 2 {
				mustMigrationExec(t, db, "INSERT INTO labels(target_id, scope_type, scope_id, label) VALUES ('upgrade-wing', 'org', 'upgrade-org', 'Durable wing')")
			}
			if baseline >= 5 {
				mustMigrationExec(t, db, "INSERT INTO passkey_credentials(id, user_id, credential_id, public_key, sign_count, label) VALUES ('upgrade-passkey', 'upgrade-owner', X'0102', X'0304', 7, 'Durable key')")
			}
			if baseline >= 6 {
				mustMigrationExec(t, db, "UPDATE users SET ntfy_topic='durable-topic', ntfy_token='durable-token', ntfy_events='attention' WHERE id='upgrade-owner'")
			}
			if baseline >= 7 {
				mustMigrationExec(t, db, "INSERT INTO mcp_oauth_clients(client_id, client_name, redirect_uris, expires_at) VALUES ('upgrade-client', 'Durable client', '[\"http://127.0.0.1/callback\"]', '2030-01-01 00:00:00')")
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			store, err := OpenRelay(path)
			if err != nil {
				t.Fatalf("open upgraded relay store: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			user, err := store.GetSession("upgrade-session")
			if err != nil || user == nil || user.ID != "upgrade-owner" {
				t.Fatalf("upgraded session user = %#v, %v", user, err)
			}
			if userID, deviceID, err := store.ValidateToken("upgrade-device-token"); err != nil || userID != "upgrade-owner" || deviceID != "upgrade-wing" {
				t.Fatalf("upgraded device token = %q/%q, %v", userID, deviceID, err)
			}
			org, err := store.GetOrgByID("upgrade-org")
			if err != nil || org == nil || org.OwnerUserID != "upgrade-owner" || org.MaxSeats != 3 {
				t.Fatalf("upgraded org = %#v, %v", org, err)
			}
			members, err := store.ListOrgMembers("upgrade-org")
			if err != nil || len(members) != 2 || store.GetOrgMemberRole("upgrade-org", "upgrade-member") != "admin" {
				t.Fatalf("upgraded members = %#v, %v", members, err)
			}
			invite, err := store.GetInviteByToken("upgrade-invite-token")
			if err != nil || invite == nil {
				t.Fatalf("upgraded invite = %#v, %v", invite, err)
			}
			wantInviteRole := "member"
			if baseline >= 3 {
				wantInviteRole = "admin"
			}
			if invite.Role != wantInviteRole {
				t.Fatalf("upgraded invite role = %q, want %q", invite.Role, wantInviteRole)
			}
			subscription, err := store.GetActiveOrgSubscription("upgrade-org")
			if err != nil || subscription == nil || subscription.ID != "upgrade-sub" || !store.IsUserPro("upgrade-member") {
				t.Fatalf("upgraded subscription = %#v, pro=%v, err=%v", subscription, store.IsUserPro("upgrade-member"), err)
			}
			if baseline >= 2 && store.ResolveLabel("upgrade-wing", "", "upgrade-org") != "Durable wing" {
				t.Fatal("upgraded label was not preserved")
			}
			if baseline >= 5 {
				credentials, err := store.ListPasskeyCredentials("upgrade-owner")
				if err != nil || len(credentials) != 1 || credentials[0].SignCount != 7 {
					t.Fatalf("upgraded passkeys = %#v, %v", credentials, err)
				}
			}
			if baseline >= 6 {
				config, err := store.GetNtfyConfig("upgrade-owner")
				if err != nil || config.Topic != "durable-topic" || config.Token != "durable-token" {
					t.Fatalf("upgraded ntfy config = %#v, %v", config, err)
				}
			}
			if baseline >= 7 {
				client, err := store.GetMCPClientRegistration("upgrade-client", time.Now())
				if err != nil || client == nil || client.ClientName != "Durable client" {
					t.Fatalf("upgraded MCP client = %#v, %v", client, err)
				}
			}
			var audits int
			if err := store.DB().QueryRow("SELECT COUNT(*) FROM audit_log WHERE user_id='upgrade-owner' AND detail='durable audit'").Scan(&audits); err != nil || audits != 1 {
				t.Fatalf("upgraded audit rows = %d, %v", audits, err)
			}
			if err := store.CreateOrg("duplicate-slug-org", "Second Org", "shared-slug", "upgrade-owner"); err != nil {
				t.Fatalf("post-upgrade duplicate slug behavior: %v", err)
			}
			var applied int
			if err := store.DB().QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&applied); err != nil {
				t.Fatal(err)
			}
			if applied != len(migrations) {
				t.Fatalf("applied migrations = %d, want %d", applied, len(migrations))
			}
			rows, err := store.DB().Query("PRAGMA foreign_key_check")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			if rows.Next() {
				t.Fatal("upgraded relay store has a foreign-key violation")
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func mustMigrationExec(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.Exec(statement); err != nil {
		t.Fatal(err)
	}
}
