package egg

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
)

const multiprocessingFixture = `
import json, multiprocessing, os, signal, sys, time
from multiprocessing import resource_tracker

def worker(ready):
    os.setsid()
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    ready.send(os.getpid())
    while True: time.sleep(1)

if __name__ == '__main__':
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    resource_tracker.ensure_running()
    ctx = multiprocessing.get_context('spawn')
    recv, send = ctx.Pipe(False)
    proc = ctx.Process(target=worker, args=(send,))
    proc.start()
    worker_pid = recv.recv()
    recv_fd, send_fd = os.pipe()
    middle = os.fork()
    if middle == 0:
        os.close(recv_fd)
        grandchild = os.fork()
        if grandchild == 0:
            os.setsid()
            os.write(send_fd, str(os.getpid()).encode())
            os.close(send_fd)
            while True: time.sleep(1)
        os.close(send_fd)
        if len(sys.argv) > 2 and sys.argv[2] == 'orphan': os._exit(0)
        while True: time.sleep(1)
    os.close(send_fd)
    detached = int(os.read(recv_fd, 64))
    os.close(recv_fd)
    if len(sys.argv) > 2 and sys.argv[2] == 'orphan': os.waitpid(middle, 0)
    with open(sys.argv[1] + '.tmp', 'w') as f:
        json.dump(dict(provider=os.getpid(), worker=worker_pid,
                       tracker=resource_tracker._resource_tracker._pid,
                       detached=detached, middle=middle), f)
    os.rename(sys.argv[1] + '.tmp', sys.argv[1])
    while True: time.sleep(1)
`

func waitFixturePIDs(t *testing.T, ready string) map[string]int {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if data, err := os.ReadFile(ready); err == nil {
			var pids map[string]int
			if err := json.Unmarshal(data, &pids); err != nil || len(pids) != 5 {
				t.Fatalf("fixture readiness: %s, %v", data, err)
			}
			return pids
		}
		select {
		case <-deadline.C:
			t.Fatal("multiprocessing fixture did not become ready")
		case <-ticker.C:
		}
	}
}

func assertFixtureGone(t *testing.T, pids map[string]int) {
	t.Helper()
	snapshot, err := processSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for kind, pid := range pids {
		if process, alive := snapshot[pid]; alive && !process.zombie {
			t.Errorf("%s survived: %+v", kind, process)
		}
	}
}

// Crosses the real PTY/server lifecycle, rather than only a fake Kill callback.
// On Linux, this acceptance gate requires actual writable cgroup delegation.
func TestSessionContainmentStopAndTimeout(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("missing fixture: Python 3 multiprocessing runtime")
	}
	if runtime.GOOS == "linux" {
		cg, err := newTestCgroup()
		if err != nil {
			t.Skipf("unsupported: cgroup v2 delegation unavailable: %v", err)
		}
		if err := cg.close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []string{"stop", "timeout", "provider-exit"} {
		t.Run(action, func(t *testing.T) {
			dir := shortEndpointTempDir(t)
			home := t.TempDir()
			t.Setenv("WINGTHING_DIR", home)
			ready := filepath.Join(home, "ready.json")
			script := filepath.Join(home, "provider.py")
			if err := os.WriteFile(script, []byte(multiprocessingFixture), 0600); err != nil {
				t.Fatal(err)
			}
			server, err := NewServer(dir)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			exited := make(chan error, 1)
			go func() {
				exited <- server.RunSession(ctx, RunConfig{Command: []string{python, script, ready}, CWD: home,
					UserHome: home, OuterBoundary: true, Network: []string{"*"}, Rows: 24, Cols: 80, OmitBrowserBridge: true})
			}()
			t.Cleanup(cancel)
			pids := waitFixturePIDs(t, ready)
			// Capture identities before allowing any process to exit, including
			// safe fixture cleanup if an assertion fails.
			snapshot, err := processSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				for _, pid := range pids {
					if p, ok := snapshot[pid]; ok {
						_ = signalIdentifiedProcess(p, syscall.SIGKILL)
					}
				}
			})
			server.mu.RLock()
			sess := server.session
			server.mu.RUnlock()
			if sess == nil {
				t.Fatal("provider became ready before session publication")
			}
			if runtime.GOOS == "linux" && sess.processTree.boundary == nil {
				t.Fatal("delegation is available but the egg did not create a cgroup")
			}
			sess.processTree.observe(snapshot)
			switch action {
			case "stop":
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 12*time.Second)
				defer stopCancel()
				if _, err := server.Kill(stopCtx, &pb.KillRequest{}); err != nil {
					t.Fatal(err)
				}
			case "timeout":
				fixture := newRunFixture(t)
				fixture.runtime.backend.Kill = func() ([]RunDescendant, error) { return sess.processTree.kill(sess) }
				fixture.request.Deadline = time.Now().Add(-time.Second)
				if _, err := fixture.runtime.submit(fixture.request); err != nil {
					t.Fatal(err)
				}
				result := waitRunFixture(t, fixture)
				if result.Status != "timeout" || result.ContainmentError != "" || len(result.SurvivingDescendants) != 0 {
					t.Fatalf("timeout cleanup: %+v", result)
				}
			case "provider-exit":
				if err := signalIdentifiedProcess(snapshot[pids["provider"]], syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-exited:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("egg did not finish descendant cleanup")
			}
			assertFixtureGone(t, pids)
		})
	}
}

func TestProcessTreeRejectsReusedIdentities(t *testing.T) {
	tree := &runProcessTree{root: 100, rootStart: "original", seen: map[int]observedProcess{
		200: {RunDescendant: RunDescendant{200, 100, 200}, start: "old-child", session: 200},
	}}
	tree.observe(map[int]observedProcess{
		100: {RunDescendant: RunDescendant{100, 1, 100}, start: "reused-root", session: 100},
		200: {RunDescendant: RunDescendant{200, 1, 200}, start: "reused-child", session: 200},
		300: {RunDescendant: RunDescendant{300, 100, 100}, start: "unrelated", session: 100},
		400: {RunDescendant: RunDescendant{400, 200, 200}, start: "unrelated", session: 200},
	})
	if len(tree.seen) != 1 || tree.seen[200].start != "old-child" {
		t.Fatalf("reused PID/group/session was attributed to provider: %+v", tree.seen)
	}
}

func TestIdentifiedSignalDoesNotKillReusedPID(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	snapshot, err := processSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	p := snapshot[cmd.Process.Pid]
	p.start = "different incarnation"
	if err := signalIdentifiedProcess(p, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("mismatched identity was killed: %v", err)
	}
}
