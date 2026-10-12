package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/pake"
	"github.com/ehrlich-b/wingthing/internal/phonepair"
	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"
)

func phoneAdmin(ctx context.Context, op, id, name string, result any) error {
	if !pake.Available {
		return pakeUnavailable()
	}
	cfg, e := config.Load()
	if e != nil {
		return e
	}
	b, e := os.ReadFile(filepath.Join(cfg.Dir, "phone-control.json"))
	if e != nil {
		return errors.New("main wing is not ready for pairing; start it with wt wing start --local-only (or wt wing start if registered)")
	}
	var control phonepair.Control
	if json.Unmarshal(b, &control) != nil || control.Port < 1 || control.Port > 65535 || control.WingID != cfg.WingID || control.Token == "" {
		return errors.New("invalid main wing pairing control")
	}
	body, _ := json.Marshal(map[string]string{"Op": op, "ID": id, "Name": name})
	req, e := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://127.0.0.1:%d/admin", control.Port), bytes.NewReader(body))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+control.Token)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, e := client.Do(req)
	if e != nil {
		return errors.New("main wing pairing endpoint is unavailable; update and restart that wing before pairing")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("main wing pairing endpoint refused (%d)", resp.StatusCode)
	}
	b, e = io.ReadAll(io.LimitReader(resp.Body, phonepair.MaxPayload))
	if e != nil {
		return e
	}
	var problem struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(b, &problem)
	if problem.Error != "" {
		return errors.New(problem.Error)
	}
	if result != nil {
		return json.Unmarshal(b, result)
	}
	return nil
}
func pairCmd() *cobra.Command {
	var showLink bool
	cmd := &cobra.Command{Use: "pair", Short: "Pair a phone with this machine's main wing", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return runPairWithLink(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), showLink)
	}}
	cmd.Flags().BoolVar(&showLink, "show-link", false, "show the one-time pairing link for the app’s hidden paste fallback")
	return cmd
}
func runPair(ctx context.Context, in io.Reader, out io.Writer) error {
	return runPairWithLink(ctx, in, out, false)
}
func runPairWithLink(ctx context.Context, in io.Reader, out io.Writer, showLink bool) error {
	lines := make(chan string)
	readErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(in)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		e := scanner.Err()
		if e == nil {
			e = io.EOF
		}
		readErr <- e
	}()
	for {
		var q phonepair.Invitation
		if e := phoneAdmin(ctx, "create", "", "", &q); e != nil {
			return e
		}
		err := showInvitation(out, q)
		if err != nil {
			_ = phoneAdmin(ctx, "cancel", q.ID, "", nil)
			return err
		}
		if showLink {
			fmt.Fprintf(out, "\nOne-time pairing link (keep private):\n%s\n", q.Link())
		}
		ticker := time.NewTicker(time.Second)
		state := "waiting"
		showed := ""
		expired := false
		allowed := false
		pairErr := func() error {
			defer ticker.Stop()
			completed := false
			defer func() {
				if !completed {
					_ = phoneAdmin(context.WithoutCancel(ctx), "cancel", q.ID, "", nil)
				}
			}()
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case e := <-readErr:
					return e
				case line := <-lines:
					if expired || state == "cancelled" || state == "used" {
						return nil
					}
					if state == "confirming" && !allowed && strings.TrimSpace(line) == "" {
						if e := phoneAdmin(ctx, "allow", q.ID, "", nil); e != nil {
							return e
						}
						allowed = true
						fmt.Fprintf(out, "Allowed on %s's terminal. Waiting for the phone to confirm…\n", q.Name)
					}
				case <-ticker.C:
					remaining := time.Until(q.ExpiresAt)
					if remaining <= 0 && !expired {
						expired = true
						fmt.Fprintln(out, "\nQR expired or already used. Press Enter for a fresh QR.")
						continue
					}
					if expired {
						continue
					}
					var status phonepair.PairStatus
					e := phoneAdmin(ctx, "status", q.ID, "", &status)
					if e != nil {
						expired = true
						fmt.Fprintln(out, "\nQR expired or already used. Press Enter for a fresh QR.")
						continue
					}
					state = status.State
					switch state {
					case "paired":
						completed = true
						fmt.Fprintf(out, "\nPaired %s with wing %s. Use wt devices to manage access.\n", status.Name, q.Name)
						return errPaired
					case "confirming":
						if showed != status.Emoji {
							showed = status.Emoji
							fmt.Fprintf(out, "\n%s\n%s wants to pair. Enter to allow.\nOnly allow if all 4 emoji match the phone. Ctrl-C cancels.\n", status.Emoji, status.Name)
						}
					case "cancelled", "used":
						if showed != "cancelled" {
							showed = "cancelled"
							fmt.Fprintln(out, "\nPairing cancelled or QR already used. Press Enter for a fresh QR.")
						}
					}
					seconds := int(remaining.Seconds())
					if state == "waiting" {
						fmt.Fprintf(out, "\r%d:%02d · Waiting for scan...    ", seconds/60, seconds%60)
					} else {
						fmt.Fprintf(out, "\r%d:%02d · %s    ", seconds/60, seconds%60, state)
					}
				}
			}
		}()
		if errors.Is(pairErr, errPaired) {
			return nil
		}
		if pairErr != nil {
			return pairErr
		}
	}
}

