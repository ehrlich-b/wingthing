package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
	pionwebrtc "github.com/pion/webrtc/v4"
)

func TestReplacedDataChannelCannotInjectOrDeleteCurrentSessionChannel(t *testing.T) {
	var sessions sync.Map
	old := &pionwebrtc.DataChannel{}
	current := &pionwebrtc.DataChannel{}
	sessions.Store("session", old)
	if !currentDataChannel(&sessions, "session", old) {
		t.Fatal("initial data channel is not current")
	}
	sessions.Store("session", current)
	if currentDataChannel(&sessions, "session", old) {
		t.Fatal("replaced data channel remained authorized for input")
	}
	if sessions.CompareAndDelete("session", old) {
		t.Fatal("stale close deleted the replacement channel")
	}
	if !currentDataChannel(&sessions, "session", current) {
		t.Fatal("replacement data channel was lost")
	}
}

func TestReplayChunkEndDoesNotSplitUTF8(t *testing.T) {
	raw := append(bytes.Repeat([]byte{'a'}, replayChunkSize-1), []byte("🙂tail")...)
	firstEnd := replayChunkEnd(raw, 0)
	if firstEnd != replayChunkSize-1 {
		t.Fatalf("first replay chunk ended at %d, want %d", firstEnd, replayChunkSize-1)
	}
	if !utf8.Valid(raw[:firstEnd]) || !utf8.Valid(raw[firstEnd:]) {
		t.Fatal("replay chunk boundary split a UTF-8 sequence")
	}
	if finalEnd := replayChunkEnd(raw, firstEnd); finalEnd != len(raw) {
		t.Fatalf("final replay chunk ended at %d, want %d", finalEnd, len(raw))
	}
}

func TestConsumeBrowserRequestChunkBoundsAndReassemblesLines(t *testing.T) {
	var pending string
	var discarding bool
	var got []string
	emit := func(value string) { got = append(got, value) }

	consumeBrowserRequestChunk([]byte(" https://one.example/path\nhttps://two.exam"), &pending, &discarding, emit)
	consumeBrowserRequestChunk([]byte("ple/next\n"+strings.Repeat("x", maxBrowserOpenURLBytes+1)), &pending, &discarding, emit)
	consumeBrowserRequestChunk([]byte("still-too-long\nhttps://three.example/\n"), &pending, &discarding, emit)

	want := []string{"https://one.example/path", "https://two.example/next", "https://three.example/"}
	if len(got) != len(want) {
		t.Fatalf("browser requests = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("browser request %d = %q, want %q", i, got[i], want[i])
		}
	}
	if pending != "" || discarding {
		t.Fatalf("parser state after complete lines = pending %q, discarding %v", pending, discarding)
	}
}

