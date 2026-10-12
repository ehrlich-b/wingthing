package egg

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ehrlich-b/wingthing/internal/fsutil"
)

const SessionAuthorityFile = "session.authority.json"

// SessionAuthority is a display-only projection of the runtime's prepared
// policy. It never carries environment values, arguments, credentials or output
// and must never be used to authorize a launch or resume. Native confinement is
// a separate assertion from this saved policy.
type SessionAuthority struct {
	Version         int              `json:"version"`
	Platform        string           `json:"platform"`
	Workspace       string           `json:"workspace"`
	ProviderHome    string           `json:"provider_home"`
	Sandboxed       bool             `json:"sandboxed"`
	Mounts          []AuthorityMount `json:"mounts,omitempty"`
	ProviderDirs    []string         `json:"provider_dirs,omitempty"`
	Denied          []string         `json:"denied,omitempty"`
	WriteDenied     []string         `json:"write_denied,omitempty"`
	RenameDenied    []string         `json:"rename_denied,omitempty"`
	ProtectedWrites []string         `json:"protected_writes,omitempty"`
	Network         string           `json:"network"`
	NetworkMode     string           `json:"network_mode,omitempty"`
	Domains         []string         `json:"domains,omitempty"`
	LocalPorts      []int            `json:"local_ports,omitempty"`
}

type AuthorityMount struct {
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only"`
	Prefix   bool   `json:"prefix,omitempty"`
}

func sessionAuthority(rc RunConfig, prepared PreparedLaunch, home string) SessionAuthority {
	spec := prepared.Sandbox
	a := SessionAuthority{Version: 1, Platform: runtime.GOOS, Workspace: rc.CWD,
		ProviderHome: home, Sandboxed: prepared.Sandboxed}
	if !a.Sandboxed {
		a.Network = "unrestricted"
		return a
	}
	a.Network, a.NetworkMode = spec.NetworkNeed.String(), spec.NetworkMode
	a.Domains, a.LocalPorts = spec.Domains, spec.LocalPorts
	a.ProviderDirs = prepared.ProfileDirs
	a.Denied, a.WriteDenied, a.RenameDenied = spec.Deny, spec.DenyWrite, spec.DenyRename
	a.ProtectedWrites = spec.ProtectedWriteTargets
	for _, m := range spec.Mounts {
		a.Mounts = append(a.Mounts, AuthorityMount{m.Source, m.ReadOnly, m.UseRegex})
	}
	return a
}

func writeSessionAuthority(dir string, a SessionAuthority) error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return atomicWritePrivate(filepath.Join(dir, SessionAuthorityFile), data)
}

// Missing, malformed, linked or oversized evidence is unknown. Inventory must
// not discover today's egg.yaml and present it as a running session's policy.
func ReadSessionAuthority(dir string) *SessionAuthority {
	path := filepath.Join(dir, SessionAuthorityFile)
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil
	}
	f, err := fsutil.OpenBoundedRegular(path, 64<<10)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	var a SessionAuthority
	if err != nil || len(data) > 64<<10 || json.Unmarshal(data, &a) != nil || a.Version != 1 ||
		(a.Platform != "linux" && a.Platform != "darwin") || a.Workspace == "" || a.Network == "" {
		return nil
	}
	return &a
}
