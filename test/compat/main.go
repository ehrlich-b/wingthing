// Command compatcheck supplies black-box helpers for the release compatibility
// gate. It intentionally talks to built binaries and live WebSockets instead of
// importing their implementations, so the gate can compare two real releases.
package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

var flagPattern = regexp.MustCompile(`--[A-Za-z0-9][A-Za-z0-9-]*|(^|[ ,])-([A-Za-z0-9])([, ]|$)`)

type commandHelp struct {
	aliases     []string
	flags       map[string]struct{}
	subcommands []string
}

func main() {
	if len(os.Args) < 2 {
		fatalf("usage: compatcheck cli OLD NEW | port | pty URL WING_ID CWD")
	}
	var err error
	switch os.Args[1] {
	case "cli":
		if len(os.Args) != 4 {
			fatalf("usage: compatcheck cli OLD NEW")
		}
		err = compareCLI(os.Args[2], os.Args[3])
	case "port":
		if len(os.Args) != 2 {
			fatalf("usage: compatcheck port")
		}
		err = printAvailablePort()
	case "pty":
		if len(os.Args) != 5 {
			fatalf("usage: compatcheck pty URL WING_ID CWD")
		}
		err = startLegacyPTY(os.Args[2], os.Args[3], os.Args[4])
	default:
		fatalf("unknown operation %q", os.Args[1])
	}
	if err != nil {
		fatalf("%v", err)
	}
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "compatcheck: "+format+"\n", args...)
	os.Exit(1)
}

func compareCLI(baselineBinary, candidateBinary string) error {
	seen := make(map[string]bool)
	checked := 0
	var walk func([]string) error
	walk = func(path []string) error {
		key := strings.Join(path, " ")
		if seen[key] {
			return nil
		}
		seen[key] = true

		baseline, err := readCommandHelp(baselineBinary, path)
		if err != nil {
			return fmt.Errorf("baseline help for %q: %w", key, err)
		}
		candidate, err := readCommandHelp(candidateBinary, path)
		if err != nil {
			return fmt.Errorf("candidate removed command %q: %w", key, err)
		}
		for flag := range baseline.flags {
			if _, ok := candidate.flags[flag]; !ok {
				return fmt.Errorf("candidate removed flag %q from command %q", flag, key)
			}
		}
		for _, alias := range baseline.aliases {
			aliasPath := append(append([]string(nil), path[:len(path)-1]...), alias)
			if _, err := readCommandHelp(candidateBinary, aliasPath); err != nil {
				return fmt.Errorf("candidate removed alias %q for command %q: %w", alias, key, err)
			}
		}
		checked++
		for _, child := range baseline.subcommands {
			if child == "help" {
				continue
			}
			if err := walk(append(append([]string(nil), path...), child)); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(nil); err != nil {
		return err
	}
	fmt.Printf("CLI compatibility: %d baseline command surfaces preserved\n", checked)
	return nil
}

func readCommandHelp(binary string, path []string) (commandHelp, error) {
	args := append(append([]string(nil), path...), "--help")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if ctx.Err() != nil {
		return commandHelp{}, ctx.Err()
	}
	if err != nil {
		return commandHelp{}, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	parsed := commandHelp{flags: make(map[string]struct{})}
	lines := strings.Split(string(output), "\n")
	inCommands := false
	inAliases := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "Available Commands:":
			inCommands = true
			inAliases = false
			continue
		case "Aliases:":
			inAliases = true
			inCommands = false
			continue
		case "Flags:", "Global Flags:", "Additional help topics:":
			inCommands = false
			inAliases = false
		}
		if inCommands {
			if trimmed == "" {
				inCommands = false
				continue
			}
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 {
				parsed.subcommands = append(parsed.subcommands, fields[0])
			}
			continue
		}
		if inAliases {
			if trimmed == "" {
				inAliases = false
				continue
			}
			for _, alias := range strings.Split(trimmed, ",") {
				alias = strings.TrimSpace(alias)
				if alias != "" && (len(path) == 0 || alias != path[len(path)-1]) {
					parsed.aliases = append(parsed.aliases, alias)
				}
			}
			inAliases = false
		}
		for _, match := range flagPattern.FindAllStringSubmatch(line, -1) {
			flag := strings.TrimSpace(match[0])
			flag = strings.Trim(flag, " ,")
			if strings.HasPrefix(flag, "--") {
				parsed.flags[flag] = struct{}{}
			} else if len(match) > 2 && match[2] != "" {
				parsed.flags["-"+match[2]] = struct{}{}
			}
		}
	}
	sort.Strings(parsed.aliases)
	sort.Strings(parsed.subcommands)
	return parsed, nil
}

func printAvailablePort() error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
	}()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return err
	}
	fmt.Println(port)
	return nil
}

// startLegacyPTY emits the v0.144.1 browser's additive-minimum pty.start shape.
// Success requires both pty.started and encrypted output from the real agent
// process. The session ID is printed so the shell gate can kill the durable egg.
func startLegacyPTY(rawURL, wingID, cwd string) error {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate browser key: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	parsedURL, err := url.Parse(rawURL)
	if err != nil || parsedURL.Host == "" {
		return fmt.Errorf("parse browser WebSocket URL %q: %w", rawURL, err)
	}
	switch parsedURL.Scheme {
	case "ws":
		parsedURL.Scheme = "http"
	case "wss":
		parsedURL.Scheme = "https"
	}
	origin := parsedURL.Scheme + "://" + parsedURL.Host
	header := http.Header{}
	header.Set("Origin", origin)
	conn, _, err := websocket.Dial(ctx, rawURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return fmt.Errorf("dial browser WebSocket: %w", err)
	}
	defer func() {
		_ = conn.CloseNow()
	}()
	start := map[string]any{
		"type":       "pty.start",
		"agent":      "claude",
		"cols":       80,
		"rows":       24,
		"public_key": base64.StdEncoding.EncodeToString(privateKey.PublicKey().Bytes()),
		"cwd":        cwd,
		"wing_id":    wingID,
	}
	if err := wsjson.Write(ctx, conn, start); err != nil {
		return fmt.Errorf("send legacy pty.start: %w", err)
	}
	started := ""
	for {
		var message struct {
			Type      string          `json:"type"`
			SessionID string          `json:"session_id"`
			Message   string          `json:"message"`
			Error     string          `json:"error"`
			ExitCode  int             `json:"exit_code"`
			Data      json.RawMessage `json:"data"`
		}
		if err := wsjson.Read(ctx, conn, &message); err != nil {
			return fmt.Errorf("read PTY response: %w", err)
		}
		switch message.Type {
		case "error":
			return fmt.Errorf("PTY rejected: %s", message.Message)
		case "pty.started":
			if message.SessionID == "" {
				return fmt.Errorf("pty.started omitted session_id")
			}
			started = message.SessionID
		case "pty.output":
			if started != "" && message.SessionID == started && len(message.Data) > 2 {
				fmt.Println(started)
				return nil
			}
		case "pty.exited":
			return fmt.Errorf("PTY %s exited before producing output (code=%d error=%q)", message.SessionID, message.ExitCode, message.Error)
		}
	}
}