func TestWatchBrowserRequestsReclaimStartsAtCurrentEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-requests")
	if err := os.WriteFile(path, []byte("https://old.example/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	messages := make(chan ws.PTYBrowserOpen, 2)
	go watchBrowserRequests(ctx, path, "session", browserRequestOffset(path), func(value any) error {
		if message, ok := value.(ws.PTYBrowserOpen); ok {
			messages <- message
		}
		return nil
	})
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("https://new.example/\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-messages:
		if message.URL != "https://new.example/" {
			t.Fatalf("reclaimed browser request = %q", message.URL)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new browser request was not forwarded")
	}
}

func appendAuditVarint(target []byte, value uint64) []byte {
	var encoded [10]byte
	count := binary.PutUvarint(encoded[:], value)
	return append(target, encoded[:count]...)
}

func TestStreamPTYAuditParsesV2Incrementally(t *testing.T) {
	recording := []byte("WTA2")
	recording = appendAuditVarint(recording, 80)
	recording = appendAuditVarint(recording, 24)
	recording = appendAuditVarint(recording, 125)
	recording = appendAuditVarint(recording, 0)
	recording = appendAuditVarint(recording, 5)
	recording = append(recording, "hello"...)
	resize := appendAuditVarint(nil, 100)
	resize = appendAuditVarint(resize, 30)
	recording = appendAuditVarint(recording, 75)
	recording = appendAuditVarint(recording, 1)
	recording = appendAuditVarint(recording, uint64(len(resize)))
	recording = append(recording, resize...)

	var chunks []string
	err := streamPTYAudit(bytes.NewReader(recording), 120, 40, func(chunk []byte) error {
		chunks = append(chunks, string(append([]byte(nil), chunk...)))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`{"version":2,"width":80,"height":24}`,
		`[0.125,"o","aGVsbG8="]`,
		`[0.200,"r","100x30"]`,
	}
	if len(chunks) != len(want) {
		t.Fatalf("PTY audit chunks = %#v, want %#v", chunks, want)
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Fatalf("PTY audit chunk %d = %q, want %q", i, chunks[i], want[i])
		}
	}
}

func TestStreamPTYAuditRejectsOversizedFrameBeforeReadingIt(t *testing.T) {
	recording := appendAuditVarint(nil, 0)
	recording = appendAuditVarint(recording, maxAuditFrameBytes+1)
	err := streamPTYAudit(bytes.NewReader(recording), 80, 24, func([]byte) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("oversized PTY audit error = %v", err)
	}
}

func TestStreamPTYAuditToleratesIncompleteLiveFrame(t *testing.T) {
	recording := []byte("WTA2")
	recording = appendAuditVarint(recording, 80)
	recording = appendAuditVarint(recording, 24)
	recording = appendAuditVarint(recording, 0)
	recording = appendAuditVarint(recording, 0)
	recording = appendAuditVarint(recording, 10)
	recording = append(recording, "partial"...)
	var chunks int
	if err := streamPTYAudit(bytes.NewReader(recording), 120, 40, func([]byte) error {
		chunks++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if chunks != 1 {
		t.Fatalf("incomplete audit emitted %d chunks, want header only", chunks)
	}
}

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}

func TestTunnelKeyCacheIsBoundedAndEvictsOldestIdentity(t *testing.T) {
	cache := newTunnelKeyCache(2)
	cache.Put("first", nil)
	cache.Put("second", nil)
	cache.Put("second", nil)
	cache.Put("third", nil)

	if cache.Len() != 2 {
		t.Fatalf("cache size = %d", cache.Len())
	}
	if _, ok := cache.Get("first"); ok {
		t.Fatal("oldest sender identity was not evicted")
	}
	if _, ok := cache.Get("second"); !ok {
		t.Fatal("updated sender identity was unexpectedly evicted")
	}
	if _, ok := cache.Get("third"); !ok {
		t.Fatal("new sender identity is missing")
	}
}

func TestTunnelKeyCacheConcurrentInsertionsStayBounded(t *testing.T) {
	cache := newTunnelKeyCache(8)
	var group sync.WaitGroup
	for i := 0; i < 128; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			cache.Put(strconv.Itoa(index), nil)
		}(i)
	}
	group.Wait()
	if cache.Len() != 8 {
		t.Fatalf("cache size = %d", cache.Len())
	}
}

func TestLoadWingConfigForStartFailsClosedWithoutBreakingLegacyAbsence(t *testing.T) {
	t.Run("missing file remains the compatible zero-value policy", func(t *testing.T) {
		cfg, err := loadWingConfigForStart(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if cfg == nil || cfg.DirectMCP != nil {
			t.Fatalf("default wing config = %#v", cfg)
		}
	})

	for name, body := range map[string]string{
		"malformed direct policy": "direct_mcp:\n  allow_grants: [terminal.read]\n  deny_grants: [terminal.stop]\n",
		"unknown direct grant":    "direct_mcp:\n  allow_grants: [host.root]\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "wing.yaml"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadWingConfigForStart(dir); err == nil || !strings.Contains(err.Error(), "load wing.yaml") {
				t.Fatalf("start config error = %v", err)
			}
		})
	}
}

