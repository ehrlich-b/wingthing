package main

import (
	"context"
	"encoding/json"

	"fmt"
	"io"
	"strconv"
	"strings"

	"text/tabwriter"
	"time"
	"unicode"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

type remoteIOContextKey struct{}

func remoteStreams(ctx context.Context) remotepkg.IO {
	if streams, ok := ctx.Value(remoteIOContextKey{}).(remotepkg.IO); ok {
		return streams
	}
	return remotepkg.ProcessIO()
}

func writeMachineSessions(out io.Writer, rows []eggclient.MachineSession, jsonOutput bool) error {
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
			if _, err := fmt.Fprintf(w, "%s\t-\t-\t-\t-\t-\t-\t-\t-\t-\t-\t%s\n", escapeSessionField(row.Machine), escapeSessionField(row.Error)); err != nil {
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
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t\n", escapeSessionField(row.Machine),
			escapeSessionField(name), escapeSessionField(row.ID), escapeSessionField(row.Kind), escapeSessionField(process), escapeSessionField(status), escapeSessionField(isolation), row.Readers,
			eggclient.HumanDuration(time.Duration(row.UptimeSecs)*time.Second), eggclient.HumanDuration(time.Duration(row.IdleSecs)*time.Second), escapeSessionField(shortenPath(row.CWD))); err != nil {
			return err
		}
	}
	return w.Flush()
}

func escapeSessionField(value string) string {
	var escaped strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) {
			quoted := strconv.QuoteRune(r)
			escaped.WriteString(quoted[1 : len(quoted)-1])
		} else {
			escaped.WriteRune(r)
		}
	}
	return escaped.String()
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
