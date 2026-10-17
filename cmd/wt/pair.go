package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"github.com/ehrlich-b/wingthing/internal/pake"
	"github.com/ehrlich-b/wingthing/internal/phonepair"
	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"
)

const lanPairStartCommand = "wt wing start --local-only --listen 0.0.0.0"

const lanPairModeGuidance = "For a running wing, keep its mode and all other startup flags, replacing any existing --listen value with 0.0.0.0. The restart hint uses wing.args, then wing.status or the invitation's relay mode; only unavailable metadata requires choosing between local-only and relay variants."

func phoneAdmin(ctx context.Context, op, id, name string, result any) error {
	if !pake.Available {
		return pakeUnavailable()
	}
	control, e := readPhoneControl()
	if e != nil {
		return e
	}
	body, _ := json.Marshal(map[string]string{"Op": op, "ID": id, "Name": name})
	req, e := http.NewRequestWithContext(ctx, "POST", control.AdminURL(), bytes.NewReader(body))
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
	b, e := io.ReadAll(io.LimitReader(resp.Body, phonepair.MaxPayload))
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

// readPhoneControl describes the listener, independently of advertised endpoints:
// a wildcard listener can advertise loopback when the host has no LAN interface.
func readPhoneControl() (phonepair.Control, error) {
	cfg, e := config.Load()
	if e != nil {
		return phonepair.Control{}, e
	}
	b, e := os.ReadFile(filepath.Join(cfg.Dir, "phone-control.json"))
	if e != nil {
		if _, pidErr := daemonctl.ReadPidFrom(daemonctl.WingPidPath(), daemonctl.WingDaemon); daemonctl.DaemonAbsentError(pidErr) {
			return phonepair.Control{}, fmt.Errorf("main wing is not ready for pairing; start it for LAN pairing with %s, then run wt pair", lanPairStartCommand)
		}
		return phonepair.Control{}, fmt.Errorf("main wing is not ready for pairing; update the running wing, then stop it with wt wing stop before restarting. %s", lanPairRestartGuidance(nil))
	}
	var control phonepair.Control
	if json.Unmarshal(b, &control) != nil || control.Port < 1 || control.Port > 65535 || control.WingID != cfg.WingID || control.Token == "" {
		return phonepair.Control{}, errors.New("invalid main wing pairing control")
	}
	return control, nil
}

func pairCmd() *cobra.Command {
	var showLink bool
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Pair a device with this machine's main wing",
		Long: "Pair a device with this machine's running main wing.\n" +
			"For LAN pairing without a relay, start the wing with:\n  " + lanPairStartCommand + "\n" +
			"The --local-only flag alone listens on loopback only; phones cannot reach it over the LAN.\n" +
			"If the wing is already running, stop it with wt wing stop before restarting.\n" + lanPairModeGuidance,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPairWithLink(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), showLink)
		},
	}
	cmd.Flags().BoolVar(&showLink, "show-link", false, "show the one-time invitation for browser or phone paste")
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
		control, err := readPhoneControl()
		if err == nil {
			err = showInvitation(out, q, control.Address)
		}
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
						fmt.Fprintf(out, "Allowed on %s's terminal. Waiting for the device to finish…\n", q.Name)
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
							scope := "Control sessions on this machine"
							if status.Scope == phonepair.ScopeInventory {
								scope = "Inventory only: list sessions and machines on this machine"
							}
							fmt.Fprintln(out, "\nComparison labels:")
							for i, label := range strings.Split(phonepair.EmojiLabels(status.Emoji), " · ") {
								fmt.Fprintf(out, "%d. %s\n", i+1, label)
							}
							fmt.Fprintf(out, "Fingerprint: %s\nAccess: %s\n%s wants to pair. Enter to allow.\nOnly allow if the full fingerprint and all 4 labels match the device in order. Ctrl-C cancels.\n", groupedFingerprint(status.Fingerprint), scope, status.Name)
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

func loopbackEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return strings.EqualFold(u.Hostname(), "localhost") || (ip != nil && ip.IsLoopback())
}

func showInvitation(out io.Writer, q phonepair.Invitation, listenAddress string) error {
	loopbackOnly := loopbackEndpoint(q.Local)
	for _, endpoint := range q.Endpoints {
		if !loopbackEndpoint(endpoint) {
			loopbackOnly = false
		}
	}
	if loopbackOnly {
		hint := "main wing listens on loopback only; a phone cannot connect over the LAN. For LAN pairing, stop it with wt wing stop before restarting. " + lanPairRestartGuidance(&q.Relay) + " Then run wt pair"
		if listenAddress != "" && !loopbackEndpoint("http://"+listenAddress) {
			hint = "main wing has no advertised LAN endpoint; connect this host to a LAN or tailnet, then restart the wing with its existing flags and run wt pair"
		}
		if q.Relay == "" {
			return errors.New(hint)
		}
		fmt.Fprintln(out, "Warning: "+hint+". This invitation can still use the registered relay.")
	}
	place := "this machine"
	if runtime.GOOS == "darwin" {
		place = "this Mac"
	}
	fmt.Fprintf(out, "\nPair a device with wing %s (%s). Access will be shown here before you approve.\n\n", q.Name, place)
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
		if !loopbackEndpoint(endpoint) {
			fmt.Fprintf(out, "Direct endpoint: %s\n", endpoint)
		}
	}
	fmt.Fprintf(out, "\n%s\n5:00 · Waiting for scan...\n", q.Secret)
	if loopbackOnly {
		fmt.Fprintln(out, "Pair through the registered relay; LAN access is unavailable.")
	} else {
		fmt.Fprintf(out, "Same Wi-Fi: no account needed. Away from home: needs %s registered once (`wt login`).\n", place)
	}
	return nil
}
func devicesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "devices", Short: "List devices paired with this wing", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var devices []phonepair.Device
		if e := phoneAdmin(cmd.Context(), "devices", "", "", &devices); e != nil {
			return e
		}
		out := cmd.OutOrStdout()
		fmt.Fprintln(out, "NAME\tACCESS\tPAIRED AT\tLAST SEEN\tDEVICE ID")
		for _, d := range devices {
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n", d.Name, deviceScope(d.Scope), d.PairedAt.Local().Format(time.RFC3339), d.LastSeen.Local().Format(time.RFC3339), d.ID)
		}
		return nil
	}}
	cmd.AddCommand(&cobra.Command{Use: "revoke <name-or-device-id>", Short: "Immediately revoke this device, including live access", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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

func groupedFingerprint(value string) string {
	var groups []string
	for len(value) > 4 {
		groups = append(groups, value[:4])
		value = value[4:]
	}
	if value != "" {
		groups = append(groups, value)
	}
	return strings.Join(groups, " ")
}
func deviceScope(scope string) string {
	if scope == "" {
		return phonepair.ScopeControl
	}
	return scope
}
