// Package sshcontrol connects to an already running wing using authenticated
// OpenSSH stream-local forwarding. It never provisions or starts remote software.
package sshcontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/fsnotify/fsnotify"
)

type Metadata struct {
	WingID        string `json:"wing_id"`
	Version       string `json:"version"`
	WingthingDir  string `json:"wingthing_dir"`
	ControlSocket string `json:"control_socket"`
}

// InspectLocal performs only a handshake against the selected, existing wing.
func InspectLocal(ctx context.Context, dir, client string) (Metadata, error) {
	if strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return Metadata{}, err
		}
		dir = filepath.Join(home, dir[2:])
	}
	if !filepath.IsAbs(dir) {
		return Metadata{}, errors.New("wingthing-dir must be absolute or start with ~/")
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return Metadata{}, err
	}
	socket, err := controlsocket.Path(canonical)
	if err != nil {
		return Metadata{}, err
	}
	c, err := controlsocket.DialPath(ctx, socket, controlsocket.Hello{Client: client})
	if err != nil {
		return Metadata{}, err
	}
	defer c.Close()
	return Metadata{WingID: c.Welcome.WingID, Version: c.Welcome.Version, WingthingDir: canonical, ControlSocket: socket}, nil
}

type Transport struct {
	// SSHPath is supplied only by fixtures; the CLI uses the user's OpenSSH.
	SSHPath   string
	SocketDir string
	Timeout   time.Duration
}

func (t Transport) sshPath() string {
	if t.SSHPath != "" {
		return t.SSHPath
	}
	return "ssh"
}
func (t Transport) options() []string {
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	seconds := max(1, int(timeout.Seconds()))
	return []string{"-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UpdateHostKeys=no", "-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2", "-o", "ConnectTimeout=" + strconv.Itoa(seconds)}
}
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// cappedBuffer keeps a failed/malicious peer from consuming unbounded memory.
type cappedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.b.Len() < 8192 {
		_, _ = b.b.Write(p[:min(len(p), 8192-b.b.Len())])
	}
	return len(p), nil
}
func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(b.b.String())
}
func (t Transport) Inspect(ctx context.Context, host, dir, binary, client string) (config.Remote, error) {
	if err := config.ValidateSSHTarget(host); err != nil {
		return config.Remote{}, err
	}
	r := config.Remote{SSHTarget: host, WTBinary: binary}
	if err := config.ValidateWTBinary(r.Binary()); err != nil {
		return config.Remote{}, err
	}
	if dir != "" && !(strings.HasPrefix(dir, "/") || strings.HasPrefix(dir, "~/")) {
		return config.Remote{}, errors.New("wingthing-dir must be absolute or start with ~/")
	}
	if strings.ContainsAny(dir, "\x00\r\n") {
		return config.Remote{}, errors.New("invalid wingthing-dir")
	}
	args := append(t.options(), "--", host, quote(r.Binary())+" mcp inspect")
	args[len(args)-1] += " --client " + quote(client)
	if dir != "" {
		args[len(args)-1] += " --wingthing-dir " + quote(dir)
	}
	cmd := exec.CommandContext(ctx, t.sshPath(), args...)
	cmd.WaitDelay = time.Second
	var stdout, stderr cappedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		diagnostic := stderr.String()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() != 255 &&
			(strings.Contains(diagnostic, "unknown flag") || strings.Contains(diagnostic, "unknown command") || strings.Contains(diagnostic, "flag provided but not defined")) {
			return config.Remote{}, fmt.Errorf("verify remote wing: remote wt is too old for this build; select a compatible binary with --wt-binary PATH: %w: %s", err, diagnostic)
		}
		return config.Remote{}, fmt.Errorf("verify remote wing: %w: %s", err, diagnostic)
	}
	var m Metadata
	d := json.NewDecoder(strings.NewReader(stdout.String()))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return config.Remote{}, fmt.Errorf("remote metadata: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return config.Remote{}, errors.New("remote metadata must contain one object")
	}
	if m.Version != control.ContractVersion {
		return config.Remote{}, errors.New("incompatible remote control version; upgrade wt and restart the remote wing")
	}
	r.WingthingDir, r.WingID, r.ControlSocket, r.ControlVersion = m.WingthingDir, m.WingID, m.ControlSocket, m.Version
	// Reuse registry validation before trusting any remote-supplied path.
	if m.WingID == "" || !strings.HasPrefix(m.WingthingDir, "/") || !strings.HasPrefix(m.ControlSocket, "/") || strings.ContainsAny(m.WingthingDir+m.ControlSocket+m.WingID, "\x00\r\n") || strings.ContainsAny(m.ControlSocket+m.WingID, ":") {
		return config.Remote{}, errors.New("invalid remote wing metadata")
	}
	return r, nil
}