func TestWingConnectionTokenOverrideDoesNotTouchPersistedLogin(t *testing.T) {
	dir := t.TempDir()
	store := auth.NewTokenStore(dir)
	persisted := &auth.DeviceToken{Token: "hosted-login", DeviceID: "hosted-device"}
	if err := store.Save(persisted); err != nil {
		t.Fatal(err)
	}

	override := &auth.DeviceToken{Token: "embedded-service", DeviceID: "local"}
	selected, err := wingConnectionToken(dir, false, override)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Token != override.Token || selected == override {
		t.Fatalf("selected override = %#v, want independent copy", selected)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Token != persisted.Token || loaded.DeviceID != persisted.DeviceID {
		t.Fatalf("persisted login changed: %#v", loaded)
	}
}

func TestWingConnectionTokenRejectsMissingAndExpiredCredentials(t *testing.T) {
	if _, err := wingConnectionToken(t.TempDir(), false, nil); err == nil {
		t.Fatal("missing persisted token was accepted")
	}
	if _, err := wingConnectionToken(t.TempDir(), false, &auth.DeviceToken{Token: "expired", ExpiresAt: time.Now().Add(-time.Minute).Unix()}); err == nil {
		t.Fatal("expired embedded token was accepted")
	}
}

func TestWingConnectionTokenKeepsLocalAndPortalAuthoritiesSeparate(t *testing.T) {
	dir := t.TempDir()
	if err := auth.NewTokenStore(dir).Save(&auth.DeviceToken{Token: "hosted-login", DeviceID: "hosted"}); err != nil {
		t.Fatal(err)
	}
	if err := auth.NewLocalTokenStore(dir).Save(&auth.DeviceToken{Token: "localhost-login", DeviceID: "local"}); err != nil {
		t.Fatal(err)
	}

	localToken, err := wingConnectionToken(dir, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	hostedToken, err := wingConnectionToken(dir, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if localToken.Token != "localhost-login" || hostedToken.Token != "hosted-login" {
		t.Fatalf("selected tokens: local=%#v hosted=%#v", localToken, hostedToken)
	}
}

func TestWingConnectionTokenAcceptsLegacyLocalCredentialLocation(t *testing.T) {
	dir := t.TempDir()
	if err := auth.NewTokenStore(dir).Save(&auth.DeviceToken{Token: "legacy-local", DeviceID: "local"}); err != nil {
		t.Fatal(err)
	}
	token, err := wingConnectionToken(dir, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token.Token != "legacy-local" {
		t.Fatalf("legacy local token = %#v", token)
	}
}

func TestWingConnectionTokenDoesNotHideExpiredDedicatedLocalCredential(t *testing.T) {
	dir := t.TempDir()
	if err := auth.NewTokenStore(dir).Save(&auth.DeviceToken{Token: "hosted-login", DeviceID: "hosted"}); err != nil {
		t.Fatal(err)
	}
	if err := auth.NewLocalTokenStore(dir).Save(&auth.DeviceToken{Token: "expired-local", ExpiresAt: time.Now().Add(-time.Minute).Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, err := wingConnectionToken(dir, true, nil); err == nil || !strings.Contains(err.Error(), "local device token is expired") {
		t.Fatalf("expired local token error = %v", err)
	}
}

func TestHostedRelayPolicyAuditIsContentFreeAndPrivate(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Dir: dir}
	if err := appendHostedRelayPolicyAudit(cfg, ws.TypePTYStart); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "policy-audit.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]string
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record["event"] != "hosted_relay_denied" || record["operation"] != ws.TypePTYStart || record["policy"] != config.HostedRelayDeny {
		t.Fatalf("audit record = %#v", record)
	}
	if len(record) != 5 {
		t.Fatalf("audit record contains unexpected fields: %#v", record)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("policy audit mode = %o, want 600", got)
	}
}

func TestDirectMCPEnabledReflectsRuntimeAndLocalPolicy(t *testing.T) {
	if directMCPEnabled(false, &config.WingConfig{}) {
		t.Fatal("wing without a peer manager advertised direct MCP")
	}
	if !directMCPEnabled(true, &config.WingConfig{}) {
		t.Fatal("default wing policy disabled direct MCP")
	}
	if directMCPEnabled(true, &config.WingConfig{DirectMCP: &config.DirectMCPConfig{Disabled: true}}) {
		t.Fatal("locally disabled direct MCP was advertised")
	}
}

func TestPendingReattachAuthsAreViewerScoped(t *testing.T) {
	pending := newPendingReattachAuths()
	t.Cleanup(pending.close)

	challengeA := []byte("challenge-a")
	pending.put(ws.PTYAttach{Type: ws.TypePTYAttach, ViewerID: "viewer-a", UserID: "alice"}, challengeA, "alice:key-a", time.Hour)
	pending.put(ws.PTYAttach{Type: ws.TypePTYAttach, ViewerID: "viewer-b", UserID: "alice"}, []byte("challenge-b"), "alice:key-b", time.Hour)
	challengeA[0] = 'X'

	if _, ok := pending.take(""); ok {
		t.Fatal("response without a viewer ID consumed a pending viewer attach")
	}
	got, ok := pending.take("viewer-b")
	if !ok {
		t.Fatal("viewer-b could not resume its pending attach")
	}
	if got.attach.ViewerID != "viewer-b" || got.subject != "alice:key-b" || string(got.challenge) != "challenge-b" {
		t.Fatalf("viewer-b resumed the wrong challenge: %#v", got)
	}
	got, ok = pending.take("viewer-a")
	if !ok || string(got.challenge) != "challenge-a" {
		t.Fatalf("viewer-a challenge was lost or aliased: %#v, %v", got, ok)
	}
}

func TestPendingReattachAuthsExpireIndependently(t *testing.T) {
	pending := newPendingReattachAuths()
	t.Cleanup(pending.close)
	now := time.Now()
	pending.byViewer["expired"] = pendingReattachAuth{
		attach:    ws.PTYAttach{ViewerID: "expired"},
		expiresAt: now.Add(-time.Second),
	}
	pending.byViewer["live"] = pendingReattachAuth{
		attach:    ws.PTYAttach{ViewerID: "live"},
		expiresAt: now.Add(time.Hour),
	}
	pending.resetTimer()

	expired := pending.expire(now)
	if len(expired) != 1 || expired[0].attach.ViewerID != "expired" {
		t.Fatalf("expired attaches = %#v, want only expired", expired)
	}
	if _, ok := pending.take("live"); !ok {
		t.Fatal("expiring one viewer removed another viewer's pending attach")
	}
}

func TestMemberSessionArtifactsRequireCurrentOwnerAndPathAccess(t *testing.T) {
	workspace := t.TempDir()
	otherWorkspace := t.TempDir()
	sessionDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sessionDir, "egg.owner"), []byte("alice\nalice@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "egg.meta"), []byte("agent=claude\ncwd="+workspace+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	member := ws.TunnelRequest{SenderUserID: "alice", SenderEmail: "alice@example.com", SenderOrgRole: "member"}
	if !canAccessSessionArtifact(member, sessionDir, []string{workspace}) {
		t.Fatal("member could not read their audit inside their current workspace")
	}
	if canAccessSessionArtifact(member, sessionDir, []string{otherWorkspace}) {
		t.Fatal("member retained audit access after the session workspace was revoked")
	}
	otherMember := member
	otherMember.SenderUserID = "mallory"
	if canAccessSessionArtifact(otherMember, sessionDir, []string{workspace}) {
		t.Fatal("member could read another user's session audit")
	}
	admin := ws.TunnelRequest{SenderUserID: "admin", SenderOrgRole: "admin"}
	if !canAccessSessionArtifact(admin, sessionDir, nil) {
		t.Fatal("admin lost historical session oversight access")
	}

	missingMetadata := t.TempDir()
	if canAccessSessionArtifact(member, missingMetadata, []string{workspace}) {
		t.Fatal("member audit access did not fail closed without owner/path metadata")
	}
}

func TestSessionHistoryFiltersBeforePagination(t *testing.T) {
	workspace := t.TempDir()
	member := ws.TunnelRequest{SenderUserID: "alice", SenderOrgRole: "member"}
	sessions := []pastSessionInfo{
		{SessionID: "hidden-newest", UserID: "mallory", CWD: workspace},
		{SessionID: "alice-first", UserID: "alice", CWD: workspace},
		{SessionID: "alice-second", UserID: "alice", CWD: workspace},
	}
	visible := filterSessionsHistoryForRequest(member, sessions, []string{workspace})
	page, total := paginateSessionsHistory(visible, 0, 1)
	if total != 2 || len(page) != 1 || page[0].SessionID != "alice-first" {
		t.Fatalf("filtered history page = %#v total=%d", page, total)
	}
}

func TestSessionHistoryPaginationBoundsUntrustedIntegers(t *testing.T) {
	sessions := make([]pastSessionInfo, maxSessionsHistoryLimit+10)
	page, total := paginateSessionsHistory(sessions, -1, int(^uint(0)>>1))
	if total != len(sessions) || len(page) != maxSessionsHistoryLimit {
		t.Fatalf("bounded history page length=%d total=%d", len(page), total)
	}
	page, total = paginateSessionsHistory(sessions, int(^uint(0)>>1), 1)
	if total != len(sessions) || len(page) != 0 {
		t.Fatalf("oversized history offset returned length=%d total=%d", len(page), total)
	}
}

func TestFormatUserIdentity(t *testing.T) {
	tests := []struct {
		name string
		info auth.UserInfo
		want string
	}{
		{
			"full info",
			auth.UserInfo{DisplayName: "Phil Heckel", Email: "phil@test.com", Provider: "github"},
			"Phil Heckel (phil@test.com) via github",
		},
		{
			"email only",
			auth.UserInfo{Email: "phil@test.com", Provider: "google"},
			"phil@test.com via google",
		},
		{
			"name only",
			auth.UserInfo{DisplayName: "Phil Heckel"},
			"Phil Heckel",
		},
		{
			"name and provider",
			auth.UserInfo{DisplayName: "Phil Heckel", Provider: "github"},
			"Phil Heckel via github",
		},
		{
			"user_id fallback",
			auth.UserInfo{UserID: "abc123"},
			"abc123",
		},
		{
			"empty",
			auth.UserInfo{},
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatUserIdentity(&tt.info)
			if got != tt.want {
				t.Errorf("formatUserIdentity(%+v) = %q, want %q", tt.info, got, tt.want)
			}
		})
	}
}

func requireSetupAPIKeyHelper(t *testing.T, agent string, envMap map[string]string, home string) {
	t.Helper()
	if err := setupAPIKeyHelper(agent, envMap, home); err != nil {
		t.Fatalf("setupAPIKeyHelper: %v", err)
	}
}

func TestSetupAPIKeyHelper_RemovesKeyFromEnv(t *testing.T) {
	home := t.TempDir()
	envMap := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test123", "OTHER": "keep"}
	requireSetupAPIKeyHelper(t, "claude", envMap, home)
	if _, ok := envMap["ANTHROPIC_API_KEY"]; ok {
		t.Error("ANTHROPIC_API_KEY should be removed from envMap")
	}
	if envMap["OTHER"] != "keep" {
		t.Error("other env vars should be preserved")
	}
}

func TestSetupAPIKeyHelper_WritesKeyFile(t *testing.T) {
	home := t.TempDir()
	envMap := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-secret"}
	requireSetupAPIKeyHelper(t, "claude", envMap, home)
	keyFile := filepath.Join(home, ".anthropic_key")
	data, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("key file not written: %v", err)
	}
	if string(data) != "sk-ant-secret" {
		t.Errorf("key file = %q, want sk-ant-secret", string(data))
	}
	info, _ := os.Stat(keyFile)
	if info.Mode().Perm() != 0400 {
		t.Errorf("key file perm = %o, want 0400", info.Mode().Perm())
	}
}

func TestSetupAPIKeyHelper_SetsApiKeyHelperInSettings(t *testing.T) {
	home := t.TempDir()
	envMap := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"}
	requireSetupAPIKeyHelper(t, "claude", envMap, home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings not written: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	want := "cat " + filepath.Join(home, ".anthropic_key")
	if got := settings["apiKeyHelper"]; got != want {
		t.Errorf("apiKeyHelper = %q, want %q", got, want)
	}
}

func TestSetupAPIKeyHelper_PreservesExistingSettings(t *testing.T) {
	home := t.TempDir()
	settingsDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(settingsDir, 0700); err != nil {
		t.Fatal(err)
	}
	existing := map[string]any{"theme": "dark", "permissions": map[string]any{"allow": true}}
	data, err := json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	envMap := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"}
	requireSetupAPIKeyHelper(t, "claude", envMap, home)
	raw, err := os.ReadFile(filepath.Join(settingsDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["theme"] != "dark" {
		t.Errorf("existing theme setting clobbered, got %v", settings["theme"])
	}
	if settings["apiKeyHelper"] == nil {
		t.Error("apiKeyHelper not set")
	}
}

func TestSetupAPIKeyHelper_StablePath_NoSessionRace(t *testing.T) {
	// Two "sessions" calling setupAPIKeyHelper should write to the same file,
	// not per-session paths. This is the v0.128.0 bug fix.
	home := t.TempDir()
	env1 := map[string]string{"ANTHROPIC_API_KEY": "key-session-1"}
	env2 := map[string]string{"ANTHROPIC_API_KEY": "key-session-2"}
	requireSetupAPIKeyHelper(t, "claude", env1, home)
	requireSetupAPIKeyHelper(t, "claude", env2, home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	helper := settings["apiKeyHelper"].(string)
	// Both sessions should point to the same stable path (no session ID in path)
	wantPath := filepath.Join(home, ".anthropic_key")
	if helper != "cat "+wantPath {
		t.Errorf("apiKeyHelper = %q, want stable path %q", helper, "cat "+wantPath)
	}
	// The key file should contain the last writer's key (both are valid,
	// the point is the PATH is stable, not per-session)
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "key-session-2" {
		t.Errorf("key file = %q, want key-session-2", string(data))
	}
}

func TestSetupAPIKeyHelper_OverwritesOldReadOnlyKeyFile(t *testing.T) {
	home := t.TempDir()
	keyFile := filepath.Join(home, ".anthropic_key")
	if err := os.WriteFile(keyFile, []byte("old-key"), 0400); err != nil {
		t.Fatal(err)
	}
	envMap := map[string]string{"ANTHROPIC_API_KEY": "new-key"}
	requireSetupAPIKeyHelper(t, "claude", envMap, home)
	data, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("key file gone after overwrite: %v", err)
	}
	if string(data) != "new-key" {
		t.Errorf("key file = %q, want new-key", string(data))
	}
}

func TestSetupAPIKeyHelper_NonClaudeAgent_Noop(t *testing.T) {
	home := t.TempDir()
	envMap := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"}
	requireSetupAPIKeyHelper(t, "codex", envMap, home)
	if _, ok := envMap["ANTHROPIC_API_KEY"]; !ok {
		t.Error("non-claude agent should not remove ANTHROPIC_API_KEY")
	}
	keyFile := filepath.Join(home, ".anthropic_key")
	if _, err := os.Stat(keyFile); err == nil {
		t.Error("key file should not be created for non-claude agent")
	}
}

func TestSetupAPIKeyHelper_NoKey_Noop(t *testing.T) {
	home := t.TempDir()
	envMap := map[string]string{"OTHER": "val"}
	requireSetupAPIKeyHelper(t, "claude", envMap, home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); err == nil {
		t.Error("settings should not be created when no API key present")
	}
}

func TestParsePreviewFile(t *testing.T) {
	tests := []struct {
		name                            string
		data                            string
		mode, url, content, file, mtype string
	}{
		{name: "empty", data: "", mode: ""},
		{name: "whitespace only", data: "  \n\t\n", mode: ""},
		{
			name: "url mode", data: "url:https://example.com/app/",
			mode: "url", url: "https://example.com/app/",
		},
		{
			name: "localhost url mode", data: "url:http://127.0.0.1:3000/",
			mode: "url", url: "http://127.0.0.1:3000/",
		},
		{
			name: "javascript url becomes inert markdown",
			data: "url:javascript:alert(1)",
			mode: "markdown", content: "url:javascript:alert(1)",
			file: "preview.md", mtype: "text/markdown",
		},
		{
			name: "credentialed url becomes inert markdown",
			data: "url:https://user:secret@example.com/",
			mode: "markdown", content: "url:https://user:secret@example.com/",
			file: "preview.md", mtype: "text/markdown",
		},
		{
			name: "relative url becomes inert markdown",
			data: "url:/local/path",
			mode: "markdown", content: "url:/local/path",
			file: "preview.md", mtype: "text/markdown",
		},
		{
			name: "bare markdown defaults to preview.md",
			data: "# Report\n\n| a | b |\n",
			mode: "markdown", content: "# Report\n\n| a | b |\n",
			file: "preview.md", mtype: "text/markdown",
		},
		{
			name: "file header carries name and mime",
			data: "file:report.csv\npartner,status\nAcme,OK\n",
			mode: "markdown", content: "partner,status\nAcme,OK\n",
			file: "report.csv", mtype: "text/csv",
		},
		{
			name: "file header preserves leading whitespace in content",
			data: "file:main.go\n\tif x {\n\t\treturn\n\t}\n",
			mode: "markdown", content: "\tif x {\n\t\treturn\n\t}\n",
			file: "main.go", mtype: "text/x-go",
		},
		{
			name: "file header after blank lines",
			data: "\n\nfile:notes.txt\nbody\n",
			mode: "markdown", content: "body\n",
			file: "notes.txt", mtype: "text/plain",
		},
		{
			name: "file header with no content",
			data: "file:empty.json",
			mode: "markdown", content: "",
			file: "empty.json", mtype: "application/json",
		},
		{
			name: "path traversal stripped to base name",
			data: "file:../../etc/passwd\nroot\n",
			mode: "markdown", content: "root\n",
			file: "passwd", mtype: "application/octet-stream",
		},
		{
			name: "unusable file name falls back to markdown",
			data: "file:   \nstill content\n",
			mode: "markdown", content: "file:   \nstill content\n",
			file: "preview.md", mtype: "text/markdown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePreviewFile([]byte(tt.data))
			if got["mode"] != tt.mode {
				t.Errorf("mode = %q, want %q", got["mode"], tt.mode)
			}
			if got["url"] != tt.url {
				t.Errorf("url = %q, want %q", got["url"], tt.url)
			}
			if got["content"] != tt.content {
				t.Errorf("content = %q, want %q", got["content"], tt.content)
			}
			if got["filename"] != tt.file {
				t.Errorf("filename = %q, want %q", got["filename"], tt.file)
			}
			if tt.mtype != "" && !strings.HasPrefix(got["mime"], tt.mtype) {
				t.Errorf("mime = %q, want prefix %q", got["mime"], tt.mtype)
			}
		})
	}
}

func TestReadPreviewFileBoundedRejectsOversizedAndNonRegularInputs(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "preview")
	if err := os.WriteFile(regular, bytes.Repeat([]byte("x"), maxPreviewFileBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := readPreviewFileBounded(regular); err != nil || len(data) != maxPreviewFileBytes {
		t.Fatalf("read maximum preview: len=%d err=%v", len(data), err)
	}
	if err := os.WriteFile(regular, bytes.Repeat([]byte("x"), maxPreviewFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPreviewFileBounded(regular); !errors.Is(err, errPreviewTooLarge) {
		t.Fatalf("oversized preview error = %v", err)
	}

	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("host data"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "preview-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPreviewFileBounded(link); !errors.Is(err, errPreviewNotRegular) {
		t.Fatalf("symlink preview error = %v", err)
	}

	swapped := filepath.Join(dir, "preview-swapped")
	if err := os.WriteFile(swapped, []byte("safe preview"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readPreviewFileBoundedWithOpen(swapped, func(path string) (*os.File, error) {
		if err := os.Remove(path); err != nil {
			return nil, err
		}
		if err := os.Symlink(target, path); err != nil {
			return nil, err
		}
		return os.Open(path)
	})
	if !errors.Is(err, errPreviewNotRegular) {
		t.Fatalf("path-swap preview error = %v", err)
	}
}

func TestMarshalPreviewFileStaysInsideRelayEnvelopeBudget(t *testing.T) {
	data := bytes.Repeat([]byte("\x00"), maxPreviewJSONBytes)
	if _, err := marshalPreviewFile(data); !errors.Is(err, errPreviewTooLarge) {
		t.Fatalf("expanded preview error = %v", err)
	}
	encoded, err := marshalPreviewFile(bytes.Repeat([]byte("x"), 64<<10))
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxPreviewJSONBytes {
		t.Fatalf("encoded preview = %d bytes, limit %d", len(encoded), maxPreviewJSONBytes)
	}
}

func TestPreviewMIME(t *testing.T) {
	tests := []struct{ name, want string }{
		{"a.md", "text/markdown"},
		{"a.csv", "text/csv"},
		{"a.json", "application/json"},
		{"a.png", "image/png"},
		{"a.pdf", "application/pdf"},
		{"a.go", "text/x-go"},
		{"a.zig", "text/x-zig"},
		{"a.yaml", "application/yaml"},
		{"a.wasm", "application/wasm"},
		{"Makefile", "application/octet-stream"},
		{"a.qqqzzz", "application/octet-stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := previewMIME(tt.name); !strings.HasPrefix(got, tt.want) {
				t.Errorf("previewMIME(%q) = %q, want prefix %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestForgetAttentionStateRemovesAllSessionEntries(t *testing.T) {
	sessionID := "attention-cleanup-test"
	wingAttention.Store(sessionID, true)
	wingAttentionCooldown.Store(sessionID, time.Now())
	wingAttentionNonce.Store(sessionID, "nonce")
	t.Cleanup(func() { forgetAttentionState(sessionID) })

	forgetAttentionState(sessionID)
	if _, exists := wingAttention.Load(sessionID); exists {
		t.Fatal("attention entry was retained")
	}
	if _, exists := wingAttentionCooldown.Load(sessionID); exists {
		t.Fatal("attention cooldown was retained")
	}
	if _, exists := wingAttentionNonce.Load(sessionID); exists {
		t.Fatal("attention nonce was retained")
	}
}
