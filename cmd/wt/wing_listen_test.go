package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/pake"
	"github.com/ehrlich-b/wingthing/internal/phonepair"
)

func TestWingListenHelpAndValidation(t *testing.T) {
	cmd := wingStartCmd()
	var help bytes.Buffer
	cmd.SetOut(&help)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--listen", "127.0.0.1", "0.0.0.0", "LAN access"} {
		if !strings.Contains(help.String(), want) {
			t.Fatalf("help missing %q: %s", want, help.String())
		}
	}
	for _, host := range []string{"", "localhost", "0.0.0.0:8080", "bad-address"} {
		cmd := wingStartCmd()
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetArgs([]string{"--local-only", "--listen", host})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--listen must be an IP address") {
			t.Fatalf("invalid listen %q: %v", host, err)
		}
	}
}

func TestBuiltWTLocalOnlyListen(t *testing.T) {
	if !pake.Available {
		t.Skip("PAKE unavailable on this build")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := localOnlyFixtureBinary(t, repo, t.TempDir())
	// A local interface is used only to connect back to this test's own listener.
	lan := ""
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())
		if err == nil && ip.To4() != nil && !ip.IsLoopback() {
			lan = ip.String()
			break
		}
	}
	tests := []struct {
		name, listen, want string
		daemon, localOnly  bool
	}{
		{"foreground-loopback", "", "127.0.0.1", false, true},
		{"daemon-loopback", "", "127.0.0.1", true, true},
		{"foreground-lan", "0.0.0.0", "0.0.0.0", false, true},
		{"daemon-lan", "0.0.0.0", "0.0.0.0", true, true},
		{"specific-loopback", "127.0.0.2", "127.0.0.2", false, true},
		{"ipv6-loopback", "::1", "::1", false, true},
		{"unchanged-default", "", "0.0.0.0", false, false},
	}
	if lan != "" {
		tests = append(tests, struct {
			name, listen, want string
			daemon, localOnly  bool
		}{"specific-interface", lan, lan, false, true})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.want == "::1" {
				probe, err := net.Listen("tcp6", "[::1]:0")
				if err != nil {
					t.Skipf("IPv6 loopback unavailable: %v", err)
				}
				probe.Close()
			}
			scratch := filepath.Join(repo, ".scratch")
			if err := os.MkdirAll(scratch, 0700); err != nil {
				t.Fatal(err)
			}
			root, err := os.MkdirTemp(scratch, "listen-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			home, state := filepath.Join(root, "home"), filepath.Join(root, "s")
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			environment := []string{"HOME=" + home, "WINGTHING_DIR=" + state, "PATH=/bin:/usr/bin", "TMPDIR=" + os.Getenv("TMPDIR")}
			args := []string{"wing", "start", "--paths", home}
			if !tt.daemon {
				args = append(args, "--foreground")
			}
			if tt.localOnly {
				args = append(args, "--local-only")
			}
			if tt.listen != "" {
				args = append(args, "--listen", tt.listen)
			}
			var startup []byte
			if tt.daemon {
				child := exec.CommandContext(t.Context(), binary, args...)
				child.Env = environment
				startup, err = child.CombinedOutput()
				t.Cleanup(func() {
					stop := exec.Command(binary, "wing", "stop")
					stop.Env = environment
					if output, err := stop.CombinedOutput(); err != nil {
						t.Errorf("stop fixture daemon: %v: %s", err, output)
					}
				})
				if err != nil {
					t.Fatalf("start daemon: %v: %s", err, startup)
				}
			} else {
				output, err := os.Create(filepath.Join(root, "startup.txt"))
				if err != nil {
					t.Fatal(err)
				}
				defer output.Close()
				child := exec.Command(binary, args...)
				child.Env, child.Stdout, child.Stderr = environment, output, output
				if err := child.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- child.Wait() }()
				t.Cleanup(func() {
					_ = child.Process.Signal(os.Interrupt)
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						_ = child.Process.Kill()
						<-done
						t.Error("fixture wing did not stop promptly")
					}
				})
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					startup, _ = os.ReadFile(output.Name())
					if strings.Contains(string(startup), "wing: local control ready") {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				if !strings.Contains(string(startup), "wing: local control ready") {
					t.Fatalf("wing not ready: %s", startup)
				}
			}
			data, err := os.ReadFile(filepath.Join(state, "phone-control.json"))
			if err != nil {
				t.Fatal(err)
			}
			var control phonepair.Control
			if err := json.Unmarshal(data, &control); err != nil {
				t.Fatal(err)
			}
			host, port, err := net.SplitHostPort(control.Address)
			if err != nil || host != tt.want {
				t.Fatalf("bound %q; want %s: %v", control.Address, tt.want, err)
			}
			if !strings.Contains(string(startup), "phone pairing: listening on "+control.Address) {
				t.Fatalf("startup missing bound address: %s", startup)
			}
			if tt.daemon {
				wantsRestart := net.ParseIP(host).IsLoopback()
				if strings.Contains(string(startup), "for LAN phone pairing, stop with") != wantsRestart {
					t.Fatalf("startup restart advice disagrees with listener: %s", startup)
				}
				if wantsRestart && (!strings.Contains(string(startup), lanPairStartCommand) || !strings.Contains(string(startup), "retaining all other startup flags")) {
					t.Fatalf("startup restart advice drops local-only mode or other flags: %s", startup)
				}
				if strings.Contains(string(startup), "relay: disabled; local control is ready") {
					t.Fatalf("duplicate local control readiness: %s", startup)
				}
			}
			client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
			defer client.CloseIdleConnections()
			req, err := http.NewRequestWithContext(t.Context(), "POST", control.AdminURL(), strings.NewReader(`{"Op":"create"}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+control.Token)
			response, err := client.Do(req)
			if err != nil {
				t.Fatalf("local client connection: %v", err)
			}
			defer response.Body.Close()
			var invitation phonepair.Invitation
			if err := json.NewDecoder(response.Body).Decode(&invitation); err != nil || response.StatusCode != http.StatusOK || invitation.Secret == "" {
				t.Fatalf("local pairing response: status=%d invitation=%+v error=%v", response.StatusCode, invitation, err)
			}
			if ip := net.ParseIP(host); !ip.IsUnspecified() {
				if invitation.Local != "http://"+control.Address || len(invitation.Endpoints) != 0 {
					t.Fatalf("invitation advertises unbound endpoints: %+v", invitation)
				}
			}
			if net.ParseIP(host).IsLoopback() {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				pair := exec.CommandContext(ctx, binary, "pair", "--show-link")
				pair.Env = environment
				output, err := pair.CombinedOutput()
				if ctx.Err() != nil || err == nil || !strings.Contains(string(output), "main wing listens on loopback only") || !strings.Contains(string(output), "--listen 0.0.0.0") {
					t.Fatalf("pair did not explain loopback restriction promptly: %v: %s", err, output)
				}
				if bytes.Contains(output, []byte("wingthing://pair")) || bytes.Contains(output, []byte("\x1b[30;47m")) || bytes.Contains(output, []byte("Direct endpoint:")) {
					t.Fatalf("pair displayed an unusable invitation: %s", output)
				}
				if !strings.Contains(string(output), "--local-only") || strings.Contains(string(output), "wt wing start --listen 0.0.0.0") || strings.Contains(string(output), "Current startup flags are unknown") || strings.Contains(string(output), "otherwise use") {
					t.Fatalf("pair restart advice drops --local-only: %s", output)
				}
				t.Log("pair=clear-loopback-warning invitation=hidden")
			}
			if tt.want == "127.0.0.1" && lan != "" {
				connection, err := net.DialTimeout("tcp4", net.JoinHostPort(lan, port), time.Second)
				if err == nil {
					connection.Close()
					t.Fatal("loopback listener accepted a LAN connection")
				}
			}
			if tt.want == "0.0.0.0" && lan != "" {
				connection, err := net.DialTimeout("tcp4", net.JoinHostPort(lan, port), time.Second)
				if err != nil {
					t.Fatalf("explicit LAN listener unreachable at its local interface: %v", err)
				}
				connection.Close()
			}
			t.Logf("bound=%s startup-address=visible local-client=connected", control.Address)
		})
	}
}
