package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
	"google.golang.org/grpc"
)

type northstarInventorySSHRecord struct {
	northstarSSHRecord
	PID    int    `json:"pid"`
	TMPDIR string `json:"tmpdir"`
}

func init() {
	if len(os.Args) > 1 && os.Args[1] == "northstar-inventory-ssh" {
		os.Exit(northstarInventorySSH(os.Args[2:]))
	}
}

func northstarInventorySSH(args []string) int {
	if len(args) != 5 || args[0] != "-T" || args[1] != "-o" || args[2] != "BatchMode=yes" {
		return 91
	}
	r := northstarInventorySSHRecord{northstarSSHRecord: northstarSSHRecord{Target: args[3], Mode: args[0], Batch: true, Command: args[4]}, PID: os.Getpid(), TMPDIR: os.Getenv("TMPDIR")}
	if r.Target != "ns-healthy" && r.Target != "ns-offline" && r.Target != "ns-slow" {
		return 92
	}
	data, _ := json.Marshal(r)
	if err := os.WriteFile(filepath.Join(os.Getenv("NORTHSTAR_INVENTORY_MAILBOX"), fmt.Sprintf("ssh-%d.json", r.PID)), data, 0600); err != nil {
		return 93
	}
	if strings.Contains(r.Command, "'--version'") && r.Target == "ns-healthy" {
		fmt.Fprintln(os.Stdout, "wt version fixture")
		return 0
	}
	if !strings.Contains(r.Command, "'session' 'ps' '--json' '--remote-inventory'") && !strings.Contains(r.Command, "'--version'") {
		return 94
	}
	switch r.Target {
	case "ns-offline":
		fmt.Fprintln(os.Stderr, "fixture offline")
		return 255
	case "ns-slow":
		// Keep both SSH and its output reader outstanding until cancellation.
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
		defer signal.Stop(signals)
		fmt.Fprint(os.Stdout, "{")
		<-signals
		return 0
	default:
		returnCode := 0
		err := json.NewEncoder(os.Stdout).Encode(eggclient.RemoteSessionInventory{Version: "fixture", ContractVersion: eggclient.RemoteSessionContractVersion,
			Sessions: []eggclient.LocalSession{{ID: "remote", Name: "healthy-remote", Kind: "agent", Agent: "claude", Status: "working", PID: os.Getpid(), CWD: "/fixture"}}})
		if err != nil {
			returnCode = 95
		}
		return returnCode
	}
}

type northstarInventoryEgg struct {
	pb.UnimplementedEggServer
	slow                bool
	active, peak, calls *atomic.Int32
	started             chan struct{}
}

func (f northstarInventoryEgg) Status(ctx context.Context, _ *pb.StatusRequest) (*pb.StatusResponse, error) {
	f.calls.Add(1)
	n := f.active.Add(1)
	defer f.active.Add(-1)
	for old := f.peak.Load(); n > old; old = f.peak.Load() {
		if f.peak.CompareAndSwap(old, n) {
			break
		}
	}
	select {
	case f.started <- struct{}{}:
	default:
	}
	if f.slow {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &pb.StatusResponse{Readers: 2, TotalWritten: 12}, nil
}

func northstarInventoryWait(t *testing.T, label string, ready func() bool) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-timer.C:
			t.Fatalf("inventory event timeout: %s", label)
		case <-ticker.C:
		}
	}
}

func northstarInventoryRecords(t *testing.T, mailbox string) []northstarInventorySSHRecord {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(mailbox, "ssh-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var records []northstarInventorySSHRecord
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var record northstarInventorySSHRecord
		if json.Unmarshal(data, &record) == nil {
			records = append(records, record)
		}
	}
	return records
}

