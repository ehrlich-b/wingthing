//go:build linux

package egg

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func newTestCgroup() (processBoundary, error) { return newEggCgroup(nil) }

func TestProcStatIdentityWithComplexName(t *testing.T) {
	fields := make([]string, 20)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0], fields[1], fields[2], fields[3], fields[19] = "S", "7", "42", "42", "12345"
	p, err := parseProcStat("42 (name with ) and\nspaces) " + strings.Join(fields, " "))
	if err != nil || p.PID != 42 || p.ParentPID != 7 || p.ProcessGroupID != 42 || p.session != 42 || p.start != "12345" {
		t.Fatalf("stat identity: %+v %v", p, err)
	}
}

func TestSubreaperContainsImmediateOrphans(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("missing fixture: Python 3 multiprocessing runtime")
	}
	// Keep the process-wide subreaper setting and adoption sweep in a dedicated
	// helper, exactly as in the egg executable, never the shared test runner.
	if os.Getenv("WT_SUBREAPER_FIXTURE") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSubreaperContainsImmediateOrphans$", "-test.v")
		cmd.Env = append(os.Environ(), "WT_SUBREAPER_FIXTURE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("subreaper fixture: %v\n%s", err, out)
		}
		return
	}
	dir := t.TempDir()
	script, ready := filepath.Join(dir, "provider.py"), filepath.Join(dir, "ready.json")
	if err := os.WriteFile(script, []byte(multiprocessingFixture), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, script, ready, "orphan")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	tree, err := prepareProcessTree(cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately force the fallback even on delegated hosts.
	if tree.boundary != nil {
		if err := tree.boundary.close(); err != nil {
			t.Fatal(err)
		}
		tree.boundary = nil
	}
	release, closeGate, err := containmentLaunch(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGate()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	sess := &Session{PID: cmd.Process.Pid, cmd: cmd, done: done, processTree: tree}
	if err := tree.start(sess.PID); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	pids := waitFixturePIDs(t, ready)
	// No observe call: double-fork+setsid has already erased provider ancestry.
	survivors, err := tree.kill(sess)
	if err != nil || len(survivors) != 0 {
		t.Fatalf("subreaper fallback left survivors: %+v %v", survivors, err)
	}
	assertFixtureGone(t, pids)
	for kind, pid := range pids {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err == nil {
			t.Errorf("%s was not reaped: %d", kind, pid)
		}
	}
}

func TestCgroupMembershipIncludesNestedGroups(t *testing.T) {
	// Filesystem fixture tests recursive inventory without touching host cgroups.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{"cgroup.procs": "10\n", "nested/cgroup.procs": "20\n30\n"} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	pids, err := (&eggCgroup{path: dir}).members()
	if err != nil || fmt.Sprint(pids) != "[10 20 30]" {
		t.Fatalf("nested cgroup inventory: %v %v", pids, err)
	}
	if _, err := parseProcStat(strings.Repeat("x", 100)); err == nil {
		t.Fatal("invalid proc stat accepted")
	}
}
