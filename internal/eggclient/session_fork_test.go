package eggclient

import (
	"compress/gzip"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	agentpkg "github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

func forkFixture(t *testing.T, live bool) (*config.Config, string, SessionForkScope) {
	t.Helper()
	old := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = old })
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Dir: t.TempDir()}
	cwd := wingpolicy.CanonicalSessionPath(t.TempDir())
	dir := filepath.Join(cfg.Dir, "eggs", "source")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	home := EffectiveSessionHome(cfg, EggIdentity{})
	for file, value := range map[string]string{
		"egg.meta":  "agent=claude\nkind=agent\ncwd=" + cwd + "\nprovider_session_id=provider\nprovider_home=" + home + "\n",
		"egg.owner": "alice\nalice@example.com\n", "session.principal": "owner\n", "session.name": "original\n",
		"chat.meta":                "agent=claude\ncwd=" + cwd + "\nagent_session_id=provider\n",
		ProviderResumeMetadataFile: string(providerResumeMetadata(ProviderResumeKey(home, "claude", "provider"), "")),
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	captured := "{\"type\":\"assistant\",\"message\":{\"model\":\"claude-test-model\"}}\n"
	f, err := os.Create(filepath.Join(dir, "chat.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	w := gzip.NewWriter(f)
	if _, err := w.Write([]byte(captured)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "egg.yaml"), []byte("base: none\nfs: [rw:"+cwd+"]\nshell: /bin/sh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if live {
		if err := os.WriteFile(filepath.Join(dir, "egg.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			t.Fatal(err)
		}
		native := filepath.Join(home, egg.Profile("claude").SessionDir, strings.ReplaceAll(cwd, "/", "-"))
		if err := os.MkdirAll(native, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(native, "provider.jsonl"), []byte(captured), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return cfg, dir, SessionForkScope{Principal: "owner", Identity: EggIdentity{UserID: "alice"}, AllowedPaths: []string{cwd}, EnforcePathBounds: true}
}

func TestSessionForkFakeClaudeArgvAndSourcePreserved(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(strconv.FormatBool(live), func(t *testing.T) {
			cfg, dir, scope := forkFixture(t, live)
			before := map[string]string{}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				before[entry.Name()] = string(data)
			}
			fake := filepath.Join(t.TempDir(), "claude")
			if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			scope.Spawn = func(plan *SessionForkPlan) error {
				if plan.Source.ID != "source" || plan.Options.Label != "branch" || !plan.Options.ForkSession || plan.Config.Shell != "/bin/sh" || !reflect.DeepEqual(plan.Config.FS, []string{"rw:" + scope.AllowedPaths[0], "deny-write:" + filepath.Join(scope.AllowedPaths[0], "egg.yaml"), "deny-rename:" + scope.AllowedPaths[0]}) {
					t.Fatalf("fork plan: %#v", plan)
				}
				newProvider, args, resume, err := effectiveSpawnProviderSession(plan.Source.Agent, plan.Options)
				if err != nil {
					return err
				}
				if newProvider == "provider" || !ValidProviderSessionID(newProvider) || resume != "provider" {
					t.Fatalf("provider identity: %q %q", newProvider, resume)
				}
				_, argv, ok := agentpkg.InteractiveInvocation("claude", false, resume, args...)
				if !ok {
					t.Fatal("no Claude invocation")
				}
				output, err := exec.Command(fake, argv...).Output()
				if err != nil {
					return err
				}
				want := []string{"--resume", "provider", "--session-id", newProvider, "--fork-session"}
				if !reflect.DeepEqual(strings.Split(strings.TrimSpace(string(output)), "\n"), want) {
					t.Fatalf("fake Claude argv = %s, want %q", output, want)
				}
				return nil
			}
			result, err := ForkSession(context.Background(), cfg, "source", "branch", scope)
			if err != nil {
				t.Fatal(err)
			}
			if result.Session == "source" || result.Label != "branch" || result.SourceSession != "source" {
				t.Fatalf("result: %#v", result)
			}
			for name, want := range before {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(data) != want {
					t.Fatalf("source %s changed: %v", name, err)
				}
			}
			if info, err := os.Stat(filepath.Join(cfg.Dir, "eggs", result.Session, ProviderResumeMetadataFile)); err == nil {
				t.Fatalf("fork claimed the source provider identity: %v", info)
			}
			entries, _ = os.ReadDir(filepath.Join(cfg.Dir, "eggs"))
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".fork-history-") {
					t.Fatal("temporary history leaked")
				}
			}
		})
	}
}

func TestSessionForkIgnoresEggDirectoryPolicyAndIdentity(t *testing.T) {
	for _, snapshot := range []string{"missing", "edited", "symlink"} {
		t.Run(snapshot, func(t *testing.T) {
			cfg, dir, scope := forkFixture(t, false)
			path := filepath.Join(dir, "session.launch.json")
			if snapshot == "edited" {
				if err := os.WriteFile(path, []byte(`{"config":"base: none\nnetwork: '*'\nenv: '*'","model":"untrusted-model"}`), 0600); err != nil {
					t.Fatal(err)
				}
			} else if snapshot == "symlink" {
				if err := os.Symlink(filepath.Join(dir, "egg.yaml"), path); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "egg.yaml"), []byte("base: none\nnetwork: '*'\nenv: '*'\nfs: [rw:/]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=claude\ncwd="+scope.AllowedPaths[0]+"\nprovider_home=/untrusted\nshared_host=false\norg_wing=false\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := WriteSessionPrincipal(dir, "agent-chosen-principal"); err != nil {
				t.Fatal(err)
			}
			fresh, err := LoadSpawnEggConfig("", scope.AllowedPaths[0], false)
			if err != nil {
				t.Fatal(err)
			}
			scope.Identity.OrgWing, scope.Identity.SharedHost, scope.Identity.SealedFS = true, true, true
			scope.Identity.AllowedPaths = scope.AllowedPaths
			scope.Spawn = func(plan *SessionForkPlan) error {
				if !reflect.DeepEqual(plan.Config, fresh) || !reflect.DeepEqual(plan.Identity, scope.Identity) || plan.Options.Principal != scope.Principal || len(plan.Options.AgentArgs) != 0 {
					t.Fatalf("source supplied launch authority: %#v", plan)
				}
				return nil
			}
			if _, err := ForkSession(context.Background(), cfg, "source", "branch", scope); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionForkUsesOwnedActiveLabel(t *testing.T) {
	cfg, _, scope := forkFixture(t, true)
	for _, id := range []string{"a-foreign", "z-archived"} {
		dir := filepath.Join(cfg.Dir, "eggs", id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := WriteSessionName(dir, "original"); err != nil {
			t.Fatal(err)
		}
		if id == "a-foreign" {
			if err := os.WriteFile(filepath.Join(dir, "egg.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "egg.owner"), []byte("bob\n"), 0600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(filepath.Join(dir, "egg.owner"), []byte("alice\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	scope.Spawn = func(plan *SessionForkPlan) error {
		if plan.Source.ID != "source" {
			t.Fatalf("fork selected %q instead of the owned active label", plan.Source.ID)
		}
		return nil
	}
	if _, err := ForkSession(context.Background(), cfg, "original", "branch", scope); err != nil {
		t.Fatal(err)
	}
}

func TestSessionForkRejectsMalformedProviderID(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, id := range []string{"../other", "--continue", "with space", `with\backslash`, "bad\x00id"} {
			t.Run(strconv.FormatBool(live)+"/"+id, func(t *testing.T) {
				cfg, dir, scope := forkFixture(t, live)
				for file, value := range map[string]string{
					"egg.meta":                 "agent=claude\ncwd=" + scope.AllowedPaths[0] + "\nprovider_session_id=" + id + "\n",
					"chat.meta":                "agent=claude\ncwd=" + scope.AllowedPaths[0] + "\nagent_session_id=" + id + "\n",
					ProviderResumeMetadataFile: "agent=claude\nprovider_session_id=" + id + "\n",
				} {
					if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0600); err != nil {
						t.Fatal(err)
					}
				}
				scope.Spawn = func(*SessionForkPlan) error { t.Fatal("invalid provider ID spawned"); return nil }
				if _, err := ForkSession(context.Background(), cfg, "source", "branch", scope); err == nil {
					t.Fatal("accepted malformed provider ID")
				}
			})
		}
	}
}

func TestSessionForkRejectsOwnershipPathsUnsupportedAndNameCollision(t *testing.T) {
	for _, name := range []string{"principal", "browser owner", "revoked path", "empty member paths", "unsupported", "name collision", "unverified live provider"} {
		t.Run(name, func(t *testing.T) {
			cfg, dir, scope := forkFixture(t, name == "unverified live provider")
			want := "owned"
			switch name {
			case "principal":
				scope.Principal = "other"
				scope.Identity = EggIdentity{}
			case "browser owner":
				scope.Identity.UserID = "bob"
			case "revoked path":
				scope.AllowedPaths = []string{t.TempDir()}
			case "empty member paths":
				scope.AllowedPaths = nil
			case "unsupported":
				if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=codex\ncwd="+scope.AllowedPaths[0]+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "only Claude"
			case "name collision":
				other := filepath.Join(cfg.Dir, "eggs", "other")
				if err := os.MkdirAll(other, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(other, "egg.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
					t.Fatal(err)
				}
				if err := WriteSessionName(other, "branch"); err != nil {
					t.Fatal(err)
				}
				want = ErrSessionNameInUse.Error()
			case "unverified live provider":
				if err := os.Remove(filepath.Join(dir, ProviderResumeMetadataFile)); err != nil {
					t.Fatal(err)
				}
				want = "identity"
			}
			scope.Spawn = func(*SessionForkPlan) error { t.Fatal("refused fork spawned"); return nil }
			_, err := ForkSession(context.Background(), cfg, "source", "branch", scope)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want %q", err, want)
			}
		})
	}
}

func TestSessionForkConversationIsSibling(t *testing.T) {
	for _, parent := range []string{"", "root"} {
		t.Run("parent="+parent, func(t *testing.T) {
			cfg, _, scope := forkFixture(t, false)
			db, err := store.Open(cfg.DBPath())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			if parent != "" {
				_, _, err = db.ReserveConversation(store.Conversation{ID: "root", OwnerID: "owner", Agent: "claude", SessionID: "root-session", LaunchKey: "root-key", SpecDigest: "root"})
				if err != nil {
					t.Fatal(err)
				}
			}
			original, _, err := db.ReserveConversation(store.Conversation{ID: "original", OwnerID: "owner", ParentID: parent, Agent: "claude", CWD: scope.AllowedPaths[0], SessionID: "source", LaunchKey: "source", SpecDigest: "source"})
			if err != nil {
				t.Fatal(err)
			}
			scope.Spawn = func(*SessionForkPlan) error { return nil }
			result, err := ForkSession(context.Background(), cfg, "source", "branch", scope)
			if err != nil {
				t.Fatal(err)
			}
			c, err := db.ConversationForSession(result.Session)
			if err != nil {
				t.Fatal(err)
			}
			if c.ID == original.ID || c.ParentID != original.ParentID || c.ParentID == original.ID || c.LaunchState != "started" {
				t.Fatalf("not sibling: %#v", c)
			}
			if parent != "" && c.RootID != original.RootID || parent == "" && c.RootID != c.ID {
				t.Fatalf("root: %#v", c)
			}
			unchanged, err := db.ConversationForSession("source")
			if err != nil || !reflect.DeepEqual(original, unchanged) {
				t.Fatalf("source conversation changed: %#v %v", unchanged, err)
			}
		})
	}
}

func TestSessionForkAdmissionAndReservationRelease(t *testing.T) {
	cfg, _, scope := forkFixture(t, false)
	scope.Admit = func(func() error) error { return errors.New("spawn budget exhausted") }
	if _, err := ForkSession(context.Background(), cfg, "source", "branch", scope); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("admission: %v", err)
	}
	scope.Admit = nil
	scope.Spawn = func(*SessionForkPlan) error { return errors.New("spawn failed") }
	if _, err := ForkSession(context.Background(), cfg, "source", "branch", scope); err == nil {
		t.Fatal("accepted failed spawn")
	}
	scope.Spawn = func(*SessionForkPlan) error { return nil }
	if _, err := ForkSession(context.Background(), cfg, "source", "branch", scope); err != nil {
		t.Fatalf("failed fork leaked reservation: %v", err)
	}
}

func TestProviderForkReservationAllowsLiveSourceAndSerializesRestore(t *testing.T) {
	cfg, dir, _ := forkFixture(t, true)
	home := ReadEggMetaValues(dir)["provider_home"]
	r := providerResumeRegistry{Active: make(map[string]string)}
	release, err := r.ReserveFork(cfg, home, "claude", "provider", "source", "branch")
	if err != nil {
		t.Fatal(err)
	}
	defer release(false)
	if _, err := os.Stat(filepath.Join(cfg.Dir, "eggs", "branch", ProviderResumeMetadataFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fork claimed source identity")
	}
	contender := providerResumeRegistry{Active: make(map[string]string)}
	if _, err := contender.ReserveFork(cfg, home, "claude", "provider", "source", "contender"); err == nil {
		t.Fatal("fork restore bypassed the cross-process reservation lock")
	}
	release(false)
	if _, err := contender.Reserve(cfg, home, "claude", "provider", "source", "ordinary-resume"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("ordinary resume bypassed the live source: %v", err)
	}
}

func TestSessionForkHoldsNameLockThroughSpawn(t *testing.T) {
	cfg, _, scope := forkFixture(t, false)
	done := make(chan error, 1)
	scope.Spawn = func(*SessionForkPlan) error {
		go func() {
			lock, err := AcquireSessionNameLock(cfg)
			if err == nil {
				_ = lock.Close()
			}
			done <- err
		}()
		select {
		case err := <-done:
			t.Fatalf("name lock released before spawn finished: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		return nil
	}
	if _, err := ForkSession(context.Background(), cfg, "source", "branch", scope); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("name lock remained held after fork")
	}
}
