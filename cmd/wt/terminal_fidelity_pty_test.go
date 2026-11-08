package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/creack/pty"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
	"golang.org/x/term"
)

type fidelityCase struct {
	Name, Output, Input string
	Query               bool
}

func fidelityCases(t *testing.T) []fidelityCase {
	t.Helper()
	data, err := os.ReadFile("testdata/terminal_fidelity_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []fidelityCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 24 {
		t.Fatal("missing behaviours")
	}
	return cases
}

// Protocol terminal oracle, deliberately independent of the egg's VT shadow.
// It implements the measured query replies and DEC state, and records all wire
// bytes. GUI policies (clipboard permission, link opening) are outside its scope.
type fidelityTerminal struct {
	before                *term.State
	t                     *testing.T
	master, slave         *os.File
	cmd                   *exec.Cmd
	mu                    sync.Mutex
	wire                  []byte
	pending               []byte
	modes, saved          map[int]bool
	changed               chan struct{}
	readDone, processDone chan struct{}
	err                   error
	queries               map[string]string
	reporting             int // 0: DECRQM replies, 1: unsupported reports, 2: no reports
}

var fidelityCSI = regexp.MustCompile("^\x1b\\[([?<>0-9;\x7f]*)([ $\x7f]*)([@-~])")

func startFidelityTerminal(t *testing.T, cmd *exec.Cmd, initial map[int]bool, options ...func(*fidelityTerminal)) *fidelityTerminal {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := pty.Setsize(master, &pty.Winsize{Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	h := &fidelityTerminal{t: t, master: master, slave: slave, cmd: cmd, modes: map[int]bool{25: true}, saved: map[int]bool{}, changed: make(chan struct{}, 1), readDone: make(chan struct{}), processDone: make(chan struct{}), queries: map[string]string{}}
	for mode, value := range initial {
		h.modes[mode] = value
	}
	for _, c := range fidelityCases(t) {
		if c.Query {
			h.queries[c.Output] = c.Input
		}
	}
	// CPR's positioning prefix is parsed separately.
	h.queries["\x1b[6n"] = "\x1b[7;13R"
	for _, option := range options {
		option(h)
	}
	h.before, err = term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { h.err = cmd.Wait(); close(h.processDone) }()
	go h.read()
	t.Cleanup(func() {
		select {
		case <-h.processDone:
		default:
			_ = cmd.Process.Kill()
			<-h.processDone
		}
		_ = master.Close()
		_ = slave.Close()
		<-h.readDone
	})
	return h
}
func (h *fidelityTerminal) send(data string) {
	h.t.Helper()
	for len(data) > 0 {
		n, err := io.WriteString(h.master, data)
		if err != nil {
			h.t.Error(err)
			return
		}
		data = data[n:]
	}
}
func (h *fidelityTerminal) read() {
	defer close(h.readDone)
	buf := make([]byte, 8192)
	for {
		n, err := h.master.Read(buf)
		if n > 0 {
			h.mu.Lock()
			h.wire = append(h.wire, buf[:n]...)
			h.pending = append(h.pending, buf[:n]...)
			replies := h.parse()
			h.mu.Unlock()
			for _, reply := range replies {
				h.send(reply)
			}
			select {
			case h.changed <- struct{}{}:
			default:
			}
		}
		if err != nil {
			return
		}
	}
}
func (h *fidelityTerminal) parse() (replies []string) {
	for len(h.pending) > 0 {
		p := h.pending
		if p[0] != 0x1b {
			h.pending = p[1:]
			continue
		}
		if len(p) < 2 {
			break
		}
		var seq string
		if p[1] == ']' {
			end := bytes.IndexByte(p, 7)
			width := 1
			if st := bytes.Index(p, []byte("\x1b\\")); st >= 0 && (end < 0 || st < end) {
				end = st
				width = 2
			}
			if end < 0 {
				break
			}
			seq = string(p[:end+width])
		} else if p[1] == '[' {
			match := fidelityCSI.FindSubmatch(p)
			if match == nil {
				break
			}
			seq = string(match[0])
			// DEL is ignored inside CSI, including between parameter bytes.
			// Keep seq raw so consumption and captured wire bytes remain exact.
			params := strings.ReplaceAll(string(match[1]), "\x7f", "")
			mid := strings.ReplaceAll(string(match[2]), "\x7f", "")
			final := string(match[3])
			if strings.HasPrefix(params, "?") {
				modes := strings.Split(params[1:], ";")
				for _, raw := range modes {
					mode, e := strconv.Atoi(raw)
					if e != nil {
						continue
					}
					switch final {
					case "h":
						h.modes[mode] = true
					case "l":
						h.modes[mode] = false
					case "s":
						h.saved[mode] = h.modes[mode]
					case "r":
						if v, ok := h.saved[mode]; ok {
							h.modes[mode] = v
						}
					case "p":
						if mid == "$" {
							if h.reporting == 2 {
								continue
							}
							v := 2
							if h.modes[mode] {
								v = 1
							}
							if h.reporting == 1 {
								v = 0
							}
							replies = append(replies, fmt.Sprintf("\x1b[?%d;%d$y", mode, v))
						}
					}
				}
			}
		} else {
			seq = string(p[:2])
		}
		if reply, ok := h.queries[seq]; ok {
			replies = append(replies, reply)
		}
		h.pending = p[len(seq):]
	}
	return
}
func (h *fidelityTerminal) snapshot() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return bytes.Clone(h.wire)
}
func (h *fidelityTerminal) wait(needle string) {
	h.t.Helper()
	for {
		if bytes.Contains(h.snapshot(), []byte(needle)) {
			return
		}
		select {
		case <-h.changed:
		case <-h.processDone:
			_ = h.slave.Close()
			<-h.readDone
			if bytes.Contains(h.snapshot(), []byte(needle)) {
				return
			}
			h.t.Fatalf("process exited (%v) waiting for %q: %q", h.err, needle, h.snapshot())
		case <-h.readDone:
			h.t.Fatalf("terminal closed waiting for %q: %q", needle, h.snapshot())
		case <-h.t.Context().Done():
			h.t.Fatal(h.t.Context().Err())
		}
	}
}
func fidelityWrite(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}
func fidelityBuild(t *testing.T) string {
	t.Helper()
	if binary := os.Getenv("WT_TEST_BINARY"); binary != "" {
		return binary
	}
	binary := filepath.Join(t.TempDir(), "wt")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-p", "2", "-o", binary, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	return binary
}

type fidelityEgg struct {
	home, socket, token string
	cmd                 *exec.Cmd
	done                chan error
	logs                *nativeMouseCapture
}

func startFidelityEgg(t *testing.T, binary, provider string, args ...string) *fidelityEgg {
	t.Helper()
	// A short, owned state directory keeps AF_UNIX paths valid on every platform.
	home := testsocket.Dir(t)
	dir := filepath.Join(home, "eggs", "fidelity")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	argv := []string{"egg", "run", "--session-id", "fidelity", "--agent=", "--kind", "command", "--outer-boundary", "--network", "*", "--agent-domains", "none", "--omit-browser-bridge", "--vte", "--cwd", home, "--command-arg", "/usr/bin/python3", "--command-arg", provider}
	for _, arg := range args {
		argv = append(argv, "--command-arg", arg)
	}
	cmd := exec.CommandContext(t.Context(), binary, argv...)
	cmd.Env = []string{"HOME=" + home, "WINGTHING_DIR=" + home, "PATH=/usr/bin:/bin", "TERM=xterm-256color", "LANG=C.UTF-8"}
	logs := newNativeMouseCapture()
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f := &fidelityEgg{home: home, socket: filepath.Join(dir, "egg.sock"), token: filepath.Join(dir, "egg.token"), cmd: cmd, done: make(chan error, 1), logs: logs}
	go func() { f.done <- cmd.Wait(); close(f.done) }()
	t.Cleanup(func() {
		ec, err := egg.Dial(f.socket, f.token)
		if err == nil {
			_ = ec.Kill(context.Background(), "fidelity")
			ec.Close()
		} else {
			_ = cmd.Process.Kill()
		}
		<-f.done
	})
	// An event capture rather than a socket polling interval.
	for !bytes.Contains(logs.bytes(), []byte("serving on ")) {
		select {
		case <-logs.changed:
		case err := <-f.done:
			t.Fatalf("egg startup: %v: %s", err, logs.bytes())
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
	return f
}
func (f *fidelityEgg) attach(t *testing.T, binary string, initial map[int]bool) *fidelityTerminal {
	cmd := exec.CommandContext(t.Context(), binary, "attach", "fidelity")
	cmd.Env = f.cmd.Env
	return startFidelityTerminal(t, cmd, initial)
}

func TestTerminalFidelityPTY(t *testing.T) {
	binary := fidelityBuild(t)
	cases := fidelityCases(t)
	provider, err := filepath.Abs("testdata/terminal_fidelity.py")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs("testdata/terminal_fidelity_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	evidence := os.Getenv("WT_FIDELITY_EVIDENCE")
	var direct []byte
	for _, attached := range []bool{false, true} {
		name := "direct"
		if attached {
			name = "attached"
		}
		t.Run(name, func(t *testing.T) {
			records := filepath.Join(t.TempDir(), "records.json")
			var h *fidelityTerminal
			var browser *nativeMouseCapture
			if attached {
				f := startFidelityEgg(t, binary, provider, records, fixture)
				ec, err := egg.Dial(f.socket, f.token)
				if err != nil {
					t.Fatal(err)
				}
				defer ec.Close()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				stream, err := ec.AttachSessionWithOptions(ctx, "fidelity", egg.AttachOptions{ReadOnly: true})
				if err != nil {
					t.Fatal(err)
				}
				browser = newNativeMouseCapture()
				done := make(chan struct{})
				go func() {
					defer close(done)
					for {
						msg, err := stream.Recv()
						if err != nil {
							return
						}
						_, _ = browser.Write(msg.GetOutput())
						if _, ok := msg.Payload.(*pb.SessionMsg_ExitCode); ok {
							return
						}
					}
				}()
				defer func() { cancel(); <-done }()
				h = f.attach(t, binary, nil)
			} else {
				cmd := exec.CommandContext(t.Context(), "/usr/bin/python3", provider, records, fixture)
				h = startFidelityTerminal(t, cmd, nil)
			}
			h.wait("FID_READY")
			h.send("g")
			for _, c := range cases {
				h.wait("WIRE:" + c.Name + "\r\n")
				if c.Name == "sigwinch" {
					if err := pty.Setsize(h.master, &pty.Winsize{Cols: 93, Rows: 31}); err != nil {
						t.Fatal(err)
					}
					h.wait("SIZE:93x31\r\n")
				}
				if !c.Query {
					input := c.Input
					if c.Name == "ctrl-b" && attached {
						input = "\x02\x02"
					}
					if c.Name == "large-paste" {
						payload := bytes.Repeat([]byte("wide-界\x02q"), 131072)[:1<<20]
						input = "\x1b[200~" + string(payload) + "\x1b[201~"
					}
					h.send(input)
				}
				h.wait("PASS:" + c.Name + "\r\n")
				wire := h.snapshot()
				begin := []byte("BEGIN:" + c.Name + "\r\n")
				end := []byte("WIRE:" + c.Name + "\r\n")
				start := bytes.Index(wire, begin) + len(begin)
				stop := bytes.Index(wire, end)
				if start < len(begin) || stop < start || string(wire[start:stop]) != c.Output {
					t.Fatalf("%s output changed: %q", c.Name, wire[start:stop])
				}
				t.Logf("%s %s output=%s input verified by provider", name, c.Name, hex.EncodeToString([]byte(c.Output)))
			}
			h.wait("FID_DONE\r\n")
			result, err := os.ReadFile(records)
			if err != nil {
				t.Fatal(err)
			}
			if attached {
				if !bytes.Equal(direct, result) {
					t.Fatalf("provider recordings differ:\ndirect %s\nattached %s", direct, result)
				}
				for !bytes.Contains(browser.bytes(), []byte("FID_DONE")) {
					select {
					case <-browser.changed:
					case <-t.Context().Done():
						t.Fatal(t.Context().Err())
					}
				}
				got := browser.bytes()
				if !bytes.Contains(got, []byte("\x1b[?1004h")) || bytes.Contains(got, []byte("\x1b[?1000h")) || !bytes.Contains(got, []byte("\x1b]7771;1\a")) {
					t.Fatalf("browser focus/mouse projection: %q", got)
				}
				fidelityWrite(t, evidence, "browser.ansi", got)
				h.send("\x02q")
			} else {
				direct = result
				h.send("q")
			}
			<-h.processDone
			if h.err != nil {
				t.Fatal(h.err)
			}
			fidelityWrite(t, evidence, name+".json", result)
			fidelityWrite(t, evidence, name+".ansi", h.snapshot())
		})
	}
	t.Run("cleanup", func(t *testing.T) { fidelityCleanup(t, binary, evidence) })
}

var fidelityCleanupStates = []map[int]bool{
	{25: true},
	{25: false, 1000: true, 1006: true, 1004: true, 2004: true, 1049: true},
	{25: true, 1002: true, 1004: true, 47: true},
}
var fidelityTrackedModes = []int{1, 9, 25, 47, 1047, 1049, 1000, 1001, 1002, 1003, 1004, 1005, 1006, 1007, 1015, 1016, 2004, 2026}

func (h *fidelityTerminal) assertModes(initial map[int]bool) {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, mode := range fidelityTrackedModes {
		want := initial[mode]
		if mode == 25 {
			if _, ok := initial[mode]; !ok {
				want = true
			}
		}
		if h.modes[mode] != want {
			h.t.Errorf("mode %d got %t want %t", mode, h.modes[mode], want)
		}
	}
}

func (h *fidelityTerminal) assertCleanup(initial map[int]bool, killed bool) {
	h.t.Helper()
	<-h.processDone
	// PTY EOF fences the independent guardian's output and termios restoration.
	slaveName := h.slave.Name()
	_ = h.slave.Close()
	<-h.readDone
	h.assertModes(initial)
	reopened, err := os.OpenFile(slaveName, os.O_RDWR, 0600)
	if err != nil {
		h.t.Fatal(err)
	}
	after, err := term.GetState(int(reopened.Fd()))
	reopened.Close()
	if err != nil {
		h.t.Fatal(err)
	}
	if !reflect.DeepEqual(h.before, after) {
		h.t.Error("terminal termios changed after attachment")
	}
	if !killed && h.err != nil {
		h.t.Fatal(h.err)
	}
}

func (h *fidelityTerminal) exerciseSavedModes(initial map[int]bool, typeahead bool) {
	h.t.Helper()
	h.wait("FID_READY")
	if !typeahead {
		h.send("g")
	}
	h.wait("ORIGINAL_SAVED\r\n")
	// Snapshot replay may set presentation modes before the provider starts.
	// A provider save must restore that current state, independently of the
	// outer state which assertCleanup checks after detachment.
	h.mu.Lock()
	saved := make(map[int]bool, len(h.modes))
	for mode, value := range h.modes {
		saved[mode] = value
	}
	h.mu.Unlock()
	h.send("s")
	h.wait("ORIGINAL_RESTORED\r\n")
	h.assertModes(saved)
	h.send("s")
	h.wait("MODES_CHANGED\r\n")
	changed := map[int]bool{25: false}
	for _, mode := range fidelityTrackedModes {
		if mode != 25 {
			changed[mode] = true
		}
	}
	h.assertModes(changed)
}

// Exercise empty parameters through the built CLI, including terminals which
// cannot report modes and therefore rely entirely on protected save slots.
func TestTerminalFidelityEmptyParameters(t *testing.T) {
	evidence := os.Getenv("WT_FIDELITY_EVIDENCE")
	if evidence != "" {
		evidence = filepath.Join(evidence, "empty-parameters")
	}
	fidelityCleanup(t, fidelityBuild(t), evidence, "--empty-params")
}

// Ignored DEL must not let provider saves overwrite the outer fallback slots.
func TestTerminalFidelityIgnoredDEL(t *testing.T) {
	evidence := os.Getenv("WT_FIDELITY_EVIDENCE")
	if evidence != "" {
		evidence = filepath.Join(evidence, "ignored-del")
	}
	fidelityCleanup(t, fidelityBuild(t), evidence, "--ignored-del")
}

// Oversized parameters must not expose protected modes in the same sequence.
func TestTerminalFidelityOversizedParameters(t *testing.T) {
	evidence := os.Getenv("WT_FIDELITY_EVIDENCE")
	if evidence != "" {
		evidence = filepath.Join(evidence, "oversized-parameters")
	}
	fidelityCleanup(t, fidelityBuild(t), evidence, "--oversized-params")
}

func fidelityCleanup(t *testing.T, binary, evidence string, providerArgs ...string) {
	provider, err := filepath.Abs("testdata/terminal_cleanup.py")
	if err != nil {
		t.Fatal(err)
	}
	for reporting := range 3 {
		for index, initial := range fidelityCleanupStates {
			for _, kill := range []bool{false, true} {
				name := fmt.Sprintf("state-%d-reporting-%d-kill-%t", index, reporting, kill)
				t.Run(name, func(t *testing.T) {
					f := startFidelityEgg(t, binary, provider, append([]string{"--exercise-saves"}, providerArgs...)...)
					cmd := exec.CommandContext(t.Context(), binary, "attach", "fidelity")
					cmd.Env = f.cmd.Env
					h := startFidelityTerminal(t, cmd, initial, func(h *fidelityTerminal) { h.reporting = reporting })
					h.exerciseSavedModes(initial, false)
					if kill {
						if err := h.cmd.Process.Kill(); err != nil {
							t.Fatal(err)
						}
					} else {
						h.send("\x02q")
					}
					h.assertCleanup(initial, kill)
					fidelityWrite(t, evidence, "cleanup-"+name+".ansi", h.snapshot())
				})
			}
		}
	}
}

// Use the real CLI dispatch for both SSH routes; the SSH executable is a local
// recording fixture, so these tests neither connect to a server nor need keys.
func TestTerminalFidelityRemoteHelper(t *testing.T) {
	route := os.Getenv("WT_FIDELITY_REMOTE_ROUTE")
	if route == "" {
		return
	}
	streams := remotepkg.ProcessIO()
	provider, err := filepath.Abs("testdata/terminal_cleanup.py")
	if err != nil {
		t.Fatal(err)
	}
	streams.SSHPath = writeFakeRemoteSSH(t, "exec /usr/bin/python3 "+remotepkg.ShellQuote(provider)+" --ssh --exercise-saves\n")
	args := []string{"--remote", "fixture-host", "attach", "--", "full-id"}
	if route == "configured" {
		dir := t.TempDir()
		t.Setenv("WINGTHING_DIR", dir)
		t.Setenv("WINGTHING_PREVIEW_DIR", "")
		binary, state := os.Getenv("WT_FIDELITY_REMOTE_BINARY"), os.Getenv("WT_FIDELITY_REMOTE_STATE")
		if binary == "" || state == "" {
			t.Fatal("configured route requires the built receiving CLI and persistent egg")
		}
		// The configured route uses framed stdio; execute its actual receiving
		// CLI so the provider still has a PTY and resize/detach stay observable.
		streams.SSHPath = writeFakeRemoteSSH(t, "for arg do command=$arg; done\nexec /bin/sh -c \"$command\"\n")
		if err := config.SaveRemotes(dir, map[string]config.Remote{"work": {SSHTarget: "fixture-host", WTBinary: binary, WingthingDir: state}}); err != nil {
			t.Fatal(err)
		}
		args = []string{"attach", "work:fidelity"}
	}
	if err := executeCLI(t.Context(), args, streams); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalFidelityRemoteCleanup(t *testing.T) {
	evidence := os.Getenv("WT_FIDELITY_EVIDENCE")
	binary := fidelityBuild(t)
	provider, err := filepath.Abs("testdata/terminal_cleanup.py")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"global", "configured"} {
		for reporting := range 3 {
			for index, initial := range fidelityCleanupStates {
				for _, kill := range []bool{false, true} {
					name := fmt.Sprintf("%s-state-%d-reporting-%d-kill-%t", route, index, reporting, kill)
					t.Run(name, func(t *testing.T) {
						cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestTerminalFidelityRemoteHelper$")
						cmd.Env = append(os.Environ(), "WT_FIDELITY_REMOTE_ROUTE="+route)
						if route == "configured" {
							f := startFidelityEgg(t, binary, provider, "--ssh", "--exercise-saves")
							cmd.Env = append(cmd.Env, "WT_FIDELITY_REMOTE_BINARY="+binary, "WT_FIDELITY_REMOTE_STATE="+f.home)
						}
						// Include typeahead in the negotiation read; SSH must receive it once.
						typeahead := index == 2
						h := startFidelityTerminal(t, cmd, initial, func(h *fidelityTerminal) {
							h.reporting = reporting
							if typeahead {
								h.queries["\x1b[6n"] = "\x1b[7;13Rg"
							}
						})
						h.exerciseSavedModes(initial, typeahead)
						if err := pty.Setsize(h.master, &pty.Winsize{Cols: 93, Rows: 31}); err != nil {
							t.Fatal(err)
						}
						h.wait("SIZE:93x31\r\n")
						if kill {
							if err := h.cmd.Process.Kill(); err != nil {
								t.Fatal(err)
							}
						} else {
							h.send("\x02q")
						}
						h.assertCleanup(initial, kill)
						fidelityWrite(t, evidence, "ssh-cleanup-"+name+".ansi", h.snapshot())
					})
				}
			}
		}
	}
}