var errPaired = errors.New("paired")

func showInvitation(out io.Writer, q phonepair.Invitation) error {
	place := "this machine"
	if runtime.GOOS == "darwin" {
		place = "this Mac"
	}
	fmt.Fprintf(out, "\nPair a phone with wing %s (%s). The phone will be able to see and control the sessions on this machine.\n\n", q.Name, place)
	qr, e := qrcode.New(q.Link(), qrcode.Low)
	if e != nil {
		return e
	}
	matrix := qr.Bitmap()
	// Two QR rows per terminal row; explicit colors preserve quiet zones in either terminal theme.
	for y := 0; y < len(matrix); y += 2 {
		fmt.Fprint(out, "\x1b[30;47m")
		for x := range matrix[y] {
			top := matrix[y][x]
			bottom := false
			if y+1 < len(matrix) {
				bottom = matrix[y+1][x]
			}
			switch {
			case top && bottom:
				fmt.Fprint(out, "█")
			case top:
				fmt.Fprint(out, "▀")
			case bottom:
				fmt.Fprint(out, "▄")
			default:
				fmt.Fprint(out, " ")
			}
		}
		fmt.Fprintln(out, "\x1b[0m")
	}
	if q.Relay == "" {
		fmt.Fprintln(out, "Local network only. That's fine; nothing else needed.")
	}
	for _, endpoint := range q.Endpoints {
		fmt.Fprintf(out, "Direct endpoint: %s\n", endpoint)
	}
	fmt.Fprintf(out, "\n%s\n5:00 · Waiting for scan...\nSame Wi-Fi: no account needed. Away from home: needs %s registered once (`wt login`).\n", q.Secret, place)
	return nil
}
func devicesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "devices", Short: "List phones paired with this wing", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var devices []phonepair.Device
		if e := phoneAdmin(cmd.Context(), "devices", "", "", &devices); e != nil {
			return e
		}
		out := cmd.OutOrStdout()
		fmt.Fprintln(out, "NAME\tPAIRED AT\tLAST SEEN\tDEVICE ID")
		for _, d := range devices {
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", d.Name, d.PairedAt.Local().Format(time.RFC3339), d.LastSeen.Local().Format(time.RFC3339), d.ID)
		}
		return nil
	}}
	cmd.AddCommand(&cobra.Command{Use: "revoke <name-or-device-id>", Short: "Immediately revoke this phone, including live access", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if e := phoneAdmin(cmd.Context(), "revoke", "", args[0], nil); e != nil {
			return e
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Revoked %s. Live access closed.\n", args[0])
		return nil
	}})
	return cmd
}

func pakeUnavailable() error {
	return errors.New("phone pairing requires a native build with CGO_ENABLED=1 and a C++17 compiler; see docs/pairing/PROTOCOL.md")
}
