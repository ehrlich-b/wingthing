package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/relay"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func TestRoostHTTPMCPSemanticRunLifecycleIsOwnerScoped(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "state")
	cfg := &config.Config{Dir: stateDir, DefaultAgent: "claude", WingID: "shared-roost"}
	if err := config.SaveWingConfig(stateDir, &config.WingConfig{
		Paths: config.PathList{{Path: workspace}},
	}); err != nil {
		t.Fatal(err)
	}

	relayStore, err := relay.OpenRelay(filepath.Join(root, "roost.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := relayStore.Close(); err != nil {
			t.Errorf("close relay store: %v", err)
		}
	})
	key, _, err := relay.GenerateECKey()
	if err != nil {
		t.Fatal(err)
	}
	srv := relay.NewServer(relayStore, relay.ServerConfig{
		RoostAllowedEmails: []string{"alice@example.com", "bob@example.com"},
	})
	srv.SetJWTKey(key)
	srv.RoostMode = true
	for _, userID := range []string{"alice", "bob"} {
		if err := relayStore.CreateUser(userID); err != nil {
			t.Fatal(err)
		}
		if _, err := relayStore.DB().Exec("UPDATE users SET email = ? WHERE id = ?", userID+"@example.com", userID); err != nil {
			t.Fatal(err)
		}
		if err := relayStore.CreateSession("session-"+userID, userID, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	runnerIsolation := make(chan string, 1)
	runnerGate := make(chan struct{})
	var runnerGateOnce sync.Once
	t.Cleanup(func() { runnerGateOnce.Do(func() { close(runnerGate) }) })
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce, releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	runner := func(ctx context.Context, _ *config.Config, taskStore *store.Store, task *store.Task, _ taskRunOptions) error {
		if task.CWD != canonicalWorkspace {
			return fmt.Errorf("runner received cwd %q, want %q", task.CWD, canonicalWorkspace)
		}
		runnerIsolation <- task.Isolation
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-runnerGate:
		}
		if err := taskStore.UpdateTaskStatus(task.ID, "running"); err != nil {
			return err
		}
		startedOnce.Do(func() { close(started) })
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
		}
		if err := taskStore.SetTaskOutput(task.ID, "organization semantic result"); err != nil {
			return err
		}
		return taskStore.UpdateTaskStatus(task.ID, "done")
	}
	srv.EnableMCP(nil, nil, roostNativeMCPToolsWithTaskRunner(cfg, false, runner)...)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	aliceCodex := issueRoostMCPToken(t, ts.URL, "session-alice", "Alice Codex")
	aliceClaude := issueRoostMCPToken(t, ts.URL, "session-alice", "Alice Claude")
	bobCodex := issueRoostMCPToken(t, ts.URL, "session-bob", "Bob Codex")

	created, isError := callRoostMCPTool(t, ts.URL, aliceCodex.accessToken, "agent_run", map[string]any{
		"prompt": "characterize organization semantic execution",
		"agent":  "claude",
		"cwd":    workspace,
	})
	if isError {
		t.Fatalf("agent_run = %#v", created)
	}
	runID, _ := created["run_id"].(string)
	if runID == "" {
		t.Fatalf("agent_run omitted run_id: %#v", created)
	}
	if created["status"] != "pending" || created["isolation"] != "standard" {
		t.Fatalf("original pending response = %#v", created)
	}
	select {
	case got := <-runnerIsolation:
		if got != "standard" {
			t.Fatalf("runner isolation = %q, want standard", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("organization agent runner did not receive isolation")
	}
	status, isError := callRoostMCPTool(t, ts.URL, aliceClaude.accessToken, "agent_status", map[string]any{"run_id": runID})
	if isError || status["status"] != "pending" || status["isolation"] != "standard" {
		t.Fatalf("same-owner pending reconnect = %#v isError=%v", status, isError)
	}
	otherOwner, isError := callRoostMCPTool(t, ts.URL, bobCodex.accessToken, "agent_status", map[string]any{"run_id": runID})
	if !isError || !strings.Contains(fmt.Sprint(otherOwner["error"]), "not found or not owned") {
		t.Fatalf("other-owner status = %#v isError=%v", otherOwner, isError)
	}
	runnerGateOnce.Do(func() { close(runnerGate) })
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		status, statusIsError := callRoostMCPTool(t, ts.URL, aliceCodex.accessToken, "agent_result", map[string]any{"run_id": runID})
		t.Fatalf("organization agent run did not start: result=%#v isError=%v", status, statusIsError)
	}

	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	denied, isError := callRoostMCPTool(t, ts.URL, aliceCodex.accessToken, "agent_run", map[string]any{
		"prompt": "must not run outside the organization path",
		"agent":  "claude",
		"cwd":    outside,
	})
	if !isError || !strings.Contains(fmt.Sprint(denied["error"]), "outside this user's roost paths") {
		t.Fatalf("outside-path run = %#v isError=%v", denied, isError)
	}

	releaseOnce.Do(func() { close(release) })
	waited, isError := callRoostMCPTool(t, ts.URL, aliceClaude.accessToken, "agent_wait", map[string]any{
		"run_id": runID, "timeout_seconds": 2,
	})
	if isError || waited["status"] != "done" {
		t.Fatalf("same-owner wait = %#v isError=%v", waited, isError)
	}
	result, isError := callRoostMCPTool(t, ts.URL, aliceCodex.accessToken, "agent_result", map[string]any{
		"run_id": runID, "max_chars": 100,
	})
	if isError || result["ready"] != true || result["output"] != "organization semantic result" {
		t.Fatalf("same-owner result = %#v isError=%v", result, isError)
	}

	assertRoostMCPAudit(t, relayStore, map[roostMCPAuditKey]int{
		{ownerID: "alice", actorID: aliceCodex.clientID, tool: "agent_run"}:                1,
		{ownerID: "alice", actorID: aliceCodex.clientID, tool: "agent_run", isError: true}: 1,
		{ownerID: "alice", actorID: aliceCodex.clientID, tool: "agent_result"}:             1,
		{ownerID: "alice", actorID: aliceClaude.clientID, tool: "agent_status"}:            1,
		{ownerID: "alice", actorID: aliceClaude.clientID, tool: "agent_wait"}:              1,
		{ownerID: "bob", actorID: bobCodex.clientID, tool: "agent_status", isError: true}:  1,
	})
}

