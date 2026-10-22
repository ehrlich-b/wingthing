package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func TestShellReportThreeExecutableTasksOneRefresh(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	binary := localOnlyFixtureBinary(t, repo, root)
	cfg := &config.Config{Dir: root}
	type task struct {
		cmd   *exec.Cmd
		input io.WriteCloser
		lines *bufio.Scanner
		dir   string
	}
	tasks := []task{}
	snapshots := map[string]any{}
	snapshot := func(name string, blocked int) {
		t.Helper()
		rows, err := eggclient.DiscoverActiveSessions(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		actual := 0
		for _, r := range rows {
			if r.Status == "blocked" {
				actual++
			}
			if r.Name == "" {
				t.Fatalf("missing fixture name: %+v", r)
			}
			if r.StateSource != "shell_report" {
				t.Fatalf("lost reported provenance: %+v", r)
			}
		}
		if actual != blocked || len(rows) != 3 {
			t.Fatalf("one refresh %s: blocked=%d rows=%+v", name, actual, rows)
		}
		snapshots[name] = rows
	}
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("immutable-shell-task-%d", i)
		dir := filepath.Join(root, "eggs", id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for file, value := range map[string]string{"egg.meta": "agent=\nkind=terminal\ncwd=/projects/release\nprovider_home=" + root + "\n", "session.name": fmt.Sprintf("release-compatibility-customer-migration-validation-task-%d", i)} {
			if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.CommandContext(t.Context(), "sh", filepath.Join(repo, "scripts/examples/reported-shell.sh"))
		cmd.Env = append(os.Environ(), "WINGTHING_DIR="+root, "WT_SESSION_ID="+id, "WT_BIN="+binary)
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = input.Close()
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		if err := os.WriteFile(filepath.Join(dir, "egg.pid"), []byte(fmt.Sprint(os.Getpid())), 0600); err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "Waiting for continue") {
				break
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		v, err := eggclient.LifecycleViewForSession(cfg, eggclient.LocalSession{ID: id}, 0, 200)
		if err != nil || v.Status != "blocked" || len(v.Events) != 2 {
			t.Fatalf("waiting: %+v %v", v, err)
		}
		tasks = append(tasks, task{cmd, input, scanner, dir})
	}
	snapshot("waiting", 3)
	// The CLI refuses mutable labels, prefixes and cross-session acknowledgements.
	for _, ref := range []string{"immutable-shell-task-", "release-compatibility-customer-migration-validation-task-0", "immutable-shell-task-1"} {
		cmd := exec.CommandContext(t.Context(), binary, "session", "report", ref, "--json")
		cmd.Env = append(os.Environ(), "WINGTHING_DIR="+root)
		cmd.Stdin = strings.NewReader(`{"goal":"g","update":"u","next_action":"n","shell":{"session_id":"immutable-shell-task-0","revision":3,"after":2,"state":"continue"}}`)
		if output, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("accepted ref %s: %s", ref, output)
		}
	}
	if _, err := fmt.Fprintln(tasks[0].input); err != nil {
		t.Fatal(err)
	}
	for tasks[0].lines.Scan() {
	}
	if err := tasks[0].cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	// Keep metadata discoverable after the fixture task exits; an egg owns this
	// PID for the entire terminal lifetime, independently of its foreground task.

	snapshot("acknowledged", 2)
	for i := 1; i < 3; i++ {
		fmt.Fprintln(tasks[i].input)
		for tasks[i].lines.Scan() {
		}
		if err := tasks[i].cmd.Wait(); err != nil {
			t.Fatal(err)
		}

	}
	snapshot("finished", 0)
	for _, task := range tasks {
		view, err := egg.ReadSessionLifecycle(task.dir, "", "", "", "", true, 0, 200)
		if err != nil || len(view.Events) != 4 || view.Events[2].Report.Shell.State != "continue" || view.Status != "done" {
			t.Fatalf("ordered example: %+v %v", view, err)
		}
	}
	if path := os.Getenv("WT_SHELL_SNAPSHOTS"); path != "" {
		data, err := json.MarshalIndent(snapshots, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("Three executable shell tasks: one inventory refresh exposes exactly 3 waiting, 2 after exact acknowledgement, 0 after finish", time.Now().UTC())
}
