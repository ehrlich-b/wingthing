package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	agentpkg "github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

const continuationModel = "claude-opus-4-6"

func continuationFixture(t *testing.T) (*localMCPServer, *store.Store, *store.Conversation, string) {
	t.Helper()
	old := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = old })
	cfg := &config.Config{Dir: canonicalSessionPath(t.TempDir())}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c, _, err := db.ReserveConversation(store.Conversation{ID: "root", OwnerID: "owner", Agent: "claude", CWD: cfg.Dir, SessionID: "source", WingID: "wing", LaunchKey: "initial", SpecDigest: "initial", Title: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetConversationLaunch(c.ID, "started", ""); err != nil {
		t.Fatal(err)
	}
	c.LaunchState = "started"
	transcript := `{"uuid":"first","type":"user","sessionId":"provider","message":{"role":"user","content":"First message"}}` + "\n" +
		`{"uuid":"answer","type":"assistant","sessionId":"provider","message":{"role":"assistant","model":"` + continuationModel + `","content":[{"type":"text","text":"First answer"}]}}` + "\n"
	dir := writeResumeSessionFixture(t, cfg, "source", "owner", "claude", cfg.Dir, "provider", transcript)
	meta := "agent=claude\ncwd=" + cfg.Dir + "\nprovider_session_id=provider\nprovider_home=" + effectiveSessionHome(cfg, EggIdentity{}) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte(meta), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeSessionPrincipal(dir, "owner"); err != nil {
		t.Fatal(err)
	}
	server := &localMCPServer{cfg: cfg, principal: "owner", unsandboxed: true, logs: &bytes.Buffer{}}
	return server, db, c, dir
}

func continuationRequest(source, input, request string) json.RawMessage {
	data, _ := json.Marshal(continuationArgs{SourceSession: source, Role: "parent", Input: input, RequestID: request})
	return data
}

func TestHeadlessContinuationAdvertisementEligibility(t *testing.T) {
	for _, name := range []string{"ended owned resumable", "active", "foreign principal", "foreign browser owner", "child", "missing provider", "provider mismatch", "missing archive", "unverified provider", "unknown model", "other agent", "superseded", "bound MCP", "shared host", "path revoked"} {
		t.Run(name, func(t *testing.T) {
			s, db, c, dir := continuationFixture(t)
			write := func(file, value string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "active":
				config.ReleaseChannel = "stable"
				write("egg.pid", strconv.Itoa(os.Getpid()))
			case "foreign principal":
				s.principal = "someone-else"
			case "foreign browser owner":
				s.identity.UserID = "someone-else"
			case "child":
				if _, err := db.DB().Exec(`UPDATE conversations SET parent_id = 'other', root_id = 'other' WHERE id = ?`, c.ID); err != nil {
					t.Fatal(err)
				}
			case "missing provider":
				write("chat.meta", "agent=claude\ncwd="+c.CWD+"\n")
			case "provider mismatch":
				write("egg.meta", "agent=claude\ncwd="+c.CWD+"\nprovider_session_id=different\n")
			case "missing archive":
				if err := os.Remove(filepath.Join(dir, "chat.jsonl.gz")); err != nil {
					t.Fatal(err)
				}
			case "unverified provider":
				if err := os.Remove(filepath.Join(dir, providerResumeMetadataFile)); err != nil {
					t.Fatal(err)
				}
			case "unknown model":
				writeResumeSessionFixture(t, s.cfg, c.SessionID, "owner", "claude", c.CWD, "provider", "{}\n")
			case "other agent":
				write("egg.meta", "agent=codex\ncwd="+c.CWD+"\nprovider_session_id=provider\n")
			case "superseded":
				if err := db.ResumeConversationExecution(c.SessionID, "later"); err != nil {
					t.Fatal(err)
				}
			case "bound MCP":
				s.boundConversation = c.ID
			case "shared host":
				s.identity.SharedHost = true
			case "path revoked":
				s.enforcePathBounds = true
				s.allowedPaths = []string{t.TempDir()}
			}
			result, err := s.toolSessionRead(context.Background(), json.RawMessage(`{"session":"source"}`))
			if err != nil && name != "foreign principal" && name != "path revoked" {
				t.Fatal(err)
			}
			available := result["headless_continuation"]
			if name == "ended owned resumable" {
				want, _ := json.Marshal(map[string]any{"available": true, "source_session": "source", "conversation_id": "root", "provider_session_id": "provider", "model": continuationModel})
				got, _ := json.Marshal(available)
				if !bytes.Equal(got, want) {
					t.Fatalf("advertisement %s want %s", got, want)
				}
				tree, err := s.toolConversationRead(context.Background(), json.RawMessage(`{"conversation_id":"root"}`))
				if err != nil || tree["headless_continuation"] == nil || tree["tasks"].([]map[string]any)[0]["headless_continuation"] == nil {
					t.Fatalf("tree advertisement %v %v", tree, err)
				}
			} else if available != nil {
				t.Fatalf("ineligible advertisement: %v", available)
			}
			if name != "ended owned resumable" {
				s.startContinuation = func(*store.Conversation, *egg.EggConfig, spawnEggOpts) error {
					t.Fatal("ineligible source reached launch")
					return nil
				}
				if _, err := s.toolAgentStart(continuationRequest("source", "Follow up", "ineligible")); err == nil {
					t.Fatal("ineligible continuation accepted")
				}
			}
		})
	}
}

func TestHeadlessContinuationReplayMismatchAndTreeLinkage(t *testing.T) {
	s, db, root, dir := continuationFixture(t)
	starts := 0
	s.startContinuation = func(c *store.Conversation, _ *egg.EggConfig, opts spawnEggOpts) error {
		starts++
		current, err := db.GetConversation("owner", root.ID)
		if err != nil || current.SessionID != c.SessionID || current.LaunchState != "starting" {
			t.Fatalf("bound MCP sees previous execution at launch: %v %v", current, err)
		}
		if opts.ResumeSessionID != "provider" || opts.ResumeSourceSessionID != "source" || !opts.ProviderReserved || contextForConversation(c).DotID != root.ID {
			t.Fatalf("continuation launch binding %v %v", c, opts)
		}
		if !strings.Contains(strings.Join(opts.AgentArgs, "\n"), publicCoordinatorPrompt(c)) {
			t.Fatal("coordinator role missing")
		}
		return nil
	}
	input := "  Exact message 🦉\nsecond line  "
	args := continuationRequest("source", input, "followup")
	first, err := s.toolAgentStart(args)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(input))
	if first["request_id"] != "followup" || first["source_session"] != "source" || first["wing_id"] != "wing" || first["conversation_id"] != root.ID || first["root_conversation_id"] != root.ID || first["parent_conversation_id"] != "" || first["provider_session_id"] != "provider" || first["continuation_state"] != "started" || first["launch_state"] != "started" || first["new_turn"].(map[string]any)["input_sha256"] != hex.EncodeToString(digest[:]) {
		t.Fatalf("Swift acknowledgement mismatch %v", first)
	}
	if starts != 1 {
		t.Fatalf("starts %d", starts)
	}
	current, err := db.GetConversation("owner", root.ID)
	if err != nil || current.SessionID != first["session"] {
		t.Fatalf("current %v %v", current, err)
	}
	link := sessionConversationLink(s.cfg, first["session"].(string))
	if link.ConversationID != root.ID || link.RootConversationID != root.ID || link.ParentConversationID != "" {
		t.Fatalf("tree link %v", link)
	}
	executions, err := db.ConversationExecutions(root.ID)
	if err != nil || len(executions) != 2 {
		t.Fatalf("executions %v %v", executions, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	reconnected := &localMCPServer{cfg: s.cfg, principal: s.principal, logs: &bytes.Buffer{}, startContinuation: s.startContinuation}
	replay, err := reconnected.toolAgentStart(args)
	if err != nil || replay["session"] != first["session"] || replay["reused"] != true || starts != 1 {
		t.Fatalf("replay %v %v starts %d", replay, err, starts)
	}
	for _, change := range []json.RawMessage{continuationRequest("source", input+"changed", "followup"), continuationRequest("different", input, "followup"), json.RawMessage(`{"agent":"claude","cwd":"` + s.cfg.Dir + `","conversation_role":"parent","request_id":"followup"}`)} {
		if _, err := reconnected.toolAgentStart(change); err == nil {
			t.Fatalf("mismatched retry accepted: %s", change)
		}
	}
	if _, err := (&localMCPServer{cfg: s.cfg, principal: "foreign"}).toolAgentStart(args); err == nil {
		t.Fatal("foreign replay accepted")
	}
}

func TestHeadlessContinuationFailedAndUnconfirmedReservationsNeverRelaunch(t *testing.T) {
	s, db, root, _ := continuationFixture(t)
	starts := 0
	s.startContinuation = func(*store.Conversation, *egg.EggConfig, spawnEggOpts) error {
		starts++
		return errors.New("fake launch refused")
	}
	args := continuationRequest("source", "Follow up", "failure")
	result, err := s.toolAgentStart(args)
	if err != nil || result["launch_state"] != "failed" || result["continuation_state"] != "failed" {
		t.Fatalf("failed ack %v %v", result, err)
	}
	replay, err := s.toolAgentStart(args)
	if err != nil || replay["session"] != result["session"] || starts != 1 {
		t.Fatalf("failed replay %v %v", replay, err)
	}
	current, err := db.GetConversation("owner", root.ID)
	if err != nil || current.SessionID != "source" {
		t.Fatalf("failed launch replaced source %v %v", current, err)
	}
	if _, err := s.toolAgentStart(continuationRequest("source", "Follow up", "new-attempt")); err != nil || starts != 2 {
		t.Fatalf("new attempt %v starts %d", err, starts)
	}
	if _, err := db.DB().Exec(`UPDATE conversation_continuations SET launch_state = 'starting' WHERE request_id = 'failure'`); err != nil {
		t.Fatal(err)
	}
	replay, err = s.toolAgentStart(args)
	if err != nil || replay["launch_state"] != "starting" || starts != 2 {
		t.Fatalf("unconfirmed replay %v %v", replay, err)
	}
	if _, err := s.toolAgentStart(continuationRequest("source", "Other message", "third")); err == nil {
		t.Fatal("unconfirmed reservation allowed another continuation")
	}
}

func TestHeadlessContinuationStrictRequest(t *testing.T) {
	s, _, _, _ := continuationFixture(t)
	for _, input := range []string{"", "  ", "-flag", "nul\x00", strings.Repeat("x", 64<<10+1)} {
		if _, err := s.toolAgentStart(continuationRequest("source", input, "request")); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	for _, input := range []string{`{"resume_session":"source","conversation_role":"parent","input":"message","request_id":"request","agent":"claude"}`, `{"resume_session":"source","conversation_role":"parent","input":"message","request_id":"request","unknown":true}`, `{"agent":"claude","input":"message"}`} {
		if _, err := s.toolAgentStart(json.RawMessage(input)); err == nil {
			t.Fatalf("invalid fields accepted: %s", input)
		}
	}
}

func TestHeadlessContinuationKeepsLegacyLaunchRetryDigest(t *testing.T) {
	s, db, _, _ := continuationFixture(t)
	// The exact pre-continuation argument shape is persisted by running eggs.
	var legacy struct {
		Agent                string   `json:"agent"`
		Model                string   `json:"model"`
		CWD                  string   `json:"cwd"`
		Label                string   `json:"label"`
		Unattended           bool     `json:"unattended"`
		Args                 []string `json:"args"`
		ConversationRole     string   `json:"conversation_role"`
		ParentConversationID string   `json:"parent_conversation_id"`
		RequestID            string   `json:"request_id"`
	}
	legacy.Agent, legacy.Model, legacy.CWD, legacy.Label, legacy.ConversationRole = "claude", continuationModel, s.cfg.Dir, "legacy", "parent"
	legacy.Args = []string{"--model", continuationModel, "-p", "Original turn"}
	c, _, err := s.reserveAgentConversation("claude", s.cfg.Dir, "legacy", "parent", "", "legacy-request", "legacy-execution", legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetConversationLaunch(c.ID, "started", ""); err != nil {
		t.Fatal(err)
	}
	legacy.RequestID = "legacy-request"
	legacy.Args = []string{"-p", "Original turn"}
	args, _ := json.Marshal(legacy)
	replay, err := s.toolAgentStart(args)
	if err != nil || replay["session"] != c.SessionID || replay["reused"] != true {
		t.Fatalf("legacy launch retry changed: %v %v", replay, err)
	}
}

func TestHeadlessContinuationConcurrentReplay(t *testing.T) {
	s, _, _, _ := continuationFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var starts int
	s.startContinuation = func(*store.Conversation, *egg.EggConfig, spawnEggOpts) error {
		starts++
		close(entered)
		<-release
		return nil
	}
	args := continuationRequest("source", "One message", "one-request")
	var first map[string]any
	var firstErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); first, firstErr = s.toolAgentStart(args) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("launch not reached")
	}
	replay, err := (&localMCPServer{cfg: s.cfg, principal: "owner"}).toolAgentStart(args)
	close(release)
	wg.Wait()
	if err != nil || firstErr != nil || replay["session"] != first["session"] || replay["launch_state"] != "starting" || starts != 1 {
		t.Fatalf("concurrent replay %v %v %v starts %d", replay, err, firstErr, starts)
	}
}

func TestHeadlessContinuationBrowserAdapterMatchesNativeContract(t *testing.T) {
	s, db, root, dir := continuationFixture(t)
	user := "owner"
	principal := roostSessionPrincipal(user)
	if _, err := db.DB().Exec(`UPDATE conversations SET owner_id = ? WHERE id = ?`, principal, root.ID); err != nil {
		t.Fatal(err)
	}
	if err := writeSessionPrincipal(dir, principal); err != nil {
		t.Fatal(err)
	}
	s.principal = principal
	s.identity.UserID = user
	req := ws.TunnelRequest{SenderUserID: user, SenderOrgRole: "owner"}
	wc := &config.WingConfig{WingID: "wing"}
	for operation, args := range map[string]string{"session_read": `{"session":"source"}`, "conversation_read": `{"conversation_id":"root"}`} {
		result, err := browserSessionControl(context.Background(), s.cfg, wc, req, operation, json.RawMessage(args), s.cfg.Dir, false)
		if err != nil || result["headless_continuation"] == nil {
			t.Fatalf("browser advertisement %v %v", result, err)
		}
	}
	s.startContinuation = func(*store.Conversation, *egg.EggConfig, spawnEggOpts) error { return nil }
	args := continuationRequest("source", "Native follow-up", "native-request")
	first, err := s.toolAgentStart(args)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := browserSessionControl(context.Background(), s.cfg, wc, req, "agent_start", args, s.cfg.Dir, false)
	if err != nil || replay["session"] != first["session"] || replay["wing_id"] != "wing" || replay["request_id"] != "native-request" || replay["reused"] != true {
		t.Fatalf("browser continuation replay %v %v", replay, err)
	}
}

func TestHeadlessContinuationFakeClaudeResumesSameProvider(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	fake, err := filepath.Abs(filepath.Join(filepath.Dir(source), "..", "..", "test", "conversation", "fake_claude.py"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fake); os.IsNotExist(err) {
		t.Skip("requires repository fixture test/conversation/fake_claude.py; the Linux battery contains only compiled test binaries")
	} else if err != nil {
		t.Fatal(err)
	}
	s, db, root, _ := continuationFixture(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	s.startContinuation = func(c *store.Conversation, _ *egg.EggConfig, opts spawnEggOpts) error {
		starts++
		provider, extra, resume, err := effectiveProviderSession("claude", opts.ResumeSessionID, opts.AgentArgs)
		if err != nil || provider != "provider" || resume != "provider" {
			return errors.New("wrong provider-native resume")
		}
		_, argv, ok := agentpkg.InteractiveInvocation("claude", false, resume, extra...)
		if !ok || argv[0] != "--resume" || argv[1] != "provider" {
			return errors.New("missing Claude --resume")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, python, append([]string{fake}, argv...)...)
		cmd.Dir = c.CWD
		home := effectiveSessionHome(s.cfg, s.identity)
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude")}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fake Claude: %v %s", err, output)
		}
		dir := filepath.Join(s.cfg.Dir, "eggs", c.SessionID)
		if err := writeSessionPrincipal(dir, "owner"); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=claude\ncwd="+c.CWD+"\nprovider_session_id=provider\nprovider_home="+home+"\n"), 0600); err != nil {
			return err
		}
		return egg.CaptureSessionHistory("claude", c.CWD, dir, home, time.Now().Add(-time.Minute), "provider")
	}
	args := continuationRequest("source", "Follow-up 🦉", "fake-request")
	result, err := s.toolAgentStart(args)
	if err != nil || result["launch_state"] != "started" {
		t.Fatalf("fake continuation %v %v", result, err)
	}
	read, err := s.toolSessionRead(context.Background(), json.RawMessage(`{"session":"`+result["session"].(string)+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	view := read["lifecycle"].(egg.SessionView)
	if view.ProviderSessionID != "provider" || read["headless_continuation"] == nil {
		t.Fatalf("continued provider view %v", read)
	}
	var text []string
	for _, event := range view.Events {
		text = append(text, event.Text)
	}
	if !strings.Contains(strings.Join(text, "\n"), "First answer") || !strings.Contains(strings.Join(text, "\n"), "fixture-result:Follow-up 🦉") {
		t.Fatalf("provider history was not continued: %v", text)
	}
	tree, err := s.toolConversationRead(context.Background(), json.RawMessage(`{"conversation_id":"root"}`))
	if err != nil || len(tree["tasks"].([]map[string]any)) != 1 {
		t.Fatalf("continued tree %v %v", tree, err)
	}
	current, _ := db.GetConversation("owner", root.ID)
	if current.SessionID != result["session"] || contextForConversation(current).DotID != root.ID {
		t.Fatalf("logical identity moved %v", current)
	}
	replay, err := s.toolAgentStart(args)
	if err != nil || replay["session"] != result["session"] || starts != 1 {
		t.Fatalf("fake replay %v %v starts %d", replay, err, starts)
	}
	next, err := s.toolAgentStart(continuationRequest(result["session"].(string), "Second follow-up", "second-request"))
	if err != nil || next["launch_state"] != "started" || next["provider_session_id"] != "provider" || next["conversation_id"] != root.ID || starts != 2 {
		t.Fatalf("second provider turn %v %v starts %d", next, err, starts)
	}
	replay, err = s.toolAgentStart(args)
	if err != nil || replay["session"] != result["session"] || replay["session"] == next["session"] || starts != 2 {
		t.Fatalf("old request retargeted after later turn: %v %v", replay, err)
	}
}
