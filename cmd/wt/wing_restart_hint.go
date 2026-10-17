package main

import (
	"os"
	"regexp"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/daemonctl"
)

var safeRestartWord = regexp.MustCompile(`^[a-zA-Z0-9_@%+=:,./-]+$`)

// lanPairRestartGuidance prefers the daemon's exact saved flags. Invitations
// also identify the mode for foreground wings, which have no daemon args file.
func lanPairRestartGuidance(relay *string) string {
	var args []string
	if data, err := os.ReadFile(daemonctl.WingArgsPath()); err == nil {
		if _, err := daemonctl.ParseSavedDaemonArgs(data, daemonctl.WingDaemon); err == nil {
			// Validate with the lifecycle parser, then preserve whitespace in values.
			saved := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
			args = lanPairStartArgs(saved)
		}
	}
	if args == nil {
		if relay == nil {
			if status, err := daemonctl.ReadWingStatus(); err == nil {
				relay = &status.RoostURL
			}
		}
		if relay == nil {
			return "Current startup flags are unknown: neither wing.args nor wing.status supplied usable metadata. If started with --local-only, use " + lanPairStartCommand + " (keeps the relay disabled); otherwise use wt wing start --listen 0.0.0.0 (keeps the existing relay mode). Keep all other startup flags."
		}
		args = []string{"wing", "start"}
		if *relay == "" {
			args = append(args, "--local-only")
		} else {
			args = append(args, "--roost", *relay)
		}
		args = append(args, "--listen", "0.0.0.0")
	}
	words := append([]string{"wt"}, args...)
	for index, word := range words {
		if !safeRestartWord.MatchString(word) {
			words[index] = "'" + strings.ReplaceAll(word, "'", "'\"'\"'") + "'"
		}
	}
	return "Restart with " + strings.Join(words, " ") + "; retain any other startup flags not recorded in this metadata."
}

func lanPairStartArgs(saved []string) []string {
	args := append([]string(nil), saved[:2]...)
	for index := 2; index < len(saved); index++ {
		arg := saved[index]
		switch {
		case arg == "--foreground":
			// The daemon launcher injects this for its detached child.
		case arg == "--listen":
			if index+1 == len(saved) {
				return nil
			}
			index++
		case strings.HasPrefix(arg, "--listen="):
		default:
			args = append(args, arg)
		}
	}
	return append(args, "--listen", "0.0.0.0")
}