func TestNorthstarPTYInventoryBudget(t *testing.T) {
	t.Run("symlinked-TMPDIR", func(t *testing.T) {
		repo, err := filepath.Abs("../..")
		if err != nil {
			t.Fatal(err)
		}
		// Use 06's short IDs, then assert the actual canonical bind addresses.
		// The shared helper's full-runtime-ID reservation exceeds this clone's
		// path budget, but these fixture endpoints fit without relocation.
		home, local, remote := testprovider.ScopedHomeDirs(t, "../../.scratch")
		home, _ = filepath.Abs(home)
		local, _ = filepath.Abs(local)
		remote, _ = filepath.Abs(remote)
		root, err := os.MkdirTemp(filepath.Join(repo, ".scratch"), "inventory-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(root) })
		f := &northstarFixture{repo: repo, root: root, home: home, local: local, remote: remote, work: filepath.Join(root, "work"), tmp: filepath.Join(root, "tmp-alias"),
			evidence: filepath.Join(repo, ".scratch", "northstar", "inventory-evidence")}
		f.bin = filepath.Join(f.tmp, "bin")
		if err := os.Mkdir(filepath.Join(root, "tmp-real"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "tmp-real"), f.tmp); err != nil {
			t.Fatal(err)
		}
		mailbox := filepath.Join(home, "mailbox")
		for _, dir := range []string{f.bin, f.work, mailbox, f.evidence} {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
		}
		f.env = []string{"HOME=" + home, "WINGTHING_DIR=" + local, "TMPDIR=" + f.tmp, "PATH=" + f.bin + ":/usr/bin:/bin:/usr/sbin:/sbin", "TERM=xterm-256color", "LANG=en_US.UTF-8", "NORTHSTAR_INVENTORY_MAILBOX=" + mailbox}
		binary := northstarBuild(t, f, "stable")
		if !strings.HasPrefix(binary, f.tmp+string(filepath.Separator)) {
			t.Fatal("wt did not launch through symlinked TMPDIR")
		}
		if strings.HasPrefix(local, f.work+"/") || strings.HasPrefix(mailbox, f.work+"/") || !strings.HasPrefix(local, home+"/") {
			t.Fatal("fixture state/mailbox escaped protected HOME")
		}
		for _, state := range []string{local, remote} {
			socket, err := controlsocket.Path(state)
			if err != nil {
				t.Fatal(err)
			}
			northstarSocket(t, socket)
		}
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		northstarWrite(t, filepath.Join(f.bin, "ssh"), "#!/bin/sh\nexec "+remotepkg.ShellQuote(exe)+" northstar-inventory-ssh \"$@\"\n", 0700)
		northstarWrite(t, filepath.Join(local, "remotes.yaml"), "remotes:\n  healthy:\n    ssh_target: ns-healthy\n  offline:\n    ssh_target: ns-offline\n  slow:\n    ssh_target: ns-slow\n", 0600)
		identity := exec.CommandContext(t.Context(), "/bin/cat")
		identity.Env, identity.Dir = f.env, f.work
		input, err := identity.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := identity.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = input.Close(); _ = identity.Wait() })
		var active, peak, calls atomic.Int32
		started := make(chan struct{}, 100)
		for n := range 20 {
			dir := filepath.Join(local, "eggs", fmt.Sprintf("s%02d", n))
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			for file, value := range map[string]string{"egg.pid": strconv.Itoa(identity.Process.Pid), "egg.token": "fixture", "session.name": fmt.Sprintf("local-%02d", n),
				"egg.meta": "kind=agent\nagent=claude\nprovider_home=" + home + "\ncwd=" + f.work + "\n"} {
				northstarWrite(t, filepath.Join(dir, file), value, 0600)
			}
			// Native lifecycle evidence is deliberately absent for slow eggs.
			if n < 12 {
				northstarWrite(t, filepath.Join(dir, "lifecycle.jsonl"), "{\"sequence\":1,\"type\":\"session_ready\",\"source\":\"claude_hook\",\"state\":\"working\"}\n", 0600)
			}
			listener, err := net.Listen("unix", northstarSocket(t, filepath.Join(dir, "egg.sock")))
			if err != nil {
				t.Fatal(err)
			}
			rpc := grpc.NewServer()
			pb.RegisterEggServer(rpc, northstarInventoryEgg{slow: n >= 12, active: &active, peak: &peak, calls: &calls, started: started})
			done := make(chan struct{})
			go func() { defer close(done); _ = rpc.Serve(listener) }()
			t.Cleanup(func() { rpc.Stop(); _ = listener.Close(); <-done })
		}
		start := time.Now()
		h := northstarStartPTY(t, f, binary, 120, 40)
		h.expect("first frame", 0, "R refresh", "q quit")
		firstFrame := time.Since(start)
		northstarInventoryWait(t, "outstanding local probes", func() bool { return active.Load() > 0 })
		reactionStart := time.Now()
		rev := h.send("/")
		h.expect("filter while inventory waits", rev, "Search sessions")
		keyReaction := time.Since(reactionStart)
		rev = h.send("\x1b")
		h.expect("leave filter", rev, "R refresh")
		h.expect("healthy local metadata", h.send("/local-19\r"), "local-19", "? Unknown")
		localReady := time.Since(start)
		phaseMetrics := fmt.Sprintf("first_frame=%s local_metadata=%s key_response=%s\n", firstFrame, localReady, keyReaction)
		northstarWrite(t, h.artifact+".phases", phaseMetrics, 0600)
		t.Log(strings.TrimSpace(phaseMetrics))
		h.expect("healthy remote independent of slow SSH", h.send("/\x15healthy-remote\r"), "healthy-remote", "offline could not be searched")
		northstarInventoryWait(t, "one SSH per machine", func() bool { return len(northstarInventoryRecords(t, mailbox)) >= 3 })
		records := northstarInventoryRecords(t, mailbox)
		// Initial dispatch has one request per saved machine. Later refreshes may
		// finish while the slow local probes and PTY assertions are still waiting.
		initialTargets := map[string]bool{}
		for _, record := range records[:3] {
			if initialTargets[record.Target] {
				t.Fatalf("duplicate initial SSH dispatch: %+v", records[:3])
			}
			initialTargets[record.Target] = true
		}
		for _, record := range records {
			if !record.Batch || record.Mode != "-T" || record.TMPDIR != f.tmp || strings.Contains(record.Command, "'--version'") {
				t.Fatalf("bad inventory route: %+v", record)
			}
		}
		rev = h.send("/\x15local-1\r")
		h.expect("filter local metadata", rev, "> local-10")
		// Start a second local query and coalesce repeated R while slow SSH
		// remains in flight. Quit with RPC and SSH readers still outstanding.
		rev = h.send("RRRR")
		h.expect("explicit refresh", rev, "R refresh")
		northstarInventoryWait(t, "new local status RPC", func() bool { return active.Load() > 0 && calls.Load() > 20 })
		rev = h.send("j")
		h.expect("navigate while refresh waits", rev, "> local-11")
		h.exit("q")
		northstarInventoryWait(t, "all status readers canceled", func() bool { return active.Load() == 0 })
		records = northstarInventoryRecords(t, mailbox)
		counts := map[string]int{}
		for _, record := range records {
			counts[record.Target]++
			if err := syscall.Kill(record.PID, 0); err != syscall.ESRCH {
				t.Fatalf("SSH query child not joined: pid=%d err=%v", record.PID, err)
			}
		}
		if counts["ns-slow"] != 1 {
			t.Fatalf("R overlapped slow machine: %v", counts)
		}
		if peak.Load() > 8 {
			t.Fatalf("unbounded local query concurrency: %d", peak.Load())
		}
		// Close the test PTY reader explicitly as well as the CLI's reader.
		_ = h.master.Close()
		_ = h.slave.Close()
		select {
		case <-h.readDone:
		case <-time.After(time.Second):
			t.Fatal("PTY output reader did not join")
		}
		metrics := fmt.Sprintf("first_frame=%s local_metadata=%s key_response=%s status_calls=%d peak_status=%d ssh_subprocesses=%v\n", firstFrame, localReady, keyReaction, calls.Load(), peak.Load(), counts)
		northstarWrite(t, h.artifact+".metrics", metrics, 0600)
		t.Log(strings.TrimSpace(metrics))
		for _, target := range []struct {
			name      string
			got, want time.Duration
		}{{"first frame", firstFrame, 250 * time.Millisecond}, {"healthy metadata", localReady, time.Second}, {"key response", keyReaction, 100 * time.Millisecond}} {
			if target.got > target.want {
				t.Logf("LATENCY MISS: %s %s > %s", target.name, target.got, target.want)
			}
		}
	})
}
