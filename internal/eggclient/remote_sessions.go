package eggclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"sort"
	"strings"
	"sync"

	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

const (
	RemoteSessionContractVersion = "v1"
	RemoteSessionTimeout         = 3 * time.Second
	remoteSessionStdoutLimit     = 4 << 20
	remoteSessionStderrLimit     = 64 << 10
)

// Only the internal inventory response is versioned. Public session ps --json
// remains an array, with machine and error fields added to its rows.
type RemoteSessionInventory struct {
	Version         string         `json:"version"`
	ContractVersion string         `json:"contract_version"`
	Sessions        []LocalSession `json:"sessions"`
}

type MachineSession struct {
	LocalSession
	Machine string `json:"machine"`
	Error   string `json:"error,omitempty"`
}

type remoteSessionBuffer struct {
	buffer   bytes.Buffer
	limit    int
	stream   string
	cancel   context.CancelFunc
	overflow error
}

func (b *remoteSessionBuffer) Write(p []byte) (int, error) {
	if b.overflow != nil {
		return 0, b.overflow
	}
	remaining := b.limit - b.buffer.Len()
	if len(p) > remaining {
		n, _ := b.buffer.Write(p[:remaining])
		b.overflow = fmt.Errorf("%s exceeded %d-byte limit", b.stream, b.limit)
		b.cancel()
		return n, b.overflow
	}
	return b.buffer.Write(p)
}

func QueryRemoteSessions(localVersion string, ctx context.Context, name string, remote config.Remote, streams remotepkg.IO) ([]LocalSession, error) {
	ctx, cancel := context.WithTimeout(ctx, RemoteSessionTimeout)
	defer cancel()
	invocation := remotepkg.ProfileInvocation(remote, nil)
	invocation.NonInteractive = true
	stdout := remoteSessionBuffer{limit: remoteSessionStdoutLimit, stream: "stdout", cancel: cancel}
	stderr := remoteSessionBuffer{limit: remoteSessionStderrLimit, stream: "stderr", cancel: cancel}
	streams.In, streams.Out, streams.ErrOut = nil, &stdout, &stderr
	run := func() error {
		err := remotepkg.RunRemoteInvocation(ctx, invocation, streams)
		// Cancellation also returns context.Canceled; retain the overflow cause.
		if stdout.overflow != nil {
			return stdout.overflow
		}
		if stderr.overflow != nil {
			return stderr.overflow
		}
		return err
	}
	// A compatible response already carries its version. Only old/malformed
	// receivers need a separate version query for an actionable diagnostic.
	mismatch := func(remoteVersion, contract, detail string) error {
		if remoteVersion == "" {
			remoteVersion = "unknown"
			if ctx.Err() == nil && stdout.overflow == nil && stderr.overflow == nil {
				stdout.buffer.Reset()
				stderr.buffer.Reset()
				invocation.Args = []string{"--version"}
				if err := run(); err != nil {
					return remoteQueryError(name, err, stderr.buffer.String())
				}
				if value := strings.TrimSpace(stdout.buffer.String()); value != "" {
					remoteVersion = value
				}
			}
		}
		return remoteInventoryMismatch(localVersion, name, remoteVersion, contract, detail)
	}
	// The receiver skips its registry entirely, so this never recursively fans
	// out to that machine's configured remotes (even a cycle back to this one).
	invocation.Args = []string{"session", "ps", "--json", "--remote-inventory"}
	err := run()
	if err != nil {
		var exitErr *cmdutil.CommandExitError
		if !errors.As(err, &exitErr) || exitErr.Code == 255 {
			return nil, remoteQueryError(name, err, stderr.buffer.String())
		}
		diagnostic := strings.TrimSpace(stderr.buffer.String())
		if strings.Contains(diagnostic, "unknown flag") || strings.Contains(diagnostic, "unknown command") || strings.Contains(diagnostic, "flag provided but not defined") {
			return nil, mismatch("", "unsupported", diagnostic)
		}
		return nil, remoteQueryError(name, err, diagnostic)
	}
	var inventory RemoteSessionInventory
	if err := json.Unmarshal(stdout.buffer.Bytes(), &inventory); err != nil {
		return nil, mismatch("", "unsupported", "invalid session ps --json inventory")
	}
	remoteVersion := ""
	if inventory.Version != "" {
		remoteVersion = config.BinaryName() + " version " + inventory.Version
	}
	if inventory.ContractVersion != RemoteSessionContractVersion || inventory.Version == "" || inventory.Sessions == nil {
		contract := inventory.ContractVersion
		if contract == "" {
			contract = "unsupported"
		}
		return nil, mismatch(remoteVersion, contract, "session inventory contract is incompatible")
	}
	return inventory.Sessions, nil
}

func remoteQueryError(name string, err error, diagnostic string) error {
	if diagnostic = strings.TrimSpace(diagnostic); diagnostic != "" {
		return fmt.Errorf("remote %q: %w (%s)", name, err, diagnostic)
	}
	return fmt.Errorf("remote %q: %w", name, err)
}

func remoteInventoryMismatch(localVersion string, name, remoteVersion, contract, detail string) error {
	return fmt.Errorf("remote %q version mismatch: local wt %s (session contract %s), remote %s (session contract %s): %s; upgrade the remote wt to a compatible version",
		name, localVersion, RemoteSessionContractVersion, remoteVersion, contract, detail)
}

func DiscoverMachineSessions(localVersion string, ctx context.Context, cfg *config.Config, streams remotepkg.IO) ([]MachineSession, error) {
	remotes, err := config.LoadRemotes(cfg.Dir)
	if err != nil {
		return nil, err
	}
	local, err := DiscoverActiveSessions(ctx, cfg)
	if err != nil {
		return nil, err
	}
	rows := make([]MachineSession, 0, len(local))
	for _, session := range local {
		rows = append(rows, MachineSession{LocalSession: session, Machine: "local"})
	}
	names := SortedRemoteNames(remotes)
	results := make([][]MachineSession, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sessions, err := QueryRemoteSessions(localVersion, ctx, name, remotes[name], streams)
			if err != nil {
				results[i] = []MachineSession{{Machine: name, Error: err.Error()}}
				return
			}
			for _, session := range sessions {
				results[i] = append(results[i], MachineSession{LocalSession: session, Machine: name})
			}
		}()
	}
	wg.Wait()
	for _, result := range results {
		rows = append(rows, result...)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Pinned && !rows[j].Pinned })
	return rows, nil
}

// ValidateRemoteSession refuses cached names, abbreviated IDs, and inventories
// without an active process identity. It never dispatches a mutation or retries.
func ValidateRemoteSession(ctx context.Context, name string, remote config.Remote, id string, streams remotepkg.IO) error {
	sessions, err := QueryRemoteSessions("dashboard", ctx, name, remote, streams)
	if err != nil {
		return fmt.Errorf("cannot validate remote session %s:%s; refresh before acting: %w", name, id, err)
	}
	matches := 0
	fresh := false
	for _, session := range sessions {
		if session.ID == id {
			matches++
			fresh = session.PID > 0
		}
	}
	if id == "" || matches != 1 || !fresh {
		return fmt.Errorf("cannot validate remote session %s:%s: no unique fresh process identity; refresh before acting", name, id)
	}
	return nil
}