type Connection struct {
	Client *controlsocket.Client
	cmd    *exec.Cmd
	exited chan struct{}
	done   chan struct{}
	stderr *cappedBuffer
	dir    string
	once   sync.Once
}

func (c *Connection) Done() <-chan struct{} { return c.done }
func (c *Connection) Close() error {
	c.once.Do(func() {
		if c.Client != nil {
			_ = c.Client.Close()
		}
		_ = c.cmd.Process.Kill()
		<-c.exited
		_ = os.RemoveAll(c.dir)
	})
	return nil
}
func (t Transport) Dial(ctx context.Context, remote config.Remote, hello controlsocket.Hello) (*Connection, error) {
	if remote.WingID == "" || remote.ControlSocket == "" || remote.ControlVersion != control.ContractVersion {
		return nil, errors.New("remote is not verified; run wt mcp connect add after removing its old entry")
	}
	if err := config.ValidateSSHTarget(remote.SSHTarget); err != nil {
		return nil, err
	}
	// A private directory prevents another user from replacing the local forward.
	socketDir := t.SocketDir
	if socketDir == "" {
		socketDir = "/tmp"
	}
	prefix, socketName := "wt-ssh-", "control.sock"
	if t.SocketDir != "" {
		prefix, socketName = "s", "c"
	}
	dir, err := os.MkdirTemp(socketDir, prefix)
	if err != nil {
		return nil, err
	}
	local := filepath.Join(dir, socketName)
	if len(local) > 103 {
		_ = os.RemoveAll(dir)
		return nil, errors.New("SSH socket directory is too long; select a shorter private state path")
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	defer watcher.Close()
	if err = watcher.Add(dir); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	args := append(t.options(), "-o", "StreamLocalBindUnlink=no", "-N", "-L", local+":"+remote.ControlSocket, "--", remote.SSHTarget)
	// The caller's connection-attempt context does not own an accepted transport.
	cmd := exec.Command(t.sshPath(), args...)
	cmd.WaitDelay = time.Second
	c := &Connection{cmd: cmd, exited: make(chan struct{}), done: make(chan struct{}), stderr: &cappedBuffer{}, dir: dir}
	cmd.Stderr = c.stderr
	if err = cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("start SSH forward: %w", err)
	}
	go func() { _ = cmd.Wait(); close(c.exited) }()
	success := false
	defer func() {
		if !success {
			_ = c.Close()
		}
	}()
	// Watch before spawn; creation is the barrier, never a timing guess.
	for {
		if info, statErr := os.Lstat(local); statErr == nil {
			if info.Mode()&os.ModeSocket == 0 {
				return nil, errors.New("SSH forward did not create a socket")
			}
			if err = os.Chmod(local, 0600); err != nil {
				return nil, err
			}
			break
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, statErr
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.exited:
			return nil, fmt.Errorf("SSH forward closed: %s", c.stderr.String())
		case _, ok := <-watcher.Events:
			if !ok {
				return nil, errors.New("SSH socket watcher closed")
			}
		case err := <-watcher.Errors:
			return nil, fmt.Errorf("SSH socket watcher: %w", err)
		}
	}
	hello.WingID = remote.WingID
	client, err := controlsocket.DialPath(ctx, local, hello)
	if err != nil {
		return nil, fmt.Errorf("verify forwarded wing %s: %w", remote.WingID, err)
	}
	c.Client = client
	go func() {
		select {
		case <-c.exited:
			_ = client.Close()
		case <-client.Done():
		}
		close(c.done)
	}()
	success = true
	return c, nil
}
