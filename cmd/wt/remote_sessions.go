package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
)

const (
	remoteSessionContractVersion = "v1"
	remoteSessionTimeout         = 3 * time.Second
)

// Only the internal inventory response is versioned. Public session ps --json
// remains an array, with machine and error fields added to its rows.
type remoteSessionInventory struct {
	Version         string         `json:"version"`
	ContractVersion string         `json:"contract_version"`
	Sessions        []localSession `json:"sessions"`
}

type machineSession struct {
	localSession
	Machine string `json:"machine"`
	Error   string `json:"error,omitempty"`
}

type remoteIOContextKey struct{}

func remoteStreams(ctx context.Context) remoteIO {
	if streams, ok := ctx.Value(remoteIOContextKey{}).(remoteIO); ok {
		return streams
	}
	return remoteProcessIO()
}

func queryRemoteSessions(ctx context.Context, name string, remote config.Remote, streams remoteIO) ([]localSession, error) {
	ctx, cancel := context.WithTimeout(ctx, remoteSessionTimeout)
	defer cancel()
	invocation := remoteInvocation{target: remote.SSHTarget, binary: config.BinaryName(), state: remote.WingthingDir}
	var stdout, stderr bytes.Buffer
	streams.in, streams.out, streams.errOut = nil, &stdout, &stderr
	invocation.args = []string{"--version"}
	if err := runRemoteInvocation(ctx, invocation, streams); err != nil {
		return nil, remoteQueryError(name, err, stderr.String())
	}
	remoteVersion := strings.TrimSpace(stdout.String())
	if remoteVersion == "" {
		remoteVersion = "unknown"
	}
	stdout.Reset()
	stderr.Reset()
	// The receiver skips its registry entirely, so this never recursively fans
	// out to that machine's configured remotes (even a cycle back to this one).
	invocation.args = []string{"session", "ps", "--json", "--remote-inventory"}
	err := runRemoteInvocation(ctx, invocation, streams)
	if err != nil {
		var exitErr *commandExitError
		if !errors.As(err, &exitErr) || exitErr.code == 255 {
			return nil, remoteQueryError(name, err, stderr.String())
		}
		diagnostic := strings.TrimSpace(stderr.String())
		if strings.Contains(diagnostic, "unknown flag") || strings.Contains(diagnostic, "unknown command") || strings.Contains(diagnostic, "flag provided but not defined") {
			return nil, remoteInventoryMismatch(name, remoteVersion, "unsupported", diagnostic)
		}
		return nil, remoteQueryError(name, err, diagnostic)
	}
	var inventory remoteSessionInventory
	if err := json.Unmarshal(stdout.Bytes(), &inventory); err != nil {
		return nil, remoteInventoryMismatch(name, remoteVersion, "unsupported", "invalid session ps --json inventory")
	}
	if inventory.Version != "" {
		remoteVersion = config.BinaryName() + " version " + inventory.Version
	}
	if inventory.ContractVersion != remoteSessionContractVersion || inventory.Version == "" || inventory.Sessions == nil {
		contract := inventory.ContractVersion
		if contract == "" {
			contract = "unsupported"
		}
		return nil, remoteInventoryMismatch(name, remoteVersion, contract, "session inventory contract is incompatible")
	}
	return inventory.Sessions, nil
}

func remoteQueryError(name string, err error, diagnostic string) error {
	if diagnostic = strings.TrimSpace(diagnostic); diagnostic != "" {
		return fmt.Errorf("remote %q: %w (%s)", name, err, diagnostic)
	}
	return fmt.Errorf("remote %q: %w", name, err)
}

func remoteInventoryMismatch(name, remoteVersion, contract, detail string) error {
	return fmt.Errorf("remote %q version mismatch: local wt %s (session contract %s), remote %s (session contract %s): %s; upgrade the remote wt to a compatible version",
		name, version, remoteSessionContractVersion, remoteVersion, contract, detail)
}

func discoverMachineSessions(ctx context.Context, cfg *config.Config, streams remoteIO) ([]machineSession, error) {
	remotes, err := config.LoadRemotes(cfg.Dir)
	if err != nil {
		return nil, err
	}
	local, err := discoverActiveSessions(ctx, cfg)
	if err != nil {
		return nil, err
	}
	rows := make([]machineSession, 0, len(local))
	for _, session := range local {
		rows = append(rows, machineSession{localSession: session, Machine: "local"})
	}
	names := sortedRemoteNames(remotes)
	results := make([][]machineSession, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sessions, err := queryRemoteSessions(ctx, name, remotes[name], streams)
			if err != nil {
				results[i] = []machineSession{{Machine: name, Error: err.Error()}}
				return
			}
			for _, session := range sessions {
				results[i] = append(results[i], machineSession{localSession: session, Machine: name})
			}
		}()
	}
	wg.Wait()
	for _, result := range results {
		rows = append(rows, result...)
	}
	return rows, nil
}

func writeMachineSessions(out io.Writer, rows []machineSession, jsonOutput bool) error {
	if jsonOutput {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rows)
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(out, "no active sessions")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "MACHINE\tNAME\tID\tKIND\tPROCESS\tSTATUS\tISOLATION\tREADERS\tUPTIME\tIDLE\tCWD\tERROR"); err != nil {
		return err
	}
	for _, row := range rows {
		if row.Error != "" {
			if _, err := fmt.Fprintf(w, "%s\t-\t-\t-\t-\t-\t-\t-\t-\t-\t-\t%s\n", row.Machine, strings.Join(strings.Fields(row.Error), " ")); err != nil {
				return err
			}
			continue
		}
		name, process, status, isolation := row.Name, row.Agent, row.Status, row.Isolation
		if name == "" {
			name = "-"
		}
		if process == "" {
			process = row.Command
		}
		if process == "" {
			process = "-"
		}
		if status == "" {
			status = "unknown"
		}
		if isolation == "" {
			isolation = "unknown"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t\n", row.Machine,
			name, row.ID, row.Kind, process, status, isolation, row.Readers,
			humanDuration(time.Duration(row.UptimeSecs)*time.Second), humanDuration(time.Duration(row.IdleSecs)*time.Second), shortenPath(row.CWD)); err != nil {
			return err
		}
	}
	return w.Flush()
}

func parseRemoteSession(ref string) (name, session string, err error) {
	name, session, remote := strings.Cut(ref, ":")
	if !remote {
		return "", ref, nil
	}
	if err := config.ValidateRemoteName(name); err != nil {
		return "", "", err
	}
	if session == "" || strings.Contains(session, ":") {
		return "", "", fmt.Errorf("invalid remote session %q; use NAME:SESSION", ref)
	}
	return name, session, nil
}

func configuredRemote(dir, name string) (config.Remote, error) {
	remotes, err := config.LoadRemotes(dir)
	if err != nil {
		return config.Remote{}, err
	}
	remote, exists := remotes[name]
	if !exists {
		return config.Remote{}, fmt.Errorf("unknown remote %q; configure it with 'wt remote add'", name)
	}
	return remote, nil
}
