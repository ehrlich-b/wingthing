package main

// Linux fixture adaptation of TestNorthstarPTYNativeMouse. All journey assertions are retained;
// original Northstar tests are unchanged. See test/storyboard/README.md.
import (
	"bytes"
	"context"
	"fmt"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func storyboardNativeMouse(t *testing.T) {
	f := northstarNativeMouseFixture(t) // creates a real parent and symlinked TMPDIR
	storyboardStateEvidence(t, f, f.local)
	binary := storyboardBinary(t, f)
	// All actual children inherit GOTMPDIR too; the build helper's clone-local
	// caches and this alias keep fixture work out of ambient temporary roots.
	f.env = append(f.env, "GOTMPDIR="+f.tmp)
	resolved, err := filepath.EvalSymlinks(f.tmp)
	if err != nil || resolved != filepath.Join(f.root, "tmp-real") {
		t.Fatalf("fixture TMPDIR alias: %s %v", resolved, err)
	}
	if !strings.HasPrefix(binary, f.tmp+string(filepath.Separator)) {
		t.Fatal("built CLI did not launch through alias")
	}
	provider := filepath.Join(f.work, "native-mouse.py")
	// Linux read-only bind mounts return EROFS; the original Darwin fixture
	// catches EACCES/EPERM through PermissionError. Accept only those three
	// protection errors, preserving the failed-write probe and every assertion.
	script := "import errno\n" + strings.Replace(nativeMouseProvider,
		"except PermissionError: emit('PROTECTED_OK\\r\\n')",
		"except OSError as error:\n    if error.errno not in (errno.EPERM, errno.EACCES, errno.EROFS): raise\n    emit('PROTECTED_OK\\r\\n')", 1)
	northstarWrite(t, provider, script, 0600)
	dir := filepath.Join(f.local, "eggs", "mouse")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	socket := northstarSocket(t, filepath.Join(dir, "egg.sock"))
	args := []string{"egg", "run", "--session-id", "mouse", "--agent=", "--kind", "command", "--command-arg", "/usr/bin/python3", "--command-arg", provider, "--cwd", f.work, "--network", "none", "--omit-browser-bridge", "--env", "TMPDIR=" + f.tmp, "--env", "PYTHONDONTWRITEBYTECODE=1"}
	// Keep the established policy, including protected HOME and ancestor guards.
	for _, rule := range storyboardLinuxPolicy(f).FS {
		mode, path, ok := strings.Cut(rule, ":")
		if !ok {
			t.Fatalf("invalid fixture FS rule %q", rule)
		}
		if strings.HasPrefix(path, "~/") {
			path = filepath.Join(f.home, strings.TrimPrefix(path, "~/"))
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(f.work, path)
		}
		args = append(args, "--fs", mode+":"+filepath.Clean(path))
	}
	args = append(args, "--fs", "rw:"+f.work, "--fs", "rw:"+f.tmp)
	command := exec.CommandContext(t.Context(), binary, args...)
	command.Env, command.Dir = f.env, f.work
	logs := newNativeMouseCapture()
	command.Stdout, command.Stderr = logs, logs
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait(); close(done) }()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
				<-done
			}
		}
		northstarWrite(t, filepath.Join(f.evidence, "native-mouse-egg.log"), string(logs.bytes()), 0600)
	})
	logs.wait(t, "serving on ")
	ec, err := egg.Dial(socket, filepath.Join(dir, "egg.token"))
	if err != nil {
		t.Fatal(err)
	}
	defer ec.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	legacy, err := ec.AttachSessionWithOptions(ctx, "mouse", egg.AttachOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	browser := newNativeMouseCapture()
	readerDone := make(chan error, 1)
	go func() {
		for {
			msg, err := legacy.Recv()
			if err != nil {
				readerDone <- err
				return
			}
			if _, ok := msg.Payload.(*pb.SessionMsg_ExitCode); ok {
				readerDone <- nil
				return
			}
			_, _ = browser.Write(msg.GetOutput())
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-readerDone
		northstarWrite(t, filepath.Join(f.evidence, "native-mouse-legacy.ansi"), string(browser.bytes()), 0600)
	})
	browser.wait(t, "MOUSE_READY")
	h := northstarStartPTY(t, f, binary, 80, 24, "attach", "mouse")
	initial := h.waitNativeBytes("MOUSE_READY")
	if !bytes.Contains(initial, []byte("PROTECTED_OK")) || bytes.Contains(initial, []byte("PROTECTION_BROKEN")) {
		t.Fatal("native provider wrote protected mailbox")
	}
	for i := range 40 {
		if !bytes.Contains(initial, []byte(fmt.Sprintf("HISTORY-%02d", i))) {
			t.Fatalf("raw history line %d disappeared", i)
		}
	}
	h.send("e")
	h.waitNativeBytes("\x1b[?10") // provider waits for f: two different kernel PTY reads
	h.expect("split enable / alternate screen", h.send("f"), "ALT_READY")
	native := h.waitNativeBytes("\x1b[?1000h\x1b[?1006h")
	if !bytes.Contains(native, []byte("\x1b[?1049h")) {
		t.Fatal("native alternate screen missing")
	}
	browser.wait(t, "ALT_READY")
	if bytes.Contains(browser.bytes(), []byte("\x1b[?1000h")) || bytes.Contains(browser.bytes(), []byte("\x1b[?1006h")) || !bytes.Contains(browser.bytes(), []byte("\x1b]7771;1\a")) {
		t.Fatalf("browser mouse contract changed: %q", browser.bytes())
	}
	h.expect("native wheel up reaches provider", h.send("\x1b[<64;5;3M"), "WHEEL_UP_SGR")
	h.expect("native wheel down reaches provider", h.send("\x1b[<65;5;3M"), "WHEEL_DOWN_SGR")
	browser.wait(t, "WHEEL_DOWN_SGR")
	h.expect("provider resize", h.resize(40, 12), "SIZE:40x12")
	h.exit("\x02q")
	// Reconnect while enabled after resizing: native mode and alternate grid survive.
	h2 := northstarStartPTY(t, f, binary, 40, 12, "attach", "mouse")
	h2.expect("reconnect enabled / alternate grid", 0, "WHEEL_DOWN_SGR", "SIZE:40x12")
	h2.waitNativeBytes("\x1b[?1000h\x1b[?1006h")
	rev := h2.send("d")
	h2.waitNativePartial("\x1b[?100", rev)
	h2.expect("split disable / normal screen", h2.send("D"), "NORMAL_HISTORY_READY")
	browser.wait(t, "NORMAL_HISTORY_READY")
	h2.exit("\x02q")
	// Old enables in replay must not win over the later split disable.
	h3 := northstarStartPTY(t, f, binary, 40, 12, "attach", "mouse")
	h3.expect("reconnect disabled / normal history", 0, "NORMAL_HISTORY_READY")
	snapshot := h3.waitNativeBytes("NORMAL_HISTORY_READY")
	for _, mode := range []string{"1000", "1006", "1049"} {
		enabled := bytes.LastIndex(snapshot, []byte("\x1b[?"+mode+"h"))
		disabled := bytes.LastIndex(snapshot, []byte("\x1b[?"+mode+"l"))
		if enabled < 0 || disabled < enabled {
			t.Fatalf("reconnect restored stale mode %s: %q", mode, snapshot)
		}
	}
	for i := range 40 {
		if !bytes.Contains(snapshot, []byte(fmt.Sprintf("HISTORY-%02d", i))) {
			t.Fatalf("reconnect lost normal history %d", i)
		}
	}
	if _, err := os.Stat(filepath.Join(f.home, "mailbox", "forbidden")); !os.IsNotExist(err) {
		t.Fatalf("provider wrote protected mailbox: %v", err)
	}
	if !bytes.Contains(initial, []byte("PROTECTED_OK")) || !bytes.Contains(initial, []byte("TMP_ALIAS:"+f.tmp)) {
		t.Fatal("provider boundary or TMPDIR alias was not exercised")
	}
	h3.exit("\x02q")
	// Close the observer RPC before graceful shutdown, then stop only the
	// owned fake-provider session and join it.
	cancel()
	if err := ec.Kill(t.Context(), "mouse"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("owned egg did not join")
	}
	t.Log("kernel PTY bytes and SGR wheel input passed; physical trackpad/selection modifier remains a GUI canary")
}
