package wingsession

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

// This subprocess only acknowledges input. Its argv lets the real PID verifier
// recognize a fixture session without starting an egg or provider.
func TestOrphanStopProcessFixture(t *testing.T) {
	if os.Getenv("WT_ORPHAN_STOP_FIXTURE") != "1" {
		return
	}
	fmt.Println("ready")
	input := bufio.NewScanner(os.Stdin)
	for input.Scan() {
		fmt.Println(input.Text())
	}
	os.Exit(0)
}

type orphanStopProcess struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Reader
	done   chan struct{}
	err    error
}

func startOrphanStopProcess(t *testing.T, id string) *orphanStopProcess {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestOrphanStopProcessFixture$", "--", "--session-id", id)
	// Keep Darwin's ps command display short enough to include the session ID.
	cmd.Args[0] = "stop-fixture"
	cmd.Env = append(os.Environ(), "WT_ORPHAN_STOP_FIXTURE=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &orphanStopProcess{cmd: cmd, input: input, output: bufio.NewReader(output), done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		_ = input.Close()
		_ = cmd.Process.Kill()
		<-p.done
	})
	p.expect(t, "ready")
	return p
}

func (p *orphanStopProcess) expect(t *testing.T, line string) {
	t.Helper()
	got, err := p.output.ReadString('\n')
	if err != nil || got != line+"\n" {
		t.Fatalf("fixture response = %q, %v; want %q", got, err, line)
	}
}

func (p *orphanStopProcess) assertAlive(t *testing.T) {
	t.Helper()
	if _, err := io.WriteString(p.input, "alive\n"); err != nil {
		t.Fatal(err)
	}
	p.expect(t, "alive")
}

func unreachableStopSession(t *testing.T, pid int, token bool) (*Service, string, Authority, Authority) {
	t.Helper()
	root := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", root)
	// All state stays in t.TempDir; this checkout alias keeps the socket path
	// below Darwin's sockaddr_un limit, so the failure is the absent socket.
	scratch, err := filepath.Abs("../../.scratch")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	alias, err := os.MkdirTemp(scratch, "o")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(alias) })
	t.Setenv("WINGTHING_DIR", alias)
	dir := filepath.Join(alias, "eggs", "fixture")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"egg.meta": "cwd=" + root + "\n"}
	if pid != 0 {
		files["egg.pid"] = strconv.Itoa(pid)
	}
	if token {
		files["egg.token"] = "fixture-token"
	}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := eggclient.WriteEggOwner(dir, "alice", "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	principal := UserPrincipal("alice")
	if err := eggclient.WriteSessionPrincipal(dir, principal); err != nil {
		t.Fatal(err)
	}
	web := Authority{UserID: "alice", Role: "member", Browser: true, EnforcePaths: true, AllowedPaths: []string{root}}
	mcp := web
	mcp.Browser, mcp.Principal = false, principal
	return &Service{Config: &config.Config{Dir: alias}}, dir, web, mcp
}

func TestStopUnreachableOwnedSessionTerminatesVerifiedPID(t *testing.T) {
	for _, surface := range []string{"web", "mcp"} {
		for _, token := range []bool{false, true} {
			t.Run(surface+"/token="+strconv.FormatBool(token), func(t *testing.T) {
				p := startOrphanStopProcess(t, "fixture")
				s, dir, web, mcp := unreachableStopSession(t, p.cmd.Process.Pid, token)
				if !eggclient.EggPidMatchesSession(p.cmd.Process.Pid, "fixture") {
					out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(p.cmd.Process.Pid)).CombinedOutput()
					t.Fatalf("fixture PID does not match its session: ps = %q, %v", out, err)
				}
				a := web
				if surface == "mcp" {
					a = mcp
				}
				foreign, revoked := a, a
				foreign.UserID = "bob"
				revoked.AllowedPaths = []string{filepath.Join(s.Config.Dir, "other")}
				for _, authority := range []Authority{foreign, revoked} {
					if _, err := s.Stop(t.Context(), authority, "fixture"); err == nil {
						t.Fatalf("stop admitted foreign or revoked authority: %+v", authority)
					}
					p.assertAlive(t)
					data, err := os.ReadFile(filepath.Join(dir, "egg.pid"))
					if err != nil || string(data) != strconv.Itoa(p.cmd.Process.Pid) {
						t.Fatalf("denied stop changed PID metadata: %q, %v", data, err)
					}
				}
				session, err := s.Stop(t.Context(), a, "fixture")
				if err != nil || session.ID != "fixture" {
					t.Fatalf("stop unreachable session: %+v, %v", session, err)
				}
				<-p.done
				var exit *exec.ExitError
				if !errors.As(p.err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
					t.Fatalf("fixture did not exit on SIGTERM: %v", p.err)
				}
				if _, err := os.Stat(filepath.Join(dir, "egg.pid")); err != nil {
					t.Fatalf("runtime metadata removed before egg cleanup: %v", err)
				}
			})
		}
	}
}

func TestStopUnreachableSessionDenialsAndStaleCleanup(t *testing.T) {
	for _, surface := range []string{"web", "mcp"} {
		for _, state := range []string{"dead", "recycled-pid", "retained-history"} {
			t.Run(surface+"/"+state, func(t *testing.T) {
				p := startOrphanStopProcess(t, "another-session")
				pid := 0
				if state == "recycled-pid" {
					pid = p.cmd.Process.Pid
				}
				s, dir, web, mcp := unreachableStopSession(t, pid, true)
				if state == "retained-history" {
					if err := os.WriteFile(filepath.Join(dir, "lifecycle.jsonl"), []byte("retained\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				before := map[string]string{}
				for _, entry := range entries {
					data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
					if err != nil {
						t.Fatal(err)
					}
					before[entry.Name()] = string(data)
				}
				a := web
				if surface == "mcp" {
					a = mcp
				}
				foreign, revoked := a, a
				foreign.UserID = "bob"
				revoked.AllowedPaths = []string{filepath.Join(s.Config.Dir, "other")}
				denied := []Authority{foreign, revoked}
				if surface == "mcp" {
					otherPrincipal := a
					otherPrincipal.Principal = "other"
					denied = append(denied, otherPrincipal)
				}
				for _, authority := range denied {
					if _, err := s.Stop(t.Context(), authority, "fixture"); err == nil {
						t.Fatalf("stop admitted foreign or revoked authority: %+v", authority)
					}
					p.assertAlive(t)
					for name, want := range before {
						data, err := os.ReadFile(filepath.Join(dir, name))
						if err != nil || string(data) != want {
							t.Fatalf("denied stop changed %s: %q, %v", name, data, err)
						}
					}
				}
				if session, err := s.Stop(t.Context(), a, "fixture"); err != nil || session.ID != "fixture" {
					t.Fatalf("stop stale session: %+v, %v", session, err)
				}
				p.assertAlive(t)
				if state == "retained-history" {
					for _, name := range []string{"egg.meta", "egg.owner", "session.principal", "lifecycle.jsonl"} {
						data, err := os.ReadFile(filepath.Join(dir, name))
						if err != nil || string(data) != before[name] {
							t.Fatalf("cleanup lost retained %s: %q, %v", name, data, err)
						}
					}
					if _, err := os.Stat(filepath.Join(dir, "egg.token")); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("cleanup retained runtime token: %v", err)
					}
				} else if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("stale session directory remains: %v", err)
				}
			})
		}
	}
}
