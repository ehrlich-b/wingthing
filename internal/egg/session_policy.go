package egg

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/fsutil"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

// SessionPolicy is a content-free copy of the prepared runtime authority. It
// never includes provider arguments, environment values or credential contents.
// A saved policy is a review aid, not independent proof of confinement.
type SessionPolicy struct {
	Version      int             `json:"version"`
	SessionID    string          `json:"session_id"`
	Workspace    string          `json:"workspace"`
	ProviderHome string          `json:"provider_home"`
	Sandboxed    bool            `json:"sandboxed"`
	Source       string          `json:"source,omitempty"`
	Mounts       []sandbox.Mount `json:"mounts"`
	Denied       []string        `json:"denied"`
	DeniedWrite  []string        `json:"denied_write"`
	Network      string          `json:"network"`
	NetworkMode  string          `json:"network_mode"`
	Domains      []string        `json:"domains"`
	LocalPorts   []int           `json:"local_ports"`
}

func SaveSessionPolicy(dir string, rc RunConfig, launch PreparedLaunch) error {
	p := SessionPolicy{Version: 1, SessionID: filepath.Base(dir), Workspace: rc.CWD,
		ProviderHome: launch.Sandbox.UserHome, Sandboxed: launch.Sandboxed,
		Mounts: launch.Sandbox.Mounts, Denied: launch.Sandbox.Deny, DeniedWrite: launch.Sandbox.DenyWrite,
		Network: launch.Sandbox.NetworkNeed.String(), NetworkMode: launch.Sandbox.NetworkMode,
		Domains: launch.Sandbox.Domains, LocalPorts: launch.Sandbox.LocalPorts}
	if rc.LaunchPolicy != nil {
		p.Source = rc.LaunchPolicy.Path
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(data) > 64<<10 {
		return errors.New("session policy exceeds 64 KiB")
	}
	return atomicWritePrivate(filepath.Join(dir, "session-policy.json"), data)
}

// ReadSessionPolicy accepts only bounded regular receipts for this exact egg.
// Missing/invalid receipts remain unknown; never re-resolve today's egg.yaml
// and pretend it describes an already admitted process.
func ReadSessionPolicy(dir string) *SessionPolicy {
	path := filepath.Join(dir, "session-policy.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	f, err := fsutil.OpenBoundedRegular(path, 64<<10)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil
	}
	var p SessionPolicy
	if json.Unmarshal(data, &p) != nil || p.Version != 1 || p.SessionID != filepath.Base(dir) {
		return nil
	}
	return &p
}
