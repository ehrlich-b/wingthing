package egg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func ptyInputFlags(t *testing.T, file *os.File) int {
	t.Helper()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags int
	var flagErr error
	if err = raw.Control(func(fd uintptr) { flags, flagErr = unix.FcntlInt(fd, unix.F_GETFL, 0) }); err != nil || flagErr != nil {
		t.Fatalf("PTY flags: %v %v", err, flagErr)
	}
	return flags
}

func TestSessionPTYInputMode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		rc          RunConfig
		exclusive   bool
		codexRun    bool
		cancellable bool
	}{
		{name: "command", rc: RunConfig{Kind: "command", Command: []string{"fixture"}}},
		{name: "claude-command", rc: RunConfig{Kind: "command", Agent: "claude", Command: []string{"fixture"}}},
		{name: "unsupported-provider", rc: RunConfig{Kind: "agent", Agent: "gemini"}},
		{name: "hookless-codex", rc: RunConfig{Kind: "agent", Agent: "codex"}},
		{name: "exclusive-command", rc: RunConfig{Kind: "command", Command: []string{"fixture"}}, exclusive: true, cancellable: true},
		{name: "claude-run", rc: RunConfig{Kind: "agent", Agent: "claude"}, cancellable: true},
		{name: "codex-run", rc: RunConfig{Kind: "agent", Agent: "codex"}, codexRun: true, cancellable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			server := &Server{exclusiveInput: tc.exclusive}
			mode, err := server.prepareSessionPTYInput(master, tc.rc, tc.codexRun)
			if err != nil {
				t.Fatal(err)
			}
			if nonblocking := ptyInputFlags(t, master)&unix.O_NONBLOCK != 0; mode != tc.cancellable || nonblocking != tc.cancellable {
				t.Fatalf("PTY mode: cancellable=%t nonblocking=%t, want %t", mode, nonblocking, tc.cancellable)
			}
		})
	}
}

func TestNativeRunPTYResizeKeepsInputCancellable(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if _, err := term.MakeRaw(int(slave.Fd())); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	lifecycleWrite(t, filepath.Join(dir, "egg.meta"), "cols=80\nrows=24\n")
	server := &Server{dir: dir}
	mode, err := server.prepareSessionPTYInput(master, RunConfig{Kind: "agent", Agent: "claude"}, false)
	if err != nil {
		t.Fatal(err)
	}
	sess := &Session{ptmx: master, nonblockInput: mode, vtermCh: make(chan vtermMsg, 1)}
	if err := server.resizeSession(sess, 30, 90); err != nil {
		t.Fatal(err)
	}
	if ptyInputFlags(t, master)&unix.O_NONBLOCK == 0 {
		t.Fatal("resize made native run input blocking")
	}
	// Fill the raw slave's input queue without sleeps or a timing assertion.
	raw, err := master.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(strings.Repeat("x", 4096))
	for {
		var writeErr error
		if err = raw.Control(func(fd uintptr) { _, writeErr = unix.Write(int(fd), data) }); err != nil {
			t.Fatal(err)
		}
		if errors.Is(writeErr, unix.EAGAIN) {
			break
		}
		if writeErr != nil && !errors.Is(writeErr, unix.EINTR) {
			t.Fatal(writeErr)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	started, finished := make(chan struct{}), make(chan error, 1)
	go func() { close(started); finished <- writePTYInput(ctx, master, []byte("run prompt")) }()
	<-started
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("run input ignored cancellation: %v", err)
	}
}