type roostMCPClient struct {
	accessToken string
	clientID    string
}

func issueRoostMCPToken(t *testing.T, baseURL, session, clientName string) roostMCPClient {
	t.Helper()
	const redirectURL = "http://localhost:9999/callback"
	registrationBody, err := json.Marshal(map[string]any{
		"redirect_uris": []string{redirectURL},
		"client_name":   clientName,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(baseURL+"/oauth/register", "application/json", strings.NewReader(string(registrationBody)))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		closeRoostTestBody(t, resp.Body)
		t.Fatalf("register = %d: %s", resp.StatusCode, body)
	}
	var registration struct {
		ClientID string `json:"client_id"`
	}
	decodeRoostTestJSON(t, resp.Body, &registration)
	closeRoostTestBody(t, resp.Body)

	verifier := "verifier-abcdefghijklmnopqrstuvwxyz-0123456789"
	digest := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"client_id":             {registration.ClientID},
		"redirect_uri":          {redirectURL},
		"response_type":         {"code"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(digest[:])},
		"code_challenge_method": {"S256"},
		"state":                 {"org-semantic-test"},
		"resource":              {baseURL + "/mcp"},
	}
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/oauth/authorize?"+query.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "wt_session", Value: session})
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	closeRoostTestBody(t, resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize = %d: %s", resp.StatusCode, body)
	}
	ridMatch := regexp.MustCompile(`name="rid" value="([^"]+)"`).FindSubmatch(body)
	if len(ridMatch) != 2 {
		t.Fatalf("authorize response omitted rid: %s", body)
	}

	form := url.Values{"rid": {string(ridMatch[1])}, "action": {"approve"}}
	req, _ = http.NewRequest(http.MethodPost, baseURL+"/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "wt_session", Value: session})
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err = noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	closeRoostTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("authorize approval = %d", resp.StatusCode)
	}
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	code := location.Query().Get("code")
	if code == "" {
		t.Fatalf("authorization redirect omitted code: %s", location)
	}

	tokenForm := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURL},
		"client_id":     {registration.ClientID},
		"code_verifier": {verifier},
		"resource":      {baseURL + "/mcp"},
	}
	resp, err = http.PostForm(baseURL+"/oauth/token", tokenForm)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		closeRoostTestBody(t, resp.Body)
		t.Fatalf("token = %d: %s", resp.StatusCode, body)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	decodeRoostTestJSON(t, resp.Body, &token)
	closeRoostTestBody(t, resp.Body)
	if token.AccessToken == "" {
		t.Fatal("token response omitted access_token")
	}
	return roostMCPClient{accessToken: token.AccessToken, clientID: registration.ClientID}
}

type roostMCPAuditKey struct {
	ownerID string
	actorID string
	tool    string
	isError bool
}

func assertRoostMCPAudit(t *testing.T, relayStore *relay.RelayStore, want map[roostMCPAuditKey]int) {
	t.Helper()
	rows, err := relayStore.DB().Query("SELECT detail FROM audit_log WHERE event = 'mcp_control_call'")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close audit rows: %v", err)
		}
	}()
	got := make(map[roostMCPAuditKey]int)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var detail struct {
			OwnerID string `json:"owner_id"`
			ActorID string `json:"actor_id"`
			Tool    string `json:"tool"`
			IsError bool   `json:"is_error"`
		}
		if err := json.Unmarshal([]byte(raw), &detail); err != nil {
			t.Fatalf("decode MCP audit %q: %v", raw, err)
		}
		got[roostMCPAuditKey{ownerID: detail.OwnerID, actorID: detail.ActorID, tool: detail.Tool, isError: detail.IsError}]++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MCP audit calls = %#v, want %#v", got, want)
	}
}

func callRoostMCPTool(t *testing.T, baseURL, token, name string, arguments map[string]any) (map[string]any, bool) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": arguments,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/mcp", strings.NewReader(string(payload)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRoostTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s HTTP status = %d: %s", name, resp.StatusCode, body)
	}
	var rpc struct {
		Result struct {
			Structured map[string]any `json:"structuredContent"`
			IsError    bool           `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeRoostTestJSON(t, resp.Body, &rpc)
	if rpc.Error != nil {
		t.Fatalf("%s RPC error %d: %s", name, rpc.Error.Code, rpc.Error.Message)
	}
	return rpc.Result.Structured, rpc.Result.IsError
}

func decodeRoostTestJSON(t *testing.T, reader io.Reader, destination any) {
	t.Helper()
	if err := json.NewDecoder(reader).Decode(destination); err != nil {
		t.Fatal(err)
	}
}

func closeRoostTestBody(t *testing.T, body io.Closer) {
	t.Helper()
	if err := body.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
}
