// permission-spike is a separately built, fixed-script experiment. It is not
// registered as a wing, CLI control, browser or MCP permission capability.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/permissionbridge"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "use hook, broker, digest or settings")
		os.Exit(1)
	}
	err := run(os.Args[1], os.Args[2:])
	if err != nil {
		// A hook failure must emit no decision and exit zero. Avoid logging input,
		// tool arguments, paths or error values that could contain provider data.
		if os.Args[1] == "hook" {
			fmt.Fprintln(os.Stderr, "permission experiment returned no decision")
			return
		}
		fmt.Fprintln(os.Stderr, "permission experiment failed")
		os.Exit(1)
	}
}

func run(mode string, args []string) error {
	f := flag.NewFlagSet(mode, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	socket := f.String("socket", "", "private per-session Unix socket")
	session := f.String("session", "", "owning egg session")
	script := f.String("script", "", "finite exact-digest script file")
	deadline := f.Duration("deadline", 2*time.Second, "application deadline")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *deadline <= 0 || *deadline > 10*time.Second {
		return errors.New("invalid arguments")
	}
	switch mode {
	case "hook":
		in, err := permissionbridge.ParseInput(os.Stdin)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), *deadline)
		defer cancel()
		return permissionbridge.Hook(ctx, *socket, *session, in, os.Stdout)
	case "digest":
		in, err := permissionbridge.ParseInput(os.Stdin)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, permissionbridge.Digest(in))
		return nil
	case "broker":
		file, err := os.Open(*script)
		if err != nil {
			return err
		}
		defer file.Close()
		b := &permissionbridge.Broker{Log: os.Stdout}
		d := json.NewDecoder(io.LimitReader(file, permissionbridge.MaxMessage))
		d.DisallowUnknownFields()
		if err := d.Decode(&b.Script); err != nil {
			return err
		}
		if d.Decode(new(any)) != io.EOF {
			return errors.New("trailing script")
		}
		if err := b.Validate(); err != nil {
			return err
		}
		l, err := permissionbridge.Listen(*socket)
		if err != nil {
			return err
		}
		defer l.Close()
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer cancel()
		return b.Serve(ctx, l)
	case "settings":
		binary, err := os.Executable()
		if err != nil || *session == "" || *socket == "" {
			return errors.New("session and socket required")
		}
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
		command := quote(binary) + " hook --socket " + quote(*socket) + " --session " + quote(*session) + " --deadline " + quote(deadline.String())
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"hooks": map[string]any{"PermissionRequest": []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": command, "timeout": int(deadline.Seconds()) + 3}}}}}})
	default:
		return errors.New("unknown mode")
	}
}
