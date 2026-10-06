package localmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func lifecycleMCPFixture(t *testing.T) (*Server, string) {
	t.Helper()
	cfg := &config.Config{Dir: t.TempDir()}
	home := t.TempDir()
	dir := filepath.Join(cfg.Dir, "eggs", "archived")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf("agent=claude\nkind=agent\ncwd=/fixture\nprovider_session_id=ours\nprovider_home=%s\n", home)
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte(meta), 0600); err != nil {
		t.Fatal(err)
	}
	if err := eggclient.WriteSessionPrincipal(dir, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := egg.RecordSessionProcessEvent(dir, "session_exit", "failed", "cancelled"); err != nil {
		t.Fatal(err)
	}
	return &Server{Version: "dev", Cfg: cfg, Principal: "owner", Logs: os.Stderr}, dir
}

func TestSessionLifecycleMCPDispatchArchiveOwnershipStrictArguments(t *testing.T) {
	s, _ := lifecycleMCPFixture(t)
	for _, name := range []string{"session_status", "session_read", "session_wait"} {
		arguments := json.RawMessage(`{"session":"archived"}`)
		if name == "session_wait" {
			arguments = json.RawMessage(`{"session":"archived","state":"failed","timeout_seconds":0.1}`)
		}
		data, isError, protocolErr := s.callTool(context.Background(), name, arguments)
		if protocolErr != nil || isError || data["session"] != "archived" {
			t.Fatalf("%s: %v %v %v", name, data, isError, protocolErr)
		}
		if _, _, err := s.callTool(context.Background(), name, json.RawMessage(`{"session":"archived","unexpected":true}`)); err != nil {
			t.Fatal(err)
		}
		_, isError, _ = s.callTool(context.Background(), name, json.RawMessage(`{"session":"archived","unexpected":true}`))
		if !isError {
			t.Fatalf("%s allowed unknown arguments", name)
		}
	}
	s.Principal = "foreign"
	if _, err := s.toolSessionRead(context.Background(), json.RawMessage(`{"session":"archived"}`)); err == nil {
		t.Fatal("foreign owner read archived session")
	}
	s.Principal = "owner"
	s.enforcePathBounds = true
	s.allowedPaths = []string{"/other"}
	if _, err := s.toolSessionStatus(context.Background(), json.RawMessage(`{"session":"archived"}`)); err == nil {
		t.Fatal("ignored browser path bounds")
	}
}

func TestSessionLifecycleWaitHeadBeyondPageAndCancellation(t *testing.T) {
	s, dir := lifecycleMCPFixture(t)
	for i := 0; i < 205; i++ {
		if err := egg.RecordSessionProcessEvent(dir, "fixture_event", "working", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := egg.RecordSessionProcessEvent(dir, "session_exit", "completed", "provider exited"); err != nil {
		t.Fatal(err)
	}
	session, err := eggclient.ResolveLifecycleSession(s.Cfg, "archived")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	view, matched, err := eggclient.WaitSessionLifecycle(ctx, s.Cfg, session, 1, "completed")
	if err != nil || !matched || view.StateCursor <= 200 {
		t.Fatalf("completion behind page never matched: %+v %t %v", view, matched, err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, _, err = eggclient.WaitSessionLifecycle(ctx2, s.Cfg, session, view.HeadCursor, "working"); err != context.Canceled {
		t.Fatalf("cancellation ignored: %v", err)
	}
	result, err := s.toolSessionWait(context.Background(), json.RawMessage(fmt.Sprintf(`{"session":"archived","after_cursor":%d,"state":"completed","timeout_seconds":0.1}`, view.HeadCursor)))
	if err != nil || result["timed_out"] != true {
		t.Fatalf("old completion satisfied new wait: %v %v", result, err)
	}
}
