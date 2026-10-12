package main

// Linux fixture adaptation of TestNorthstarPTYLaunchFailure. All journey assertions are retained;
// original Northstar tests are unchanged. See test/storyboard/README.md.
import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func storyboardLaunchFailure(t *testing.T) {
	buildFixture := northstarNewFixture(t)
	built := storyboardBinary(t, buildFixture)
	t.Run("transport", func(t *testing.T) {
		f := northstarNewFixture(t)
		stateAlias := filepath.Join(f.home, "s")
		if err := os.Symlink(f.local, stateAlias); err != nil {
			t.Fatal(err)
		}
		f.local = stateAlias
		f.env = append(f.env, "WINGTHING_DIR="+stateAlias)
		if !strings.HasPrefix(f.local, f.home+string(filepath.Separator)) || strings.HasPrefix(f.local, f.work+string(filepath.Separator)) {
			t.Fatal("state/mailbox authority left protected HOME")
		}
		binary := coldInstallBinary(t, f, built)
		if err := os.Remove(filepath.Join(f.local, "remotes.yaml")); err != nil {
			t.Fatal(err)
		}
		nested := filepath.Join(f.work, "nested")
		if err := os.Mkdir(nested, 0700); err != nil {
			t.Fatal(err)
		}
		provider := filepath.Join(f.bin, "claude")
		northstarWrite(t, provider, "#!/bin/sh\nprintf 'UNEXPECTED_PROVIDER_EXECUTION'\nexit 99\n", 0700)
		inherited := filepath.Join(f.home, "inherited.yaml")
		selected := filepath.Join(f.work, "egg.yaml")
		northstarWrite(t, inherited, "base: none\nfs: ['ro:/', 'rw:"+f.work+"', 'deny:"+provider+"']\n", 0600)
		northstarWrite(t, selected, "base: "+inherited+"\nenv: [HOME, PATH]\n", 0600)
		// A writable nested policy must not replace the configured root's denial.
		northstarWrite(t, filepath.Join(nested, "egg.yaml"), "base: none\nfs: ['rw:/']\n", 0600)
		f.env = append(f.env, "EXCLUDED_PROMPT="+launchSecret)
		t.Setenv("HOME", f.home)
		t.Setenv("WINGTHING_DIR", f.local)
		t.Setenv("TMPDIR", f.tmp)
		wc := &config.WingConfig{WingID: "fixture-wing", Paths: config.PathList{{Path: f.work}}}
		if err := config.SaveWingConfig(f.local, wc); err != nil {
			t.Fatal(err)
		}
		var starts atomic.Int32

		handler := func(_ context.Context, request control.DirectRequest) control.DirectResponse {
			response := control.DirectResponse{Version: control.ContractVersion, ID: request.ID}
			if request.Tool != "agent_start" {
				response.Result = map[string]any{"sessions": []any{}}
				return response
			}
			starts.Add(1)
			var launch scopedNewLaunch
			if err := json.Unmarshal(request.Arguments, &launch); err != nil || !launch.ScopedMCP || launch.Role != "parent" {
				t.Error("scoped parent request lost")
			}
			start := ws.PTYStart{CWD: launch.CWD, UserID: "fixture-owner", Email: "fixture-owner", OrgRole: "owner"}
			selectedConfig, _, err := eggclient.PrepareMCPLaunch(wc, &start, f.home, false, egg.DefaultEggConfig())
			if err != nil {
				response.Result = control.ErrorResult(err)
				response.IsError = true
				return response
			}
			policy := selectedConfig.LaunchPolicy
			if policy == nil || policy.Path != selected || start.CWD != nested {
				t.Fatalf("selected config/cwd lost: %+v cwd=%s", policy, start.CWD)
			}
			// Admission request UUIDs are separate from the compact runtime IDs
			// used for egg routing and Darwin's bounded Unix socket paths.
			dir := filepath.Join(f.local, "eggs", cmdutil.NewRuntimeID())
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			northstarSocket(t, filepath.Join(dir, "egg.sock"))
			mirror, err := os.MkdirTemp(f.tmp, "dry-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(mirror)
			mirror, err = filepath.EvalSymlinks(mirror)
			if err != nil {
				t.Fatal(err)
			}
			state, err := egg.NewDryLaunchState(mirror)
			if err != nil {
				t.Fatal(err)
			}
			rc := egg.RunConfig{Agent: "claude", CWD: start.CWD, UserHome: f.home, FS: selectedConfig.FS, AgentArgs: launch.Args}
			prepared, err := egg.PrepareLaunch(rc, egg.LaunchPreparationOptions{Binary: provider, Home: f.home, HostHome: f.home, EggDir: dir, Environment: map[string]string{"HOME": f.home, "EXCLUDED_PROMPT": launchSecret}, CodexCapability: egg.SavedCodexCapability(""), Requirements: &egg.LaunchRequirements{PolicyBase: f.work, Rules: policy.Rules}}, state)
			if err == nil {
				err = errors.New(launchSecret)
			}
			response.Result = control.ErrorResult(egg.ProjectLaunchFailure(err, policy, prepared, "admission/policy", "preparation_failed"))
			response.IsError = true
			return response
		}
		path, err := controlsocket.Path(f.local)
		if err != nil {
			t.Fatal(err)
		}
		northstarSocket(t, path)
		bindState, err := filepath.EvalSymlinks(f.local)
		if err != nil {
			t.Fatal(err)
		}
		// Existing wing binds the protected physical root; the launching client
		// traverses its alias. Keep the server's no-symlink admission rule intact.
		server, err := controlsocket.Listen(t.Context(), bindState, "fixture", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
			return controlsocket.Welcome{}, handler, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		h := northstarStartPTY(t, f, binary, 240, 35, "claude", "--cwd", nested, "--", launchSecret)
		h.expect("CLI phase/provenance/action", 0, "admission/policy", "provider executable", selected, inherited, "Doctor launch-plan", "--home", "--executable")
		coldPTYExit(t, h, true)
		if starts.Load() != 1 {
			t.Fatalf("CLI admission repeated: %d", starts.Load())
		}
		launchFailureNoSecrets(t, h, f.local)
		dashboard := launchFailureDashboard(t, f, binary, nested)
		dashboard.expect("dashboard policy provenance", 0, selected, inherited)
		if starts.Load() != 2 {
			t.Fatalf("dashboard admission repeated: %d", starts.Load())
		}
		dashboard.exit("q")
		launchFailureNoSecrets(t, dashboard, f.local)
		if ids := coldSessionIDs(t, f); len(ids) != 0 {
			t.Fatalf("denied request left live eggs: %v", ids)
		}
	})
	t.Run("remote", func(t *testing.T) {
		for _, admitted := range []bool{false, true} {
			name := "policy"
			if admitted {
				name = "attach"
			}
			t.Run(name, func(t *testing.T) {
				f := northstarNewFixture(t)
				binary, state, cwd := scopedNewMachine(t, f, built, "forge")
				python, err := exec.LookPath("python3")
				if err != nil {
					t.Fatal(err)
				}
				// Allocate the receiving terminal, as real ssh -t does. The fake
				// transport still rejects every target except the fixture host.
				wrapper := filepath.Join(f.home, "ssh-pty.py")
				northstarWrite(t, wrapper, "import os, pty, sys\nsys.exit(os.waitstatus_to_exitcode(pty.spawn(['/bin/sh', '-c', sys.argv[1]])))\n", 0600)
				northstarWrite(t, filepath.Join(f.bin, "ssh"), `#!/bin/sh
mode=$1
case "$mode" in -t|-T) shift ;; *) exit 90 ;; esac
[ "$1" = -o ] && shift 2
[ "$1" = ns-forge ] || exit 91
shift
printf '%s\n%s\n' "$mode" "$1" > "$NORTHSTAR_SCOPED_SSH_LOG/$$"
if [ "$mode" = -t ]; then exec "$NORTHSTAR_PTY_PYTHON" "$NORTHSTAR_PTY_SSH_WRAPPER" "$1"; fi
exec /bin/sh -c "$1"
`, 0700)
				f.env = append(f.env, "NORTHSTAR_PTY_PYTHON="+python, "NORTHSTAR_PTY_SSH_WRAPPER="+wrapper)
				selected := filepath.Join(f.home, "remote-policy.yaml")
				inherited := filepath.Join(f.home, "remote-base.yaml")
				northstarWrite(t, inherited, "base: none\nfs: ['deny:/fixture/provider']\n", 0600)
				northstarWrite(t, selected, "base: "+inherited+"\n", 0600)
				var starts atomic.Int32
				server, err := controlsocket.Listen(t.Context(), state, "fixture", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
					return controlsocket.Welcome{}, func(_ context.Context, request control.DirectRequest) control.DirectResponse {
						response := control.DirectResponse{Version: control.ContractVersion, ID: request.ID, Result: map[string]any{"sessions": []any{}}}
						if request.Tool != "agent_start" {
							return response
						}
						starts.Add(1)
						if admitted {
							response.Result = map[string]any{"session": "admitted-parent"}
						} else {
							response.IsError = true
							response.Result = control.ErrorResult(control.WithLaunchDiagnostic(errors.New(launchSecret), control.LaunchDiagnostic{Stage: "admission/policy", Reason: "executable_denied", PolicyOrigin: "selected policy", PolicyPath: selected, PolicySource: inherited}))
						}
						return response
					}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				defer server.Close()
				h := northstarStartPTY(t, f, binary, 120, 35)
				h.expect("remote navigator ready", 0, "wt |", "n new")
				h.expect("select fixture remote", h.send("mjj\r"), "forge", "(no sessions)")
				h.expect("remote provider prompt", h.send("n"), "Agent (blank = shell)")
				h.expect("remote cwd prompt", h.send("\x15claude\r"), "Cwd on forge:")
				mark := h.send("\x15" + cwd + "\r")
				if admitted {
					h.expect("remote exact reattach returned to navigator", mark, "wt |", "attach-after-admission", "wt attach forge:admitted-parent")
				} else {
					h.expect("remote policy returned to navigator", mark, "wt |", "admission/policy", selected, inherited, "Doctor launch-plan")
				}
				if starts.Load() != 1 {
					t.Fatalf("remote launch repeated: %d", starts.Load())
				}
				scopedNewAssertSSH(t, f, state, cwd)
				h.exit("q")
				launchFailureNoSecrets(t, h, state)
			})
		}
	})
	t.Run("success", func(t *testing.T) {
		f := northstarNewFixture(t)
		binary := coldInstallBinary(t, f, built)
		var starts atomic.Int32
		server, err := controlsocket.Listen(t.Context(), f.local, "fixture", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
			return controlsocket.Welcome{}, func(_ context.Context, req control.DirectRequest) control.DirectResponse {
				if req.Tool != "agent_start" {
					return control.DirectResponse{Version: control.ContractVersion, ID: req.ID, Result: map[string]any{"sessions": []any{}}}
				}
				starts.Add(1)
				var launch scopedNewLaunch
				if err := json.Unmarshal(req.Arguments, &launch); err != nil || !launch.ScopedMCP {
					t.Error("scoped launch lost")
				}
				return control.DirectResponse{Version: control.ContractVersion, ID: req.ID, Result: map[string]any{"session": "successful-parent"}}
			}, nil
		}, func(context.Context, controlsocket.Hello) (controlsocket.Welcome, controlsocket.AttachHandler, error) {
			return controlsocket.Welcome{}, func(_ context.Context, stream *controlsocket.SessionStream) error {
				if err := stream.Send(&pb.SessionMsg{SessionId: "successful-parent", Payload: &pb.SessionMsg_Output{Output: []byte("SUCCESSFUL_PROVIDER_READY\r\n")}}); err != nil {
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
		defer server.Close()
		h := northstarStartPTY(t, f, binary, 120, 30, "claude")
		screen := h.expect("successful launch quiet", 0, "SUCCESSFUL_PROVIDER_READY")
		if strings.Contains(screen, "Policy:") || strings.Contains(screen, "Doctor launch-plan") || strings.Contains(screen, "launch failed") {
			t.Fatal("success emitted diagnostic noise")
		}
		h.exit("\x02q")
		if starts.Load() != 1 {
			t.Fatalf("success launched twice: %d", starts.Load())
		}
	})
	t.Run("native", func(t *testing.T) {
		coldRequireSandbox(t)
		f := coldFixture(t)
		storyboardWriteLinuxPolicy(t, f, f.local)
		binary := coldInstallBinary(t, f, built)
		coldCleanup(t, f, binary)
		storyboardStateEvidence(t, f, f.local)
		nested := filepath.Join(f.work, "nested")
		if err := os.Mkdir(nested, 0700); err != nil {
			t.Fatal(err)
		}
		inherited := filepath.Join(f.home, "inherited.yaml")
		selected := filepath.Join(f.work, "egg.yaml")
		policy, err := os.ReadFile(filepath.Join(f.local, "egg.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		northstarWrite(t, inherited, string(policy), 0600)
		northstarWrite(t, selected, "base: "+inherited+"\nfs: ['deny:"+filepath.Join(f.bin, "claude")+"']\n", 0600)
		f.env = append(f.env, "EXCLUDED_PROMPT="+launchSecret)
		h := northstarStartPTY(t, f, binary, 240, 35, "claude", "--cwd", nested, "--", launchSecret)
		h.expect("native denial", 0, "admission/policy", "provider executable", selected, "Doctor launch-plan")
		coldPTYExit(t, h, true)
		launchFailureNoSecrets(t, h, f.local)
		if count := launchFailureSessionCount(t, f.local); count != 1 {
			t.Fatalf("native launch count: %d", count)
		}
		dashboard := launchFailureDashboard(t, f, binary, nested)
		dashboard.exit("q")
		launchFailureNoSecrets(t, dashboard, f.local)
		if count := launchFailureSessionCount(t, f.local); count != 2 {
			t.Fatalf("native dashboard launch count: %d", count)
		}
		if ids := coldSessionIDs(t, f); len(ids) != 0 {
			t.Fatalf("denial left live eggs: %v", ids)
		}
		if err := os.Remove(selected); err != nil {
			t.Fatal(err)
		}
		success := northstarStartPTY(t, f, binary, 120, 30, "claude", "--cwd", nested)
		screen := success.expect("native success quiet", 0, "COLD_CLAUDE_READY")
		if strings.Contains(screen, "Doctor launch-plan") || strings.Contains(screen, "Policy:") {
			t.Fatal("native success emitted diagnostic noise")
		}
		success.exit("\x02q")
	})
}
