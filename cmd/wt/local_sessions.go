package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"strconv"
	"strings"

	"text/tabwriter"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"

	"golang.org/x/term"
)

func printActiveSessions(ctx context.Context, cfg *config.Config, jsonOutput bool) error {
	sessions, err := eggclient.DiscoverActiveSessions(ctx, cfg)
	if err != nil {
		return err
	}
	return writeLocalSessions(os.Stdout, sessions, jsonOutput)
}

func writeLocalSessions(out io.Writer, sessions []eggclient.LocalSession, jsonOutput bool) error {
	if jsonOutput {
		if sessions == nil {
			sessions = []eggclient.LocalSession{}
		}
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(sessions)
	}
	if len(sessions) == 0 {
		_, err := fmt.Fprintln(out, "no active sessions")
		return err
	}

	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tID\tKIND\tPROCESS\tSTATUS\tISOLATION\tREADERS\tUPTIME\tIDLE\tCWD"); err != nil {
		return err
	}
	for _, session := range sessions {
		name := session.Name
		if name == "" {
			name = "-"
		}
		process := session.Agent
		if process == "" {
			process = session.Command
		}
		if process == "" {
			process = "-"
		}
		isolation := session.Isolation
		if isolation == "" {
			isolation = "unknown"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			name, session.ID, session.Kind, process, session.Status, isolation, session.Readers,
			eggclient.HumanDuration(time.Duration(session.UptimeSecs)*time.Second),
			eggclient.HumanDuration(time.Duration(session.IdleSecs)*time.Second),
			shortenPath(session.CWD),
		); err != nil {
			return err
		}
	}
	return w.Flush()
}

func selectActiveSession(ctx context.Context, cfg *config.Config) (eggclient.LocalSession, error) {
	sessions, err := eggclient.DiscoverActiveSessions(ctx, cfg)
	if err != nil {
		return eggclient.LocalSession{}, err
	}
	return selectSession(sessions)
}

func selectSession(sessions []eggclient.LocalSession) (eggclient.LocalSession, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return eggclient.LocalSession{}, errors.New("interactive selection requires a terminal; pass a session ID or name")
	}
	if len(sessions) == 0 {
		return eggclient.LocalSession{}, errors.New("no active sessions")
	}
	if len(sessions) == 1 {
		return sessions[0], nil
	}

	fmt.Fprintln(os.Stderr, "Active sessions:")
	for i, session := range sessions {
		name := session.Name
		if name == "" {
			name = session.ID
		}
		process := session.Agent
		if process == "" {
			process = session.Command
		}
		fmt.Fprintf(os.Stderr, "  %d) %-20s %-8s %-8s %s  %s\n", i+1, name, session.Kind, session.Status, shortenPath(session.CWD), process)
	}
	fmt.Fprintf(os.Stderr, "Attach [1-%d]: ", len(sessions))
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return eggclient.LocalSession{}, fmt.Errorf("read selection: %w", err)
	}
	selection, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || selection < 1 || selection > len(sessions) {
		return eggclient.LocalSession{}, fmt.Errorf("invalid selection %q", strings.TrimSpace(line))
	}
	return sessions[selection-1], nil
}
