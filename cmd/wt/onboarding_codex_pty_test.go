package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
)

// The CLI, egg, sandbox and two PTYs are real; only the account-free provider
// is a fixture. The selected policy stays in force on launch and reattachment.
func TestOnboardingCodexPTYSession(t *testing.T) {
	if ok, reason := sandbox.CheckCapability(); !ok {
		t.Skip("REQUIRED-UNRUN: native sandbox unavailable: " + reason)
	}
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(f.home, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(private, "fixture.txt"), "fixture-only", 0600)
	provider := fmt.Sprintf(`#!%s
import os, sys, tty
args=sys.argv[1:]
if args==['--version']:
 print('codex-cli 0.159.3'); sys.exit(0)
assert '--no-daemon' in args
assert args[args.index('-m')+1]=='fixture-onboarding'
assert args.count('fixture-prompt')==1, repr(args)
try:
 with open(os.path.join(os.environ['HOME'],'private','fixture.txt')): pass
except (PermissionError,FileNotFoundError): pass
else: raise RuntimeError('selected policy did not deny private fixture')
tty.setraw(0)
os.write(1,b'OpenAI Codex - isolated fixture provider\r\nPOLICY_DENIED_PRIVATE\r\n')
os.write(1,('CWD:'+os.getcwd()+'\r\nMODEL:fixture-onboarding\r\nCODEX_READY\r\n').encode())
line=b''
while True:
 c=os.read(0,1)
 if not c: break
 if c not in (b'\r',b'\n'): line+=c; continue
 if line==b'exit': break
 if line==b'fail':
  os.write(1,b'\x1b[?1000h\x1b[?1006h\x1b[?2004hFIXTURE_PROVIDER_FAILED\r\n')
  sys.exit(7)
 if line==b'enable':
  os.write(1,b'\x1b[?1000h\x1b[?1006h\x1b[?2004hMOUSE_MODES_ENABLED\r\n')
 else:
  os.write(1,b'CODEX_INPUT:'+line+b'\r\nCODEX_BYTES:'+line.hex().encode()+b'\r\n')
 line=b''
`, python)
	northstarWrite(t, filepath.Join(f.bin, "codex"), provider, 0700)
	policy := filepath.Join(f.work, "egg.yaml")
	northstarWrite(t, policy, fmt.Sprintf("base: none\nfs:\n  - rw:%s\n  - deny:%s\nnetwork: none\nshell: /bin/sh\n", f.work, private), 0600)
	policyBefore, err := os.ReadFile(policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		entries, _ := os.ReadDir(filepath.Join(f.local, "eggs"))
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if t.Failed() {
				testprovider.LogEggTails(t, filepath.Join(f.local, "eggs", entry.Name()))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := eggclient.KillOrphanEggContext(ctx, &config.Config{Dir: f.local}, entry.Name())
			cancel()
			if err != nil {
				t.Errorf("clean owned Codex fixture: %v", err)
			}
		}
		if after, err := os.ReadFile(policy); err != nil || string(after) != string(policyBefore) {
			t.Errorf("Codex launch changed selected policy: %v", err)
		}
	})
	h := northstarStartPTY(t, f, binary, 120, 36, "codex", "--cwd", f.work, "--name", "onboarding-codex", "--", "-m", "fixture-onboarding", "fixture-prompt")
	h.expect("sandboxed Codex ready in selected cwd", 0, "CODEX_READY", "CWD:"+f.work, "POLICY_DENIED_PRIVATE")
	h.expect("provider receives interactive input", h.send("hello\r"), "CODEX_INPUT:hello")
	storyboardCapture(t, h, "02-4")
	h.expect("native provider enables mouse and paste", h.send("enable\r"), "MOUSE_MODES_ENABLED")
	h.waitNativeBytes("\x1b[?1000h\x1b[?1006h\x1b[?2004h")
	h.expect("native wheel reaches provider", h.send("\x1b[<64;5;3M\r"), "CODEX_BYTES:1b5b3c36343b353b334d")
	h.expect("bracketed paste reaches provider", h.send("\x1b[200~pasted\x1b[201~\r"), "CODEX_BYTES:1b5b3230307e7061737465641b5b3230317e")
	storyboardCapture(t, h, "03-4")
	ids := coldSessionIDs(t, f)
	if len(ids) != 1 {
		t.Fatalf("Codex shortcut admitted %d sessions: %v", len(ids), ids)
	}
	h.exit("\x02q")
	h.waitNativeBytes(string(egg.NativeTerminalReset()))
	reattach := northstarStartPTY(t, f, binary, 120, 36, "attach", ids[0])
	reattach.expect("same Codex session survives detach", 0, "CODEX_INPUT:hello")
	reattach.waitNativeBytes("\x1b[?1000h\x1b[?1006h")
	reattach.expect("reattached input reaches same provider", reattach.send("again\r"), "CODEX_INPUT:again")
	reattach.exit("\x02q")
	if got := coldSessionIDs(t, f); len(got) != 1 || got[0] != ids[0] {
		t.Fatalf("reattachment replaced session: %v", got)
	}
	meta, err := os.ReadFile(filepath.Join(f.local, "eggs", ids[0], "egg.meta"))
	if err != nil || !strings.Contains(string(meta), "agent=codex") || !strings.Contains(string(meta), "isolation=wingthing-sandbox") {
		t.Fatalf("Codex fixture did not retain native sandbox identity: %s %v", meta, err)
	}
	healthyPID, alive := eggclient.ReadAliveEggPID(filepath.Join(f.local, "eggs", ids[0]))
	if !alive {
		t.Fatal("detached healthy fixture is not alive")
	}
	failed := northstarStartPTY(t, f, binary, 120, 36, "codex", "--cwd", f.work, "--name", "onboarding-failure", "--", "-m", "fixture-onboarding", "fixture-prompt")
	failed.expect("second fixture ready", 0, "CODEX_READY")
	failed.expect("provider failure returned without relaunch", failed.send("fail\r"), "FIXTURE_PROVIDER_FAILED", "agent exited with code 7")
	coldPTYExit(t, failed, true)
	failed.waitNativeBytes(string(egg.NativeTerminalReset()))
	if pid, alive := eggclient.ReadAliveEggPID(filepath.Join(f.local, "eggs", ids[0])); !alive || pid != healthyPID {
		t.Fatal("failed provider replaced or stopped the admitted healthy session")
	}
	// The failed egg may still be joining its recorder after the CLI observes
	// the provider exit; process liveness alone is not provider readiness.
	if got := coldSessionIDs(t, f); len(got) > 2 {
		t.Fatalf("failed provider admitted replacement work: %v", got)
	}
}
