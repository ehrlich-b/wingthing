package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
)

// These captures contain real built CLI output and kernel PTY handoffs. Vendor,
// SSH and inventory endpoints are disposable fixtures, never production state.
func onboardingStoryboardCapture(t *testing.T, h *northstarPTY, card string) {
	t.Helper()
	dir := os.Getenv("WT_STORYBOARD_CAPTURE_ROOT")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	northstarWrite(t, filepath.Join(dir, card+".ansi"), h.transcript.String(), 0600)
	northstarWrite(t, filepath.Join(dir, card+".txt"), h.emu.String(), 0600)
}

func TestOnboardingPTYStoryboard(t *testing.T) {
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	// Provider discovery is part of this fixture. Expose the real installer
	// utilities without inheriting an already installed host Claude or Codex.
	for _, tool := range []string{"awk", "basename", "bash", "cat", "chmod", "cp", "cut", "date", "dirname", "grep", "head", "id", "ls", "mkdir", "mktemp", "mv", "ps", "readlink", "rm", "sed", "sh", "sleep", "sort", "tr", "uname", "wc", "sha256sum", "shasum"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			if tool == "sha256sum" || tool == "shasum" {
				continue
			}
			t.Fatalf("installer fixture requires %s: %v", tool, err)
		}
		if err := os.Symlink(path, filepath.Join(f.bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	for i, value := range f.env {
		if strings.HasPrefix(value, "PATH=") {
			f.env[i] = "PATH=" + f.bin
		}
	}
	if err := os.Remove(filepath.Join(f.local, "remotes.yaml")); err != nil {
		t.Fatal(err)
	}
	h := northstarStartPTY(t, f, binary, 120, 36)
	h.expect("empty onboarding", 0, "Install an agent", "a install an agent")
	onboardingStoryboardCapture(t, h, "01-2")
	onboardingStoryboardCapture(t, h, "01-8")
	h.expect("local detection", h.send("a"), "Install an agent", "Missing")
	onboardingStoryboardCapture(t, h, "01-3")
	h.expect("consent", h.send("1"), "Install Claude Code", "https://claude.ai/install.sh", "Enter install Claude Code")
	onboardingStoryboardCapture(t, h, "01-4")
	// No external download: the curl fixture emits an installer which writes only
	// the selected fixture provider. Its --version/login paths have exact argv.
	provider := filepath.Join(f.bin, "claude")
	northstarWrite(t, filepath.Join(f.bin, "provider-source"), "#!/bin/sh\ncase \"$*\" in '--version') echo 'fixture Claude 1.0';; 'auth login') echo 'fixture native provider login handoff';; *) exit 91;; esac\n", 0700)
	northstarWrite(t, filepath.Join(f.bin, "curl"), fmt.Sprintf("#!/bin/sh\nif [ -f '%s/fail-download' ]; then exit 22; fi\nprintf 'cp %s/provider-source %s; chmod 700 %s\\n'\n", f.home, f.bin, provider, provider), 0700)
	h.expect("installed provider", h.send("\r"), "Installed", "v verify")
	h.expect("version receipt", h.send("v"), "Installed", "l sign in")
	h.waitScreen("native login completes or refuses managed capture", h.send("l"), func(s string) bool {
		return strings.Contains(s, "Sign-in failed.") || strings.Contains(s, "Enter new session")
	})
	onboardingStoryboardCapture(t, h, "01-5")
	// A missing second provider and failed pipe preserve the exact stage; retry
	// returns to consent, while inventory remains available.
	northstarWrite(t, filepath.Join(f.home, "fail-download"), "fixture", 0600)
	h.expect("other provider", h.send("\x1b"), "0 sessions", "1 machine")
	h.expect("fresh detection", h.send("a2"), "Codex · This machine", "Install Codex")
	h.expect("download failure", h.send("\r"), "Install failed.", "r try install again")
	onboardingStoryboardCapture(t, h, "01-6")
	h.expect("fresh consent after failure", h.send("r"), "Enter install")
	h.exit("\x1bq")

	// Install the built executable using the real checksum/contract installer,
	// with a local-only curl fixture serving the exact built binary and checksum.
	install := filepath.Join(f.home, "installed")
	f.env = append(f.env, "WT_INSTALL_DIR="+install, "STORYBOARD_BINARY="+binary)
	northstarWrite(t, filepath.Join(f.bin, "curl"), strings.ReplaceAll(`#!/bin/sh
out=''
while [ "$#" -gt 0 ]; do
 case "$1" in -o) out=$2; shift 2;; *) url=$1;shift;; esac
done
case "$url" in
 */releases/latest) printf '{"tag_name":"fixture-build"}\n';;
 */SHA256SUMS) if command -v sha256sum >/dev/null 2>&1; then sha256sum "$STORYBOARD_BINARY"; else shasum -a 256 "$STORYBOARD_BINARY"; fi | awk '{print $1 "  wt-linux-amd64"}' > "$out";;
 */wt-linux-amd64) cp "$STORYBOARD_BINARY" "$out";;
 *) exit 99;;
esac
`, "wt-linux-amd64", "wt-"+runtime.GOOS+"-"+runtime.GOARCH), 0700)
	installer := northstarStartPTY(t, f, "/bin/sh", 120, 36, filepath.Join(f.repo, "scripts", "install.sh"))
	installer.expect("checksum verified build installed", 0, "installed wt fixture-build", "open the session navigator", "add to your PATH")
	onboardingStoryboardCapture(t, installer, "01-1")
	// Deterministic permission/path diagnostic without touching system directories.
	f.env = append(f.env, "WT_INSTALL_DIR="+filepath.Join(f.home, "not-a-directory", "bin"))
	northstarWrite(t, filepath.Join(f.home, "not-a-directory"), "fixture", 0600)
	failed := northstarStartPTY(t, f, "/bin/sh", 120, 36, filepath.Join(f.repo, "scripts", "install.sh"))
	failed.expect("installer writable path diagnosis", 0, "choose a writable WT_INSTALL_DIR", "no automatic sudo")
	onboardingStoryboardCapture(t, failed, "01-7")

	// Cold launch diagnostics remain real. Native protected-parent acceptance is
	// tested on macOS separately; this Linux capture does not claim Seatbelt.
	config.SaveWingConfig(f.local, &config.WingConfig{Paths: config.PathList{{Path: f.work}}, Roost: "http://127.0.0.1:1", HostedRelay: config.HostedRelayDeny})
	coldCleanup(t, f, binary)
	launch := northstarStartPTY(t, f, binary, 120, 36, "claude", "--cwd", f.work, "--name", "cold-fixture")
	launch.waitScreen("cold launch reached a ready provider or diagnostic", 0, func(s string) bool { return strings.Contains(s, "Error:") || strings.Contains(s, "fixture Claude") })
	onboardingStoryboardCapture(t, launch, "02-1")
	onboardingStoryboardCapture(t, launch, "02-2")
	onboardingStoryboardCapture(t, launch, "02-5")
	// Two explicit launches use the production startup lock. Exact wing status
	// is displayed after the pair; policy failures do not replace the daemon.
	pairScript := filepath.Join(f.bin, "pair-starts")
	northstarWrite(t, pairScript, fmt.Sprintf(`#!/usr/bin/python3
import os,subprocess
p=[subprocess.Popen([%q,'claude','--cwd',%q,'--name',name]) for name in ['first','second']]
for child in p: child.wait()
subprocess.run([%q,'wing','status'])
`, binary, f.work, binary), 0700)
	stopCold := exec.Command(binary, "wing", "stop")
	stopCold.Env = f.env
	if out, err := stopCold.CombinedOutput(); err != nil {
		t.Fatalf("stop first isolated launch: %v %s", err, out)
	}
	if err := os.Remove(filepath.Join(f.local, "wing.log")); err != nil {
		t.Fatal(err)
	}
	pair := northstarStartPTY(t, f, "/usr/bin/python3", 120, 36, pairScript)
	pair.waitScreen("two explicit cold requests and wing status", 0, func(s string) bool {
		return strings.Contains(s, "wing daemon is running (pid ") && strings.Count(s, "Error:") >= 2
	})
	onboardingStoryboardCapture(t, pair, "02-6")
	log, err := os.ReadFile(filepath.Join(f.local, "wing.log"))
	if err != nil || strings.Count(string(log), "wing: local control ready") != 1 {
		t.Fatalf("concurrent launches replaced daemon: %v %s", err, log)
	}

	stop := exec.Command(binary, "wing", "stop")
	stop.Env = f.env
	if output, err := stop.CombinedOutput(); err != nil {
		t.Fatalf("stop isolated cold-start fixture: %v %s", err, output)
	}
	northstarWrite(t, filepath.Join(f.bin, "codex"), "#!/bin/sh\necho 'fixture Codex binary'\n", 0700)
	// Populated navigator: pinned coordinator, colliding IDs/names and native
	// completion cursors. Fixture eggs speak the real gRPC attach protocol.
	pin := northstarStandaloneEgg(t, f, f.local, "coordinator", "wing", "working", "FIXTURE coordinator terminal")
	northstarWrite(t, filepath.Join(pin.dir, "session.role.json"), `{"pinned":true,"role":"coordinator"}`, 0600)
	local := northstarStandaloneEgg(t, f, f.local, "same", "pairing", "needs_input", "FIXTURE Claude pairing terminal\r\nCtrl+B then q returns to navigator")
	remote := northstarStandaloneEgg(t, f, f.remote, "same", "pairing", "working", "FIXTURE remote pairing terminal\r\nNative terminal modes and wheel input pass through")
	_ = remote
	northstarWrite(t, filepath.Join(f.local, "remotes.yaml"), fmt.Sprintf("remotes:\n  forge:\n    ssh_target: ns-forge\n    wingthing_dir: %s\n", f.remote), 0600)
	nav := northstarStartPTY(t, f, binary, 120, 36)
	nav.expect("qualified inventory with pin", 0, "wing", "pairing", "machines connected")
	onboardingStoryboardCapture(t, nav, "03-1")
	nav.expect("scoped new agent", nav.send("n\x15codex\r"), "Agent: Codex", "Working directory", f.work)
	onboardingStoryboardCapture(t, nav, "02-3")
	nav.expect("cancel scoped launch", nav.send("\x1b"), "wing")
	nav.expect("choose colliding local row", nav.send("j\r"), "FIXTURE Claude pairing terminal")
	onboardingStoryboardCapture(t, nav, "03-2")
	nav.expect("detach preserves session", nav.send("\x02q"), "wing", "pairing")
	// Completion is sourced from the actual durable lifecycle journal and remains
	// distinct from blocked. Native provider content is not part of the inventory.
	northstarWrite(t, filepath.Join(local.dir, "lifecycle.jsonl"), "{\"sequence\":1,\"type\":\"session_ready\",\"source\":\"claude_hook\",\"state\":\"needs_input\"}\n{\"sequence\":2,\"type\":\"turn_completed\",\"source\":\"claude_hook\",\"state\":\"completed\",\"reason\":\"native Stop observed for foreground response; other Stop hooks or later prompts may continue the conversation\"}\n", 0600)
	northstarWrite(t, filepath.Join(remote.dir, "lifecycle.jsonl"), "{\"sequence\":1,\"type\":\"session_ready\",\"source\":\"claude_hook\",\"state\":\"needs_input\"}\n", 0600)
	nav.expect("native unread marker", nav.send("R"), "1 unread", "pairing *", "wing")
	onboardingStoryboardCapture(t, nav, "03-3")
	nav.expect("qualified remote details", nav.send("j"), "wing")
	nav.expect("remote attachment modes", nav.send("\r"), "FIXTURE remote pairing terminal")
	onboardingStoryboardCapture(t, nav, "03-4")
	nav.expect("detach remote", nav.send("\x02q"), "wing", "machines connected")
	northstarWrite(t, filepath.Join(f.home, "forge-offline"), "fixture", 0600)
	nav.expect("retained stale remote", nav.send("R"), "forge unreachable", "Last seen", "wing")
	onboardingStoryboardCapture(t, nav, "03-6")
	os.Remove(filepath.Join(f.home, "forge-offline"))
	nav.expect("restore verified route", nav.send("R"), "machines connected")
	nav.expect("explicit qualified stop", nav.send("x"), "pairing on forge", "Enter keep running")
	onboardingStoryboardCapture(t, nav, "03-7")
	storyboardCapture(t, nav, "03-7-qualified-stop")
	nav.expect("enter cancels destructive action", nav.send("\r"), "machines connected")
	if remote.killed.Load() != 0 {
		t.Fatal("Enter stopped the colliding remote")
	}
	nav.exit("q")

	os.Remove(filepath.Join(f.bin, "codex"))
	// Generic Codex shortcut runs the same policy admission; errors preserve the
	// actual diagnostic instead of displaying a simulated successful provider.
	codex := northstarStartPTY(t, f, binary, 120, 36, "codex", "--cwd", f.work, "--name", "review")
	codex.expect("Codex native admission diagnostic", 0, "Error:")
	onboardingStoryboardCapture(t, codex, "02-4")

	// Read-only recent runs endpoint with durable fixture records; no relaunch.
	socket, err := controlsocket.Listen(t.Context(), f.local, "fixture-local", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{Principal: "fixture-owner"}, func(_ context.Context, req control.DirectRequest) control.DirectResponse {
			res := control.DirectResponse{Version: control.ContractVersion, ID: req.ID}
			switch req.Tool {
			case "wing_list":
				res.Result = map[string]any{"count": 1, "wings": []any{map[string]any{"wing_id": "fixture-local", "name": "local", "mcp_control": true}}}
			case "agent_list":
				res.Result = map[string]any{"wing_id": "fixture-local", "count": 1, "runs": []any{map[string]any{"wing_id": "fixture-local", "run_id": "recent-run", "session_id": "same", "status": "done", "agent": "fixture", "cwd": f.work, "created_at": time.Now().UTC().Format(time.RFC3339), "output_availability": "available", "terminal": true}}, "snapshot_revision": "fixture", "complete": true}
			default:
				res.Error = "fixture refuses mutation"
			}
			return res
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { socket.Close() })
	recent := northstarStartPTY(t, f, binary, 120, 36)
	recent.expect("live inventory before recent", 0, "wing")
	recent.expect("durable discovery", recent.send("h"), "Recent runs", "This machine", "+ Done")
	onboardingStoryboardCapture(t, recent, "03-5")
	recent.exit("q")
	t.Run("explicit_cwd_scoped_parent_transport", func(t *testing.T) {
		chosen := northstarNewFixture(t)
		os.Remove(filepath.Join(chosen.local, "remotes.yaml"))
		server, err := controlsocket.Listen(t.Context(), chosen.local, "fixture-cwd-wing", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
			return controlsocket.Welcome{}, func(_ context.Context, request control.DirectRequest) control.DirectResponse {
				var launch scopedNewLaunch
				if err := json.Unmarshal(request.Arguments, &launch); err != nil || request.Tool != "agent_start" || launch.CWD != chosen.work || launch.Agent != "claude" || !launch.ScopedMCP || launch.Role != "parent" {
					t.Errorf("cwd/scoped admission changed: %s %v", request.Arguments, err)
				}
				return control.DirectResponse{Version: control.ContractVersion, ID: request.ID, Result: map[string]any{"session": "chosen-parent"}}
			}, nil
		}, func(_ context.Context, hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.AttachHandler, error) {
			if hello.Attach.Session != "chosen-parent" {
				return controlsocket.Welcome{}, nil, fmt.Errorf("wrong chosen parent")
			}
			return controlsocket.Welcome{}, func(_ context.Context, stream *controlsocket.SessionStream) error {
				if err := stream.Send(&pb.SessionMsg{SessionId: "chosen-parent", Payload: &pb.SessionMsg_Output{Output: []byte("FIXTURE scoped Claude parent\r\nChosen cwd: " + chosen.work + "\r\nCtrl+B then q detaches without relaunch\r\n")}}); err != nil {
					return err
				}
				for {
					msg, err := stream.Recv()
					if err != nil {
						return err
					}
					if msg.GetDetach() {
						return nil
					}
				}
			}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { server.Close() })
		h := northstarStartPTY(t, chosen, binary, 120, 36, "claude", "--cwd", chosen.work, "--name", "chosen")
		h.expect("explicit workspace scoped parent", 0, "FIXTURE scoped Claude parent", "Chosen cwd: "+chosen.work)
		onboardingStoryboardCapture(t, h, "02-2")
		h.exit("\x02q")
	})
}
