//go:build e2e

package integ

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Real preview CLI, egg, provider process and Unix RPC; no model credentials,
// host connection, daemon, or shared workspace is involved.
func TestPreviewInputLeaseAcrossAttachments(t *testing.T) {
	binary := os.Getenv("WT_TEST_PREVIEW_BINARY")
	if binary == "" {
		t.Fatal("run make test-integ")
	}
	root, err := os.MkdirTemp("/tmp", "wt-lease-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home, stateDir, work := filepath.Join(root, "home"), filepath.Join(root, "state"), filepath.Join(root, "work")
	for _, dir := range []string{home, work} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(work, "fixture.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'ready\\n'\nwhile IFS= read -r line; do printf 'result:%s\\n' \"$line\"; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	env := append(os.Environ(), "HOME="+home, "WINGTHING_DIR="+stateDir)
	cmd := exec.CommandContext(ctx, binary, "terminal", "--cwd", work, "--name", "lease", "--json", "--", "/bin/sh", script)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start fixture: %v %s", err, output)
	}
	var started struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(output, &started); err != nil || started.Session == "" {
		t.Fatalf("start: %s %v", output, err)
	}
	dir := filepath.Join(stateDir, "eggs", started.Session)
	dial := func() *egg.Client {
		t.Helper()
		client, err := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	client := dial()
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := client.Kill(cleanup, started.Session); err != nil {
			t.Errorf("fixture cleanup: %v", err)
			logLeaseCleanupDiagnostics(t, dir)
		}
	})
	attach := func(options egg.AttachOptions) pb.Egg_SessionClient {
		t.Helper()
		stream, err := client.AttachSessionWithOptions(ctx, started.Session, options)
		if err != nil {
			t.Fatal(err)
		}
		return stream
	}
	writer := attach(egg.AttachOptions{Claim: true, Owner: "mcp:fixture"})
	oldLease := egg.AttachmentInfo(writer)
	if oldLease == nil || oldLease.AttachmentToken == "" {
		t.Fatal("writer acknowledgment missing")
	}
	observers := []pb.Egg_SessionClient{attach(egg.AttachOptions{ReadOnly: true, Owner: "browser:observer"}), attach(egg.AttachOptions{ReadOnly: true, Owner: "cli:observer"})}
	for _, observer := range observers {
		if egg.AttachmentInfo(observer).AttachmentToken != "" {
			t.Fatal("observer got private writer token")
		}
	}
	before, err := client.Status(ctx)
	if err != nil || before.ProcessPid == 0 || before.WriterOwner != "mcp:fixture" {
		t.Fatalf("before status: %v %v", before, err)
	}
	if _, err := client.AttachSessionWithOptions(ctx, started.Session, egg.AttachOptions{Claim: true, Owner: "cli:second"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("second writer admitted: %v", err)
	}
	stranger := dial()
	if err := stranger.Resize(ctx, started.Session, 30, 100); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unattached resize bypass: %v", err)
	}
	public := &pb.AttachmentInfo{AttachmentId: before.WriterId, InputEpoch: before.InputEpoch}
	if err := stranger.ResizeForAttachment(ctx, started.Session, 30, 100, public); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("public status resize bypass: %v", err)
	}
	if err := client.ResizeForAttachment(ctx, started.Session, 30, 100, oldLease); err != nil {
		t.Fatal(err)
	}
	if err := writer.Send(&pb.SessionMsg{SessionId: started.Session, Payload: &pb.SessionMsg_Input{Input: []byte("one\n")}}); err != nil {
		t.Fatal(err)
	}
	readUntil := func(stream pb.Egg_SessionClient, match string) {
		t.Helper()
		var text strings.Builder
		for !strings.Contains(text.String(), match) {
			msg, err := stream.Recv()
			if err != nil {
				t.Fatalf("read %q: %v", match, err)
			}
			text.Write(msg.GetOutput())
		}
	}
	for _, observer := range observers {
		readUntil(observer, "result:one")
	}
	// The actual CLI observes the busy error before stdin can submit bytes.
	blocked := exec.CommandContext(ctx, binary, "attach", started.Session)
	blocked.Env, blocked.Stdin = env, strings.NewReader("must-not-write\n")
	if diagnostic, err := blocked.CombinedOutput(); err == nil || !strings.Contains(string(diagnostic), "terminal input owned") {
		t.Fatalf("CLI busy contract: %v %s", err, diagnostic)
	}
	next := attach(egg.AttachOptions{Claim: true, Takeover: true, Owner: "browser:controller"})
	nextLease := egg.AttachmentInfo(next)
	if nextLease.InputEpoch <= oldLease.InputEpoch {
		t.Fatal("takeover epoch did not advance")
	}
	for {
		_, err := writer.Recv()
		if err != nil {
			if status.Code(err) != codes.Aborted {
				t.Fatalf("old writer not revoked: %v", err)
			}
			break
		}
	}
	if err := client.ResizeForAttachment(ctx, started.Session, 40, 120, oldLease); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("stale epoch resized: %v", err)
	}
	if err := next.Send(&pb.SessionMsg{SessionId: started.Session, Payload: &pb.SessionMsg_Input{Input: []byte("two\n")}}); err != nil {
		t.Fatal(err)
	}
	for _, observer := range observers {
		readUntil(observer, "result:two")
	}
	if err := next.Send(&pb.SessionMsg{SessionId: started.Session, Payload: &pb.SessionMsg_Detach{Detach: true}}); err != nil {
		t.Fatal(err)
	}
	for {
		_, err := next.Recv()
		if err != nil {
			break
		}
	}
	fresh := attach(egg.AttachOptions{Claim: true, Owner: "cli:reattach"})
	defer fresh.CloseSend()
	after, err := client.Status(ctx)
	if err != nil || after.ProcessPid != before.ProcessPid || after.WriterOwner != "cli:reattach" || after.Readers < 3 {
		t.Fatalf("provider/readers changed: before=%v after=%v err=%v", before, after, err)
	}
	t.Logf("provider PID %d survived; two observers, busy writer, takeover epoch %d->%d, stale/unattached resize rejection, detach and reattach proved", after.ProcessPid, oldLease.InputEpoch, nextLease.InputEpoch)
}

// logLeaseCleanupDiagnostics prints what a hung egg shutdown left behind, so a
// runner-only failure explains itself without rerunning locally.
func logLeaseCleanupDiagnostics(t *testing.T, dir string) {
	t.Helper()
	tail := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			return err.Error()
		}
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		if len(lines) > 60 {
			lines = lines[len(lines)-60:]
		}
		return strings.Join(lines, "\n")
	}
	eggLog := tail(filepath.Join(dir, "egg.log"))
	t.Logf("egg.log tail:\n%s", eggLog)
	for _, line := range strings.Split(eggLog, "\n") {
		if _, rest, ok := strings.Cut(line, "created tmpdir="); ok {
			t.Logf("deny_init.log tail:\n%s", tail(filepath.Join(strings.Fields(rest)[0], "deny_init.log")))
		}
	}
	if out, err := exec.Command("ps", "-eo", "pid,ppid,stat,wchan:20,args").CombinedOutput(); err == nil {
		t.Logf("processes:\n%s", out)
	}
}
