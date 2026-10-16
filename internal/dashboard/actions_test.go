package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
)

func TestDashboardActionRoutes(t *testing.T) {
	remotes := map[string]config.Remote{"work": {SSHTarget: "me@host", WTBinary: "/opt/wt builds/wt-dev", WingthingDir: "/state with space"}}
	remotePrefix := []string{"--remote", "me@host", "--remote-binary", "/opt/wt builds/wt-dev", "--remote-state", "/state with space"}
	for _, test := range []struct {
		name string
		act  action
		want []string
	}{
		{"local attach", action{row: row{machine: "local", LocalSession: session("one", "name", "/src", "idle")}, attach: true}, []string{"attach", "--", "one"}},
		{"remote attach", action{row: row{machine: "work", LocalSession: session("one", "name", "/src", "idle")}, attach: true}, []string{"attach", "--", "work:one"}},
		{"remote rename", action{kind: rename, row: row{machine: "work", LocalSession: session("one", "name", "/src", "idle")}, name: "renamed"}, append(append([]string{}, remotePrefix...), "session", "rename", "--", "one", "renamed")},
		{"remote stop", action{kind: confirmStop, row: row{machine: "work", LocalSession: session("full-session-id", "name", "/src", "idle")}}, append(append([]string{}, remotePrefix...), "session", "kill", "--", "full-session-id")},
		{"remote shell-looking cwd", action{kind: newCWD, row: row{machine: "work"}, cwd: "/src/$(touch canary); it's a project"}, append(append([]string{}, remotePrefix...), "terminal", "--cwd", "/src/$(touch canary); it's a project")},
		{"local stop", action{kind: confirmStop, row: row{machine: "local", LocalSession: session("one", "name", "/src", "idle")}}, []string{"session", "kill", "--", "one"}},
		{"remote new", action{kind: newCWD, row: row{machine: "work"}, agent: "codex", cwd: "/project space"}, append(append([]string{}, remotePrefix...), "egg", "codex", "--cwd", "/project space")},
		{"local Claude parent", action{kind: newCWD, row: row{machine: "local"}, agent: "claude", cwd: "/project space"}, []string{"claude", "--cwd", "/project space"}},
		{"remote Claude parent", action{kind: newCWD, row: row{machine: "work"}, agent: "claude", cwd: "../remote project space"}, append(append([]string{}, remotePrefix...), "claude", "--cwd", "../remote project space")},
		{"remote Claude routing-looking cwd", action{kind: newCWD, row: row{machine: "work"}, agent: "claude", cwd: "--remote-state"}, append(append([]string{}, remotePrefix...), "claude", "--cwd", "--remote-state")},
		{"local shell", action{kind: newCWD, row: row{machine: "local"}, cwd: "/src"}, []string{"terminal", "--cwd", "/src"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := actionArgs(test.act, remotes)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("args = %q, %v; want %q", got, err, test.want)
			}
		})
	}
	if _, err := actionArgs(action{kind: newCWD, row: row{machine: "local"}, agent: "--help"}, remotes); err == nil {
		t.Fatal("provider became a command flag")
	}
	if _, err := actionArgs(action{kind: rename, row: row{machine: "missing"}}, remotes); err == nil {
		t.Fatal("unconfigured remote was accepted")
	}
}

func TestDashboardBackgroundActionPanicBecomesError(t *testing.T) {
	err := backgroundCommand(context.Background(), func(context.Context, []string, remotepkg.IO) error {
		panic("broken action")
	}, nil, remotepkg.IO{Out: io.Discard})
	if err == nil {
		t.Fatal("background action panic escaped terminal cleanup")
	}
}

func TestDashboardBackgroundTimeoutReportsUncertainRemoteOutcome(t *testing.T) {
	for _, remote := range []bool{false, true} {
		args := []string{"session", "rename", "--", "one", "renamed"}
		if remote {
			args = append([]string{"--remote", "me@host"}, args...)
		}
		calls := 0
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		err := backgroundCommand(ctx, func(context.Context, []string, remotepkg.IO) error {
			calls++
			return errors.New("signal: killed")
		}, args, remotepkg.IO{})
		cancel()
		if calls != 1 {
			t.Fatalf("mutation dispatched %d times after uncertain timeout", calls)
		}
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("timeout reported as %v", err)
		}
		if strings.Contains(err.Error(), "the remote may still complete") != remote {
			t.Fatalf("remote=%v: %v", remote, err)
		}
	}
}

