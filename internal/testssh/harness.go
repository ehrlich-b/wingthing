// Package testssh supplies a deterministic subprocess fixture for socket tests.
package testssh

import (
	_ "embed"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
)

//go:embed ssh.py
var fixtureScript []byte

type Forward struct {
	Host   string   `json:"host"`
	PID    int      `json:"pid"`
	Args   []string `json:"args"`
	Socket string   `json:"socket"`
	conn   net.Conn
}

func (f *Forward) Drop() { _, _ = f.conn.Write([]byte{1}); _ = f.conn.Close() }

type Harness struct {
	Root    string
	SSHPath string
	Started chan *Forward
	mu      sync.Mutex
	conns   []net.Conn
}

func New(t *testing.T) *Harness {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	if _, err := os.Stat(filepath.Join(repo, "go.mod")); err != nil {
		repo, err = os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
	}
	scratch := filepath.Join(repo, ".scratch")
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(scratch, "ssh-")
	if err != nil {
		t.Fatal(err)
	}
	h := &Harness{Root: root, SSHPath: filepath.Join(root, "ssh"), Started: make(chan *Forward, 64)}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(root, "ssh.py")
	if err := os.WriteFile(fixturePath, fixtureScript, 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexec '" + python + "' '" + fixturePath + "' \"$@\"\n"
	if err := os.WriteFile(h.SSHPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(root, "supervisor.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_FAKE_SSH_ROOT", root)
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			h.mu.Lock()
			h.conns = append(h.conns, c)
			h.mu.Unlock()
			go func() {
				var f Forward
				if json.NewDecoder(c).Decode(&f) == nil {
					f.conn = c
					h.Started <- &f
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		h.mu.Lock()
		for _, c := range h.conns {
			_ = c.Close()
		}
		h.mu.Unlock()
		_ = os.RemoveAll(root)
	})
	return h
}
func (h *Harness) Host(t *testing.T, name string, m sshcontrol.Metadata) {
	t.Helper()
	wire, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(h.Root, name+".json"), wire, 0600); err != nil {
		t.Fatal(err)
	}
}
func (h *Harness) Offline(t *testing.T, name string, offline bool) {
	t.Helper()
	path := filepath.Join(h.Root, name+".down")
	if offline {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
}
