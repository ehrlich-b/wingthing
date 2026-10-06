package dashboard

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

func TestDashboardActionRoutes(t *testing.T) {
	remotes := map[string]config.Remote{"work": {SSHTarget: "me@host", WingthingDir: "/state with space"}}
	remotePrefix := []string{"--remote", "me@host", "--remote-state", "/state with space"}
	for _, test := range []struct {
		name string
		act  action
		want []string
	}{
		{"local attach", action{row: row{machine: "local", LocalSession: session("one", "name", "/src", "idle")}, attach: true}, []string{"attach", "--", "one"}},
		{"remote attach", action{row: row{machine: "work", LocalSession: session("one", "name", "/src", "idle")}, attach: true}, []string{"attach", "--", "work:one"}},
		{"remote rename", action{kind: rename, row: row{machine: "work", LocalSession: session("one", "name", "/src", "idle")}, name: "renamed"}, append(append([]string{}, remotePrefix...), "session", "rename", "--", "one", "renamed")},
		{"local stop", action{kind: confirmStop, row: row{machine: "local", LocalSession: session("one", "name", "/src", "idle")}}, []string{"session", "kill", "--", "one"}},
		{"remote new", action{kind: newCWD, row: row{machine: "work"}, agent: "codex", cwd: "/project space"}, append(append([]string{}, remotePrefix...), "egg", "codex", "--cwd", "/project space")},
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
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		err := backgroundCommand(ctx, func(context.Context, []string, remotepkg.IO) error {
			return errors.New("signal: killed")
		}, args, remotepkg.IO{})
		cancel()
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
	if background.SysProcAttr == nil || !background.SysProcAttr.Setpgid || background.Cancel != nil {
		t.Fatal("background action does not own its process group and cancellation")
	}
	attach := cliCommand(context.Background(), "fake-wt", []string{"attach", "--", "one"}, remotepkg.IO{In: strings.NewReader(""), Out: io.Discard})
	if attach.SysProcAttr != nil || attach.Cancel == nil {
		t.Fatal("attachment cannot read in the foreground or be canceled")
	}
}
