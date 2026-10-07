package wing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

func TestSortSessionsByStartKeepsLaunchOrder(t *testing.T) {
	sessions := []ws.SessionInfo{{SessionID: "11bb"}, {SessionID: "8e2e"}, {SessionID: "21da"}, {SessionID: "aa00"}}
	started := map[string]int64{"11bb": 300, "8e2e": 200, "21da": 100, "aa00": 200}
	sortSessionsByStart(sessions, started)
	var got []string
	for _, s := range sessions {
		got = append(got, s.SessionID)
	}
	if strings.Join(got, ",") != "21da,8e2e,aa00,11bb" {
		t.Fatalf("order = %v, want oldest first with ID tie-break", got)
	}
}
