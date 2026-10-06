package dashboard

import (
	"context"
	"io"
	"reflect"
	"testing"

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