func TestDashboardActionProcessGroupCancellationReapsChild(t *testing.T) {
	for _, graceful := range []bool{false, true} {
		t.Run(map[bool]string{false: "forced", true: "graceful leader"}[graceful], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{})
			exited := make(chan struct{})
			waited := make(chan struct{})
			finished := make(chan error, 1)
			var signals []syscall.Signal
			transportAlive := true
			go func() {
				finished <- runActionProcess(ctx, func() error { close(started); return nil }, func() error {
					<-exited
					close(waited)
					return errors.New("signal: terminated")
				}, func(sig syscall.Signal) error {
					signals = append(signals, sig)
					if sig == syscall.SIGTERM && graceful {
						close(exited)
					}
					if sig == syscall.SIGKILL {
						transportAlive = false
						if !graceful {
							close(exited)
						}
					}
					return nil
				})
			}()
			receive(t, started)
			cancel()
			if err := receive(t, finished); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			receive(t, waited)
			if transportAlive || !reflect.DeepEqual(signals, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
				t.Fatalf("transportAlive=%v signals=%v", transportAlive, signals)
			}
		})
	}
}

func TestDashboardActionProcessSkipsCanceledStartAndHandlesExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	unexpected := func() error { t.Error("canceled action touched a process"); return nil }
	if err := runActionProcess(ctx, unexpected, unexpected, func(syscall.Signal) error { return unexpected() }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	failure := errors.New("process error")
	if err := runActionProcess(context.Background(), func() error { return failure }, unexpected, func(syscall.Signal) error { return unexpected() }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := runActionProcess(context.Background(), func() error { return nil }, func() error { return failure }, func(syscall.Signal) error { return unexpected() }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}

func TestDashboardCLIIsolatesBackgroundButKeepsAttachForeground(t *testing.T) {
	background := cliCommand(context.Background(), "fake-wt", []string{"--remote", "host", "session", "rename"}, remotepkg.IO{Out: io.Discard})
	if !reflect.DeepEqual(background.Args, []string{"fake-wt", "--remote-batch", "--remote", "host", "session", "rename"}) {
		t.Fatalf("background did not opt into batch SSH: %q", background.Args)
	}
	if background.SysProcAttr == nil || !background.SysProcAttr.Setpgid || background.Cancel != nil {
		t.Fatal("background action does not own its process group and cancellation")
	}
	attach := cliCommand(context.Background(), "fake-wt", []string{"attach", "--", "one"}, remotepkg.IO{In: strings.NewReader(""), Out: io.Discard})
	if attach.SysProcAttr != nil || attach.Cancel == nil {
		t.Fatal("attachment cannot read in the foreground or be canceled")
	}
}

func TestDashboardRemoteMutationValidatesSameCurrentProfile(t *testing.T) {
	scratch, err := filepath.Abs("../../.scratch")
	if err != nil {
		t.Fatal(err)
	}
	home, state, remoteState := testprovider.ScopedHomeDirs(t, scratch)
	root := t.TempDir()
	real, alias := filepath.Join(root, "tmp-real"), filepath.Join(root, "tmp-alias")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", alias)
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", state)
	profile := config.Remote{SSHTarget: "fixture-devbox", WTBinary: "/opt/custom wt", WingthingDir: remoteState}
	log := filepath.Join(home, "ssh-routes")
	ssh := filepath.Join(alias, "ssh")
	data, err := json.Marshal(eggclient.RemoteSessionInventory{Version: "fixture", ContractVersion: eggclient.RemoteSessionContractVersion, Sessions: []eggclient.LocalSession{{ID: "full-id", PID: 42}}})
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n[ \"$1\" = -T ] && [ \"$2\" = -o ] && [ \"$3\" = BatchMode=yes ] && [ \"$4\" = fixture-devbox ] || exit 91\n" +
		"printf '%s\\n' \"$5\" >> " + remotepkg.ShellQuote(log) + "\ncase \"$5\" in\n" +
		"*\"'--version'\"*) echo 'wt version fixture' ;;\n" +
		"*\"'session' 'ps' '--json' '--remote-inventory'\"*) printf '%s\\n' " + remotepkg.ShellQuote(string(data)) + " ;;\n*) exit 92 ;; esac\n"
	if err := os.WriteFile(ssh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name          string
		current       config.Remote
		present, fail bool
	}{
		{"same route", profile, true, false},
		{"target changed", config.Remote{SSHTarget: "other-peer", WTBinary: profile.WTBinary, WingthingDir: remoteState}, true, true},
		{"binary changed", config.Remote{SSHTarget: profile.SSHTarget, WTBinary: "/opt/another wt", WingthingDir: remoteState}, true, true},
		{"state changed", config.Remote{SSHTarget: profile.SSHTarget, WTBinary: profile.WTBinary, WingthingDir: "/another state"}, true, true},
		{"removed", config.Remote{}, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_ = os.Remove(log)
			profiles := map[string]config.Remote{}
			if test.present {
				profiles["devbox"] = test.current
			}
			if err := config.SaveRemotes(state, profiles); err != nil {
				t.Fatal(err)
			}
			for _, mutation := range []string{"rename", "kill"} {
				args := append(remotepkg.ProfileArgs(profile), "session", mutation, "--", "full-id")
				if mutation == "rename" {
					args = append(args, "new-name")
				}
				err := validateRemoteAction(t.Context(), args, remotepkg.IO{SSHPath: ssh})
				if (err != nil) != test.fail {
					t.Fatalf("guard %s: %v", mutation, err)
				}
				if test.fail && !strings.Contains(err.Error(), "saved profile changed or was removed") {
					t.Fatal(err)
				}
			}
			if test.fail {
				if _, err := os.Stat(log); !os.IsNotExist(err) {
					t.Fatal("changed saved profile was contacted")
				}
			} else {
				data, err := os.ReadFile(log)
				if err != nil {
					t.Fatal(err)
				}
				// Each mutation validates with one inventory query. Compare the
				// full route so state/channel drift or an extra version probe fails.
				state := remotepkg.ShellQuote(remoteState)
				want := "WINGTHING_DIR=" + state + " WINGTHING_PREVIEW_DIR=" + state +
					" '/opt/custom wt' '--expected-channel' " + remotepkg.ShellQuote(config.Channel()) +
					" 'session' 'ps' '--json' '--remote-inventory'\n"
				if string(data) != want+want {
					t.Fatalf("validation query lost exact route: got %q, want %q", data, want+want)
				}
			}
		})
	}
	if err := validateRemoteAction(t.Context(), []string{"session", "kill", "--", "local-id"}, remotepkg.IO{SSHPath: "/must-not-run"}); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardGenerationPinsEveryRemoteAction(t *testing.T) {
	s := &state{machines: map[string]machine{"local": {}}}
	inv := newInventory(nil)
	profile := config.Remote{SSHTarget: "old", WTBinary: "/custom/wt", WingthingDir: "/state"}
	inv.reload(s, map[string]config.Remote{"work": profile}, nil)
	target := s.sessionRow("work", session("same", "old", "/cwd", "working"))
	for _, act := range []action{{row: target, attach: true}, {row: target, kind: rename, name: "rename"}, {row: target, kind: confirmStop}, {row: target, kind: newCWD, agent: "claude", cwd: "/cwd"}} {
		args, err := actionArgs(act, s.remotes)
		if err != nil || len(args) < 6 || !reflect.DeepEqual(args[:6], remotepkg.ProfileArgs(profile)) {
			t.Fatalf("action lost pinned generation route: %v %v", args, err)
		}
		replacement := profile
		replacement.WingID = "other-identity"
		if _, err := actionArgs(act, map[string]config.Remote{"work": replacement}); err == nil {
			t.Fatal("old action accepted replacement identity")
		}
		if _, err := actionArgs(act, map[string]config.Remote{}); err == nil {
			t.Fatal("old action accepted removed profile")
		}
	}
}
