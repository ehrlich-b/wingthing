package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/cipher"
	"crypto/ecdh"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	agentpkg "github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	directpkg "github.com/ehrlich-b/wingthing/internal/direct"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	relaypkg "github.com/ehrlich-b/wingthing/internal/relay"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/fsnotify/fsnotify"
	pionwebrtc "github.com/pion/webrtc/v4"
	"github.com/spf13/cobra"
)

// wingAttention tracks sessions that have triggered a terminal bell (need user attention).
var wingAttention sync.Map // sessionID → bool

// wingAttentionCooldown tracks last attention send time per session (30s throttle).
var wingAttentionCooldown sync.Map // sessionID → time.Time

// wingAttentionNonce tracks the current attention nonce per session.
// Same nonce = same attention episode. Cleared when user responds.
var wingAttentionNonce sync.Map // sessionID → string

// sessionIdleState tracks I/O timestamps for wing-side idle detection.
type sessionIdleState struct {
	mu         sync.Mutex
	lastInput  time.Time
	lastOutput time.Time
	connected  bool
	eggDir     string
}

// sessionStates tracks idle state for all active sessions.
var sessionStates sync.Map // sessionID -> *sessionIdleState

const attentionCooldown = 30 * time.Second

func writePTYMessage(write ws.PTYWriteFunc, message any) {
	if err := write(message); err != nil {
		log.Printf("send PTY message %T: %v", message, err)
	}
}

// checkAndSendAttention fires session.attention if the cooldown has elapsed.
// Returns true if the attention was sent.
func checkAndSendAttention(sessionID, agent, cwd string, write ws.PTYWriteFunc) bool {
	now := time.Now()
	if v, ok := wingAttentionCooldown.Load(sessionID); ok {
		if now.Sub(v.(time.Time)) < attentionCooldown {
			return false
		}
	}
	// Reuse nonce for the same attention episode; relay deduplicates by nonce.
	nonce, _ := wingAttentionNonce.LoadOrStore(sessionID, generateAttentionNonce())
	message := ws.SessionAttention{Type: ws.TypeSessionAttention, SessionID: sessionID, Agent: agent, CWD: cwd, Nonce: nonce.(string)}
	if err := write(message); err != nil {
		log.Printf("send attention for session %s: %v", sessionID, err)
		return false
	}
	wingAttention.Store(sessionID, true)
	wingAttentionCooldown.Store(sessionID, now)
	return true
}

// clearAttentionCooldown resets attention state for a session (user responded).
// Only applies the 30s grace period if there was an active notification — routine
// typing (no active attention) clears the cooldown entirely so the next bell fires.
func clearAttentionCooldown(sessionID string) {
	_, hadAttention := wingAttention.LoadAndDelete(sessionID)
	wingAttentionNonce.Delete(sessionID)
	if hadAttention {
		wingAttentionCooldown.Store(sessionID, time.Now()) // 30s grace after ack
	} else {
		wingAttentionCooldown.Delete(sessionID)
	}
}

func forgetAttentionState(sessionID string) {
	wingAttention.Delete(sessionID)
	wingAttentionCooldown.Delete(sessionID)
	wingAttentionNonce.Delete(sessionID)
}

// generateAttentionNonce returns a random 8-byte hex nonce.
func generateAttentionNonce() string {
	b := make([]byte, 8)
	if _, err := crand.Read(b); err != nil {
		log.Printf("generateAttentionNonce: crypto/rand failed: %v", err)
	}
	return fmt.Sprintf("%x", b)
}

// previewMIMEFallback covers extensions the system MIME database commonly
// misses. Go's builtin table is tiny (~20 types) and hosts without a
// /etc/mime.types are otherwise stuck with application/octet-stream.
var previewMIMEFallback = map[string]string{
	// docs / markup
	".md": "text/markdown", ".markdown": "text/markdown", ".rst": "text/x-rst",
	".txt": "text/plain", ".text": "text/plain", ".adoc": "text/asciidoc",
	".tex": "application/x-tex", ".org": "text/org",
	// config / data
	".yaml": "application/yaml", ".yml": "application/yaml",
	".toml": "application/toml", ".ini": "text/plain", ".conf": "text/plain",
	".cfg": "text/plain", ".properties": "text/plain", ".env": "text/plain",
	".json": "application/json", ".json5": "application/json",
	".jsonl": "application/x-ndjson", ".ndjson": "application/x-ndjson",
	".csv": "text/csv", ".tsv": "text/tab-separated-values",
	".xml": "application/xml", ".plist": "application/xml",
	".lock": "text/plain", ".log": "text/plain", ".diff": "text/x-diff",
	".patch": "text/x-diff", ".sql": "application/sql", ".proto": "text/plain",
	".graphql": "application/graphql", ".gql": "application/graphql",
	// web
	".html": "text/html", ".htm": "text/html", ".css": "text/css",
	".scss": "text/x-scss", ".sass": "text/x-sass", ".less": "text/x-less",
	".js": "text/javascript", ".mjs": "text/javascript", ".cjs": "text/javascript",
	".ts": "text/typescript", ".tsx": "text/typescript", ".jsx": "text/javascript",
	".vue": "text/plain", ".svelte": "text/plain", ".map": "application/json",
	// languages
	".go": "text/x-go", ".rs": "text/x-rust", ".zig": "text/x-zig",
	".c": "text/x-c", ".h": "text/x-c", ".cc": "text/x-c++", ".cpp": "text/x-c++",
	".cxx": "text/x-c++", ".hpp": "text/x-c++", ".hh": "text/x-c++",
	".py": "text/x-python", ".pyi": "text/x-python", ".rb": "text/x-ruby",
	".java": "text/x-java", ".kt": "text/x-kotlin", ".kts": "text/x-kotlin",
	".scala": "text/x-scala", ".swift": "text/x-swift", ".m": "text/x-objcsrc",
	".cs": "text/x-csharp", ".fs": "text/x-fsharp", ".php": "application/x-httpd-php",
	".pl": "text/x-perl", ".pm": "text/x-perl", ".lua": "text/x-lua",
	".r": "text/x-r", ".jl": "text/x-julia", ".dart": "application/dart",
	".ex": "text/x-elixir", ".exs": "text/x-elixir", ".erl": "text/x-erlang",
	".hs": "text/x-haskell", ".clj": "text/x-clojure", ".lisp": "text/x-lisp",
	".nim": "text/x-nim", ".v": "text/plain", ".asm": "text/x-asm", ".s": "text/x-asm",
	// shell / build
	".sh": "application/x-sh", ".bash": "application/x-sh", ".zsh": "application/x-sh",
	".fish": "application/x-sh", ".ps1": "application/x-powershell",
	".bat": "application/x-bat", ".cmd": "application/x-bat",
	".mk": "text/x-makefile", ".make": "text/x-makefile",
	".cmake": "text/x-cmake", ".gradle": "text/plain", ".bazel": "text/plain",
	".dockerfile": "text/plain", ".tf": "text/plain", ".tfvars": "text/plain",
	// images / media
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".svg": "image/svg+xml", ".webp": "image/webp",
	".avif": "image/avif", ".bmp": "image/bmp", ".ico": "image/x-icon",
	".tif": "image/tiff", ".tiff": "image/tiff", ".heic": "image/heic",
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg",
	".flac": "audio/flac", ".m4a": "audio/mp4", ".mp4": "video/mp4",
	".webm": "video/webm", ".mov": "video/quicktime", ".mkv": "video/x-matroska",
	// documents
	".pdf": "application/pdf", ".rtf": "application/rtf", ".epub": "application/epub+zip",
	".doc": "application/msword", ".xls": "application/vnd.ms-excel",
	".ppt":  "application/vnd.ms-powerpoint",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	// archives / binary
	".zip": "application/zip", ".gz": "application/gzip", ".tgz": "application/gzip",
	".bz2": "application/x-bzip2", ".xz": "application/x-xz", ".zst": "application/zstd",
	".tar": "application/x-tar", ".7z": "application/x-7z-compressed",
	".rar": "application/vnd.rar", ".wasm": "application/wasm",
	".cast": "application/x-asciicast", ".ttf": "font/ttf", ".otf": "font/otf",
	".woff": "font/woff", ".woff2": "font/woff2",
}

// previewMIME resolves a content type for a preview filename. The system MIME
// database covers the long tail; previewMIMEFallback covers what it misses.
func previewMIME(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return "application/octet-stream"
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	if ct, ok := previewMIMEFallback[ext]; ok {
		return ct
	}
	return "application/octet-stream"
}

// previewFilename sanitizes an agent-supplied preview filename down to a bare
// base name, so it can't steer the browser's download path.
func previewFilename(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		return ""
	}
	if len(name) > 128 {
		name = name[:128]
	}
	return name
}

// previewURL accepts only absolute HTTP(S) URLs without embedded credentials.
// The browser repeats this validation for compatibility with older wings, but
// rejecting unsafe schemes here keeps them out of the encrypted protocol too.
func previewURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 8192 {
		return "", false
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return "", false
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return "", false
	}
	return parsed.String(), true
}

// parsePreviewFile parses a .wt-preview file into a mode/url/content map.
//
// First line "url:<url>"   → URL mode.
// First line "file:<name>" → content mode carrying a filename, so the browser
// can offer a download with a real name and MIME type.
// Anything else            → content mode as markdown.
func parsePreviewFile(data []byte) map[string]string {
	if strings.TrimSpace(string(data)) == "" {
		return map[string]string{"mode": ""}
	}
	// Leading-trimmed only: the header may sit after blank lines, but content
	// after the header must survive byte-for-byte.
	lead := strings.TrimLeft(string(data), " \t\r\n")
	firstLine := lead
	if idx := strings.IndexByte(lead, '\n'); idx >= 0 {
		firstLine = lead[:idx]
	}
	firstLine = strings.TrimRight(firstLine, "\r")
	if strings.HasPrefix(firstLine, "url:") {
		if previewURL, ok := previewURL(firstLine[4:]); ok {
			return map[string]string{"mode": "url", "url": previewURL}
		}
	}
	// "file:" header: everything after the header line is content.
	if strings.HasPrefix(firstLine, "file:") {
		if name := previewFilename(firstLine[5:]); name != "" {
			body := ""
			if idx := strings.IndexByte(lead, '\n'); idx >= 0 {
				body = lead[idx+1:]
			}
			return map[string]string{
				"mode":     "markdown",
				"content":  body,
				"filename": name,
				"mime":     previewMIME(name),
			}
		}
	}
	return map[string]string{
		"mode":     "markdown",
		"content":  string(data),
		"filename": "preview.md",
		"mime":     "text/markdown",
	}
}

const (
	maxPreviewFileBytes = 1 << 20
	// Wing and relay WebSockets cap envelopes at 512 KiB. Leave room for GCM
	// nonce/tag, base64 expansion, and the outer pty.preview JSON envelope.
	maxPreviewJSONBytes = 350 << 10
)

var (
	errPreviewNotRegular = errors.New("preview path is not a regular file")
	errPreviewTooLarge   = errors.New("preview exceeds size limit")
)

func readPreviewFileBounded(path string) ([]byte, error) {
	return readPreviewFileBoundedWithOpen(path, os.Open)
}

func readPreviewFileBoundedWithOpen(path string, open func(string) (*os.File, error)) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	// Reject symlinks, sockets, devices, and FIFOs. Besides preventing host-file
	// reads through a symlink, this keeps a named pipe from blocking a watcher
	// goroutine forever while it waits for a writer.
	if !info.Mode().IsRegular() {
		return nil, errPreviewNotRegular
	}
	file, err := open(path)
	if err != nil {
		return nil, err
	}
	defer closeWithLog("preview file", file)
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	// Bind the pathname check to the file descriptor we actually read. An
	// agent can rename and replace files in its writable workspace between
	// Lstat and Open; reject that swap rather than following a newly installed
	// symlink with the host wing's broader filesystem authority.
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, errPreviewNotRegular
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPreviewFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPreviewFileBytes {
		return nil, errPreviewTooLarge
	}
	return data, nil
}

func marshalPreviewFile(data []byte) ([]byte, error) {
	jsonBytes, err := json.Marshal(parsePreviewFile(data))
	if err != nil {
		return nil, err
	}
	if len(jsonBytes) > maxPreviewJSONBytes {
		return nil, errPreviewTooLarge
	}
	return jsonBytes, nil
}

// consumeAndSendPreview reads a .wt-preview file, deletes it, encrypts the content, and sends it.
func consumeAndSendPreview(path, sessionID string, mu *sync.Mutex, gcm *cipher.AEAD, write ws.PTYWriteFunc) {
	data, err := readPreviewFileBounded(path)
	if err != nil {
		if errors.Is(err, errPreviewNotRegular) || errors.Is(err, errPreviewTooLarge) {
			log.Printf("pty session %s: discard preview: %v", sessionID, err)
			if removeErr := removeIfExists(path); removeErr != nil {
				log.Printf("pty session %s: remove rejected preview: %v", sessionID, removeErr)
			}
		}
		return
	}
	jsonBytes, err := marshalPreviewFile(data)
	if err != nil {
		if errors.Is(err, errPreviewTooLarge) {
			log.Printf("pty session %s: discard preview: %v", sessionID, err)
			if removeErr := removeIfExists(path); removeErr != nil {
				log.Printf("pty session %s: remove rejected preview: %v", sessionID, removeErr)
			}
		}
		return
	}

	mu.Lock()
	currentGCM := *gcm
	mu.Unlock()
	if currentGCM == nil {
		return
	}

	encrypted, err := auth.Encrypt(currentGCM, jsonBytes)
	if err != nil {
		log.Printf("pty session %s: preview encrypt error: %v", sessionID, err)
		return
	}
	if err := write(ws.PTYPreview{Type: ws.TypePTYPreview, SessionID: sessionID, Data: encrypted}); err != nil {
		log.Printf("pty session %s: preview send error: %v", sessionID, err)
		return
	}
	if err := removeIfExists(path); err != nil {
		log.Printf("pty session %s: remove consumed preview: %v", sessionID, err)
	}
}

// watchPreviewFile watches for the session-specific preview file in the given directory.
func watchPreviewFile(ctx context.Context, cwd, sessionID string, mu *sync.Mutex, gcm *cipher.AEAD, write ws.PTYWriteFunc) {
	previewFile := ".wt-preview-" + sessionID
	previewPath := filepath.Join(cwd, previewFile)

	// Try fsnotify first
	watcher, err := fsnotify.NewWatcher()
	if err == nil {
		defer closeWithLog("preview watcher", watcher)
		if addErr := watcher.Add(cwd); addErr != nil {
			log.Printf("pty session %s: fsnotify add failed, falling back to polling: %v", sessionID, addErr)
			goto poll
		}
		var debounce *time.Timer
		defer func() {
			if debounce != nil {
				debounce.Stop()
			}
		}()
		for {
			select {
			case ev, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Base(ev.Name) != previewFile {
					continue
				}
				if ev.Op&(fsnotify.Create|fsnotify.Write) == 0 {
					continue
				}
				if debounce != nil {
					debounce.Stop()
				}
				debounce = time.AfterFunc(50*time.Millisecond, func() {
					select {
					case <-ctx.Done():
						return
					default:
					}
					consumeAndSendPreview(previewPath, sessionID, mu, gcm, write)
				})
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}

poll:
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(previewPath); err == nil {
				consumeAndSendPreview(previewPath, sessionID, mu, gcm, write)
			}
		case <-ctx.Done():
			return
		}
	}
}

const (
	maxBrowserRequestReadBytes = 64 << 10
	maxBrowserOpenURLBytes     = 4096
)

// consumeBrowserRequestChunk extracts complete, bounded lines. An agent owns
// the request file, so a line without a newline must not grow host memory
// without bound. When a line crosses the cap, discard it through its newline.
func consumeBrowserRequestChunk(data []byte, pending *string, discarding *bool, emit func(string)) {
	combined := *pending + string(data)
	*pending = ""
	parts := strings.Split(combined, "\n")
	for _, raw := range parts[:len(parts)-1] {
		if *discarding {
			*discarding = false
			continue
		}
		line := strings.TrimSpace(raw)
		if line != "" && len(line) <= maxBrowserOpenURLBytes {
			emit(line)
		}
	}
	tail := parts[len(parts)-1]
	if *discarding {
		return
	}
	if len(tail) > maxBrowserOpenURLBytes {
		*discarding = true
		return
	}
	*pending = tail
}

// watchBrowserRequests polls for new lines in the browser-requests file and forwards them as PTYBrowserOpen messages.
func watchBrowserRequests(ctx context.Context, path, sessionID string, write ws.PTYWriteFunc) {
	var lastOffset int64
	var pending string
	var discarding bool
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			f, err := os.Open(path)
			if err != nil {
				continue
			}
			info, err := f.Stat()
			if err != nil {
				closeWithLog("browser request file", f)
				continue
			}
			if info.Size() < lastOffset {
				lastOffset = 0
				pending = ""
				discarding = false
			}
			if info.Size() == lastOffset {
				closeWithLog("browser request file", f)
				continue
			}
			if unread := info.Size() - lastOffset; unread > maxBrowserRequestReadBytes {
				lastOffset = info.Size() - maxBrowserRequestReadBytes
				pending = ""
				discarding = true // the retained window may begin in the middle of a line
			}
			if _, err := f.Seek(lastOffset, io.SeekStart); err != nil {
				closeWithLog("browser request file", f)
				log.Printf("seek browser request file for session %s: %v", sessionID, err)
				continue
			}
			data, err := io.ReadAll(io.LimitReader(f, maxBrowserRequestReadBytes))
			closeErr := f.Close()
			if err != nil || len(data) == 0 {
				continue
			}
			if closeErr != nil {
				log.Printf("close browser request file for session %s: %v", sessionID, closeErr)
				continue
			}
			lastOffset += int64(len(data))
			consumeBrowserRequestChunk(data, &pending, &discarding, func(line string) {
				writePTYMessage(write, ws.PTYBrowserOpen{Type: ws.TypePTYBrowserOpen, SessionID: sessionID, URL: line})
			})
		case <-ctx.Done():
			return
		}
	}
}

const maxTunnelKeyCacheEntries = 1024

// tunnelKeyCache bounds derived AES-GCM keys per sender public key. Direct-free
// users may send coordination tunnels, so a process-lifetime sync.Map would let
// an authenticated caller grow the wing indefinitely by rotating X25519 keys.
type tunnelKeyCache struct {
	mu      sync.Mutex
	max     int
	entries map[string]cipher.AEAD
	order   []string
}

func newTunnelKeyCache(max int) *tunnelKeyCache {
	return &tunnelKeyCache{max: max, entries: make(map[string]cipher.AEAD)}
}

func (c *tunnelKeyCache) Get(senderPub string) (cipher.AEAD, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key, ok := c.entries[senderPub]
	return key, ok
}

func (c *tunnelKeyCache) Put(senderPub string, key cipher.AEAD) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[senderPub]; exists {
		c.entries[senderPub] = key
		return
	}
	if c.max <= 0 {
		return
	}
	for len(c.entries) >= c.max && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	c.entries[senderPub] = key
	c.order = append(c.order, senderPub)
}

func (c *tunnelKeyCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

var tunnelKeys = newTunnelKeyCache(maxTunnelKeyCacheEntries)

// wingCfgMu serializes tunnel-driven wing.yaml mutations. Tunnel requests run
// on concurrent goroutines; unsynchronized admin edits could race each other
// into a corrupt config.
var wingCfgMu sync.Mutex

// clonePathList deep-copies a PathList so a failed save can roll the live ACL
// back to exactly its prior state.
func clonePathList(paths config.PathList) config.PathList {
	out := make(config.PathList, len(paths))
	for i, e := range paths {
		out[i] = e
		out[i].Members = append([]string(nil), e.Members...)
	}
	return out
}

// readEggOwner reads the creator user ID from an egg's owner file.
func readEggOwner(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "egg.owner"))
	if err != nil {
		return ""
	}
	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	return lines[0]
}

// readEggOwnerEmail reads the creator email from an egg's owner file (line 2).
func readEggOwnerEmail(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "egg.owner"))
	if err != nil {
		return ""
	}
	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	if len(lines) < 2 {
		return ""
	}
	return lines[1]
}

// killSessionsViolatingACLs checks active sessions and kills any that no longer
// have access under the current path ACLs.
func killSessionsViolatingACLs(cfg *config.Config, paths config.PathList, home string) {
	sessions := listAliveEggSessions(cfg)
	for _, s := range sessions {
		dir := filepath.Join(cfg.Dir, "eggs", s.SessionID)
		email := readEggOwnerEmail(dir)
		if email == "" {
			continue // pre-ACL session or admin — leave it
		}
		// Re-check if this user still has access to the session's CWD
		userPaths := resolvePathStrings(paths.PathsForUser(email, "member"), home)
		if len(userPaths) == 0 || !isUnderPaths(s.CWD, userPaths) {
			log.Printf("ACL revoke: killing session %s (user=%s cwd=%s)", s.SessionID, email, s.CWD)
			killOrphanEgg(cfg, s.SessionID)
		}
	}
}

// readEggMeta reads agent/cwd from an egg's meta file.
func readEggMeta(dir string) (agent, cwd string) {
	data, err := os.ReadFile(filepath.Join(dir, "egg.meta"))
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "agent":
			agent = v
		case "cwd":
			cwd = v
		}
	}
	return agent, cwd
}

// hasBell returns true if data contains any BEL character (0x07).
// Does NOT try to distinguish OSC terminators from "real" bells — callers
// use a time-window heuristic instead (repeated BELs = real notification).
func hasBell(data []byte) bool {
	return bytes.IndexByte(data, 0x07) >= 0
}

func gzipData(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ptyChunkSize is the max raw data size per WebSocket message. Larger payloads are
// split into multiple pty.output messages to stay under the 512KB WS read limit.
const ptyChunkSize = 128 * 1024 // 128KB raw → compresses well under WS limit

// sendPTYOutput encrypts and sends PTY output, chunking if the data exceeds ptyChunkSize.
func sendPTYOutputTagged(sessionID, viewerID string, data []byte, gcm cipher.AEAD, write ws.PTYWriteFunc) {
	if len(data) <= ptyChunkSize {
		encrypted, err := auth.Encrypt(gcm, data)
		if err != nil {
			log.Printf("pty session %s: encrypt error: %v", sessionID, err)
			return
		}
		writePTYMessage(write, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sessionID, Data: encrypted, ViewerID: viewerID})
		return
	}
	for sent := 0; sent < len(data); {
		end := sent + ptyChunkSize
		if end > len(data) {
			end = len(data)
		}
		encrypted, err := auth.Encrypt(gcm, data[sent:end])
		if err != nil {
			log.Printf("pty session %s: chunk encrypt error: %v", sessionID, err)
			return
		}
		writePTYMessage(write, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sessionID, Data: encrypted, ViewerID: viewerID})
		sent = end
	}
}

func sendPTYOutput(sessionID string, data []byte, gcm cipher.AEAD, write ws.PTYWriteFunc) {
	sendPTYOutputTagged(sessionID, "", data, gcm, write)
}

// sendReplayChunked splits replay data into chunks, compresses and encrypts each
// independently, and sends as multiple pty.output messages. Each chunk is a complete
// gzip stream so the browser can decompress them individually.
const replayChunkSize = 128 * 1024 // 128KB raw → compresses well under WS limit

func replayChunkEnd(raw []byte, start int) int {
	end := start + replayChunkSize
	if end >= len(raw) {
		return len(raw)
	}
	adjusted := end
	for steps := 0; steps < utf8.UTFMax-1 && adjusted > start && !utf8.RuneStart(raw[adjusted]); steps++ {
		adjusted--
	}
	if adjusted > start && utf8.RuneStart(raw[adjusted]) {
		return adjusted
	}
	return end
}

func sendReplayChunkedTagged(sessionID, viewerID string, raw []byte, gcm cipher.AEAD, write ws.PTYWriteFunc) {
	sent := 0
	chunks := 0
	totalCompressed := 0
	for sent < len(raw) {
		end := replayChunkEnd(raw, sent)
		chunk := raw[sent:end]
		compressed, gzErr := gzipData(chunk)
		if gzErr != nil {
			compressed = chunk
		}
		isCompressed := gzErr == nil
		encrypted, encErr := auth.Encrypt(gcm, compressed)
		if encErr != nil {
			log.Printf("pty session %s: replay chunk encrypt error: %v", sessionID, encErr)
			return
		}
		writePTYMessage(write, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sessionID, Data: encrypted, Compressed: isCompressed, ViewerID: viewerID})
		totalCompressed += len(compressed)
		sent = end
		chunks++
	}
	log.Printf("pty session %s: replayed %d bytes (gzip %d, %d chunks)", sessionID, len(raw), totalCompressed, chunks)
}

func sendReplayChunked(sessionID string, raw []byte, gcm cipher.AEAD, write ws.PTYWriteFunc) {
	sendReplayChunkedTagged(sessionID, "", raw, gcm, write)
}

// resolvePathStrings resolves ~/ prefixes and makes paths absolute.
// Returns empty if input is empty (no path restrictions).
func resolvePathStrings(paths []string, home string) []string {
	var out []string
	for _, p := range paths {
		if strings.HasPrefix(p, "~/") {
			p = filepath.Join(home, p[2:])
		} else if p == "~" {
			p = home
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		out = append(out, p)
	}
	return out
}

// pathsForRequest returns resolved paths filtered by the request sender's ACLs.
func pathsForRequest(pathList config.PathList, email, orgRole, home string) []string {
	return resolvePathStrings(pathList.PathsForUser(email, orgRole), home)
}

// filterProjectsByPaths returns only projects whose paths are under one of the resolved paths.
func filterProjectsByPaths(projects []ws.WingProject, resolvedPaths []string) []ws.WingProject {
	var out []ws.WingProject
	for _, p := range projects {
		if isUnderPaths(p.Path, resolvedPaths) {
			out = append(out, p)
		}
	}
	return out
}

// discoverWingProjects returns the project metadata a wing may advertise.
// Explicit path configuration is a disclosure boundary: never supplement it
// with projects found beneath the process cwd.
func discoverWingProjects(resolvedPaths []string, cwd string) []ws.WingProject {
	scanPaths := resolvedPaths
	maxDepth := 3
	if len(scanPaths) == 0 {
		if cwd == "" {
			return nil
		}
		scanPaths = []string{cwd}
		maxDepth = 2
	}

	seen := make(map[string]bool)
	var projects []ws.WingProject
	for _, scanPath := range scanPaths {
		for _, project := range discoverProjects(scanPath, maxDepth) {
			if seen[project.Path] {
				continue
			}
			seen[project.Path] = true
			projects = append(projects, project)
		}
	}
	return projects
}

// isUnderPaths returns true if path is equal to or under one of the resolved paths.
func isUnderPaths(path string, resolvedPaths []string) bool {
	cleaned := filepath.Clean(path)
	for _, rp := range resolvedPaths {
		if cleaned == rp || strings.HasPrefix(cleaned, rp+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// filterProjectsExact returns only projects whose paths exactly match one of the resolved paths.
func filterProjectsExact(projects []ws.WingProject, resolvedPaths []string) []ws.WingProject {
	var out []ws.WingProject
	for _, p := range projects {
		if isExactPath(p.Path, resolvedPaths) {
			out = append(out, p)
		}
	}
	return out
}

// isExactPath returns true if path exactly matches one of the configured paths.
func isExactPath(path string, paths []string) bool {
	cleaned := filepath.Clean(path)
	for _, p := range paths {
		if cleaned == p {
			return true
		}
	}
	return false
}

// isMemberRole grants elevated behavior only to the two coordinator roles the
// wing understands. Empty, legacy, and unexpected values stay least-privilege.
func isMemberRole(orgRole string) bool {
	return orgRole != "owner" && orgRole != "admin"
}

// discoverProjects scans dir for git repositories up to maxDepth levels deep.
// Returns group directories (sorted by project count) followed by individual repos (sorted by mtime).
func discoverProjects(dir string, maxDepth int) []ws.WingProject {
	var repos []ws.WingProject
	scanDir(dir, 0, maxDepth, &repos)

	// Count repos per parent directory
	parentCount := make(map[string]int)
	for _, r := range repos {
		parent := filepath.Dir(r.Path)
		if parent != dir { // skip the root scan dir itself
			parentCount[parent]++
		}
	}

	// Build group entries for parents with 2+ repos
	var groups []ws.WingProject
	seen := make(map[string]bool)
	for parent, count := range parentCount {
		if count >= 2 && !seen[parent] {
			seen[parent] = true
			groups = append(groups, ws.WingProject{
				Name:    filepath.Base(parent),
				Path:    parent,
				ModTime: int64(count), // abuse ModTime to carry count for sorting
			})
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].ModTime > groups[j].ModTime // most projects first
	})
	// Reset ModTime to actual value
	for i := range groups {
		groups[i].ModTime = projectModTime(groups[i].Path)
	}

	// Sort individual repos by mtime
	sort.Slice(repos, func(i, j int) bool {
		return repos[i].ModTime > repos[j].ModTime
	})

	return append(groups, repos...)
}

func projectModTime(dir string) int64 {
	info, err := os.Stat(dir)
	if err != nil {
		return 0
	}
	return info.ModTime().Unix()
}

func scanDir(dir string, depth, maxDepth int, projects *[]ws.WingProject) {
	if depth > maxDepth {
		return
	}

	// At depth 0, check if the configured path itself is a project.
	// This handles paths that point directly at project dirs (e.g.
	// paths: [~/repos/myproject]). At depth > 0, the parent's child
	// scan already added this dir if it had .git or egg.yaml.
	if depth == 0 {
		hasGit := false
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			hasGit = true
		}
		hasEgg := false
		if _, err := os.Stat(filepath.Join(dir, "egg.yaml")); err == nil {
			hasEgg = true
		}
		if hasGit || hasEgg {
			*projects = append(*projects, ws.WingProject{
				Name:    filepath.Base(dir),
				Path:    dir,
				ModTime: projectModTime(dir),
			})
			if hasGit {
				return
			}
			// egg.yaml only: also scan children for git repos
		}
	}

	// Not a project itself — scan children.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		gitDir := filepath.Join(full, ".git")
		eggFile := filepath.Join(full, "egg.yaml")
		hasGit := false
		hasEgg := false
		if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
			hasGit = true
		}
		if info, err := os.Stat(eggFile); err == nil && !info.IsDir() {
			hasEgg = true
		}
		if hasGit || hasEgg {
			*projects = append(*projects, ws.WingProject{
				Name:    e.Name(),
				Path:    full,
				ModTime: projectModTime(full),
			})
		}
		if hasGit {
			// Git repo found. Also check immediate children for egg.yaml
			// sub-projects (e.g. ai-playground/.git + ai-playground/dev/egg.yaml).
			if subs, err := os.ReadDir(full); err == nil {
				for _, sub := range subs {
					if !sub.IsDir() || strings.HasPrefix(sub.Name(), ".") {
						continue
					}
					subFull := filepath.Join(full, sub.Name())
					if info, err := os.Stat(filepath.Join(subFull, "egg.yaml")); err == nil && !info.IsDir() {
						*projects = append(*projects, ws.WingProject{
							Name:    sub.Name(),
							Path:    subFull,
							ModTime: projectModTime(subFull),
						})
					}
				}
			}
			continue
		}
		// No .git — keep scanning (egg.yaml dirs can contain git repos).
		scanDir(full, depth+1, maxDepth, projects)
	}
}

func wingPidPath() string {
	cfg, _ := config.Load()
	if cfg != nil {
		return filepath.Join(cfg.Dir, "wing.pid")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wingthing", "wing.pid")
}

const maxLogSize = 1 << 20 // 1MB

// rotateLog rotates path when it exceeds maxLogSize.
// Chain: .log -> .log.1 -> .log.2.gz -> deleted
func rotateLog(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && info.Size() < maxLogSize) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect log for rotation: %w", err)
	}

	// Delete oldest (.log.2.gz)
	if err := removeIfExists(path + ".2.gz"); err != nil {
		return fmt.Errorf("remove oldest rotated log: %w", err)
	}

	// Compress .log.1 -> .log.2.gz
	if data, err := os.ReadFile(path + ".1"); err == nil {
		if gz, err := os.Create(path + ".2.gz"); err == nil {
			w := gzip.NewWriter(gz)
			if _, werr := w.Write(data); werr != nil {
				closeWithLog("rotated gzip stream", w)
				closeWithLog("rotated log", gz)
				return fmt.Errorf("compress rotated log: %w", werr)
			}
			if err := w.Close(); err != nil {
				closeWithLog("rotated log", gz)
				return fmt.Errorf("finish rotated log compression: %w", err)
			}
			if err := gz.Close(); err != nil {
				return fmt.Errorf("close rotated log: %w", err)
			}
			if err := removeIfExists(path + ".1"); err != nil {
				return fmt.Errorf("remove compressed source log: %w", err)
			}
		} else {
			return fmt.Errorf("create compressed rotated log: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read rotated log: %w", err)
	}

	// Rotate current -> .log.1
	if err := os.Rename(path, path+".1"); err != nil {
		return fmt.Errorf("rotate current log: %w", err)
	}
	return nil
}

func wingArgsPath() string {
	cfg, _ := config.Load()
	if cfg != nil {
		return filepath.Join(cfg.Dir, "wing.args")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wingthing", "wing.args")
}

func wingLogPath() string {
	cfg, _ := config.Load()
	if cfg != nil {
		return filepath.Join(cfg.Dir, "wing.log")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wingthing", "wing.log")
}

func wingStatusPath() string {
	cfg, _ := config.Load()
	if cfg != nil {
		return filepath.Join(cfg.Dir, "wing.status")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wingthing", "wing.status")
}

// wingStatus is the JSON schema for wing.status.
type wingStatus struct {
	State    string `json:"state"` // connecting, connected, auth_failed, disconnected
	Error    string `json:"error,omitempty"`
	TS       string `json:"ts"`
	RoostURL string `json:"roost_url,omitempty"`
}

func writeWingStatus(state, lastErr string) {
	writeWingStatusForRoost(state, lastErr, "")
}

func writeWingStatusForRoost(state, lastErr, roostURL string) {
	s := wingStatus{
		State:    state,
		Error:    lastErr,
		TS:       time.Now().UTC().Format(time.RFC3339),
		RoostURL: relayMetadataURL(roostURL),
	}
	data, err := json.Marshal(s)
	if err != nil {
		log.Printf("encode wing status: %v", err)
		return
	}
	// A custom coordinator URL can itself carry deployment metadata. Keep the
	// status private just like the saved daemon arguments that selected it.
	if err := writeAtomicMetadataFile(wingStatusPath(), data, 0600); err != nil {
		log.Printf("write wing status: %v", err)
	}
}

func readWingStatus() (*wingStatus, error) {
	data, err := os.ReadFile(wingStatusPath())
	if err != nil {
		return nil, err
	}
	var s wingStatus
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// waitForWingStatus polls wing.status for up to timeout, returning the final state.
// Returns "connected", "auth_failed", or "" (timeout/still connecting).
func waitForWingStatus(pid int, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Check if daemon died
		if !ownedProcessIsAlive(pid) {
			// Process exited — check final status
			if s, err := readWingStatus(); err == nil {
				return s.State
			}
			return "auth_failed" // daemon died, likely auth
		}
		if s, err := readWingStatus(); err == nil {
			switch s.State {
			case "connected", "auth_failed":
				return s.State
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ""
}

func roostPidPath() string {
	cfg, _ := config.Load()
	if cfg != nil {
		return filepath.Join(cfg.Dir, "roost.pid")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wingthing", "roost.pid")
}

func roostArgsPath() string {
	cfg, _ := config.Load()
	if cfg != nil {
		return filepath.Join(cfg.Dir, "roost.args")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wingthing", "roost.args")
}

func roostLogPath() string {
	cfg, _ := config.Load()
	if cfg != nil {
		return filepath.Join(cfg.Dir, "roost.log")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wingthing", "roost.log")
}

func writeDaemonMetadata(pidPath, argsPath string, pid int, args []string) error {
	if err := writeAtomicMetadataFile(argsPath, []byte(strings.Join(args, "\n")), 0600); err != nil {
		return fmt.Errorf("write daemon args: %w", err)
	}
	if err := writeAtomicMetadataFile(pidPath, []byte(strconv.Itoa(pid)), 0644); err != nil {
		_ = os.Remove(argsPath)
		return fmt.Errorf("write daemon pid: %w", err)
	}
	return nil
}

func writeAtomicMetadataFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".wt-daemon-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer removeWithLog(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer closeWithLog("metadata directory", dir)
	return dir.Sync()
}

func acquireDaemonLifecycleLock() (*os.File, error) {
	return acquireDaemonLifecycleLockAt(filepath.Join(filepath.Dir(wingPidPath()), "daemon.lock"))
}

func acquireDaemonLifecycleLockAt(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open daemon lifecycle lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("another daemon start/stop is already in progress")
		}
		return nil, fmt.Errorf("lock daemon lifecycle: %w", err)
	}
	return file, nil
}

// abandonStartedDaemon terminates and reaps a child whose startup could not be
// committed to disk. This prevents a successful exec from becoming an
// invisible daemon when readiness or metadata persistence fails.
func abandonStartedDaemon(child *exec.Cmd) {
	if child == nil || child.Process == nil {
		return
	}
	_ = child.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_ = child.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = child.Process.Kill()
		<-done
	}
}

type daemonKind string

const (
	wingDaemon  daemonKind = "wing"
	roostDaemon daemonKind = "roost"
)

var (
	errNoDaemonRunning = errors.New("no daemon running")
	errStaleDaemonPID  = errors.New("stale daemon pid")
)

// readPidFrom reads a PID from a specific file and verifies that it still
// identifies the expected wt foreground daemon. PIDs are recycled, so merely
// finding a live same-UID process is not enough before callers send signals.
func readPidFrom(path string, kind daemonKind) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		_ = os.Remove(path)
		return 0, fmt.Errorf("%w: invalid PID: %v", errStaleDaemonPID, err)
	}
	if !ownedProcessIsAlive(pid) {
		_ = os.Remove(path)
		return 0, errStaleDaemonPID
	}
	matches, inspectErr := inspectDaemonPid(pid, kind)
	if inspectErr != nil {
		return 0, inspectErr
	}
	if !matches {
		_ = os.Remove(path)
		return 0, errStaleDaemonPID
	}
	return pid, nil
}

// daemonPidMatches confirms the command shape emitted by wingStartCmd or
// roostStartCmd. Failure to inspect argv fails closed: a status check may call
// a daemon stopped, but stop/update will never signal an unconfirmed process.
func inspectDaemonPid(pid int, kind daemonKind) (bool, error) {
	argv, err := processArgv(pid)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		return false, fmt.Errorf("inspect %s daemon pid %d: %w", kind, pid, err)
	}
	return daemonArgvMatches(argv, kind), nil
}

func daemonArgvMatches(argv []string, kind daemonKind) bool {
	if len(argv) < 4 || argv[2] != "start" {
		return false
	}
	switch kind {
	case wingDaemon:
		if argv[1] != "wing" && argv[1] != "daemon" {
			return false
		}
	case roostDaemon:
		if argv[1] != "roost" {
			return false
		}
	default:
		return false
	}
	for _, arg := range argv[3:] {
		if arg == "--foreground" {
			return true
		}
	}
	return false
}

func parseSavedDaemonArgs(data []byte, kind daemonKind) ([]string, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, fmt.Errorf("empty daemon args")
	}
	args := strings.Split(trimmed, "\n")
	argv := append([]string{"wt"}, args...)
	if !daemonArgvMatches(argv, kind) {
		return nil, fmt.Errorf("saved args do not describe a %s foreground daemon", kind)
	}
	return args, nil
}

// readDaemon returns the one live daemon represented by local metadata. A
// healthy installation cannot run a standalone wing and a roost at once. If
// both metadata files identify live daemons, fail closed so lifecycle commands
// do not stop an arbitrary half of the conflicting installation.
func readDaemon() (int, daemonKind, error) {
	wingPID, wingErr := readPidFrom(wingPidPath(), wingDaemon)
	roostPID, roostErr := readPidFrom(roostPidPath(), roostDaemon)
	if wingErr == nil && roostErr == nil {
		return 0, "", fmt.Errorf("both wing (pid %d) and roost (pid %d) daemons are running", wingPID, roostPID)
	}
	if wingErr == nil {
		if !daemonAbsentError(roostErr) {
			return 0, "", roostErr
		}
		return wingPID, wingDaemon, nil
	}
	if roostErr == nil {
		if !daemonAbsentError(wingErr) {
			return 0, "", wingErr
		}
		return roostPID, roostDaemon, nil
	}
	if !daemonAbsentError(wingErr) {
		return 0, "", wingErr
	}
	if !daemonAbsentError(roostErr) {
		return 0, "", roostErr
	}
	return 0, "", errNoDaemonRunning
}

func daemonAbsentError(err error) bool {
	return os.IsNotExist(err) || errors.Is(err, errStaleDaemonPID)
}

// readPid tries wing.pid first, then roost.pid. Returns the first live daemon PID.
func readPid() (int, error) {
	pid, _, err := readDaemon()
	return pid, err
}

// stopDaemonAndWait revalidates the daemon command immediately before
// signaling it, then waits until that specific daemon identity is gone. This
// keeps the lifecycle lock meaningful: callers must not delete metadata and
// allow a replacement to start while the old listener is still shutting down.
func stopDaemonAndWait(pid int, kind daemonKind, timeout time.Duration) error {
	if !ownedProcessIsAlive(pid) {
		return nil
	}
	matches, inspectErr := inspectDaemonPid(pid, kind)
	if inspectErr != nil {
		return inspectErr
	}
	if !matches {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find %s daemon pid %d: %w", kind, pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		if !ownedProcessIsAlive(pid) {
			return nil
		}
		matches, inspectErr = inspectDaemonPid(pid, kind)
		if inspectErr != nil {
			return inspectErr
		}
		if !matches {
			return nil
		}
		return fmt.Errorf("stop %s daemon pid %d: %w", kind, pid, err)
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !ownedProcessIsAlive(pid) {
			return nil
		}
		matches, inspectErr = inspectDaemonPid(pid, kind)
		if inspectErr != nil {
			return inspectErr
		}
		if !matches {
			return nil
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("%s daemon pid %d did not stop within %s", kind, pid, timeout)
		case <-ticker.C:
		}
	}
}

func wingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "daemon",
		Aliases: []string{"wing"},
		Short:   "Connect this machine to a relay, accessible from anywhere",
		Long:    "Makes this machine reachable from anywhere via the relay.\nUse 'wt daemon start' to go online, 'wt daemon status' to check.",
	}

	cmd.AddCommand(wingStartCmd())
	cmd.AddCommand(wingStopCmd())
	cmd.AddCommand(wingStatusCmd())
	cmd.AddCommand(wingAllowCmd())
	cmd.AddCommand(wingRevokeCmd())
	cmd.AddCommand(wingLockCmd())
	cmd.AddCommand(wingUnlockCmd())
	cmd.AddCommand(wingConfigCmd())

	return cmd
}

func wingStartCmd() *cobra.Command {
	var roostFlag string
	var labelsFlag string
	var convFlag string
	var foregroundFlag bool
	var debugFlag bool
	var eggConfigFlag string
	var orgFlag string
	var allowFlags []string
	var pathsFlag string
	var auditFlag bool
	var localFlag bool
	var rawReplayFlag bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start wing daemon and go online",
		Long:  "Start a wing — your machine becomes reachable from anywhere via the roost. Runs as a background daemon by default. Use --foreground for debugging.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Foreground mode: run directly
			if foregroundFlag {
				return runWingForeground(cmd, roostFlag, labelsFlag, convFlag, eggConfigFlag, orgFlag, allowFlags, pathsFlag, debugFlag, auditFlag, localFlag, !rawReplayFlag)
			}
			lifecycleLock, err := acquireDaemonLifecycleLock()
			if err != nil {
				return err
			}
			defer closeWithLog("daemon lifecycle lock", lifecycleLock)

			// Daemon mode (default): re-exec detached, write PID file, return
			if pid, _, err := readDaemon(); err == nil {
				return fmt.Errorf("wing daemon already running (pid %d)", pid)
			} else if !errors.Is(err, errNoDaemonRunning) {
				return fmt.Errorf("inspect daemon state: %w", err)
			}

			// Pre-flight auth probe: catch expired tokens before spawning daemon
			if !localFlag || roostFlag != "" {
				cfg, cfgErr := config.Load()
				if cfgErr == nil {
					ts := auth.NewTokenStore(cfg.Dir)
					tok, tokErr := ts.Load()
					if tokErr != nil || !ts.IsValid(tok) {
						return fmt.Errorf("not logged in — run: wt login")
					}
					// Use the same precedence and normalization as the child daemon.
					relayURL := resolveWingRelayHTTPURL(cfg, roostFlag, localFlag)
					if err := auth.ValidateTokenRemote(relayURL, tok.Token); err != nil {
						if errors.Is(err, auth.ErrAuthFailed) {
							return fmt.Errorf("login expired — run: wt login")
						}
						// Network error: warn but proceed (daemon will retry)
						fmt.Printf("warning: relay unreachable (%v) — starting daemon anyway\n", err)
					}
				}
			}

			exe, err := os.Executable()
			if err != nil {
				return err
			}

			// Build args for foreground child
			var childArgs []string
			childArgs = append(childArgs, "wing", "start", "--foreground")
			if roostFlag != "" {
				childArgs = append(childArgs, "--roost", roostFlag)
			}
			if labelsFlag != "" {
				childArgs = append(childArgs, "--labels", labelsFlag)
			}
			if convFlag != "auto" {
				childArgs = append(childArgs, "--conv", convFlag)
			}
			if eggConfigFlag != "" {
				childArgs = append(childArgs, "--egg-config", eggConfigFlag)
			}
			if orgFlag != "" {
				childArgs = append(childArgs, "--org", orgFlag)
			}
			for _, ak := range allowFlags {
				childArgs = append(childArgs, "--allow", ak)
			}
			if pathsFlag != "" {
				childArgs = append(childArgs, "--paths", pathsFlag)
			}
			if debugFlag {
				childArgs = append(childArgs, "--debug")
			}
			if auditFlag {
				childArgs = append(childArgs, "--audit")
			}
			if localFlag {
				childArgs = append(childArgs, "--local")
			}
			if rawReplayFlag {
				childArgs = append(childArgs, "--raw-replay")
			}

			// Remove stale status from previous run
			if err := removeIfExists(wingStatusPath()); err != nil {
				return fmt.Errorf("remove stale wing status: %w", err)
			}

			if err := rotateLog(wingLogPath()); err != nil {
				return err
			}
			logFile, err := os.OpenFile(wingLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				return fmt.Errorf("open log: %w", err)
			}

			home, err := os.UserHomeDir()
			if err != nil {
				closeWithLog("wing log", logFile)
				return fmt.Errorf("resolve user home: %w", err)
			}

			child := exec.Command(exe, childArgs...)
			child.Dir = home
			child.Stdout = logFile
			child.Stderr = logFile
			child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

			if err := child.Start(); err != nil {
				closeWithLog("wing log", logFile)
				return fmt.Errorf("start daemon: %w", err)
			}
			if err := logFile.Close(); err != nil {
				abandonStartedDaemon(child)
				return fmt.Errorf("close wing log: %w", err)
			}

			if err := writeDaemonMetadata(wingPidPath(), wingArgsPath(), child.Process.Pid, childArgs); err != nil {
				abandonStartedDaemon(child)
				return fmt.Errorf("start daemon: %w", err)
			}

			// Wait for daemon to report initial connection state
			startupResult := waitForWingStatus(child.Process.Pid, 5*time.Second)
			switch startupResult {
			case "auth_failed":
				// Kill daemon, clean up
				abandonStartedDaemon(child)
				if err := removeFiles(wingPidPath(), wingArgsPath(), wingStatusPath()); err != nil {
					return errors.Join(fmt.Errorf("login expired — run: wt login"), fmt.Errorf("remove failed daemon metadata: %w", err))
				}
				return fmt.Errorf("login expired — run: wt login")
			case "connected":
				fmt.Printf("wing daemon started (pid %d)\n", child.Process.Pid)
				fmt.Printf("  relay: connected\n")
			default:
				// Timeout or still connecting — daemon is running, relay might be slow
				fmt.Printf("wing daemon started (pid %d)\n", child.Process.Pid)
				fmt.Printf("  relay: connecting...\n")
			}
			if err := child.Process.Release(); err != nil {
				log.Printf("warning: failed to release daemon process handle: %v", err)
			}
			// Show account identity
			if cfgLoaded, cfgErr := config.Load(); cfgErr == nil {
				relayURL := resolveWingRelayHTTPURL(cfgLoaded, roostFlag, localFlag)
				if tok, tokErr := auth.NewTokenStore(cfgLoaded.Dir).Load(); tokErr == nil && tok != nil {
					if info, infoErr := auth.FetchUserInfo(relayURL, tok.Token); infoErr == nil {
						fmt.Printf("  account: %s\n", formatUserIdentity(info))
					}
				}
			}
			fmt.Printf("  log: %s\n", wingLogPath())
			fmt.Println()
			if cfgLoaded, cfgErr := config.Load(); cfgErr == nil {
				browserURL := roostBrowserURL(resolveWingRelayHTTPURL(cfgLoaded, roostFlag, localFlag))
				fmt.Printf("open %s for wing status and direct-agent setup\n", browserURL)
			} else if localFlag {
				fmt.Println("open http://localhost:8080/app/ for wing status and direct-agent setup")
			} else {
				fmt.Println("open https://app.wingthing.ai/ for wing status and direct-agent setup")
			}
			if !localFlag {
				fmt.Println("hosted browser terminals require relay access")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&roostFlag, "roost", "", "roost server URL (default: ws.wingthing.ai)")
	cmd.Flags().StringVar(&labelsFlag, "labels", "", "comma-separated wing labels (e.g. gpu,cuda,research)")
	cmd.Flags().StringVar(&convFlag, "conv", "auto", "conversation mode: auto (daily rolling), new (fresh), or a named thread")
	cmd.Flags().BoolVar(&foregroundFlag, "foreground", false, "run in foreground instead of daemonizing")
	cmd.Flags().BoolVar(&debugFlag, "debug", false, "dump raw PTY output to /tmp/wt-pty-<session>.bin for each egg")
	cmd.Flags().StringVar(&eggConfigFlag, "egg-config", "", "path to egg.yaml for wing-level sandbox defaults")
	cmd.Flags().StringVar(&orgFlag, "org", "", "org name or ID — share this wing with org members")
	cmd.Flags().StringSliceVar(&allowFlags, "allow", nil, "ephemeral passkey public key(s) for this session")
	cmd.Flags().StringVar(&pathsFlag, "paths", "", "comma-separated directories the wing can browse (default: ~/)")
	cmd.Flags().BoolVar(&auditFlag, "audit", false, "enable audit logging for all egg sessions")
	cmd.Flags().BoolVar(&localFlag, "local", false, "connect to localhost:8080 (for self-hosted wt serve)")
	cmd.Flags().BoolVar(&rawReplayFlag, "raw-replay", false, "use raw replay buffer for reconnect instead of VTerm snapshot")

	return cmd
}

func runWingForeground(cmd *cobra.Command, roostFlag, labelsFlag, convFlag, eggConfigFlag, orgFlag string, allowFlags []string, pathsFlag string, debug, audit, local, vte bool) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	defer removeWithLog(wingStatusPath())

	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)
	defer signal.Stop(sighupCh)

	return runWingWithContext(ctx, sighupCh, roostFlag, labelsFlag, convFlag, eggConfigFlag, orgFlag, allowFlags, pathsFlag, debug, audit, local, vte, false, nil)
}

func runWingWithContext(ctx context.Context, sighupCh <-chan os.Signal, roostFlag, labelsFlag, convFlag, eggConfigFlag, orgFlag string, allowFlags []string, pathsFlag string, debug, audit, local, vte, sharedHost bool, tokenOverride *auth.DeviceToken) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Load wing.yaml
	wingCfg, err := loadWingConfigForStart(cfg.Dir)
	if err != nil {
		return err
	}

	// Merge wing.yaml with CLI flags (CLI extends yaml)
	if roostFlag == "" && wingCfg.Roost != "" {
		roostFlag = wingCfg.Roost
	}
	if orgFlag == "" && wingCfg.Org != "" {
		orgFlag = wingCfg.Org
	} else if orgFlag != "" && wingCfg.Org != "" && orgFlag != wingCfg.Org {
		return fmt.Errorf("org conflict: --org %q vs wing.yaml %q", orgFlag, wingCfg.Org)
	}
	// Merge paths: CLI extends yaml (same pattern as labels)
	var cliPaths []string
	if pathsFlag != "" {
		for _, p := range strings.Split(pathsFlag, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				cliPaths = append(cliPaths, p)
			}
		}
	}
	if len(cliPaths) == 0 && len(wingCfg.Paths) > 0 {
		cliPaths = wingCfg.Paths.Strings()
	}
	if eggConfigFlag == "" && wingCfg.EggConfig != "" {
		eggConfigFlag = wingCfg.EggConfig
	}
	if convFlag == "auto" && wingCfg.Conv != "" {
		convFlag = wingCfg.Conv
	}
	if wingCfg.Audit {
		audit = true
	}
	if wingCfg.Debug {
		debug = true
	}
	if labelsFlag == "" && len(wingCfg.Labels) > 0 {
		labelsFlag = strings.Join(wingCfg.Labels, ",")
	}

	// Hot-reloadable flags — new sessions read .Load(), SIGHUP updates .Store()
	var auditLive atomic.Bool
	auditLive.Store(audit)
	var debugLive atomic.Bool
	debugLive.Store(debug)

	// Build allowed passkey keys: pinned (from wing.yaml) + ephemeral (from --allow)
	var allowedKeys []config.AllowKey
	allowedKeys = append(allowedKeys, wingCfg.AllowKeys...)
	pinnedCount := len(allowedKeys)
	for _, k := range allowFlags {
		k = strings.TrimSpace(k)
		if k != "" {
			allowedKeys = append(allowedKeys, config.AllowKey{Key: k})
		}
	}
	ephemeralCount := len(allowedKeys) - pinnedCount

	// Boot-scoped passkey auth cache — tokens live until wing process dies
	passkeyCache := auth.NewAuthCache()
	passkeyChallenges := auth.NewChallengeCache()

	// Load wing-level egg config (with base chain resolution)
	var wingEggCfg *egg.EggConfig
	if eggConfigFlag != "" {
		wingEggCfg, err = egg.ResolveEggConfig(eggConfigFlag)
		if err != nil {
			return fmt.Errorf("load egg config: %w", err)
		}
		log.Printf("egg: loaded wing config from %s (network=%s)", eggConfigFlag, wingEggCfg.NetworkSummary())
	} else {
		// Check ~/.wingthing/egg.yaml
		defaultPath := filepath.Join(cfg.Dir, "egg.yaml")
		wingEggCfg, err = egg.ResolveEggConfig(defaultPath)
		if err != nil {
			wingEggCfg = egg.DefaultEggConfig()
			log.Printf("egg: using default config (network=%s)", wingEggCfg.NetworkSummary())
		} else {
			log.Printf("egg: loaded wing config from %s (network=%s)", defaultPath, wingEggCfg.NetworkSummary())
		}
	}
	var wingEggMu sync.Mutex

	// Load privileged tool configs
	toolsDir := config.ResolveToolsDir(cfg.Dir, wingCfg.ToolsDir)
	wingTools, toolErr := config.LoadToolsDir(toolsDir)
	if toolErr != nil {
		log.Printf("wing: load tools: %v (continuing without tools)", toolErr)
	} else if len(wingTools) > 0 {
		log.Printf("wing: loaded %d tool(s) from %s", len(wingTools), toolsDir)
	}
	var wingToolsMu sync.Mutex

	// Resolve roost URL
	roostURL := roostFlag
	if local && roostURL == "" {
		roostURL = "http://localhost:8080"
	}
	if roostURL == "" {
		roostURL = cfg.RoostURL
	}
	if roostURL == "" {
		roostURL = "https://ws.wingthing.ai"
	}
	var passkeyPolicyLive atomic.Value
	passkeyPolicyLive.Store(passkeyPolicyForRoost(passkeyRPURL(roostURL, os.Getenv("WT_BASE_URL"))))
	currentPasskeyPolicy := func() auth.PasskeyPolicy {
		return passkeyPolicyLive.Load().(auth.PasskeyPolicy)
	}
	// Convert HTTP URL to WebSocket URL
	wsURL := strings.Replace(roostURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL = strings.TrimRight(wsURL, "/") + "/ws/wing"

	// An all-in-one roost passes its embedded service credential in memory. It
	// must not overwrite device_token.yaml: that file may hold the operator's
	// independent wingthing.ai identity for standalone wings on this machine.
	tok, err := wingConnectionToken(cfg.Dir, local, tokenOverride)
	if err != nil {
		if local {
			return fmt.Errorf("no device token — run: wt serve --local")
		}
		return fmt.Errorf("not logged in — run: wt login")
	}

	// Detect available agents
	var agents []string
	for _, definition := range agentpkg.Definitions() {
		if _, err := exec.LookPath(definition.Command); err == nil {
			agents = append(agents, definition.Name)
		}
	}

	// List installed skills
	var skills []string
	entries, _ := os.ReadDir(cfg.SkillsDir())
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			skills = append(skills, strings.TrimSuffix(e.Name(), ".md"))
		}
	}

	// Parse labels
	var labels []string
	if labelsFlag != "" {
		labels = strings.Split(labelsFlag, ",")
	}

	// Resolve paths to absolute
	home, _ := os.UserHomeDir()
	resolvedPaths := resolvePathStrings(cliPaths, home)
	rootDir := home
	if len(resolvedPaths) > 0 {
		rootDir = resolvedPaths[0]
	}

	// Scan only within explicitly configured paths. When no paths are configured,
	// preserve the legacy cwd discovery behavior. A detached daemon starts in the
	// user's home directory, so adding cwd to an explicit --paths scan would
	// disclose unrelated project names and paths to the coordinator.
	cwd, _ := os.Getwd()
	projects := discoverWingProjects(resolvedPaths, cwd)

	fmt.Printf("connecting to %s\n", wsURL)
	fmt.Printf("  agents: %v\n", agents)
	fmt.Printf("  skills: %v\n", skills)
	if len(labels) > 0 {
		fmt.Printf("  labels: %v\n", labels)
	}
	fmt.Printf("  paths: %v\n", resolvedPaths)
	fmt.Printf("  projects: %d found\n", len(projects))
	for _, p := range projects {
		fmt.Printf("    %s → %s\n", p.Name, p.Path)
	}
	fmt.Printf("  conv: %s\n", convFlag)
	if len(allowedKeys) > 0 {
		fmt.Printf("  access control enabled: %d pinned + %d ephemeral keys\n", pinnedCount, ephemeralCount)
	}
	fmt.Println()
	fmt.Printf("open %s to start a terminal\n", roostBrowserURL(roostURL))

	// Reap dead egg directories on startup
	reapDeadEggs(cfg)

	// Ensure wing keypair exists (auto-generate on first run)
	if _, err := auth.EnsureKeyPair(cfg.Dir); err != nil {
		return fmt.Errorf("ensure keypair: %w", err)
	}
	// Load wing private key for tunnel E2E encryption
	privKey, privKeyErr := auth.LoadPrivateKey(cfg.Dir)
	if privKeyErr != nil {
		return fmt.Errorf("load private key: %w", privKeyErr)
	}

	// WebRTC backs both opt-in browser PTY migration and native direct MCP.
	// Keep the manager available in ordinary relay mode for native control, but
	// advertise browser P2P only for its existing p2p/p2p_only modes below.
	var peerMgr *webrtcpkg.PeerManager
	peerManagerEnabled := wingCfg.ConnectionMode != "direct"
	if peerManagerEnabled {
		var iceServers []pionwebrtc.ICEServer
		for _, s := range wingCfg.ICEServers {
			iceServers = append(iceServers, pionwebrtc.ICEServer{
				URLs:       s.URLs,
				Username:   s.Username,
				Credential: s.Credential,
			})
		}
		peerMgr = webrtcpkg.NewPeerManager(iceServers)
		defer peerMgr.Close()
		log.Printf("[P2P] peer manager initialized (mode=%s, ice_servers=%d)", wingCfg.ConnectionMode, len(iceServers))
	}

	// P2P: track DataChannels and SwappableWriters per session
	var dcSessions sync.Map // sessionID → *pionwebrtc.DataChannel
	var swSessions sync.Map // sessionID → *webrtcpkg.SwappableWriter
	directMCPAdmission := newMCPAdmissionState()

	var client *ws.Client // declared early so peerMgr.OnDC closure can capture it
	var directSrv *directpkg.Server

	// P2P: wire up DataChannel message routing when DCs open
	if peerMgr != nil {
		peerMgr.OnDC(func(senderPub, sessionID string, ident webrtcpkg.PeerIdentity, dc *pionwebrtc.DataChannel) {
			if strings.HasPrefix(dc.Label(), control.DirectChannelPrefix) {
				if ident.UserID == "" {
					log.Printf("[P2P] rejected direct MCP channel from %s: missing authenticated identity", shortLogValue(senderPub))
					if err := dc.Close(); err != nil {
						log.Printf("[P2P] close rejected direct MCP channel: %v", err)
					}
					return
				}
				serveDirectMCPChannelWithPolicySource(cfg, home, sharedHost, directMCPAdmission, ident, dc, func() (*config.WingConfig, []config.AllowKey) {
					wingCfgMu.Lock()
					defer wingCfgMu.Unlock()
					return wingCfg.Clone(), append([]config.AllowKey(nil), allowedKeys...)
				})
				return
			}
			if sessionID == "" {
				log.Printf("[P2P] DC opened with no session ID from %s", shortLogValue(senderPub))
				return
			}
			if !ws.ValidSessionID(sessionID) {
				log.Printf("[P2P] rejected DC with invalid session ID %q from %s", sessionID, shortLogValue(senderPub))
				if err := dc.Close(); err != nil {
					log.Printf("[P2P] close invalid session channel: %v", err)
				}
				return
			}
			// The label is client-controlled; a DataChannel feeds the session's
			// trusted input channel, so only the session owner's peer identity
			// may bind one. Anything else could inject input or kill a session
			// it does not own.
			owner := readEggOwner(filepath.Join(cfg.Dir, "eggs", sessionID))
			if ident.UserID == "" || owner == "" || ident.UserID != owner {
				log.Printf("[P2P] rejected DC for session %s from %s: sender is not the session owner", sessionID, shortLogValue(senderPub))
				if err := dc.Close(); err != nil {
					log.Printf("[P2P] close rejected session channel: %v", err)
				}
				return
			}
			dcSessions.Store(sessionID, dc)
			log.Printf("[P2P] DC stored for session %s from %s", sessionID, shortLogValue(senderPub))

			dc.OnMessage(func(msg pionwebrtc.DataChannelMessage) {
				if !currentDataChannel(&dcSessions, sessionID, dc) {
					return
				}
				client.PushPTYInput(sessionID, msg.Data)
			})
			dc.OnClose(func() {
				if !dcSessions.CompareAndDelete(sessionID, dc) {
					log.Printf("[P2P] stale DC closed for session %s", sessionID)
					return
				}
				// Trigger fallback to relay if session still active
				if swVal, ok := swSessions.Load(sessionID); ok {
					sessionSW := swVal.(*webrtcpkg.SwappableWriter)
					if err := sessionSW.FallbackToRelay(sessionID); err != nil {
						log.Printf("[P2P] fall back session %s to relay: %v", sessionID, err)
					}
				}
				log.Printf("[P2P] DC closed for session %s", sessionID)
			})
		})
	}

	client = &ws.Client{
		RoostURL:     wsURL,
		Token:        tok.Token,
		WingID:       cfg.WingID,
		Hostname:     cfg.Hostname,
		Platform:     runtime.GOOS,
		Version:      version,
		PublicKey:    base64.StdEncoding.EncodeToString(privKey.PublicKey().Bytes()),
		Agents:       agents,
		Skills:       skills,
		Labels:       labels,
		Projects:     projects,
		OrgSlug:      orgFlag,
		RootDir:      rootDir,
		Locked:       wingCfg.Locked,
		AllowedCount: len(wingCfg.AllowKeys),
		DirectMCP:    directMCPEnabled(peerMgr != nil, wingCfg),
		HostedRelay:  wingCfg.EffectiveHostedRelay(),
	}
	client.OnRegistered = func(msg ws.RegisteredMsg) {
		if policy, ok := passkeyPolicyFromRegistration(msg); ok {
			passkeyPolicyLive.Store(policy)
			log.Printf("passkey relying-party policy synchronized (rp_id=%s origins=%d)", policy.RPID, len(policy.Origins))
		}
		if directSrv != nil && msg.RelayPubKey != "" {
			pubKey, err := relaypkg.ParseECPublicKey(msg.RelayPubKey)
			if err != nil {
				log.Printf("[direct] reject relay public key: %v", err)
			} else {
				directSrv.SetRelayPublicKey(pubKey)
				log.Printf("[direct] relay public key synchronized for JWT verification")
			}
		}
	}
	client.OnHostedRelayDenied = func(operation string) {
		if err := appendHostedRelayPolicyAudit(cfg, operation); err != nil {
			log.Printf("hosted relay policy audit: %v", err)
		}
	}

	client.OnStateChange = func(state string, stateErr error) {
		errMsg := ""
		if stateErr != nil {
			errMsg = stateErr.Error()
		}
		writeWingStatusForRoost(state, errMsg, roostURL)
		switch state {
		case "auth_failed":
			log.Printf("FATAL: relay rejected authentication — run: wt logout && wt login && wt start")
		case "disconnected":
			if stateErr != nil {
				log.Printf("relay disconnected: %v", stateErr)
			} else {
				log.Printf("relay disconnected")
			}
		case "connected":
			log.Printf("relay connected")
		}
	}

	client.OnPTY = func(ctx context.Context, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte) {
		wingCfgMu.Lock()
		sessionWingCfg := wingCfg.Clone()
		sessionAllowedKeys := append([]config.AllowKey(nil), allowedKeys...)
		wingCfgMu.Unlock()
		// Wing-level admin override: admins get full access regardless of org role
		if sessionWingCfg.IsAdmin(start.Email) && isMemberRole(start.OrgRole) {
			start.OrgRole = "admin"
		}
		// Per-user path ACLs: members only see their tagged folders
		userPaths := pathsForRequest(sessionWingCfg.Paths, start.Email, start.OrgRole, home)
		if isMemberRole(start.OrgRole) && len(userPaths) == 0 {
			writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "no accessible folders on this machine"})
			return
		}
		// Clamp CWD to exact configured paths (not subdirectories).
		// Allowing subdirectories lets users write their own egg.yaml and
		// boot into a self-defined sandbox — a sandbox escape.
		if len(userPaths) > 0 {
			if !isExactPath(start.CWD, userPaths) {
				start.CWD = userPaths[0]
			}
		}
		// Members require egg.yaml in CWD (sandbox jail)
		if isMemberRole(start.OrgRole) && len(sessionWingCfg.Paths) > 0 {
			if _, err := os.Stat(filepath.Join(start.CWD, "egg.yaml")); os.IsNotExist(err) {
				writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "no egg.yaml in " + start.CWD + " — ask the wing owner to add a sandbox config"})
				return
			}
		}
		wingEggMu.Lock()
		currentEggCfg := wingEggCfg
		wingEggMu.Unlock()
		eggCfg := egg.DiscoverEggConfig(start.CWD, currentEggCfg)
		if auditLive.Load() {
			eggCfg.Audit = true
		}
		var authTTL time.Duration // default 0 = boot-scoped, no expiry
		if sessionWingCfg.AuthTTL != "" {
			if d, err := time.ParseDuration(sessionWingCfg.AuthTTL); err == nil {
				authTTL = d
			}
		}
		var idleTimeout time.Duration
		if sessionWingCfg.IdleTimeout != "" {
			if d, err := time.ParseDuration(sessionWingCfg.IdleTimeout); err == nil {
				idleTimeout = d
			}
		}
		// Snapshot tools for this session
		wingToolsMu.Lock()
		sessionTools := append([]*config.ToolConfig{}, wingTools...)
		wingToolsMu.Unlock()
		// P2P: wrap write in SwappableWriter for potential DC migration
		var sw *webrtcpkg.SwappableWriter
		if peerMgr != nil {
			sw = webrtcpkg.NewSwappableWriter(webrtcpkg.WriteFn(write))
			swSessions.Store(start.SessionID, sw)
			defer swSessions.Delete(start.SessionID)
			handlePTYSession(ctx, cfg, sessionWingCfg, start, sw.Write, input, eggCfg, debugLive.Load(), vte, &sessionAllowedKeys, passkeyCache, currentPasskeyPolicy(), authTTL, idleTimeout, sw, &dcSessions, sessionTools, sharedHost)
		} else {
			handlePTYSession(ctx, cfg, sessionWingCfg, start, write, input, eggCfg, debugLive.Load(), vte, &sessionAllowedKeys, passkeyCache, currentPasskeyPolicy(), authTTL, idleTimeout, nil, nil, sessionTools, sharedHost)
		}
	}

	client.OnTunnel = func(ctx context.Context, req ws.TunnelRequest, write ws.PTYWriteFunc) {
		handleTunnelRequest(ctx, cfg, wingCfg, req, write, &allowedKeys, passkeyCache, passkeyChallenges, currentPasskeyPolicy(), privKey, home, &wingEggMu, &wingEggCfg, auditLive.Load(), debugLive.Load(), client, peerMgr, &dcSessions)
	}

	client.OnOrphanKill = func(ctx context.Context, sessionID string) {
		killOrphanEgg(cfg, sessionID)
	}

	// Reclaim surviving egg sessions on every (re)connect
	client.OnReconnect = func(rctx context.Context) {
		wingCfgMu.Lock()
		reconnectWingCfg := wingCfg.Clone()
		reconnectAllowedKeys := append([]config.AllowKey(nil), allowedKeys...)
		wingCfgMu.Unlock()
		if !reconnectWingCfg.HostedRelayAllowed() {
			log.Printf("hosted relay payload transport disabled; skipping relay session reclaim")
			return
		}
		var authTTL time.Duration // default 0 = boot-scoped, no expiry
		if reconnectWingCfg.AuthTTL != "" {
			if d, err := time.ParseDuration(reconnectWingCfg.AuthTTL); err == nil {
				authTTL = d
			}
		}
		wingToolsMu.Lock()
		reclaimTools := append([]*config.ToolConfig{}, wingTools...)
		wingToolsMu.Unlock()
		reclaimEggSessions(rctx, cfg, client, reconnectWingCfg, reconnectAllowedKeys, passkeyCache, currentPasskeyPolicy(), authTTL, reclaimTools)
	}

	// SIGHUP reload goroutine — caller owns SIGTERM/SIGINT via ctx cancellation
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case sig, ok := <-sighupCh:
				if !ok {
					return
				}
				if sig == syscall.SIGHUP {
					log.Println("SIGHUP: reloading wing config")
					newCfg, err := config.LoadWingConfig(cfg.Dir)
					if err != nil {
						log.Printf("reload failed: %v", err)
						continue
					}
					if err := validateDirectMCPGrantConfig(newCfg); err != nil {
						log.Printf("reload failed: %v", err)
						continue
					}
					wingCfgMu.Lock()
					wingCfg.Locked = newCfg.Locked
					wingCfg.Spectate = newCfg.Spectate
					wingCfg.AllowKeys = newCfg.AllowKeys
					wingCfg.Admins = newCfg.Admins
					wingCfg.DirectMCP = newCfg.DirectMCP
					allowedKeys = append([]config.AllowKey{}, newCfg.AllowKeys...)

					// Hot-reload audit + debug (atomic, read at session start)
					auditLive.Store(newCfg.Audit)
					debugLive.Store(newCfg.Debug)

					// Hot-reload conv, auth_ttl, idle_timeout
					wingCfg.Conv = newCfg.Conv
					wingCfg.AuthTTL = newCfg.AuthTTL
					wingCfg.IdleTimeout = newCfg.IdleTimeout

					// Hot-reload labels
					wingCfg.Labels = newCfg.Labels

					// Hot-reload paths
					wingCfg.Paths = newCfg.Paths
					resolvedPaths = resolvePathStrings(newCfg.Paths.Strings(), home)
					if len(resolvedPaths) > 0 {
						rootDir = resolvedPaths[0]
					} else {
						rootDir = home
					}

					// Hot-reload egg config (if path changed)
					oldEggConfig := wingCfg.EggConfig
					wingCfg.EggConfig = newCfg.EggConfig
					if newCfg.EggConfig != oldEggConfig {
						eggPath := newCfg.EggConfig
						if eggPath == "" {
							eggPath = filepath.Join(cfg.Dir, "egg.yaml")
						}
						if newEggCfg, eggErr := egg.ResolveEggConfig(eggPath); eggErr == nil {
							wingEggMu.Lock()
							wingEggCfg = newEggCfg
							wingEggMu.Unlock()
							log.Printf("egg config reloaded from %s", eggPath)
						}
					}
					client.UpdateRuntimeConfig(newCfg.Locked, len(newCfg.AllowKeys), directMCPEnabled(peerMgr != nil, newCfg), newCfg.Labels, rootDir)
					wingCfgMu.Unlock()

					// Hot-reload tools
					newToolsDir := config.ResolveToolsDir(cfg.Dir, newCfg.ToolsDir)
					if newTools, tErr := config.LoadToolsDir(newToolsDir); tErr == nil {
						wingToolsMu.Lock()
						wingTools = newTools
						wingToolsMu.Unlock()
						log.Printf("tools reloaded: %d tool(s) from %s", len(newTools), newToolsDir)
					} else {
						log.Printf("tools reload failed: %v", tErr)
					}

					if err := client.SendConfig(ctx); err != nil {
						log.Printf("send reloaded wing config: %v", err)
					}
					log.Printf("config reloaded: locked=%v allowed=%d audit=%v debug=%v", newCfg.Locked, len(newCfg.AllowKeys), newCfg.Audit, newCfg.Debug)
				}
			}
		}
	}()

	// Idle session reaper — kills sessions that have been idle too long.
	// Always runs; reads wingCfg.IdleTimeout dynamically so SIGHUP reload works.
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			wingCfgMu.Lock()
			idleTimeoutValue := wingCfg.IdleTimeout
			wingCfgMu.Unlock()
			var idleTimeout time.Duration
			if idleTimeoutValue != "" {
				if d, parseErr := time.ParseDuration(idleTimeoutValue); parseErr == nil {
					idleTimeout = d
				}
			}
			if idleTimeout <= 0 {
				continue
			}
			sessionStates.Range(func(key, value any) bool {
				sid := key.(string)
				state := value.(*sessionIdleState)
				state.mu.Lock()
				lastIO := state.lastOutput
				if state.lastInput.After(lastIO) {
					lastIO = state.lastInput
				}
				eggDir := state.eggDir
				connected := state.connected
				state.mu.Unlock()

				if lastIO.IsZero() {
					return true // no I/O yet, skip
				}
				idle := time.Since(lastIO)

				// If disconnected and no recent output, cross-check with the egg
				if !connected && idle > idleTimeout/2 {
					sockPath := filepath.Join(eggDir, "egg.sock")
					tokenPath := filepath.Join(eggDir, "egg.token")
					if ec, dialErr := egg.Dial(sockPath, tokenPath); dialErr == nil {
						pollCtx, pollCancel := context.WithTimeout(ctx, 2*time.Second)
						if st, stErr := ec.Status(pollCtx); stErr == nil {
							polledIdle := time.Duration(st.IdleSeconds) * time.Second
							if polledIdle < idle {
								idle = polledIdle
							}
						}
						pollCancel()
						closeWithLog("idle-check egg client", ec)
					}
				}

				if idle > idleTimeout {
					log.Printf("idle reaper: killing session %s (idle %s, limit %s)", sid, idle.Round(time.Second), idleTimeout)
					sockPath := filepath.Join(eggDir, "egg.sock")
					tokenPath := filepath.Join(eggDir, "egg.token")
					if ec, dialErr := egg.Dial(sockPath, tokenPath); dialErr == nil {
						if err := ec.Kill(ctx, sid); err != nil {
							log.Printf("idle reaper: kill session %s: %v", sid, err)
						}
						closeWithLog("idle-reaper egg client", ec)
					}
					sessionStates.Delete(sid)
				}
				return true
			})
		}
	}()
	wingCfgMu.Lock()
	initialIdleTimeout := wingCfg.IdleTimeout
	wingCfgMu.Unlock()
	if initialIdleTimeout != "" {
		log.Printf("idle reaper enabled: timeout=%s", initialIdleTimeout)
	}

	// Direct mode: start a local WebSocket server for direct browser connections
	if wingCfg.ConnectionMode == "direct" && wingCfg.DirectPort > 0 {
		directSrv = &directpkg.Server{
			OnPTY: client.OnPTY,
		}
		addr := fmt.Sprintf(":%d", wingCfg.DirectPort)
		if err := directSrv.StartAsync(addr); err != nil {
			return fmt.Errorf("start direct server: %w", err)
		}
		defer closeWithLog("direct server", directSrv)
	}

	err = client.Run(ctx)
	if err != nil {
		log.Printf("wing daemon exiting: %v", err)
	} else {
		log.Printf("wing daemon exiting cleanly")
	}
	return err
}

func currentDataChannel(sessions *sync.Map, sessionID string, candidate *pionwebrtc.DataChannel) bool {
	current, ok := sessions.Load(sessionID)
	return ok && current == candidate
}

func wingConnectionToken(configDir string, local bool, tokenOverride *auth.DeviceToken) (*auth.DeviceToken, error) {
	store := auth.NewTokenStore(configDir)
	if tokenOverride != nil {
		copy := *tokenOverride
		if !store.IsValid(&copy) {
			return nil, fmt.Errorf("embedded wing token is expired")
		}
		return &copy, nil
	}
	if local {
		localStore := auth.NewLocalTokenStore(configDir)
		token, err := localStore.Load()
		if err != nil {
			return nil, err
		}
		if localStore.IsValid(token) {
			return token, nil
		}
		if token != nil {
			return nil, fmt.Errorf("local device token is expired")
		}
		// Compatibility with releases that wrote the local credential into the
		// ordinary token path. The next `wt serve --local` start writes the new
		// dedicated file without replacing this fallback.
	}
	token, err := store.Load()
	if err != nil {
		return nil, err
	}
	if !store.IsValid(token) {
		return nil, fmt.Errorf("device token is missing or expired")
	}
	return token, nil
}

func loadWingConfigForStart(dir string) (*config.WingConfig, error) {
	wingCfg, err := config.LoadWingConfig(dir)
	if err != nil {
		return nil, fmt.Errorf("load wing.yaml: %w", err)
	}
	if err := validateDirectMCPGrantConfig(wingCfg); err != nil {
		return nil, fmt.Errorf("load wing.yaml: %w", err)
	}
	return wingCfg, nil
}

func directMCPEnabled(hasPeerManager bool, wingCfg *config.WingConfig) bool {
	return hasPeerManager && wingCfg != nil && (wingCfg.DirectMCP == nil || !wingCfg.DirectMCP.Disabled)
}

func shortLogValue(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func wingStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the wing daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			lifecycleLock, lockErr := acquireDaemonLifecycleLock()
			if lockErr != nil {
				return lockErr
			}
			defer closeWithLog("daemon lifecycle lock", lifecycleLock)
			pid, kind, err := readDaemon()
			if err != nil {
				return fmt.Errorf("no wing daemon running")
			}
			if err := stopDaemonAndWait(pid, kind, 5*time.Second); err != nil {
				return err
			}
			if err := removeFiles(wingPidPath(), wingArgsPath(), wingStatusPath()); err != nil {
				return fmt.Errorf("remove wing daemon metadata: %w", err)
			}
			fmt.Printf("wing daemon stopped (pid %d)\n", pid)
			return nil
		},
	}
}

func wingStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check wing daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := readPid()
			if err != nil {
				if errors.Is(err, errNoDaemonRunning) {
					fmt.Println("wing daemon is not running")
					return nil
				}
				return fmt.Errorf("inspect daemon state: %w", err)
			}
			fmt.Printf("wing daemon is running (pid %d)\n", pid)

			cfg, _ := config.Load()
			status, _ := readWingStatus()

			// Show account identity and relay verification
			var relayVerified bool
			if cfg != nil {
				relayURL := activeWingRelayHTTPURL(cfg, status)
				if tok, tokErr := auth.NewTokenStore(cfg.Dir).Load(); tokErr == nil && tok != nil {
					if info, infoErr := auth.FetchUserInfo(relayURL, tok.Token); infoErr == nil {
						fmt.Printf("  account: %s\n", formatUserIdentity(info))
						relayVerified = true
					} else if errors.Is(infoErr, auth.ErrAuthFailed) {
						fmt.Println("  account: token expired — run: wt logout && wt login")
					} else {
						fmt.Printf("  account: relay unreachable (%v)\n", infoErr)
					}
				}
			}

			// Show relay connection state
			if status != nil {
				switch status.State {
				case "connected":
					if relayVerified {
						fmt.Println("  relay: connected (verified)")
					} else {
						fmt.Println("  relay: connected")
					}
				case "auth_failed":
					fmt.Println("  relay: auth_failed — run: wt login")
				case "connecting":
					fmt.Println("  relay: connecting...")
				case "disconnected":
					if status.Error != "" {
						fmt.Printf("  relay: disconnected (%s)\n", status.Error)
					} else {
						fmt.Println("  relay: disconnected")
					}
				default:
					fmt.Printf("  relay: %s\n", status.State)
				}
			}

			if cfg != nil {
				fmt.Printf("  wing_id: %s\n", cfg.WingID)
			}
			fmt.Printf("  log: %s\n", wingLogPath())

			// Show egg sessions from filesystem
			if cfg != nil {
				sessions := listAliveEggSessions(cfg)
				if len(sessions) > 0 {
					fmt.Println("  egg sessions:")
					for _, s := range sessions {
						fmt.Printf("    %s  %s  %s\n", s.SessionID, s.Agent, s.CWD)
					}
				} else {
					fmt.Println("  egg sessions: none")
				}
			}
			return nil
		},
	}
}

// resolveEmail calls the relay API to look up a user by email. Returns (userID, displayName, error).
func resolveEmail(cfg *config.Config, email string) (string, string, error) {
	roostURL := cfg.RoostURL
	if roostURL == "" {
		roostURL = "https://ws.wingthing.ai"
	}
	ts := auth.NewTokenStore(cfg.Dir)
	tok, err := ts.Load()
	if err != nil || !ts.IsValid(tok) {
		return "", "", fmt.Errorf("not logged in — run: wt login")
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(roostURL, "/")+"/api/app/resolve-email?email="+url.QueryEscape(email), nil)
	if err != nil {
		return "", "", fmt.Errorf("build email lookup request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, err := cliHTTPClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("resolve email: %w", err)
	}
	defer closeWithLog("email lookup response", resp.Body)
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("no user found with email: %s", email)
	}
	var result struct {
		UserID      string `json:"user_id"`
		DisplayName string `json:"display_name"`
	}
	if err := decodeCLIAPIResponse(resp.Body, &result); err != nil {
		return "", "", fmt.Errorf("parse email lookup response: %w", err)
	}
	return result.UserID, result.DisplayName, nil
}

// fetchCurrentPasskey resolves the logged-in device-token user and fetches one
// of that user's registered WebAuthn public keys. This is used only by an
// explicit local CLI action; runtime relay envelopes are never enrollment
// authority for a locked wing.
func fetchCurrentPasskey(cfg *config.Config) (config.AllowKey, error) {
	ts := auth.NewTokenStore(cfg.Dir)
	tok, err := ts.Load()
	if err != nil || !ts.IsValid(tok) {
		return config.AllowKey{}, fmt.Errorf("not logged in — run: wt login")
	}
	relayURL := resolveRelayHTTPURL(cfg)
	info, err := auth.FetchUserInfo(relayURL, tok.Token)
	if err != nil {
		return config.AllowKey{}, fmt.Errorf("resolve current user: %w", err)
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(relayURL, "/")+"/api/app/passkey", nil)
	if err != nil {
		return config.AllowKey{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, err := cliHTTPClient.Do(req)
	if err != nil {
		return config.AllowKey{}, fmt.Errorf("fetch passkeys: %w", err)
	}
	defer closeWithLog("passkey response", resp.Body)
	if resp.StatusCode != http.StatusOK {
		return config.AllowKey{}, fmt.Errorf("fetch passkeys: HTTP %d", resp.StatusCode)
	}
	var credentials []struct {
		PublicKey string `json:"public_key"`
	}
	if err := decodeCLIAPIResponse(resp.Body, &credentials); err != nil {
		return config.AllowKey{}, fmt.Errorf("parse passkeys: %w", err)
	}
	for _, credential := range credentials {
		raw, err := base64.StdEncoding.DecodeString(credential.PublicKey)
		if err == nil && auth.IsValidP256Point(raw) {
			return config.AllowKey{Key: credential.PublicKey, UserID: info.UserID, Email: info.Email}, nil
		}
	}
	return config.AllowKey{}, fmt.Errorf("no registered passkey — add one in the account page before locking this wing")
}

func wingAllowCmd() *cobra.Command {
	var userIDFlag string
	var emailFlag string
	var allFlag bool
	cmd := &cobra.Command{
		Use:   "allow [base64-public-key]",
		Short: "Allow a user or list allowlist (no args)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}

			// No args and no flags: list allowlist
			if len(args) == 0 && userIDFlag == "" && emailFlag == "" && !allFlag {
				if len(wingCfg.AllowKeys) == 0 {
					fmt.Println("no allowed users")
					return nil
				}
				for _, ak := range wingCfg.AllowKeys {
					display := ak.Email
					if display == "" {
						display = ak.UserID
					}
					if display == "" {
						display = "(key-only)"
					}
					keyInfo := ""
					if ak.Key != "" {
						prefix := ak.Key
						if len(prefix) > 16 {
							prefix = prefix[:16] + "..."
						}
						keyInfo = "  key:" + prefix
					}
					fmt.Printf("  %s%s\n", display, keyInfo)
				}
				return nil
			}

			// --all: fetch org members from relay and add all
			if allFlag {
				orgSlug := wingCfg.Org
				if orgSlug == "" {
					return fmt.Errorf("no org configured — set org in wing.yaml or use --org on wt wing")
				}
				roostURL := cfg.RoostURL
				if roostURL == "" {
					roostURL = "https://ws.wingthing.ai"
				}
				ts := auth.NewTokenStore(cfg.Dir)
				tok, err := ts.Load()
				if err != nil || !ts.IsValid(tok) {
					return fmt.Errorf("not logged in — run: wt login")
				}
				base := strings.TrimRight(roostURL, "/")

				// Resolve org slug to ID via GET /api/orgs
				orgsReq, err := http.NewRequest(http.MethodGet, base+"/api/orgs", nil)
				if err != nil {
					return fmt.Errorf("build org lookup request: %w", err)
				}
				orgsReq.Header.Set("Authorization", "Bearer "+tok.Token)
				orgsResp, err := cliHTTPClient.Do(orgsReq)
				if err != nil {
					return fmt.Errorf("fetch orgs: %w", err)
				}
				defer closeWithLog("org lookup response", orgsResp.Body)
				if orgsResp.StatusCode != 200 {
					return fmt.Errorf("fetch orgs: HTTP %d", orgsResp.StatusCode)
				}
				var orgs []struct {
					ID   string `json:"id"`
					Slug string `json:"slug"`
				}
				if err := decodeCLIAPIResponse(orgsResp.Body, &orgs); err != nil {
					return fmt.Errorf("parse orgs: %w", err)
				}
				var orgID string
				for _, o := range orgs {
					if o.Slug == orgSlug || o.ID == orgSlug {
						orgID = o.ID
						break
					}
				}
				if orgID == "" {
					return fmt.Errorf("org %q not found — check wing.yaml org setting", orgSlug)
				}

				// Fetch members via GET /api/orgs/{id}/members
				req, err := http.NewRequest(http.MethodGet, base+"/api/orgs/"+url.PathEscape(orgID)+"/members", nil)
				if err != nil {
					return fmt.Errorf("build org member request: %w", err)
				}
				req.Header.Set("Authorization", "Bearer "+tok.Token)
				resp, err := cliHTTPClient.Do(req)
				if err != nil {
					return fmt.Errorf("fetch org members: %w", err)
				}
				defer closeWithLog("org member response", resp.Body)
				if resp.StatusCode != 200 {
					return fmt.Errorf("fetch org members: HTTP %d", resp.StatusCode)
				}
				var membersResp struct {
					Members []struct {
						UserID        string `json:"user_id"`
						Email         string `json:"email"`
						DisplayName   string `json:"display_name"`
						PasskeyPubKey string `json:"passkey_public_key"`
					} `json:"members"`
				}
				if err := decodeCLIAPIResponse(resp.Body, &membersResp); err != nil {
					return fmt.Errorf("parse org members: %w", err)
				}
				members := membersResp.Members
				added := 0
				updated := 0
				skipped := 0
				for _, m := range members {
					// Skip members without a registered passkey
					if m.PasskeyPubKey == "" {
						fmt.Printf("skipped %s (no passkey)\n", m.Email)
						skipped++
						continue
					}
					// Deduplicate by user_id
					dupIdx := -1
					for i, ak := range wingCfg.AllowKeys {
						if ak.UserID == m.UserID {
							dupIdx = i
							break
						}
					}
					if dupIdx >= 0 {
						// Update passkey public key if we have one now and didn't before
						if wingCfg.AllowKeys[dupIdx].Key != m.PasskeyPubKey {
							wingCfg.AllowKeys[dupIdx].Key = m.PasskeyPubKey
							fmt.Printf("updated key: %s\n", m.Email)
							updated++
						} else {
							fmt.Printf("already allowed: %s\n", m.Email)
						}
						continue
					}
					wingCfg.AllowKeys = append(wingCfg.AllowKeys, config.AllowKey{Key: m.PasskeyPubKey, UserID: m.UserID, Email: m.Email})
					fmt.Printf("allowed %s\n", m.Email)
					added++
				}
				if added > 0 || updated > 0 {
					if !wingCfg.Locked {
						wingCfg.Locked = true
					}
					if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
						return err
					}
					if err := signalDaemon(syscall.SIGHUP); err != nil {
						return err
					}
				}
				if skipped > 0 {
					fmt.Printf("skipped %d members without passkeys\n", skipped)
				}
				fmt.Printf("added %d members, updated %d keys\n", added, updated)
				return nil
			}

			var keyB64 string
			if len(args) > 0 {
				keyB64 = args[0]
				raw, err := base64.StdEncoding.DecodeString(keyB64)
				if err != nil {
					return fmt.Errorf("invalid base64: %w", err)
				}
				if len(raw) != 64 {
					return fmt.Errorf("invalid key: expected 64 bytes (P-256 X||Y), got %d", len(raw))
				}
				if !auth.IsValidP256Point(raw) {
					return fmt.Errorf("invalid key: not a valid P-256 curve point")
				}
			}

			// Resolve email to user ID
			var resolvedEmail string
			if emailFlag != "" {
				uid, _, resolveErr := resolveEmail(cfg, emailFlag)
				if resolveErr != nil {
					return resolveErr
				}
				userIDFlag = uid
				resolvedEmail = emailFlag
			}

			if keyB64 == "" && userIDFlag == "" {
				return fmt.Errorf("must provide --email, --user-id, or a public key")
			}

			// Deduplicate by key or user_id
			for _, ak := range wingCfg.AllowKeys {
				if keyB64 != "" && ak.Key == keyB64 {
					display := ak.Email
					if display == "" {
						display = ak.UserID
					}
					fmt.Printf("already allowed: %s\n", display)
					return nil
				}
				if userIDFlag != "" && ak.UserID == userIDFlag {
					display := ak.Email
					if display == "" {
						display = ak.UserID
					}
					fmt.Printf("already allowed: %s\n", display)
					return nil
				}
			}

			wingCfg.AllowKeys = append(wingCfg.AllowKeys, config.AllowKey{Key: keyB64, UserID: userIDFlag, Email: resolvedEmail})
			if !wingCfg.Locked {
				wingCfg.Locked = true
			}
			if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
				return err
			}
			display := resolvedEmail
			if display == "" {
				display = userIDFlag
			}
			if display == "" {
				display = keyB64[:12] + "..."
			}
			fmt.Printf("allowed %s\n", display)
			return signalDaemon(syscall.SIGHUP)
		},
	}
	cmd.Flags().StringVar(&userIDFlag, "user-id", "", "relay user ID to allow")
	cmd.Flags().StringVar(&emailFlag, "email", "", "user email to allow (resolves via relay)")
	cmd.Flags().BoolVar(&allFlag, "all", false, "allow all org members from relay")
	return cmd
}

func wingRevokeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "revoke [user-id-or-email]",
		Short: "Remove from allowlist",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			revokeAll, _ := cmd.Flags().GetBool("all")

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}

			if revokeAll {
				count := len(wingCfg.AllowKeys)
				if count == 0 {
					fmt.Println("allowlist is already empty")
					return nil
				}
				wingCfg.AllowKeys = nil
				if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
					return err
				}
				fmt.Printf("revoked all %d entries\n", count)
				return signalDaemon(syscall.SIGHUP)
			}

			if len(args) == 0 {
				return fmt.Errorf("specify a user-id or email, or use --all")
			}
			query := args[0]

			// Find matches by user_id, email, or key prefix
			var matches []int
			for i, ak := range wingCfg.AllowKeys {
				if ak.UserID == query || ak.Email == query || strings.HasPrefix(ak.Key, query) {
					matches = append(matches, i)
				}
			}

			if len(matches) == 0 {
				return fmt.Errorf("no matching entry found for %q", query)
			}
			if len(matches) > 1 {
				fmt.Println("ambiguous match:")
				for _, i := range matches {
					ak := wingCfg.AllowKeys[i]
					display := ak.Email
					if display == "" {
						display = ak.UserID
					}
					if display == "" {
						display = "(key-only)"
					}
					fmt.Printf("  %s\n", display)
				}
				return fmt.Errorf("specify a more precise user_id or key prefix")
			}

			removed := wingCfg.AllowKeys[matches[0]]
			wingCfg.AllowKeys = append(wingCfg.AllowKeys[:matches[0]], wingCfg.AllowKeys[matches[0]+1:]...)
			if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
				return err
			}
			display := removed.Email
			if display == "" {
				display = removed.UserID
			}
			if display == "" {
				display = removed.Key[:12] + "..."
			}
			fmt.Printf("revoked: %s\n", display)
			return signalDaemon(syscall.SIGHUP)
		},
	}
	cmd.Flags().Bool("all", false, "Revoke all entries from the allowlist")
	return cmd
}

func signalDaemon(sig os.Signal) error {
	pid, err := readPid()
	if err != nil {
		if daemonAbsentError(err) {
			return nil
		}
		return fmt.Errorf("find daemon to signal: %w", err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find daemon process %d: %w", pid, err)
	}
	if err := proc.Signal(sig); err != nil {
		return fmt.Errorf("signal daemon process %d: %w", pid, err)
	}
	return nil
}

func wingLockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lock",
		Short: "Enable access control",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}
			if wingCfg.Locked {
				fmt.Println("wing is already locked")
				return nil
			}
			hasPinnedKey := false
			for _, allowed := range wingCfg.AllowKeys {
				raw, err := base64.StdEncoding.DecodeString(allowed.Key)
				if err == nil && auth.IsValidP256Point(raw) {
					hasPinnedKey = true
					break
				}
			}
			if !hasPinnedKey {
				allowed, err := fetchCurrentPasskey(cfg)
				if err != nil {
					return fmt.Errorf("cannot lock wing without a locally pinned passkey: %w", err)
				}
				updated := false
				for i := range wingCfg.AllowKeys {
					if wingCfg.AllowKeys[i].UserID == allowed.UserID {
						wingCfg.AllowKeys[i] = allowed
						updated = true
						break
					}
				}
				if !updated {
					wingCfg.AllowKeys = append(wingCfg.AllowKeys, allowed)
				}
				fmt.Printf("pinned passkey for %s\n", allowed.Email)
			}
			wingCfg.Locked = true
			if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
				return err
			}
			if err := signalDaemon(syscall.SIGHUP); err != nil {
				return err
			}
			fmt.Println("wing locked")
			return nil
		},
	}
}

func wingUnlockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlock",
		Short: "Disable access control",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}
			if !wingCfg.Locked {
				fmt.Println("wing is already unlocked")
				return nil
			}
			wingCfg.Locked = false
			if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
				return err
			}
			if err := signalDaemon(syscall.SIGHUP); err != nil {
				return err
			}
			fmt.Println("wing unlocked")
			return nil
		},
	}
}

func wingConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "View or set wing configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}

			daemonStatus := "(daemon stopped)"
			if _, err := readPid(); err == nil {
				daemonStatus = "(daemon running)"
			}

			fmt.Printf("wing_id:    %s\n", wingCfg.WingID)
			roost := wingCfg.Roost
			if roost == "" {
				roost = "wss://ws.wingthing.ai"
			}
			fmt.Printf("roost:      %s\n", roost)
			fmt.Printf("org:        %s\n", wingCfg.Org)
			fmt.Printf("paths:      %s\n", strings.Join(wingCfg.Paths.Strings(), ", "))
			fmt.Printf("labels:     %s\n", strings.Join(wingCfg.Labels, ", "))
			fmt.Printf("egg_config: %s\n", wingCfg.EggConfig)
			fmt.Printf("conv:       %s\n", wingCfg.Conv)
			fmt.Printf("audit:      %v\n", wingCfg.Audit)
			fmt.Printf("debug:      %v\n", wingCfg.Debug)
			fmt.Printf("locked:     %v\n", wingCfg.Locked)
			fmt.Printf("spectate:   %v\n", wingCfg.Spectate)
			fmt.Printf("hosted_relay: %s\n", wingCfg.EffectiveHostedRelay())
			authTTL := wingCfg.AuthTTL
			if authTTL == "" {
				authTTL = "0"
			}
			fmt.Printf("auth_ttl:   %s\n", authTTL)
			fmt.Printf("allow_keys: %d configured\n", len(wingCfg.AllowKeys))
			fmt.Println()
			fmt.Println(daemonStatus)
			return nil
		},
	}
	cmd.AddCommand(wingConfigSetCmd())
	return cmd
}

func wingConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set key=value [key=value ...]",
		Short: "Set wing configuration values",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}

			restartFields := map[string]bool{"org": true, "hosted_relay": true}
			immutableFields := map[string]bool{"wing_id": true, "roost": true, "allow_keys": true}

			var changedRestart []string

			for _, arg := range args {
				key, value, ok := strings.Cut(arg, "=")
				if !ok {
					return fmt.Errorf("invalid argument %q — use key=value format", arg)
				}
				key = strings.TrimSpace(key)
				value = strings.TrimSpace(value)

				if immutableFields[key] {
					return fmt.Errorf("%s cannot be changed via config set", key)
				}

				switch key {
				case "audit":
					b, err := strconv.ParseBool(value)
					if err != nil {
						return fmt.Errorf("audit: expected true or false")
					}
					wingCfg.Audit = b
				case "debug":
					b, err := strconv.ParseBool(value)
					if err != nil {
						return fmt.Errorf("debug: expected true or false")
					}
					wingCfg.Debug = b
				case "locked":
					b, err := strconv.ParseBool(value)
					if err != nil {
						return fmt.Errorf("locked: expected true or false")
					}
					wingCfg.Locked = b
				case "spectate":
					b, err := strconv.ParseBool(value)
					if err != nil {
						return fmt.Errorf("spectate: expected true or false")
					}
					wingCfg.Spectate = b
				case "hosted_relay":
					if value != config.HostedRelayAllow && value != config.HostedRelayDeny {
						return fmt.Errorf("hosted_relay: expected %q or %q", config.HostedRelayAllow, config.HostedRelayDeny)
					}
					wingCfg.HostedRelay = value
				case "labels":
					var labels []string
					for _, l := range strings.Split(value, ",") {
						l = strings.TrimSpace(l)
						if l != "" {
							labels = append(labels, l)
						}
					}
					wingCfg.Labels = labels
				case "conv":
					wingCfg.Conv = value
				case "egg_config":
					if value != "" {
						if _, err := os.Stat(value); err != nil {
							return fmt.Errorf("egg_config: %s does not exist", value)
						}
					}
					wingCfg.EggConfig = value
				case "auth_ttl":
					if _, err := time.ParseDuration(value); err != nil {
						return fmt.Errorf("auth_ttl: invalid duration %q", value)
					}
					wingCfg.AuthTTL = value
				case "paths":
					var paths config.PathList
					for _, p := range strings.Split(value, ",") {
						p = strings.TrimSpace(p)
						if p == "" {
							continue
						}
						info, err := os.Stat(p)
						if err != nil {
							return fmt.Errorf("paths: %s does not exist", p)
						}
						if !info.IsDir() {
							return fmt.Errorf("paths: %s is not a directory", p)
						}
						paths = append(paths, config.PathEntry{Path: p})
					}
					wingCfg.Paths = paths
					wingCfg.Root = "" // clear legacy
				case "root":
					// compat alias: sets paths to single entry
					if value != "" {
						info, err := os.Stat(value)
						if err != nil {
							return fmt.Errorf("root: %s does not exist", value)
						}
						if !info.IsDir() {
							return fmt.Errorf("root: %s is not a directory", value)
						}
						wingCfg.Paths = config.PathList{{Path: value}}
					} else {
						wingCfg.Paths = nil
					}
					wingCfg.Root = "" // clear legacy
				case "org":
					wingCfg.Org = value
				default:
					return fmt.Errorf("unknown config key: %s", key)
				}

				if restartFields[key] {
					changedRestart = append(changedRestart, key)
				}
			}

			if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
				return err
			}

			if err := signalDaemon(syscall.SIGHUP); err != nil {
				return err
			}

			for _, key := range changedRestart {
				fmt.Printf("%s: will take effect next restart\n", key)
			}
			return nil
		},
	}
}

// getDirEntries returns directory entries for the given path, suitable for cwd selection.
// When resolvedPaths is set, acts as a strict whitelist: only the configured paths are
// returned, no filesystem browsing. This prevents users from navigating into subdirectories
// and writing their own egg.yaml (sandbox escape).
func getDirEntries(path string, resolvedPaths []string) []ws.DirEntry {
	// Strict whitelist mode: only return configured paths, no browsing.
	if len(resolvedPaths) > 0 {
		var results []ws.DirEntry
		for _, rp := range resolvedPaths {
			results = append(results, ws.DirEntry{
				Name:  filepath.Base(rp),
				IsDir: true,
				Path:  rp,
			})
		}
		return results
	}

	if path == "" {
		home, _ := os.UserHomeDir()
		path = home
	}
	if strings.HasPrefix(path, "~") {
		home, _ := os.UserHomeDir()
		path = home + path[1:]
	}

	// Try path as a directory first; if it doesn't exist, treat the last
	// component as a prefix filter on the parent (tab-completion behavior).
	prefix := ""
	entries, err := os.ReadDir(path)
	if err != nil {
		prefix = strings.ToLower(filepath.Base(path))
		path = filepath.Dir(path)
		entries, err = os.ReadDir(path)
		if err != nil {
			return nil
		}
	}

	var results []ws.DirEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue // dirs only -- this is for cwd selection
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue // skip hidden dirs
		}
		if prefix != "" && !strings.HasPrefix(strings.ToLower(e.Name()), prefix) {
			continue
		}
		full := filepath.Join(path, e.Name())
		results = append(results, ws.DirEntry{
			Name:  e.Name(),
			IsDir: true,
			Path:  full,
		})
	}
	return results
}

// reapDeadEggs removes egg directories for dead processes on startup.
func reapDeadEggs(cfg *config.Config) {
	eggsDir := filepath.Join(cfg.Dir, "eggs")
	entries, err := os.ReadDir(eggsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(eggsDir, e.Name())
		pidPath := filepath.Join(dir, "egg.pid")
		data, err := os.ReadFile(pidPath)
		if err != nil {
			// No pid file — stale dir, clean up
			cleanEggDir(dir)
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			cleanEggDir(dir)
			continue
		}
		if !ownedProcessIsAlive(pid) {
			// Dead process
			log.Printf("egg: reaping dead egg %s (pid %d)", e.Name(), pid)
			cleanEggDir(dir)
		}
	}
}

// cleanEggDir removes the files in an egg session directory, then the directory itself.
// If audit files or chat history exist, preserves egg.meta, egg.owner, and data (only removes runtime files).
func cleanEggDir(dir string) {
	removeWithLog(filepath.Join(dir, "egg.sock"))
	removeWithLog(filepath.Join(dir, "egg.token"))
	removeWithLog(filepath.Join(dir, "egg.pid"))
	// Preserve egg.log — the parent process reads it via readEggCrashInfo
	// after this child exits. Deleting it here causes a race where the
	// crash message is lost ("egg process crashed (no log available)").
	// The log is small and the parent's cleanEggDir call cleans it up later.
	// Keep egg.meta, egg.owner, and dir if audit recordings or chat history exist
	for _, name := range []string{"audit.pty.gz", "audit.log", "chat.jsonl.gz"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			// An unreadable recording must never be mistaken for an absent one.
			log.Printf("egg: preserve %s after stat failure: %v", dir, err)
			return
		}
	}
	// The egg copies its diagnostic log to the persistent log directory before
	// normal shutdown. Remove the whole transient directory so crash logs and
	// partially-created files do not leave an unreapable directory forever.
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("egg: remove transient session directory %s: %v", dir, err)
	}
}

// listAliveEggSessions scans ~/.wingthing/eggs/ for alive egg processes.
func listAliveEggSessions(cfg *config.Config) []ws.SessionInfo {
	eggsDir := filepath.Join(cfg.Dir, "eggs")
	entries, err := os.ReadDir(eggsDir)
	if err != nil {
		return nil
	}

	var out []ws.SessionInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionID := e.Name()
		dir := filepath.Join(eggsDir, sessionID)
		pidPath := filepath.Join(dir, "egg.pid")
		data, err := os.ReadFile(pidPath)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			continue
		}
		if !ownedProcessIsAlive(pid) {
			continue
		}

		// Alive — try to dial to confirm it's responsive
		sockPath := filepath.Join(dir, "egg.sock")
		tokenPath := filepath.Join(dir, "egg.token")
		ec, dialErr := egg.Dial(sockPath, tokenPath)
		if dialErr != nil {
			continue
		}
		closeWithLog("egg health-check client", ec)

		agent, sessionCWD := readEggMeta(dir)
		info := ws.SessionInfo{
			SessionID: sessionID,
			Agent:     agent,
			CWD:       sessionCWD,
			UserID:    readEggOwner(dir),
			Email:     readEggOwnerEmail(dir),
		}
		if _, ok := wingAttention.Load(sessionID); ok {
			info.NeedsAttention = true
		}
		// Check if audit recording exists
		if _, err := os.Stat(filepath.Join(dir, "audit.pty.gz")); err == nil {
			info.Audit = true
		}
		if _, err := os.Stat(filepath.Join(dir, "chat.jsonl.gz")); err == nil {
			info.Chat = true
		}
		out = append(out, info)
	}
	return out
}

// eggPidMatchesSession reports whether pid's command line is the egg runner
// for sessionID. PIDs are recycled; a stale egg.pid can point at an unrelated
// process (another user's egg, or any roost process), so a PID whose argv
// cannot be confirmed is never signaled.
func eggPidMatchesSession(pid int, sessionID string) bool {
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		argv := strings.Split(string(data), "\x00")
		for i, a := range argv {
			if a == "--session-id" && i+1 < len(argv) && argv[i+1] == sessionID {
				return true
			}
		}
		return false
	}
	// No /proc (darwin): fall back to ps.
	out, psErr := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if psErr != nil {
		return false
	}
	return strings.Contains(string(out), "--session-id "+sessionID)
}

// killOrphanEgg kills an egg session that has no active goroutine managing it.
// This handles the case where a pty.kill arrives but the session was never reclaimed.
func killOrphanEgg(cfg *config.Config, sessionID string) {
	if err := validateSessionID(sessionID); err != nil {
		log.Printf("refuse to kill invalid egg session: %v", err)
		return
	}
	dir := filepath.Join(cfg.Dir, "eggs", sessionID)
	sockPath := filepath.Join(dir, "egg.sock")
	tokenPath := filepath.Join(dir, "egg.token")

	ec, err := egg.Dial(sockPath, tokenPath)
	if err != nil {
		// Can't reach egg — try to kill by PID, but only after confirming the
		// PID still belongs to this session's egg runner.
		pidPath := filepath.Join(dir, "egg.pid")
		terminationRequested := false
		data, readErr := os.ReadFile(pidPath)
		if readErr == nil {
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && eggPidMatchesSession(pid, sessionID) {
				if proc, findErr := os.FindProcess(pid); findErr != nil {
					log.Printf("pty session %s: find orphan egg process %d: %v", sessionID, pid, findErr)
				} else if signalErr := proc.Signal(syscall.SIGTERM); signalErr != nil {
					log.Printf("pty session %s: terminate orphan egg process %d: %v", sessionID, pid, signalErr)
				} else {
					terminationRequested = true
				}
			}
		}
		if terminationRequested {
			// Leave runtime files in place until the egg exits and performs its
			// own cleanup. Reaping them now can break an in-flight shutdown.
			log.Printf("pty session %s: orphan termination requested (pid)", sessionID)
		} else {
			cleanEggDir(dir)
			log.Printf("pty session %s: stale orphan metadata cleaned", sessionID)
		}
		return
	}
	if killErr := ec.Kill(context.Background(), sessionID); killErr != nil {
		log.Printf("pty session %s: terminate orphan over gRPC: %v", sessionID, killErr)
	} else {
		log.Printf("pty session %s: orphan termination requested (gRPC)", sessionID)
	}
	closeWithLog("orphan egg client", ec)
}

func resizeEgg(cfg *config.Config, sessionID string, rows, cols uint32) (result error) {
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if rows == 0 || cols == 0 || rows > 1000 || cols > 1000 {
		return fmt.Errorf("invalid terminal dimensions")
	}
	dir := filepath.Join(cfg.Dir, "eggs", sessionID)
	ec, err := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
	if err != nil {
		return fmt.Errorf("open session: %w", err)
	}
	defer func() { result = closeAndJoin("egg resize client", ec, result) }()
	resizeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ec.Resize(resizeCtx, sessionID, rows, cols); err != nil {
		return fmt.Errorf("resize session: %w", err)
	}
	return nil
}

// readEggCrashInfo reads the last lines of an egg's log looking for panic/crash info.
func readEggCrashInfo(dir string) string {
	logPath := filepath.Join(dir, "egg.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		return "egg process crashed (no log available)"
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		return "egg process crashed (empty log)"
	}

	// Find the last panic
	lastPanic := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "panic") || strings.Contains(lines[i], "PANIC") || strings.Contains(lines[i], "fatal error") {
			lastPanic = i
			break
		}
	}

	if lastPanic != -1 {
		// Extract up to 20 lines from the panic point
		end := lastPanic + 20
		if end > len(lines) {
			end = len(lines)
		}
		excerpt := strings.Join(lines[lastPanic:end], "\n")
		return fmt.Sprintf("egg crashed: %s", strings.TrimSpace(excerpt))
	}

	// No panic found — return the last line (cobra prints "Error: ..." there)
	// plus any "Error:" lines from the log.
	last := lines[len(lines)-1]
	if strings.Contains(last, "Error:") || strings.Contains(last, "error") {
		return strings.TrimSpace(last)
	}

	// Fall back to last 5 lines for context
	start := len(lines) - 5
	if start < 0 {
		start = 0
	}
	return strings.TrimSpace(strings.Join(lines[start:], "\n"))
}

type pendingReattachAuth struct {
	attach    ws.PTYAttach
	challenge []byte
	subject   string
	expiresAt time.Time
}

type pendingReattachAuths struct {
	byViewer map[string]pendingReattachAuth
	timer    *time.Timer
	timerC   <-chan time.Time
}

func newPendingReattachAuths() *pendingReattachAuths {
	return &pendingReattachAuths{byViewer: make(map[string]pendingReattachAuth)}
}

func (p *pendingReattachAuths) put(attach ws.PTYAttach, challenge []byte, subject string, timeout time.Duration) {
	p.byViewer[attach.ViewerID] = pendingReattachAuth{
		attach:    attach,
		challenge: append([]byte(nil), challenge...),
		subject:   subject,
		expiresAt: time.Now().Add(timeout),
	}
	p.resetTimer()
}

func (p *pendingReattachAuths) take(viewerID string) (pendingReattachAuth, bool) {
	pending, ok := p.byViewer[viewerID]
	if ok {
		delete(p.byViewer, viewerID)
		p.resetTimer()
	}
	return pending, ok
}

func (p *pendingReattachAuths) expire(now time.Time) []pendingReattachAuth {
	var expired []pendingReattachAuth
	for viewerID, pending := range p.byViewer {
		if !pending.expiresAt.After(now) {
			expired = append(expired, pending)
			delete(p.byViewer, viewerID)
		}
	}
	p.resetTimer()
	return expired
}

func (p *pendingReattachAuths) timeout() <-chan time.Time {
	return p.timerC
}

func (p *pendingReattachAuths) close() {
	if p.timer != nil {
		p.timer.Stop()
	}
}

func (p *pendingReattachAuths) resetTimer() {
	if p.timer != nil && !p.timer.Stop() {
		select {
		case <-p.timer.C:
		default:
		}
	}
	if len(p.byViewer) == 0 {
		p.timerC = nil
		return
	}
	var next time.Time
	for _, pending := range p.byViewer {
		if next.IsZero() || pending.expiresAt.Before(next) {
			next = pending.expiresAt
		}
	}
	wait := time.Until(next)
	if wait < 0 {
		wait = 0
	}
	if p.timer == nil {
		p.timer = time.NewTimer(wait)
	} else {
		p.timer.Reset(wait)
	}
	p.timerC = p.timer.C
}

// reclaimEggSessions discovers surviving egg sessions and re-registers their
// input routing goroutines. The relay no longer tracks sessions — browser
// discovers them via E2E tunnel and reattaches directly via wing_id.
func reclaimEggSessions(ctx context.Context, cfg *config.Config, wsClient *ws.Client, wingCfg *config.WingConfig, allowedKeys []config.AllowKey, passkeyCache *auth.AuthCache, passkeyPolicy auth.PasskeyPolicy, authTTL time.Duration, tools []*config.ToolConfig) {
	// Small delay to let registration complete
	time.Sleep(500 * time.Millisecond)

	eggsDir := filepath.Join(cfg.Dir, "eggs")
	entries, err := os.ReadDir(eggsDir)
	if err != nil {
		return
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionID := e.Name()
		dir := filepath.Join(eggsDir, sessionID)
		pidPath := filepath.Join(dir, "egg.pid")
		data, err := os.ReadFile(pidPath)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			continue
		}
		if !ownedProcessIsAlive(pid) {
			cleanEggDir(dir)
			continue
		}

		// If a goroutine is already handling this session (survived the
		// reconnect), skip — don't create a duplicate subscriber or
		// goroutine, which would cause decrypt errors.
		if wsClient.HasPTYSession(sessionID) {
			log.Printf("egg: session %s already tracked, skipping", sessionID)
			continue
		}

		agent, _ := readEggMeta(dir)

		// Alive — dial and set up input routing
		sockPath := filepath.Join(dir, "egg.sock")
		tokenPath := filepath.Join(dir, "egg.token")
		ec, dialErr := egg.Dial(sockPath, tokenPath)
		if dialErr != nil {
			log.Printf("egg: reclaim %s: dial failed: %v", sessionID, dialErr)
			continue
		}

		log.Printf("egg: reclaiming session %s (pid %d agent=%s)", sessionID, pid, agent)

		// Set up input routing for this session
		write, input, cleanup, registered := wsClient.RegisterPTYSession(ctx, sessionID)
		if !registered {
			closeWithLog("duplicate reclaimed egg client", ec)
			log.Printf("egg: session %s became active during reclaim, skipping", sessionID)
			continue
		}
		go func(sid string, ec *egg.Client, dir string) {
			defer cleanup()
			defer closeWithLog("reclaimed egg client", ec)
			handleReclaimedPTY(ctx, cfg, ec, sid, dir, write, input, wingCfg, allowedKeys, passkeyCache, passkeyPolicy, authTTL, tools)
		}(sessionID, ec, dir)
	}
}

// handleReclaimedPTY sets up I/O routing for a reclaimed (surviving) egg session.
func handleReclaimedPTY(ctx context.Context, cfg *config.Config, ec *egg.Client, sessionID, eggDir string, write ws.PTYWriteFunc, input <-chan []byte, wingCfg *config.WingConfig, allowedKeys []config.AllowKey, passkeyCache *auth.AuthCache, passkeyPolicy auth.PasskeyPolicy, authTTL time.Duration, tools []*config.ToolConfig) {
	reclaimAgent, reclaimCWD := readEggMeta(eggDir)
	var mu sync.Mutex
	var gcm cipher.AEAD
	var activeStream pb.Egg_SessionClient
	var cancelStream context.CancelFunc
	privKey, privKeyErr := auth.LoadPrivateKey(cfg.Dir)
	if privKeyErr != nil {
		log.Printf("pty session %s: FATAL: load private key: %v (reclaim aborted)", sessionID, privKeyErr)
		writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: sessionID, ExitCode: 1, Error: "E2E encryption required but wing private key missing"})
		return
	}
	wingPubKeyB64 := base64.StdEncoding.EncodeToString(privKey.PublicKey().Bytes())

	// Register idle state tracking (reclaimed — starts disconnected)
	reclaimIdleState := &sessionIdleState{
		lastOutput: time.Now(),
		connected:  false,
		eggDir:     eggDir,
	}
	sessionStates.Store(sessionID, reclaimIdleState)
	defer sessionStates.Delete(sessionID)
	defer forgetAttentionState(sessionID)

	// Recreate the tool socket listener. It was owned by the previous daemon
	// process and died with it, but the surviving egg still points at this path
	// via --tool-socket. Re-listen on the same socket so privileged tools keep
	// working after a daemon restart. Only sessions that were started with tools
	// have a .tools dir; skip the rest.
	if len(tools) > 0 {
		toolsDir := filepath.Join(eggDir, ".tools")
		if _, statErr := os.Stat(toolsDir); statErr == nil {
			toolSocketPath := filepath.Join(toolsDir, "tool.sock")
			if tl, tlErr := egg.NewToolListener(toolSocketPath, tools); tlErr != nil {
				log.Printf("pty session %s: reclaim tool listener failed: %v", sessionID, tlErr)
			} else {
				log.Printf("pty session %s: reclaim tool listener restarted (%d tools)", sessionID, len(tools))
				defer closeWithLog("reclaimed egg tool listener", tl)
			}
		}
	}

	// Attach to existing egg session
	streamCtx, sCancel := context.WithCancel(ctx)
	stream, err := ec.AttachSession(streamCtx, sessionID)
	if err != nil {
		sCancel()
		log.Printf("pty session %s: reclaim attach failed: %v", sessionID, err)
		return
	}
	activeStream = stream
	cancelStream = sCancel

	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()

	// Read output from egg -> encrypt -> send to relay
	go func() {
		var lastHadBell bool
		for {
			msg, err := stream.Recv()
			if err != nil {
				if err != io.EOF {
					log.Printf("pty session %s: egg stream error: %v", sessionID, err)
				}
				return
			}
			switch p := msg.Payload.(type) {
			case *pb.SessionMsg_Output:
				reclaimIdleState.mu.Lock()
				reclaimIdleState.lastOutput = time.Now()
				reclaimIdleState.mu.Unlock()
				if hasBell(p.Output) {
					if lastHadBell {
						checkAndSendAttention(sessionID, reclaimAgent, reclaimCWD, write)
					}
					lastHadBell = true
				} else {
					lastHadBell = false
				}
				mu.Lock()
				currentGCM := gcm
				mu.Unlock()
				if currentGCM == nil {
					continue // no key yet or reattach in progress
				}
				sendPTYOutput(sessionID, p.Output, currentGCM, write)
			case *pb.SessionMsg_ExitCode:
				log.Printf("pty session %s: exited with code %d", sessionID, p.ExitCode)
				writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: sessionID, ExitCode: int(p.ExitCode)})
				clearAttentionCooldown(sessionID)
				sessionCancel()
				return
			}
		}
	}()

	// Process input from browser
	go func() {
		pendingAuth := newPendingReattachAuths()
		defer pendingAuth.close()
		defer func() {
			reclaimIdleState.mu.Lock()
			reclaimIdleState.connected = false
			reclaimIdleState.mu.Unlock()
		}()
	reclaimInputLoop:
		for {
			var data []byte
			select {
			case <-ctx.Done():
				return
			case <-pendingAuth.timeout():
				for _, pending := range pendingAuth.expire(time.Now()) {
					log.Printf("pty session %s: reattach passkey timed out", sessionID)
					writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "passkey timed out", SessionID: sessionID, ViewerID: pending.attach.ViewerID})
				}
				continue
			case inputData, ok := <-input:
				if !ok {
					return
				}
				data = inputData
			}
			var env ws.Envelope
			if err := json.Unmarshal(data, &env); err != nil {
				continue
			}
			var attach ws.PTYAttach
			if env.Type == ws.TypePasskeyResponse {
				var response ws.PasskeyResponse
				if err := json.Unmarshal(data, &response); err != nil {
					continue
				}
				pending, ok := pendingAuth.take(response.ViewerID)
				if !ok {
					log.Printf("pty session %s: ignoring passkey response without matching reattach", sessionID)
					continue
				}
				authData, _ := base64.StdEncoding.DecodeString(response.AuthenticatorData)
				clientData, _ := base64.StdEncoding.DecodeString(response.ClientDataJSON)
				signature, _ := base64.StdEncoding.DecodeString(response.Signature)
				rawKey, verifyErr := verifySubjectPasskey(allowedKeys, pending.attach.UserID, pending.challenge, authData, clientData, signature, passkeyPolicy)
				if verifyErr != nil {
					writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "invalid passkey", SessionID: sessionID, ViewerID: pending.attach.ViewerID})
					continue
				}
				token, tokenErr := auth.GenerateAuthToken()
				if tokenErr != nil {
					writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "auth token generation failed", SessionID: sessionID, ViewerID: pending.attach.ViewerID})
					continue
				}
				passkeyCache.Put(token, rawKey, pending.subject)
				attach = pending.attach
				attach.AuthToken = token
				env.Type = ws.TypePTYAttach
				log.Printf("pty session %s: reattach passkey verified", sessionID)
			}
			switch env.Type {
			case ws.TypePTYAttach:
				if attach.Type == "" {
					if err := json.Unmarshal(data, &attach); err != nil {
						continue
					}
				}
				if !canAttachSession(attach.UserID, attach.OrgRole, readEggOwner(eggDir)) {
					writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "session not found or not owned by caller", SessionID: sessionID, ViewerID: attach.ViewerID})
					continue
				}
				clearAttentionCooldown(sessionID)
				if attach.PublicKey == "" {
					writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "client encryption key required", SessionID: sessionID, ViewerID: attach.ViewerID})
					continue
				}

				// Passkey auth gate — per-user check
				var attachAuthToken string
				attachSubject := passkeySubject(attach.UserID, attach.PublicKey)
				attachUserHasPasskey := len(passkeysForSubject(allowedKeys, attach.UserID)) > 0
				if wingCfg.Locked && (attachSubject == "" || !attachUserHasPasskey) {
					writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "not allowed by wing", SessionID: sessionID, ViewerID: attach.ViewerID})
					continue
				}
				if attachUserHasPasskey {
					tokenOK := false
					if attach.AuthToken != "" {
						if _, ok := passkeyCache.Check(attach.AuthToken, authTTL, attachSubject); ok {
							tokenOK = true
							attachAuthToken = attach.AuthToken
							log.Printf("pty session %s: reattach passkey auth via cached token", sessionID)
						}
					}
					if !tokenOK {
						challenge, chalErr := auth.GenerateChallenge()
						if chalErr != nil {
							log.Printf("pty session %s: reattach challenge generation failed: %v", sessionID, chalErr)
							continue
						}
						writePTYMessage(write, ws.PasskeyChallenge{
							Type:      ws.TypePasskeyChallenge,
							SessionID: sessionID,
							Challenge: base64.RawURLEncoding.EncodeToString(challenge),
							RPID:      passkeyPolicy.RPID,
							ViewerID:  attach.ViewerID,
						})
						log.Printf("pty session %s: reattach passkey challenge sent", sessionID)
						pendingAuth.put(attach, challenge, attachSubject, time.Minute)
						continue reclaimInputLoop
					}
				}

				// Spectators get an independent encrypted stream and never replace
				// the controller, including after a wing daemon reclaim.
				if attach.Spectate {
					if !wingCfg.Spectate {
						writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: sessionID, ExitCode: 1, Error: "spectate not enabled", ViewerID: attach.ViewerID})
						continue
					}
					spectatorGCM, deriveErr := auth.DeriveSharedKey(privKey, attach.PublicKey, "wt-pty")
					if deriveErr != nil {
						writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "spectator encryption setup failed", SessionID: sessionID, ViewerID: attach.ViewerID})
						continue
					}
					specCtx, specCancel := context.WithCancel(ctx)
					specStream, specErr := ec.AttachSession(specCtx, sessionID)
					if specErr != nil {
						specCancel()
						writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "spectator attach failed", SessionID: sessionID, ViewerID: attach.ViewerID})
						continue
					}
					writePTYMessage(write, ws.PTYStarted{
						Type: ws.TypePTYStarted, SessionID: sessionID, Agent: reclaimAgent,
						PublicKey: wingPubKeyB64, AuthToken: attachAuthToken, ViewerID: attach.ViewerID,
					})
					replayMsg, replayErr := specStream.Recv()
					if replayErr == nil {
						if replay, ok := replayMsg.Payload.(*pb.SessionMsg_Output); ok && len(replay.Output) > 0 {
							sendReplayChunkedTagged(sessionID, attach.ViewerID, replay.Output, spectatorGCM, write)
						}
					}
					go func(viewerID string, g cipher.AEAD, stream pb.Egg_SessionClient, cancel context.CancelFunc) {
						defer cancel()
						for {
							msg, err := stream.Recv()
							if err != nil {
								return
							}
							switch payload := msg.Payload.(type) {
							case *pb.SessionMsg_Output:
								sendPTYOutputTagged(sessionID, viewerID, payload.Output, g, write)
							case *pb.SessionMsg_ExitCode:
								writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: sessionID, ExitCode: int(payload.ExitCode), ViewerID: viewerID})
								return
							}
						}
					}(attach.ViewerID, spectatorGCM, specStream, specCancel)
					continue reclaimInputLoop
				}

				// Prepare the replacement key and egg subscription before touching
				// the current controller. Authorization/setup failures must not turn
				// an attach attempt into a denial of service.
				newGCM, deriveErr := auth.DeriveSharedKey(privKey, attach.PublicKey, "wt-pty")
				if deriveErr != nil {
					log.Printf("pty session %s: reattach derive key failed: %v", sessionID, deriveErr)
					writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "client encryption setup failed", SessionID: sessionID})
					continue
				}
				log.Printf("pty session %s: re-keyed E2E for reattach", sessionID)
				newStreamCtx, newSCancel := context.WithCancel(ctx)
				newStream, reErr := ec.AttachSession(newStreamCtx, sessionID)
				if reErr != nil {
					newSCancel()
					log.Printf("pty session %s: reattach to egg failed: %v", sessionID, reErr)
					writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "reattach failed", SessionID: sessionID})
					continue
				}

				// Replacement is ready: stop the old stream, then authorize relay
				// promotion by emitting pty.started.
				mu.Lock()
				gcm = nil
				if cancelStream != nil {
					cancelStream()
				}
				mu.Unlock()

				// Send pty.started so browser can derive key.
				reclaimIdleState.mu.Lock()
				reclaimIdleState.connected = true
				reclaimIdleState.mu.Unlock()
				{
					started := ws.PTYStarted{Type: ws.TypePTYStarted, SessionID: sessionID, PublicKey: wingPubKeyB64}
					if attachAuthToken != "" {
						started.AuthToken = attachAuthToken
					}
					writePTYMessage(write, started)
				}

				// Resize egg to browser dimensions before snapshot.
				if attach.Cols > 0 && attach.Rows > 0 {
					if err := ec.Resize(ctx, sessionID, attach.Rows, attach.Cols); err != nil {
						log.Printf("pty session %s: resize reclaimed egg: %v", sessionID, err)
					}
					time.Sleep(150 * time.Millisecond) // let agent repaint for new dimensions before VTE snapshot
				}

				// Read replay (first message) and send to browser in chunks.
				if newGCM != nil {
					replayMsg, rErr := newStream.Recv()
					if rErr == nil {
						if replay, ok := replayMsg.Payload.(*pb.SessionMsg_Output); ok && len(replay.Output) > 0 {
							sendReplayChunked(sessionID, replay.Output, newGCM, write)
						}
					}
				}

				// Activate new key + stream, start new output goroutine.
				mu.Lock()
				gcm = newGCM
				activeStream = newStream
				cancelStream = newSCancel
				mu.Unlock()

				go func() {
					var lastHadBell bool
					for {
						msg, err := newStream.Recv()
						if err != nil {
							if err != io.EOF {
								log.Printf("pty session %s: egg stream error: %v", sessionID, err)
							}
							return
						}
						switch p := msg.Payload.(type) {
						case *pb.SessionMsg_Output:
							reclaimIdleState.mu.Lock()
							reclaimIdleState.lastOutput = time.Now()
							reclaimIdleState.mu.Unlock()
							if hasBell(p.Output) {
								if lastHadBell {
									checkAndSendAttention(sessionID, reclaimAgent, reclaimCWD, write)
								}
								lastHadBell = true
							} else {
								lastHadBell = false
							}
							mu.Lock()
							currentGCM := gcm
							mu.Unlock()
							if currentGCM == nil {
								continue
							}
							sendPTYOutput(sessionID, p.Output, currentGCM, write)
						case *pb.SessionMsg_ExitCode:
							log.Printf("pty session %s: exited with code %d", sessionID, p.ExitCode)
							writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: sessionID, ExitCode: int(p.ExitCode)})
							clearAttentionCooldown(sessionID)
							sessionCancel()
							return
						}
					}
				}()

			case ws.TypePTYInput:
				clearAttentionCooldown(sessionID)
				reclaimIdleState.mu.Lock()
				reclaimIdleState.lastInput = time.Now()
				reclaimIdleState.mu.Unlock()
				var msg ws.PTYInput
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}
				mu.Lock()
				currentGCM := gcm
				currentStream := activeStream
				mu.Unlock()
				if currentGCM == nil || currentStream == nil {
					log.Printf("pty session %s: rejecting input — E2E not established", sessionID)
					continue
				}
				decoded, decErr := auth.Decrypt(currentGCM, msg.Data)
				if decErr != nil {
					continue
				}
				if err := currentStream.Send(&pb.SessionMsg{SessionId: sessionID, Payload: &pb.SessionMsg_Input{Input: decoded}}); err != nil {
					log.Printf("pty session %s: send input to reclaimed egg: %v", sessionID, err)
					sessionCancel()
					return
				}

			case ws.TypePTYAttentionAck:
				clearAttentionCooldown(sessionID)

			case ws.TypePTYResize:
				var msg ws.PTYResize
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}
				mu.Lock()
				currentStream := activeStream
				mu.Unlock()
				if currentStream != nil {
					if err := currentStream.Send(&pb.SessionMsg{SessionId: sessionID, Payload: &pb.SessionMsg_Resize{Resize: &pb.Resize{Rows: uint32(msg.Rows), Cols: uint32(msg.Cols)}}}); err != nil {
						log.Printf("pty session %s: send resize to reclaimed egg: %v", sessionID, err)
						sessionCancel()
						return
					}
				}

			case ws.TypePTYKill:
				log.Printf("pty session %s: kill received", sessionID)
				if err := ec.Kill(ctx, sessionID); err != nil {
					log.Printf("pty session %s: kill reclaimed egg: %v", sessionID, err)
				}
				sessionCancel()
				return
			}
		}
	}()

	<-sessionCtx.Done()
}

// handlePTYSession bridges a PTY session between a per-session egg and the relay.
// E2E encryption stays in the wing — the egg sees plaintext only.
func handlePTYSession(ctx context.Context, cfg *config.Config, wingCfg *config.WingConfig, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte, eggCfg *egg.EggConfig, debug, vte bool, allowedKeysPtr *[]config.AllowKey, passkeyCache *auth.AuthCache, passkeyPolicy auth.PasskeyPolicy, authTTL time.Duration, idleTimeout time.Duration, sw *webrtcpkg.SwappableWriter, dcSessions *sync.Map, tools []*config.ToolConfig, sharedHost bool) {
	allowedKeys := *allowedKeysPtr
	if start.PublicKey == "" {
		writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "E2E client key required"})
		return
	}
	subject := passkeySubject(start.UserID, start.PublicKey)
	userHasPasskey := len(passkeysForSubject(allowedKeys, start.UserID)) > 0
	if wingCfg.Locked && (subject == "" || !userHasPasskey) {
		log.Printf("pty session %s: locked wing rejected user without a locally approved passkey", start.SessionID)
		writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "not allowed by wing"})
		return
	}
	if userHasPasskey {
		// Check cached auth token first
		if start.AuthToken != "" {
			if _, ok := passkeyCache.Check(start.AuthToken, authTTL, subject); ok {
				log.Printf("pty session %s: passkey auth via cached token", start.SessionID)
				goto authDone
			}
		}

		// Generate and send challenge
		challenge, chalErr := auth.GenerateChallenge()
		if chalErr != nil {
			writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "challenge generation failed"})
			return
		}

		writePTYMessage(write, ws.PasskeyChallenge{
			Type:      ws.TypePasskeyChallenge,
			SessionID: start.SessionID,
			Challenge: base64.RawURLEncoding.EncodeToString(challenge),
			RPID:      passkeyPolicy.RPID,
		})
		log.Printf("pty session %s: passkey challenge sent, waiting for response", start.SessionID)

		// Wait for passkey response on input channel (60s timeout)
		timer := time.NewTimer(60 * time.Second)
		defer timer.Stop()
		var passkeyVerified bool
		for !passkeyVerified {
			select {
			case data, ok := <-input:
				if !ok {
					return
				}
				var env ws.Envelope
				if err := json.Unmarshal(data, &env); err != nil {
					continue
				}
				if env.Type != ws.TypePasskeyResponse {
					continue // ignore non-passkey messages during auth
				}
				var resp ws.PasskeyResponse
				if err := json.Unmarshal(data, &resp); err != nil {
					writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "invalid passkey response"})
					return
				}

				// Decode assertion fields
				authData, _ := base64.StdEncoding.DecodeString(resp.AuthenticatorData)
				clientJSON, _ := base64.StdEncoding.DecodeString(resp.ClientDataJSON)
				sig, _ := base64.StdEncoding.DecodeString(resp.Signature)

				matchedRawKey, verifyErr := verifySubjectPasskey(allowedKeys, start.UserID, challenge, authData, clientJSON, sig, passkeyPolicy)
				if verifyErr != nil {
					writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "invalid passkey signature"})
					return
				}
				log.Printf("pty session %s: passkey verified", start.SessionID)

				// Issue auth token for subsequent sessions
				token, tokErr := auth.GenerateAuthToken()
				if tokErr == nil {
					passkeyCache.Put(token, matchedRawKey, subject)
					start.AuthToken = token // will be included in PTYStarted
				}
				passkeyVerified = true

			case <-timer.C:
				writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "passkey authentication timed out"})
				return

			case <-ctx.Done():
				return
			}
		}
	}
authDone:

	// Set up E2E encryption — required, no plaintext fallback
	var mu sync.Mutex
	var gcm cipher.AEAD
	var activeStream pb.Egg_SessionClient
	var cancelStream context.CancelFunc
	var wingPubKeyB64 string
	privKey, privKeyErr := auth.LoadPrivateKey(cfg.Dir)
	if privKeyErr != nil {
		log.Printf("pty session %s: FATAL: load private key: %v", start.SessionID, privKeyErr)
		writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "E2E encryption required but wing private key missing"})
		return
	}
	wingPubKeyB64 = base64.StdEncoding.EncodeToString(privKey.PublicKey().Bytes())
	if start.PublicKey != "" {
		derived, deriveErr := auth.DeriveSharedKey(privKey, start.PublicKey, "wt-pty")
		if deriveErr != nil {
			log.Printf("pty session %s: FATAL: derive shared key: %v", start.SessionID, deriveErr)
			writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "E2E key exchange failed"})
			return
		}
		gcm = derived
		log.Printf("pty session %s: E2E encryption enabled", start.SessionID)
	}

	// Start tool socket listener if tools are configured
	var toolListener *egg.ToolListener
	var toolSocketPath string
	var toolNames []string
	if len(tools) > 0 {
		eggDir := filepath.Join(cfg.Dir, "eggs", start.SessionID)
		toolsDir := filepath.Join(eggDir, ".tools")
		if err := os.MkdirAll(toolsDir, 0700); err != nil {
			log.Printf("pty session %s: create tool directory: %v", start.SessionID, err)
			writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "create tool directory: " + err.Error()})
			return
		}
		toolSocketPath = filepath.Join(toolsDir, "tool.sock")
		var tlErr error
		toolListener, tlErr = egg.NewToolListener(toolSocketPath, tools)
		if tlErr != nil {
			log.Printf("pty session %s: tool listener failed: %v", start.SessionID, tlErr)
		} else {
			toolNames = config.ToolNames(tools)
			log.Printf("pty session %s: tool listener started (%d tools)", start.SessionID, len(toolNames))
		}
	}
	if toolListener != nil {
		defer closeWithLog("egg tool listener", toolListener)
	}

	// Spawn a per-session egg
	hostHome, _ := os.UserHomeDir()
	sharedAllowedPaths := canonicalPaths(pathsForRequest(wingCfg.Paths, start.Email, start.OrgRole, hostHome))
	ec, err := spawnEgg(cfg, start.SessionID, start.Agent, eggCfg, uint32(start.Rows), uint32(start.Cols), start.CWD, debug, vte, eggCfg.Trace, EggIdentity{
		UserID: start.UserID, Email: start.Email, DisplayName: start.DisplayName,
		OrgWing: wingCfg.Org != "", SharedHost: sharedHost,
		// Browser terminals get the same allowlist jail as MCP agent runs; a
		// shared-roost PTY without SealedFS would retain the default ro:/ rule
		// and read the host home, wing.yaml keys, and other users' agent homes.
		SealedFS:     sharedHost,
		AllowedPaths: sharedAllowedPaths,
	}, idleTimeout, spawnEggOpts{ToolNames: toolNames, ToolSocketPath: toolSocketPath})
	if err != nil {
		eggDir := filepath.Join(cfg.Dir, "eggs", start.SessionID)
		crashInfo := readEggCrashInfo(eggDir)
		log.Printf("pty session %s: spawn egg failed: %v", start.SessionID, err)
		// If no crash info from the child (e.g. pre-flight check failed before
		// child was spawned), use the spawn error directly — it contains the
		// actionable fix instructions.
		if strings.Contains(crashInfo, "no log available") || strings.Contains(crashInfo, "empty log") {
			crashInfo = err.Error()
		}
		writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: crashInfo})
		return
	}
	defer closeWithLog("PTY egg client", ec)

	log.Printf("pty session %s: spawned (user=%s agent=%s)", start.SessionID, start.UserID, start.Agent)

	// Register idle state tracking
	idleState := &sessionIdleState{
		lastOutput: time.Now(),
		lastInput:  time.Now(),
		connected:  true,
		eggDir:     filepath.Join(cfg.Dir, "eggs", start.SessionID),
	}
	sessionStates.Store(start.SessionID, idleState)
	defer sessionStates.Delete(start.SessionID)
	defer forgetAttentionState(start.SessionID)

	// Notify browser
	writePTYMessage(write, ws.PTYStarted{
		Type:      ws.TypePTYStarted,
		SessionID: start.SessionID,
		Agent:     start.Agent,
		PublicKey: wingPubKeyB64,
		CWD:       start.CWD,
		AuthToken: start.AuthToken,
	})

	// Attach to egg session stream
	streamCtx, sCancel := context.WithCancel(ctx)
	stream, err := ec.AttachSession(streamCtx, start.SessionID)
	if err != nil {
		sCancel()
		log.Printf("pty: egg attach failed: %v", err)
		writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1})
		return
	}
	activeStream = stream
	cancelStream = sCancel

	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()

	// Watch for .wt-preview file in agent working directory
	if start.CWD != "" {
		go watchPreviewFile(sessionCtx, start.CWD, start.SessionID, &mu, &gcm, write)
	}

	// Watch for browser open requests from the shim
	go watchBrowserRequests(sessionCtx, filepath.Join(cfg.Dir, "eggs", start.SessionID, "browser-requests"), start.SessionID, write)

	// Read output from egg -> encrypt -> send to browser
	go func() {
		var lastHadBell bool
		for {
			msg, err := stream.Recv()
			if err != nil {
				if err != io.EOF {
					log.Printf("pty session %s: egg stream error: %v", start.SessionID, err)
				}
				return
			}

			switch p := msg.Payload.(type) {
			case *pb.SessionMsg_Output:
				idleState.mu.Lock()
				idleState.lastOutput = time.Now()
				idleState.mu.Unlock()
				if hasBell(p.Output) {
					if lastHadBell {
						checkAndSendAttention(start.SessionID, start.Agent, start.CWD, write)
					}
					lastHadBell = true
				} else {
					lastHadBell = false
				}

				mu.Lock()
				currentGCM := gcm
				mu.Unlock()
				if currentGCM == nil {
					continue
				}
				sendPTYOutput(start.SessionID, p.Output, currentGCM, write)

			case *pb.SessionMsg_ExitCode:
				log.Printf("pty session %s: exited with code %d", start.SessionID, p.ExitCode)
				writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: int(p.ExitCode)})
				clearAttentionCooldown(start.SessionID)
				sessionCancel()
				return
			}
		}
	}()

	// Process input from browser -> decrypt -> send to egg
	go func() {
		pendingAuth := newPendingReattachAuths()
		defer pendingAuth.close()
		defer func() {
			idleState.mu.Lock()
			idleState.connected = false
			idleState.mu.Unlock()
		}()
	inputLoop:
		for {
			var data []byte
			select {
			case <-ctx.Done():
				return
			case <-pendingAuth.timeout():
				for _, pending := range pendingAuth.expire(time.Now()) {
					log.Printf("pty session %s: reattach passkey timed out", start.SessionID)
					writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "passkey timed out", SessionID: start.SessionID, ViewerID: pending.attach.ViewerID})
				}
				continue
			case inputData, ok := <-input:
				if !ok {
					return
				}
				data = inputData
			}
			var env ws.Envelope
			if err := json.Unmarshal(data, &env); err != nil {
				continue
			}
			var attach ws.PTYAttach
			if env.Type == ws.TypePasskeyResponse {
				var response ws.PasskeyResponse
				if err := json.Unmarshal(data, &response); err != nil {
					continue
				}
				pending, ok := pendingAuth.take(response.ViewerID)
				if !ok {
					log.Printf("pty session %s: ignoring passkey response without matching reattach", start.SessionID)
					continue
				}
				authData, _ := base64.StdEncoding.DecodeString(response.AuthenticatorData)
				clientData, _ := base64.StdEncoding.DecodeString(response.ClientDataJSON)
				signature, _ := base64.StdEncoding.DecodeString(response.Signature)
				rawKey, verifyErr := verifySubjectPasskey(allowedKeys, pending.attach.UserID, pending.challenge, authData, clientData, signature, passkeyPolicy)
				if verifyErr != nil {
					writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "invalid passkey", SessionID: start.SessionID, ViewerID: pending.attach.ViewerID})
					continue
				}
				token, tokenErr := auth.GenerateAuthToken()
				if tokenErr != nil {
					writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "auth token generation failed", SessionID: start.SessionID, ViewerID: pending.attach.ViewerID})
					continue
				}
				passkeyCache.Put(token, rawKey, pending.subject)
				attach = pending.attach
				attach.AuthToken = token
				env.Type = ws.TypePTYAttach
				log.Printf("pty session %s: reattach passkey verified", start.SessionID)
			}
			switch env.Type {
			case ws.TypePTYAttach:
				if attach.Type == "" {
					if err := json.Unmarshal(data, &attach); err != nil {
						continue
					}
				}
				if !canAttachSession(attach.UserID, attach.OrgRole, start.UserID) {
					writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "session not found or not owned by caller", SessionID: start.SessionID, ViewerID: attach.ViewerID})
					continue
				}
				clearAttentionCooldown(start.SessionID)
				if attach.PublicKey == "" {
					writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "client encryption key required", SessionID: start.SessionID, ViewerID: attach.ViewerID})
					continue
				}

				attachSubject := passkeySubject(attach.UserID, attach.PublicKey)
				attachUserHasPasskey := len(passkeysForSubject(allowedKeys, attach.UserID)) > 0
				if wingCfg.Locked && (attachSubject == "" || !attachUserHasPasskey) {
					writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "not allowed by wing", SessionID: start.SessionID, ViewerID: attach.ViewerID})
					continue
				}
				var attachAuthToken string
				if attachUserHasPasskey {
					if attach.AuthToken != "" {
						if _, ok := passkeyCache.Check(attach.AuthToken, authTTL, attachSubject); ok {
							attachAuthToken = attach.AuthToken
						}
					}
					if attachAuthToken == "" {
						challenge, chalErr := auth.GenerateChallenge()
						if chalErr != nil {
							writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "challenge generation failed", SessionID: start.SessionID, ViewerID: attach.ViewerID})
							continue
						}
						writePTYMessage(write, ws.PasskeyChallenge{
							Type:      ws.TypePasskeyChallenge,
							SessionID: start.SessionID,
							Challenge: base64.RawURLEncoding.EncodeToString(challenge),
							RPID:      passkeyPolicy.RPID,
							ViewerID:  attach.ViewerID,
						})
						pendingAuth.put(attach, challenge, attachSubject, time.Minute)
						continue inputLoop
					}
				}

				// Spectator mode: independent stream without disrupting controller
				if attach.Spectate {
					if !wingCfg.Spectate {
						log.Printf("pty session %s: spectate rejected (not enabled in wing config)", start.SessionID)
						writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "spectate not enabled", ViewerID: attach.ViewerID})
						continue
					}
					// Derive spectator-specific E2E key (independent of controller)
					spectatorGCM, deriveErr := auth.DeriveSharedKey(privKey, attach.PublicKey, "wt-pty")
					if deriveErr != nil {
						log.Printf("pty session %s: spectator key derive failed: %v", start.SessionID, deriveErr)
						writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "spectator encryption setup failed", SessionID: start.SessionID, ViewerID: attach.ViewerID})
						continue
					}
					log.Printf("pty session %s: spectator E2E enabled (viewer=%s)", start.SessionID, attach.ViewerID)

					// Open independent egg stream (gets replay + live cursor)
					specCtx, specCancel := context.WithCancel(ctx)
					specStream, specErr := ec.AttachSession(specCtx, start.SessionID)
					if specErr != nil {
						specCancel()
						log.Printf("pty session %s: spectator attach to egg failed: %v", start.SessionID, specErr)
						writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "spectator attach failed", SessionID: start.SessionID, ViewerID: attach.ViewerID})
						continue
					}

					// Send pty.started only after the independent stream exists.
					writePTYMessage(write, ws.PTYStarted{
						Type:      ws.TypePTYStarted,
						SessionID: start.SessionID,
						Agent:     start.Agent,
						PublicKey: wingPubKeyB64,
						AuthToken: attachAuthToken,
						ViewerID:  attach.ViewerID,
					})

					// Replay
					if spectatorGCM != nil {
						replayMsg, rErr := specStream.Recv()
						if rErr == nil {
							if replay, ok := replayMsg.Payload.(*pb.SessionMsg_Output); ok && len(replay.Output) > 0 {
								sendReplayChunkedTagged(start.SessionID, attach.ViewerID, replay.Output, spectatorGCM, write)
							}
						}
					}

					// Independent output goroutine
					go func(viewerID string, g cipher.AEAD, stream pb.Egg_SessionClient, cancel context.CancelFunc) {
						defer cancel()
						for {
							msg, err := stream.Recv()
							if err != nil {
								return
							}
							switch p := msg.Payload.(type) {
							case *pb.SessionMsg_Output:
								if g != nil {
									sendPTYOutputTagged(start.SessionID, viewerID, p.Output, g, write)
								}
							case *pb.SessionMsg_ExitCode:
								writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: int(p.ExitCode), ViewerID: viewerID})
								return
							}
						}
					}(attach.ViewerID, spectatorGCM, specStream, specCancel)
					log.Printf("pty session %s: spectator streaming started (viewer=%s)", start.SessionID, attach.ViewerID)
					continue
				}

				// Prepare replacement crypto and stream without disturbing the
				// current controller if any setup step fails.
				newGCM, deriveErr := auth.DeriveSharedKey(privKey, attach.PublicKey, "wt-pty")
				if deriveErr != nil {
					log.Printf("pty session %s: reattach derive key failed: %v", start.SessionID, deriveErr)
					writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "client encryption setup failed", SessionID: start.SessionID})
					continue
				}
				log.Printf("pty session %s: re-keyed E2E for reattach", start.SessionID)
				newStreamCtx, newSCancel := context.WithCancel(ctx)
				newStream, reErr := ec.AttachSession(newStreamCtx, start.SessionID)
				if reErr != nil {
					newSCancel()
					log.Printf("pty session %s: reattach to egg failed: %v", start.SessionID, reErr)
					writePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "reattach failed", SessionID: start.SessionID})
					continue
				}

				mu.Lock()
				gcm = nil
				if cancelStream != nil {
					cancelStream()
				}
				mu.Unlock()

				// Send pty.started so browser can derive key and the relay can
				// promote this pending controller.
				idleState.mu.Lock()
				idleState.connected = true
				idleState.mu.Unlock()
				writePTYMessage(write, ws.PTYStarted{
					Type:      ws.TypePTYStarted,
					SessionID: start.SessionID,
					Agent:     start.Agent,
					PublicKey: wingPubKeyB64,
					AuthToken: attachAuthToken,
				})

				// Resize egg to browser dimensions before snapshot.
				if attach.Cols > 0 && attach.Rows > 0 {
					if err := ec.Resize(ctx, start.SessionID, attach.Rows, attach.Cols); err != nil {
						log.Printf("pty session %s: resize egg after reattach: %v", start.SessionID, err)
					}
					time.Sleep(150 * time.Millisecond) // let agent repaint for new dimensions before VTE snapshot
				}

				// Read replay (first message) and send to browser in chunks.
				if newGCM != nil {
					replayMsg, rErr := newStream.Recv()
					if rErr == nil {
						if replay, ok := replayMsg.Payload.(*pb.SessionMsg_Output); ok && len(replay.Output) > 0 {
							sendReplayChunked(start.SessionID, replay.Output, newGCM, write)
						}
					}
				}

				// Activate new key + stream, start new output goroutine.
				mu.Lock()
				gcm = newGCM
				activeStream = newStream
				cancelStream = newSCancel
				mu.Unlock()

				go func() {
					var lastHadBell bool
					for {
						msg, err := newStream.Recv()
						if err != nil {
							if err != io.EOF {
								log.Printf("pty session %s: egg stream error: %v", start.SessionID, err)
							}
							return
						}
						switch p := msg.Payload.(type) {
						case *pb.SessionMsg_Output:
							idleState.mu.Lock()
							idleState.lastOutput = time.Now()
							idleState.mu.Unlock()
							if hasBell(p.Output) {
								if lastHadBell {
									checkAndSendAttention(start.SessionID, start.Agent, start.CWD, write)
								}
								lastHadBell = true
							} else {
								lastHadBell = false
							}
							mu.Lock()
							currentGCM := gcm
							mu.Unlock()
							if currentGCM == nil {
								continue
							}
							sendPTYOutput(start.SessionID, p.Output, currentGCM, write)
						case *pb.SessionMsg_ExitCode:
							log.Printf("pty session %s: exited with code %d", start.SessionID, p.ExitCode)
							writePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: int(p.ExitCode)})
							clearAttentionCooldown(start.SessionID)
							sessionCancel()
							return
						}
					}
				}()

			case ws.TypePTYInput:
				clearAttentionCooldown(start.SessionID)
				idleState.mu.Lock()
				idleState.lastInput = time.Now()
				idleState.mu.Unlock()
				var msg ws.PTYInput
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}
				mu.Lock()
				currentGCM := gcm
				currentStream := activeStream
				mu.Unlock()
				if currentGCM == nil || currentStream == nil {
					log.Printf("pty session %s: rejecting input — E2E not established", start.SessionID)
					continue
				}
				decoded, decErr := auth.Decrypt(currentGCM, msg.Data)
				if decErr != nil {
					log.Printf("pty session %s: decrypt error: %v", start.SessionID, decErr)
					continue
				}
				if err := currentStream.Send(&pb.SessionMsg{
					SessionId: start.SessionID,
					Payload:   &pb.SessionMsg_Input{Input: decoded},
				}); err != nil {
					log.Printf("pty session %s: send input to egg: %v", start.SessionID, err)
					sessionCancel()
					return
				}

			case ws.TypePTYAttentionAck:
				clearAttentionCooldown(start.SessionID)

			case ws.TypePTYResize:
				var msg ws.PTYResize
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}
				mu.Lock()
				currentStream := activeStream
				mu.Unlock()
				if currentStream != nil {
					if err := currentStream.Send(&pb.SessionMsg{
						SessionId: start.SessionID,
						Payload: &pb.SessionMsg_Resize{Resize: &pb.Resize{
							Rows: uint32(msg.Rows),
							Cols: uint32(msg.Cols),
						}},
					}); err != nil {
						log.Printf("pty session %s: send resize to egg: %v", start.SessionID, err)
						sessionCancel()
						return
					}
				}

			case ws.TypePTYMigrate:
				if sw == nil {
					log.Printf("[P2P] pty.migrate for %s but P2P not enabled", start.SessionID)
					continue
				}
				// Validate auth token on locked wings before allowing P2P migration
				if userHasPasskey {
					var migrateMsg ws.PTYMigrate
					if err := json.Unmarshal(data, &migrateMsg); err != nil {
						log.Printf("[P2P] pty.migrate for %s: bad message: %v", start.SessionID, err)
						continue
					}
					if _, ok := passkeyCache.Check(migrateMsg.AuthToken, authTTL, subject); !ok {
						log.Printf("[P2P] pty.migrate for %s: REJECTED — invalid or expired auth token", start.SessionID)
						continue
					}
				}
				// Look up the DataChannel — retry briefly since OnDC callback may not have fired yet
				var migrateDC *pionwebrtc.DataChannel
				for attempt := 0; attempt < 10; attempt++ {
					if dcVal, ok := dcSessions.Load(start.SessionID); ok {
						migrateDC = dcVal.(*pionwebrtc.DataChannel)
						break
					}
					time.Sleep(50 * time.Millisecond)
				}
				if migrateDC != nil {
					if err := sw.MigrateToDC(start.SessionID, migrateDC); err != nil {
						log.Printf("[P2P] migrate failed for %s: %v", start.SessionID, err)
					}
				} else {
					log.Printf("[P2P] pty.migrate for %s but no DC found after 500ms", start.SessionID)
				}

			case ws.TypePTYKill:
				log.Printf("pty session %s: kill received", start.SessionID)
				if err := ec.Kill(ctx, start.SessionID); err != nil {
					log.Printf("pty session %s: kill egg: %v", start.SessionID, err)
				}
				sessionCancel()
				return
			}
		}
	}()

	// Wait for session to end
	<-sessionCtx.Done()
}

// tunnelInner is the decrypted JSON payload inside a tunnel request.
type tunnelInner struct {
	Type        string `json:"type"`
	Path        string `json:"path,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	Kind        string `json:"kind,omitempty"`
	YAML        string `json:"yaml,omitempty"`
	Offset      int    `json:"offset,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	Cols        int    `json:"cols,omitempty"`
	Rows        int    `json:"rows,omitempty"`
	AuthToken   string `json:"auth_token,omitempty"`
	Key         string `json:"key,omitempty"`           // passkey public key for allow.add
	AllowUserID string `json:"allow_user_id,omitempty"` // target user_id for allow.remove
	SDP         string `json:"sdp,omitempty"`           // WebRTC SDP for webrtc.offer

	// Path ACL fields (for paths.set / paths.add_member / paths.remove_member)
	Paths   []config.PathEntry `json:"paths,omitempty"`   // for paths.set (bulk replace)
	Members []string           `json:"members,omitempty"` // for paths.set on a single path
	Email   string             `json:"email,omitempty"`   // for paths.add_member / paths.remove_member

	// Passkey assertion fields (for type "passkey.auth.finish")
	ChallengeID       string `json:"challenge_id,omitempty"`
	CredentialID      string `json:"credential_id,omitempty"`
	AuthenticatorData string `json:"authenticator_data,omitempty"`
	ClientDataJSON    string `json:"client_data_json,omitempty"`
	Signature         string `json:"signature,omitempty"`
}

func passkeySubject(userID, clientPublicKey string) string {
	if userID == "" || clientPublicKey == "" {
		return ""
	}
	return userID + "\x00" + clientPublicKey
}

// passkeyRPURL picks the URL whose host anchors the WebAuthn relying party.
// The embedded roost wing connects over loopback while browsers reach the
// roost at its public base URL (WT_BASE_URL) — the same source the relay's
// passkey registration endpoint derives its RP ID from. RP ID and origin must
// match the browser-facing host or every WebAuthn ceremony fails closed.
func passkeyRPURL(roostURL, baseURL string) string {
	if baseURL != "" {
		return baseURL
	}
	return roostURL
}

// passkeyPolicyForRoost mirrors the relying-party configuration used by the
// relay's registration endpoint. A custom/self-hosted roost uses its own host;
// the managed websocket and app hosts share the wingthing.ai RP ID.
func passkeyPolicyForRoost(roostURL string) auth.PasskeyPolicy {
	httpURL := strings.Replace(roostURL, "wss://", "https://", 1)
	httpURL = strings.Replace(httpURL, "ws://", "http://", 1)
	if !strings.Contains(httpURL, "://") {
		httpURL = "https://" + httpURL
	}
	u, err := url.Parse(httpURL)
	if err != nil || u.Hostname() == "" {
		return auth.PasskeyPolicy{}
	}
	hostname := strings.ToLower(u.Hostname())
	if hostname == "wingthing.ai" || strings.HasSuffix(hostname, ".wingthing.ai") {
		return auth.PasskeyPolicy{
			RPID:                    "wingthing.ai",
			Origins:                 []string{"https://app.wingthing.ai"},
			RequireUserVerification: true,
		}
	}
	origin := u.Scheme + "://" + u.Host
	origins := []string{origin}
	if hostname == "localhost" {
		for _, candidate := range []string{"http://localhost:8080", "http://localhost:5173"} {
			if candidate != origin {
				origins = append(origins, candidate)
			}
		}
	}
	return auth.PasskeyPolicy{
		RPID:                    hostname,
		Origins:                 origins,
		RequireUserVerification: true,
	}
}

// passkeyPolicyFromRegistration accepts only a coherent RP policy delivered by
// the authenticated coordinator. Additive acknowledgement fields let new wings
// support custom AppHost and localhost HTTPS while retaining their URL-derived
// fallback when connected to an older relay.
func passkeyPolicyFromRegistration(msg ws.RegisteredMsg) (auth.PasskeyPolicy, bool) {
	rpID := strings.ToLower(strings.TrimSpace(msg.PasskeyRPID))
	if rpID == "" || strings.ContainsAny(rpID, "/\\:@") || len(msg.PasskeyOrigins) == 0 || len(msg.PasskeyOrigins) > 8 {
		return auth.PasskeyPolicy{}, false
	}
	origins := make([]string, 0, len(msg.PasskeyOrigins))
	seen := make(map[string]bool, len(msg.PasskeyOrigins))
	for _, rawOrigin := range msg.PasskeyOrigins {
		parsed, err := url.Parse(rawOrigin)
		if err != nil || parsed.User != nil || parsed.Hostname() == "" || parsed.Path != "" ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return auth.PasskeyPolicy{}, false
		}
		host := strings.ToLower(parsed.Hostname())
		if host != rpID && !strings.HasSuffix(host, "."+rpID) {
			return auth.PasskeyPolicy{}, false
		}
		if parsed.Scheme != "https" {
			ip := net.ParseIP(host)
			loopback := strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
			if parsed.Scheme != "http" || !loopback {
				return auth.PasskeyPolicy{}, false
			}
		}
		origin := parsed.Scheme + "://" + parsed.Host
		if !seen[origin] {
			origins = append(origins, origin)
			seen[origin] = true
		}
	}
	return auth.PasskeyPolicy{
		RPID:                    rpID,
		Origins:                 origins,
		RequireUserVerification: true,
	}, true
}

func passkeysForSubject(allowedKeys []config.AllowKey, userID string) []config.AllowKey {
	var matches []config.AllowKey
	for _, allowed := range allowedKeys {
		if allowed.Key == "" {
			continue
		}
		// Key-only entries are an intentional compatibility mode for local
		// administrators. Identity-bound entries must match the relay user ID.
		if allowed.UserID == "" || allowed.UserID == userID {
			matches = append(matches, allowed)
		}
	}
	return matches
}

func visibleAllowKeys(req ws.TunnelRequest, allowedKeys []config.AllowKey) []config.AllowKey {
	if !isMemberFiltered(req) {
		return append([]config.AllowKey(nil), allowedKeys...)
	}
	visible := make([]config.AllowKey, 0, 1)
	for _, allowed := range allowedKeys {
		if allowed.UserID == req.SenderUserID {
			visible = append(visible, allowed)
		}
	}
	return visible
}

func verifySubjectPasskey(allowedKeys []config.AllowKey, userID string, challenge, authData, clientData, signature []byte, policy auth.PasskeyPolicy) ([]byte, error) {
	for _, allowed := range passkeysForSubject(allowedKeys, userID) {
		rawKey, err := base64.StdEncoding.DecodeString(allowed.Key)
		if err != nil || len(rawKey) != 64 {
			continue
		}
		if err := auth.VerifyPasskeyAssertion(rawKey, challenge, authData, clientData, signature, policy); err == nil {
			return rawKey, nil
		}
	}
	return nil, errors.New("passkey verification failed")
}

// pastSessionInfo is the local version of PastSessionInfo for tunnel responses.
type pastSessionInfo struct {
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	CWD       string `json:"cwd,omitempty"`
	StartedAt int64  `json:"started_at,omitempty"`
	Audit     bool   `json:"audit,omitempty"`
	Chat      bool   `json:"chat,omitempty"`
	UserID    string `json:"user_id,omitempty"`
}

// tunnelRespond encrypts a JSON response and sends it as a tunnel.res message.
func tunnelRespond(gcm cipher.AEAD, requestID string, result any, write ws.PTYWriteFunc) {
	data, _ := json.Marshal(result)
	encrypted, err := auth.Encrypt(gcm, data)
	if err != nil {
		return
	}
	writePTYMessage(write, ws.TunnelResponse{Type: ws.TypeTunnelResponse, RequestID: requestID, Payload: encrypted})
}

// tunnelStreamChunk encrypts a streaming chunk and sends it as a tunnel.stream message.
func tunnelStreamChunk(gcm cipher.AEAD, requestID string, chunk []byte, done bool, write ws.PTYWriteFunc) error {
	encrypted, err := auth.Encrypt(gcm, chunk)
	if err != nil {
		return err
	}
	return write(ws.TunnelStream{Type: ws.TypeTunnelStream, RequestID: requestID, Payload: encrypted, Done: done})
}

// isMemberFiltered returns true if the tunnel request is from an org member (not owner/admin).
// Empty/unknown roles are treated as "member" (least privilege) when a user ID is present.
func isMemberFiltered(req ws.TunnelRequest) bool {
	if req.SenderUserID == "" {
		return false
	}
	return req.SenderOrgRole != "owner" && req.SenderOrgRole != "admin"
}

// requestAgainstWingConfig recomputes only the wing-local admin override from
// the relay-authenticated role. Mutation paths call this while holding
// wingCfgMu so a removed local admin cannot commit one last stale-snapshot edit
// after SIGHUP has revoked that override.
func requestAgainstWingConfig(req ws.TunnelRequest, authenticatedOrgRole string, wingCfg *config.WingConfig) ws.TunnelRequest {
	req.SenderOrgRole = authenticatedOrgRole
	if wingCfg != nil && wingCfg.IsAdmin(req.SenderEmail) && isMemberRole(req.SenderOrgRole) {
		req.SenderOrgRole = "admin"
	}
	return req
}

// canSeeSession returns true if the request sender can view a session with the given owner.
func canSeeSession(req ws.TunnelRequest, sessionUserID string) bool {
	if !isMemberFiltered(req) {
		return true
	}
	return sessionUserID != "" && sessionUserID == req.SenderUserID
}

func canAttachSession(userID, orgRole, sessionUserID string) bool {
	if orgRole == "owner" || orgRole == "admin" {
		return true
	}
	return userID != "" && sessionUserID != "" && userID == sessionUserID
}

func canAccessSessionPath(req ws.TunnelRequest, sessionPath string, userPaths []string) bool {
	if !isMemberFiltered(req) {
		return true
	}
	return len(userPaths) > 0 && isUnderPaths(sessionPath, userPaths)
}

// canAccessSessionArtifact applies the same current owner-and-workspace policy
// used by session listings before exposing a persisted audit or chat artifact.
// Missing legacy metadata fails closed for members; owners and admins retain
// the historical oversight access.
func canAccessSessionArtifact(req ws.TunnelRequest, sessionDir string, userPaths []string) bool {
	if !isMemberFiltered(req) {
		return true
	}
	_, sessionPath := readEggMeta(sessionDir)
	return canSeeSession(req, readEggOwner(sessionDir)) && canAccessSessionPath(req, sessionPath, userPaths)
}

func requestDirEntries(req ws.TunnelRequest, path string, userPaths []string) []ws.DirEntry {
	if isMemberFiltered(req) && len(userPaths) == 0 {
		return nil
	}
	return getDirEntries(path, userPaths)
}

func requestProjects(req ws.TunnelRequest, projects []ws.WingProject, userPaths []string) []ws.WingProject {
	if len(userPaths) > 0 {
		return filterProjectsExact(projects, userPaths)
	}
	if isMemberFiltered(req) {
		return nil
	}
	return projects
}

func appendHostedRelayPolicyAudit(cfg *config.Config, operation string) (result error) {
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(cfg.Dir, "policy-audit.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { result = closeAndJoin("hosted relay policy audit", file, result) }()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	record := map[string]string{
		"time":      time.Now().UTC().Format(time.RFC3339Nano),
		"event":     "hosted_relay_denied",
		"operation": operation,
		"transport": "hosted-relay",
		"policy":    config.HostedRelayDeny,
	}
	return json.NewEncoder(file).Encode(record)
}

// handleTunnelRequest decrypts and dispatches an encrypted tunnel request from the browser.
func handleTunnelRequest(ctx context.Context, cfg *config.Config, wingCfg *config.WingConfig, req ws.TunnelRequest, write ws.PTYWriteFunc,
	allowedKeysPtr *[]config.AllowKey, passkeyCache *auth.AuthCache, passkeyChallenges *auth.ChallengeCache,
	passkeyPolicy auth.PasskeyPolicy, privKey *ecdh.PrivateKey, home string,
	wingEggMu *sync.Mutex, wingEggCfg **egg.EggConfig, audit, debug bool, client *ws.Client,
	peerMgr *webrtcpkg.PeerManager, dcSessions *sync.Map) {

	// Tunnel callbacks run concurrently and some operations stream or perform
	// network/process work for an arbitrary amount of time. Admit each request
	// against an immutable policy snapshot; hold wingCfgMu only while taking that
	// snapshot or committing a serialized wing.yaml mutation. This keeps SIGHUP
	// revocation and unrelated requests responsive to a slow peer.
	liveWingCfg := wingCfg
	authenticatedOrgRole := req.SenderOrgRole
	wingCfgMu.Lock()
	wingCfg = liveWingCfg.Clone()
	allowedKeys := append([]config.AllowKey(nil), (*allowedKeysPtr)...)
	wingCfgMu.Unlock()

	// Wing-level admin override
	if wingCfg.IsAdmin(req.SenderEmail) && isMemberRole(req.SenderOrgRole) {
		req.SenderOrgRole = "admin"
	}

	// Derive or retrieve cached AES-GCM key for this sender
	var gcm cipher.AEAD
	if cached, ok := tunnelKeys.Get(req.SenderPub); ok {
		gcm = cached
	}
	if gcm == nil {
		derived, err := auth.DeriveSharedKey(privKey, req.SenderPub, "wt-tunnel")
		if err != nil {
			log.Printf("tunnel: derive key failed: %v", err)
			return
		}
		gcm = derived
		tunnelKeys.Put(req.SenderPub, gcm)
	}

	// Decrypt the payload
	plaintext, err := auth.Decrypt(gcm, req.Payload)
	if err != nil {
		log.Printf("tunnel %s: decrypt failed: %v", req.RequestID, err)
		return
	}

	// Parse inner message
	var inner tunnelInner
	if err := json.Unmarshal(plaintext, &inner); err != nil {
		log.Printf("tunnel %s: bad inner JSON: %v", req.RequestID, err)
		return
	}
	if inner.SessionID != "" && !ws.ValidSessionID(inner.SessionID) {
		log.Printf("tunnel %s: rejected invalid session ID %q", req.RequestID, inner.SessionID)
		tunnelRespond(gcm, req.RequestID, map[string]string{"error": "invalid session ID"}, write)
		return
	}
	if req.Purpose != "" && !ws.TunnelPurposeMatches(req.Purpose, inner.Type) {
		log.Printf("tunnel %s: declared purpose %q does not match inner type %q", req.RequestID, req.Purpose, inner.Type)
		tunnelRespond(gcm, req.RequestID, map[string]string{"error": "tunnel purpose mismatch"}, write)
		return
	}
	// Every supported coordinator, including the N-1 org deployment, injects
	// authenticated sender identity. Treat its absence as a protocol failure,
	// not as legacy administrator authority.
	if req.SenderUserID == "" {
		tunnelRespond(gcm, req.RequestID, map[string]string{"error": "authenticated user identity required"}, write)
		return
	}

	isPasskeyCeremony := inner.Type == "passkey.auth.begin" || inner.Type == "passkey.auth.finish"
	subject := passkeySubject(req.SenderUserID, req.SenderPub)

	// Two-state auth check for locked wings. Relay-provided roles and passkey
	// keys are deliberately insufficient: the sender needs a key pinned in the
	// wing's local allowlist and a token bound to its encryption identity.
	if wingCfg.Locked && !isPasskeyCeremony {
		inList := subject != "" && len(passkeysForSubject(allowedKeys, req.SenderUserID)) > 0

		if !inList {
			// Not in allow list at all — locked
			tunnelRespond(gcm, req.RequestID, map[string]any{
				"error": "not_allowed",
			}, write)
			return
		}

		// Step 2: In the list — check auth token
		var authTTL time.Duration // default 0 = boot-scoped, no expiry
		if wingCfg.AuthTTL != "" {
			if d, err := time.ParseDuration(wingCfg.AuthTTL); err == nil {
				authTTL = d
			}
		}
		authorized := false
		if inner.AuthToken != "" {
			if _, ok := passkeyCache.Check(inner.AuthToken, authTTL, subject); ok {
				authorized = true
			}
		}

		if !authorized {
			// In list but not yet authenticated — passkey challenge
			tunnelRespond(gcm, req.RequestID, map[string]any{
				"error":    "passkey_required",
				"hostname": client.Hostname,
				"platform": client.Platform,
				"version":  version,
				"locked":   true,
			}, write)
			return
		}
	}

	// Per-user passkey enforcement on unlocked wings
	if !wingCfg.Locked && !isPasskeyCeremony {
		if req.SenderUserID != "" {
			userNeedsPasskey := len(passkeysForSubject(allowedKeys, req.SenderUserID)) > 0
			if userNeedsPasskey {
				var authTTL time.Duration
				if wingCfg.AuthTTL != "" {
					if d, err := time.ParseDuration(wingCfg.AuthTTL); err == nil {
						authTTL = d
					}
				}
				authorized := false
				if inner.AuthToken != "" {
					if _, ok := passkeyCache.Check(inner.AuthToken, authTTL, subject); ok {
						authorized = true
					}
				}
				if !authorized {
					tunnelRespond(gcm, req.RequestID, map[string]any{
						"error":    "passkey_required",
						"hostname": client.Hostname,
						"platform": client.Platform,
						"version":  version,
						"locked":   false,
					}, write)
					return
				}
			}
		}
	}

	log.Printf("tunnel %s: %s (user=%s role=%s)", req.RequestID, inner.Type, req.SenderUserID, req.SenderOrgRole)

	switch inner.Type {
	case "dir.list":
		userPaths := pathsForRequest(wingCfg.Paths, req.SenderEmail, req.SenderOrgRole, home)
		entries := requestDirEntries(req, inner.Path, userPaths)
		tunnelRespond(gcm, req.RequestID, map[string]any{"entries": entries}, write)

	case "wing.info":
		userPaths := pathsForRequest(wingCfg.Paths, req.SenderEmail, req.SenderOrgRole, home)
		projects := requestProjects(req, client.Projects, userPaths)
		resp := map[string]any{
			"hostname":      client.Hostname,
			"platform":      client.Platform,
			"version":       version,
			"agents":        client.Agents,
			"projects":      projects,
			"locked":        wingCfg.Locked,
			"spectate":      wingCfg.Spectate,
			"allowed_count": len(wingCfg.AllowKeys),
			"hosted_relay":  wingCfg.EffectiveHostedRelay(),
		}
		if wingCfg.Label != "" {
			resp["wing_label"] = wingCfg.Label
		}
		// Browser PTY migration remains opt-in even though the same PeerManager is
		// available in relay mode for native direct MCP control.
		if peerMgr != nil && (wingCfg.ConnectionMode == "p2p" || wingCfg.ConnectionMode == "p2p_only") {
			resp["p2p"] = true
			resp["connection_mode"] = wingCfg.ConnectionMode
		}
		if peerMgr != nil && len(wingCfg.ICEServers) > 0 {
			resp["ice_servers"] = wingCfg.ICEServers
		}
		// Report which well-known API keys are set in the wing's environment
		var globalKeys []string
		for _, k := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "CURSOR_API_KEY"} {
			if os.Getenv(k) != "" {
				globalKeys = append(globalKeys, k)
			}
		}
		if len(globalKeys) > 0 && !isMemberFiltered(req) {
			resp["global_keys"] = globalKeys
		}
		if req.SenderUserID != "" {
			enrolled := false
			for _, ak := range allowedKeys {
				if ak.UserID == req.SenderUserID && ak.Key != "" {
					enrolled = true
					break
				}
			}
			if enrolled {
				resp["passkey_enrolled"] = true
			}
		}
		tunnelRespond(gcm, req.RequestID, resp, write)

	case "webrtc.offer":
		if peerMgr == nil {
			tunnelRespond(gcm, req.RequestID, map[string]any{"error": "p2p not enabled"}, write)
			return
		}
		if inner.SDP == "" {
			tunnelRespond(gcm, req.RequestID, map[string]any{"error": "missing sdp"}, write)
			return
		}
		answerSDP, err := peerMgr.HandleOffer(req.SenderPub, req.SenderUserID, req.SenderEmail, req.SenderOrgRole, req.SenderPasskeys, inner.SDP)
		if err != nil {
			log.Printf("[P2P] webrtc.offer failed: %v", err)
			tunnelRespond(gcm, req.RequestID, map[string]any{"error": fmt.Sprintf("webrtc offer: %v", err)}, write)
			return
		}
		log.Printf("[P2P] webrtc.offer accepted from %s, answer SDP %d bytes", shortLogValue(req.SenderPub), len(answerSDP))
		tunnelRespond(gcm, req.RequestID, map[string]any{"sdp": answerSDP}, write)

	case "sessions.list":
		sessions := listAliveEggSessions(cfg)
		if isMemberFiltered(req) {
			userPaths := pathsForRequest(wingCfg.Paths, req.SenderEmail, req.SenderOrgRole, home)
			var filtered []ws.SessionInfo
			for _, s := range sessions {
				if canSeeSession(req, s.UserID) && canAccessSessionPath(req, s.CWD, userPaths) {
					filtered = append(filtered, s)
				}
			}
			sessions = filtered
		}
		tunnelRespond(gcm, req.RequestID, map[string]any{"sessions": sessions}, write)

	case "sessions.history":
		sessions := collectSessionsHistory(cfg)
		if isMemberFiltered(req) {
			userPaths := pathsForRequest(wingCfg.Paths, req.SenderEmail, req.SenderOrgRole, home)
			sessions = filterSessionsHistoryForRequest(req, sessions, userPaths)
		}
		sessions, total := paginateSessionsHistory(sessions, inner.Offset, inner.Limit)
		tunnelRespond(gcm, req.RequestID, map[string]any{"sessions": sessions, "total": total}, write)

	case "audit.request":
		if inner.SessionID != "" && isMemberFiltered(req) {
			sessionDir := filepath.Join(cfg.Dir, "eggs", inner.SessionID)
			userPaths := pathsForRequest(wingCfg.Paths, req.SenderEmail, req.SenderOrgRole, home)
			if !canAccessSessionArtifact(req, sessionDir, userPaths) {
				log.Printf("tunnel %s: denied audit outside current owner/path policy (user=%s session=%s)", req.RequestID, req.SenderUserID, inner.SessionID)
				tunnelRespond(gcm, req.RequestID, map[string]string{"error": "access denied"}, write)
				return
			}
		}
		streamAuditData(cfg, inner.SessionID, inner.Kind, gcm, req.RequestID, write)

	case "egg.config_update":
		// Rewrites the egg policy every session on this host runs under —
		// wing-wide administration, not a per-path member capability.
		if isMemberFiltered(req) {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "admin required"}, write)
			return
		}
		if inner.YAML == "" {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing yaml"}, write)
			return
		}
		newCfg, err := egg.LoadEggConfigFromYAML(inner.YAML)
		if err != nil {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		wingEggMu.Lock()
		*wingEggCfg = newCfg
		wingEggMu.Unlock()
		log.Printf("egg: config updated from tunnel (network=%s)", newCfg.NetworkSummary())
		tunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	case "pty.kill":
		if inner.SessionID == "" {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing session_id"}, write)
			return
		}
		if isMemberFiltered(req) {
			owner := readEggOwner(filepath.Join(cfg.Dir, "eggs", inner.SessionID))
			if !canSeeSession(req, owner) {
				log.Printf("tunnel %s: denied kill (user=%s session_owner=%s)", req.RequestID, req.SenderUserID, owner)
				tunnelRespond(gcm, req.RequestID, map[string]string{"error": "access denied"}, write)
				return
			}
		}
		killOrphanEgg(cfg, inner.SessionID)
		tunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	case "pty.resize":
		if inner.SessionID == "" || inner.Rows <= 0 || inner.Cols <= 0 {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing session_id or dimensions"}, write)
			return
		}
		if isMemberFiltered(req) {
			owner := readEggOwner(filepath.Join(cfg.Dir, "eggs", inner.SessionID))
			if !canSeeSession(req, owner) {
				tunnelRespond(gcm, req.RequestID, map[string]string{"error": "access denied"}, write)
				return
			}
		}
		if err := resizeEgg(cfg, inner.SessionID, uint32(inner.Rows), uint32(inner.Cols)); err != nil {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		tunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	case "wing.update":
		// Replaces the host executable — wing-wide administration.
		if isMemberFiltered(req) {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "admin required"}, write)
			return
		}
		log.Println("tunnel: remote update requested")
		exe, exeErr := os.Executable()
		if exeErr != nil {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": exeErr.Error()}, write)
			return
		}
		c := exec.Command(exe, "update")
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		tunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	case "passkey.auth.begin":
		if subject == "" {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "authenticated client identity required"}, write)
			return
		}
		if len(passkeysForSubject(allowedKeys, req.SenderUserID)) == 0 {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "not_allowed"}, write)
			return
		}
		challengeID, challenge, err := passkeyChallenges.Put(subject, time.Minute)
		if err != nil {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "challenge generation failed"}, write)
			return
		}
		tunnelRespond(gcm, req.RequestID, map[string]string{
			"challenge_id": challengeID,
			"challenge":    base64.RawURLEncoding.EncodeToString(challenge),
			"rp_id":        passkeyPolicy.RPID,
		}, write)

	case "passkey.auth.finish":
		if subject == "" || inner.ChallengeID == "" {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing passkey challenge"}, write)
			return
		}
		challenge, ok := passkeyChallenges.Consume(inner.ChallengeID, subject)
		if !ok {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "invalid or expired passkey challenge"}, write)
			return
		}
		if _, err := base64.RawURLEncoding.DecodeString(inner.CredentialID); err != nil {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "invalid credential ID"}, write)
			return
		}
		authData, err := base64.StdEncoding.DecodeString(inner.AuthenticatorData)
		if err != nil {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "invalid authenticator data"}, write)
			return
		}
		cdJSON, err := base64.StdEncoding.DecodeString(inner.ClientDataJSON)
		if err != nil {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "invalid client data"}, write)
			return
		}
		sig, err := base64.StdEncoding.DecodeString(inner.Signature)
		if err != nil {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "invalid signature encoding"}, write)
			return
		}

		matchedKey, err := verifySubjectPasskey(allowedKeys, req.SenderUserID, challenge, authData, cdJSON, sig, passkeyPolicy)
		if err != nil {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "passkey verification failed"}, write)
			return
		}
		token, err := auth.GenerateAuthToken()
		if err != nil {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "auth token generation failed"}, write)
			return
		}
		passkeyCache.Put(token, matchedKey, subject)
		tunnelRespond(gcm, req.RequestID, map[string]string{"auth_token": token}, write)

	case "allow.list":
		type allowInfo struct {
			Key    string `json:"key"`
			UserID string `json:"user_id,omitempty"`
			Email  string `json:"email,omitempty"`
		}
		var allowed []allowInfo
		for _, ak := range visibleAllowKeys(req, allowedKeys) {
			allowed = append(allowed, allowInfo{Key: ak.Key, UserID: ak.UserID, Email: ak.Email})
		}
		tunnelRespond(gcm, req.RequestID, map[string]any{"allowed": allowed}, write)

	case "allow.add":
		if req.SenderUserID == "" {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "no user identity"}, write)
			return
		}
		// Validate key if provided
		if inner.Key != "" {
			keyBytes, decErr := base64.StdEncoding.DecodeString(inner.Key)
			if decErr != nil || !auth.IsValidP256Point(keyBytes) {
				tunnelRespond(gcm, req.RequestID, map[string]string{"error": "invalid key"}, write)
				return
			}
		}
		newEntry := config.AllowKey{
			Key:    inner.Key,
			UserID: req.SenderUserID,
			Email:  req.SenderEmail,
		}
		// In-memory only — don't persist to wing.yaml. Persisting here
		// clobbers shared wings (sets locked: true + allow_keys with only
		// the enrolling user, locking everyone else out).
		// Admins manage allow_keys explicitly via `wt wing allow`.
		wingCfgMu.Lock()
		if liveWingCfg.Locked {
			wingCfgMu.Unlock()
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "locked wings require local approval via wt wing allow"}, write)
			return
		}
		for _, ak := range *allowedKeysPtr {
			if ak.UserID == req.SenderUserID {
				wingCfgMu.Unlock()
				tunnelRespond(gcm, req.RequestID, map[string]string{"error": "already allowed"}, write)
				return
			}
		}
		*allowedKeysPtr = append(*allowedKeysPtr, newEntry)
		wingCfgMu.Unlock()
		log.Printf("allowed: user=%s email=%s has_passkey=%v (session-scoped)", req.SenderUserID, req.SenderEmail, inner.Key != "")
		tunnelRespond(gcm, req.RequestID, map[string]any{
			"ok": "true", "email": req.SenderEmail, "user_id": req.SenderUserID,
			"has_passkey": inner.Key != "",
		}, write)

	case "allow.remove":
		if req.SenderUserID == "" {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "no user identity"}, write)
			return
		}
		wingCfgMu.Lock()
		liveReq := requestAgainstWingConfig(req, authenticatedOrgRole, liveWingCfg)
		allowedKeys = append([]config.AllowKey(nil), (*allowedKeysPtr)...)
		// Find entry to remove: by key or user_id against the live ACL. A
		// SIGHUP between admission and this mutation must not resurrect an old
		// snapshot or authorize a removed local admin.
		target := inner.AllowUserID
		if target == "" && inner.Key != "" {
			for _, ak := range allowedKeys {
				if ak.Key == inner.Key {
					target = ak.UserID
					break
				}
			}
		}
		if target == "" {
			wingCfgMu.Unlock()
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing allow_user_id or key"}, write)
			return
		}
		// Only wing owner or the entry's own user can remove
		isOwner := liveReq.SenderOrgRole == "owner" || liveReq.SenderOrgRole == "admin"
		if !isOwner && req.SenderUserID != target {
			wingCfgMu.Unlock()
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "access denied"}, write)
			return
		}
		// Remove from persisted config (if present). Revocation is an ACL
		// mutation: serialize with the other wing.yaml writers, and roll the
		// in-memory change back if persistence fails so a request reported as
		// failed cannot leave a live-but-unsaved authorization change.
		persistedRemoved := false
		oldAllowKeys := append([]config.AllowKey(nil), liveWingCfg.AllowKeys...)
		for i, ak := range liveWingCfg.AllowKeys {
			if ak.UserID == target || (inner.Key != "" && ak.Key == inner.Key) {
				liveWingCfg.AllowKeys = append(liveWingCfg.AllowKeys[:i], liveWingCfg.AllowKeys[i+1:]...)
				persistedRemoved = true
				break
			}
		}
		if persistedRemoved {
			if saveErr := config.SaveWingConfig(cfg.Dir, liveWingCfg); saveErr != nil {
				liveWingCfg.AllowKeys = oldAllowKeys
				wingCfgMu.Unlock()
				tunnelRespond(gcm, req.RequestID, map[string]string{"error": "persist wing.yaml: " + saveErr.Error()}, write)
				return
			}
		}
		// Also remove from in-memory list (covers session-scoped entries)
		memRemoved := false
		for i, ak := range allowedKeys {
			if ak.UserID == target || (inner.Key != "" && ak.Key == inner.Key) {
				allowedKeys = append(allowedKeys[:i], allowedKeys[i+1:]...)
				memRemoved = true
				break
			}
		}
		if !persistedRemoved && !memRemoved {
			wingCfgMu.Unlock()
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "entry not found"}, write)
			return
		}
		*allowedKeysPtr = allowedKeys
		locked, allowedCount := liveWingCfg.Locked, len(liveWingCfg.AllowKeys)
		wingCfgMu.Unlock()
		client.UpdateAccessConfig(locked, allowedCount)
		if err := client.SendConfig(ctx); err != nil {
			// The local policy mutation is already active and persisted. A later
			// reconnect will advertise it again, so report the transient sync
			// failure without rolling back the security decision.
			log.Printf("advertise access config after revoke: %v", err)
		}
		log.Printf("revoked: target=%s by=%s persisted=%v", target, req.SenderUserID, persistedRemoved)
		tunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	case "paths.list":
		if !isMemberFiltered(req) {
			// Admin/owner: return full PathList with members
			tunnelRespond(gcm, req.RequestID, map[string]any{"paths": wingCfg.Paths}, write)
		} else {
			// Member: return only their accessible paths, no member lists
			userPaths := wingCfg.Paths.PathsForUser(req.SenderEmail, req.SenderOrgRole)
			tunnelRespond(gcm, req.RequestID, map[string]any{"paths": userPaths}, write)
		}

	case "paths.set":
		if inner.Paths == nil {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing paths"}, write)
			return
		}
		wingCfgMu.Lock()
		if isMemberFiltered(requestAgainstWingConfig(req, authenticatedOrgRole, liveWingCfg)) {
			wingCfgMu.Unlock()
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "admin required"}, write)
			return
		}
		oldPaths, oldRoot := liveWingCfg.Paths, liveWingCfg.Root
		liveWingCfg.Paths = clonePathList(config.PathList(inner.Paths))
		liveWingCfg.Root = ""
		if saveErr := config.SaveWingConfig(cfg.Dir, liveWingCfg); saveErr != nil {
			// Roll back so a request reported as failed does not keep steering
			// live authorization until restart.
			liveWingCfg.Paths, liveWingCfg.Root = oldPaths, oldRoot
			wingCfgMu.Unlock()
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "persist wing.yaml: " + saveErr.Error()}, write)
			return
		}
		paths := clonePathList(liveWingCfg.Paths)
		wingCfgMu.Unlock()
		log.Printf("paths.set: %d entries by %s", len(paths), req.SenderUserID)
		go killSessionsViolatingACLs(cfg, paths, home)
		tunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	case "paths.add_member":
		if inner.Path == "" || inner.Email == "" {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing path or email"}, write)
			return
		}
		wingCfgMu.Lock()
		if isMemberFiltered(requestAgainstWingConfig(req, authenticatedOrgRole, liveWingCfg)) {
			wingCfgMu.Unlock()
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "admin required"}, write)
			return
		}
		found := false
		emailLower := strings.ToLower(inner.Email)
		oldPaths := clonePathList(liveWingCfg.Paths)
		for i, e := range liveWingCfg.Paths {
			if e.Path == inner.Path {
				// Check duplicate
				dup := false
				for _, m := range e.Members {
					if strings.ToLower(m) == emailLower {
						dup = true
						break
					}
				}
				if !dup {
					liveWingCfg.Paths[i].Members = append(liveWingCfg.Paths[i].Members, inner.Email)
				}
				found = true
				break
			}
		}
		if !found {
			wingCfgMu.Unlock()
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "path not found"}, write)
			return
		}
		if saveErr := config.SaveWingConfig(cfg.Dir, liveWingCfg); saveErr != nil {
			liveWingCfg.Paths = oldPaths
			wingCfgMu.Unlock()
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "persist wing.yaml: " + saveErr.Error()}, write)
			return
		}
		wingCfgMu.Unlock()
		log.Printf("paths.add_member: %s to %s by %s", inner.Email, inner.Path, req.SenderUserID)
		tunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	case "paths.remove_member":
		if inner.Path == "" || inner.Email == "" {
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing path or email"}, write)
			return
		}
		wingCfgMu.Lock()
		if isMemberFiltered(requestAgainstWingConfig(req, authenticatedOrgRole, liveWingCfg)) {
			wingCfgMu.Unlock()
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "admin required"}, write)
			return
		}
		found := false
		emailLower := strings.ToLower(inner.Email)
		oldPaths := clonePathList(liveWingCfg.Paths)
		for i, e := range liveWingCfg.Paths {
			if e.Path == inner.Path {
				// An empty member list means a legacy open entry visible to every
				// member, so removing the last member would silently make the
				// path public instead of revoking access. Fail closed.
				if len(e.Members) == 1 && strings.ToLower(e.Members[0]) == emailLower {
					wingCfgMu.Unlock()
					tunnelRespond(gcm, req.RequestID, map[string]string{"error": "cannot remove the last member — an empty list opens the path to everyone; remove the path entry instead"}, write)
					return
				}
				for j, m := range e.Members {
					if strings.ToLower(m) == emailLower {
						liveWingCfg.Paths[i].Members = append(e.Members[:j], e.Members[j+1:]...)
						found = true
						break
					}
				}
				break
			}
		}
		if !found {
			wingCfgMu.Unlock()
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "path or member not found"}, write)
			return
		}
		if saveErr := config.SaveWingConfig(cfg.Dir, liveWingCfg); saveErr != nil {
			liveWingCfg.Paths = oldPaths
			wingCfgMu.Unlock()
			tunnelRespond(gcm, req.RequestID, map[string]string{"error": "persist wing.yaml: " + saveErr.Error()}, write)
			return
		}
		paths := clonePathList(liveWingCfg.Paths)
		wingCfgMu.Unlock()
		log.Printf("paths.remove_member: %s from %s by %s", inner.Email, inner.Path, req.SenderUserID)
		go killSessionsViolatingACLs(cfg, paths, home)
		tunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	default:
		tunnelRespond(gcm, req.RequestID, map[string]string{"error": "unknown type: " + inner.Type}, write)
	}
}

// collectSessionsHistory returns all dead egg sessions from disk newest first.
// Authorization must be applied before pagination so member pages and totals do
// not depend on the positions of sessions they cannot see.
func collectSessionsHistory(cfg *config.Config) []pastSessionInfo {
	eggsDir := filepath.Join(cfg.Dir, "eggs")
	entries, err := os.ReadDir(eggsDir)
	if err != nil {
		return nil
	}

	var dead []pastSessionInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionID := e.Name()
		dir := filepath.Join(eggsDir, sessionID)

		// Check if process is alive -- skip alive sessions
		pidData, err := os.ReadFile(filepath.Join(dir, "egg.pid"))
		if err == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(pidData)))
			if ownedProcessIsAlive(pid) {
				continue
			}
		}

		agentName, cwd := readEggMeta(dir)
		hasAudit := false
		if _, err := os.Stat(filepath.Join(dir, "audit.pty.gz")); err == nil {
			hasAudit = true
		}
		hasChat := false
		if _, err := os.Stat(filepath.Join(dir, "chat.jsonl.gz")); err == nil {
			hasChat = true
		}
		if agentName == "" && !hasAudit && !hasChat {
			continue
		}
		if agentName == "" {
			agentName = "unknown"
		}

		info := pastSessionInfo{
			SessionID: sessionID,
			Agent:     agentName,
			CWD:       cwd,
			Audit:     hasAudit,
			Chat:      hasChat,
			UserID:    readEggOwner(dir),
		}
		if stat, err := os.Stat(dir); err == nil {
			info.StartedAt = stat.ModTime().Unix()
		}
		dead = append(dead, info)
	}

	sort.Slice(dead, func(i, j int) bool {
		return dead[i].StartedAt > dead[j].StartedAt
	})
	return dead
}

func filterSessionsHistoryForRequest(req ws.TunnelRequest, sessions []pastSessionInfo, userPaths []string) []pastSessionInfo {
	if !isMemberFiltered(req) {
		return sessions
	}
	filtered := make([]pastSessionInfo, 0, len(sessions))
	for _, session := range sessions {
		if canSeeSession(req, session.UserID) && canAccessSessionPath(req, session.CWD, userPaths) {
			filtered = append(filtered, session)
		}
	}
	return filtered
}

const (
	defaultSessionsHistoryLimit = 20
	maxSessionsHistoryLimit     = 200
)

func paginateSessionsHistory(sessions []pastSessionInfo, offset, limit int) ([]pastSessionInfo, int) {
	total := len(sessions)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	if limit <= 0 {
		limit = defaultSessionsHistoryLimit
	}
	if limit > maxSessionsHistoryLimit {
		limit = maxSessionsHistoryLimit
	}
	remaining := total - offset
	if limit > remaining {
		limit = remaining
	}
	return sessions[offset : offset+limit], total
}

const (
	auditStreamChunkBytes = 32 << 10
	maxAuditFrameBytes    = 64 << 10
	maxAuditMetadataBytes = 64 << 10
)

func streamAuditFile(file io.Reader, base64Encode bool, emit func([]byte) error) error {
	buffer := make([]byte, auditStreamChunkBytes)
	for {
		count, readErr := file.Read(buffer)
		if count > 0 {
			value := string(buffer[:count])
			if base64Encode {
				value = base64.StdEncoding.EncodeToString(buffer[:count])
			}
			chunk, marshalErr := json.Marshal(map[string]string{"data": value})
			if marshalErr != nil {
				return marshalErr
			}
			if err := emit(chunk); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func auditDimensions(dir string) (int, int) {
	cols, rows := 120, 40
	file, err := os.Open(filepath.Join(dir, "egg.meta"))
	if err != nil {
		return cols, rows
	}
	defer closeWithLog("audit metadata", file)
	meta, err := io.ReadAll(io.LimitReader(file, maxAuditMetadataBytes+1))
	if err != nil || len(meta) > maxAuditMetadataBytes {
		return cols, rows
	}
	for _, line := range strings.Split(string(meta), "\n") {
		if strings.HasPrefix(line, "cols=") {
			if value, parseErr := strconv.Atoi(strings.TrimPrefix(line, "cols=")); parseErr == nil && value > 0 && value <= 65535 {
				cols = value
			}
		}
		if strings.HasPrefix(line, "rows=") {
			if value, parseErr := strconv.Atoi(strings.TrimPrefix(line, "rows=")); parseErr == nil && value > 0 && value <= 65535 {
				rows = value
			}
		}
	}
	return cols, rows
}

func incompleteAuditRead(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func auditStreamErrorPayload(message string) []byte {
	payload, err := json.Marshal(map[string]string{"error": message})
	if err != nil {
		return []byte(`{"error":"audit stream failed"}`)
	}
	return payload
}

// streamPTYAudit converts a decompressed V1/V2 recording incrementally. It
// never retains the whole recording or trusts a frame length before enforcing
// the per-frame cap.
func streamPTYAudit(reader io.Reader, fallbackCols, fallbackRows int, emit func([]byte) error) error {
	buffered := bufio.NewReaderSize(reader, auditStreamChunkBytes)
	isV2 := false
	if header, err := buffered.Peek(4); err == nil && bytes.Equal(header, []byte("WTA2")) {
		if _, err := buffered.Discard(4); err != nil {
			return err
		}
		isV2 = true
		cols, err := binary.ReadUvarint(buffered)
		if err != nil {
			return fmt.Errorf("read audit columns: %w", err)
		}
		rows, err := binary.ReadUvarint(buffered)
		if err != nil {
			return fmt.Errorf("read audit rows: %w", err)
		}
		if cols == 0 || cols > 65535 || rows == 0 || rows > 65535 {
			return fmt.Errorf("invalid audit dimensions %dx%d", cols, rows)
		}
		fallbackCols, fallbackRows = int(cols), int(rows)
	}
	header := []byte(fmt.Sprintf(`{"version":2,"width":%d,"height":%d}`, fallbackCols, fallbackRows))
	if err := emit(header); err != nil {
		return err
	}

	var cumulativeMS uint64
	for {
		deltaMS, err := binary.ReadUvarint(buffered)
		if incompleteAuditRead(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read audit timestamp: %w", err)
		}
		frameType := uint64(0)
		if isV2 {
			frameType, err = binary.ReadUvarint(buffered)
			if incompleteAuditRead(err) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("read audit frame type: %w", err)
			}
		}
		dataLen, err := binary.ReadUvarint(buffered)
		if incompleteAuditRead(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read audit frame length: %w", err)
		}
		if dataLen > maxAuditFrameBytes {
			return fmt.Errorf("audit frame is %d bytes; maximum is %d", dataLen, maxAuditFrameBytes)
		}
		frame := make([]byte, int(dataLen))
		if _, err := io.ReadFull(buffered, frame); incompleteAuditRead(err) {
			return nil
		} else if err != nil {
			return fmt.Errorf("read audit frame: %w", err)
		}
		if ^uint64(0)-cumulativeMS < deltaMS {
			return errors.New("audit timestamp overflow")
		}
		cumulativeMS += deltaMS

		var line []byte
		if frameType == 1 {
			resize := bytes.NewReader(frame)
			cols, colErr := binary.ReadUvarint(resize)
			rows, rowErr := binary.ReadUvarint(resize)
			if colErr != nil || rowErr != nil || cols == 0 || cols > 65535 || rows == 0 || rows > 65535 {
				continue
			}
			line = []byte(fmt.Sprintf("[%.3f,\"r\",\"%dx%d\"]", float64(cumulativeMS)/1000, cols, rows))
		} else {
			encoded := base64.StdEncoding.EncodeToString(frame)
			line = []byte(fmt.Sprintf("[%.3f,\"o\",\"%s\"]", float64(cumulativeMS)/1000, encoded))
		}
		if err := emit(line); err != nil {
			return err
		}
	}
}

// streamAuditData reads audit data from disk and streams encrypted chunks via tunnel.stream.
func streamAuditData(cfg *config.Config, sessionID, kind string, gcm cipher.AEAD, requestID string, write ws.PTYWriteFunc) {
	if !ws.ValidSessionID(sessionID) {
		tunnelRespond(gcm, requestID, map[string]string{"error": "invalid session ID"}, write)
		return
	}
	dir := filepath.Join(cfg.Dir, "eggs", sessionID)

	var filePath string
	switch kind {
	case "keylog":
		filePath = filepath.Join(dir, "audit.log")
	case "chat":
		filePath = filepath.Join(dir, "chat.jsonl.gz")
	default:
		filePath = filepath.Join(dir, "audit.pty.gz")
	}

	file, err := os.Open(filePath)
	if err != nil {
		tunnelRespond(gcm, requestID, map[string]string{"error": "file not found: " + kind}, write)
		return
	}
	defer closeWithLog("audit stream", file)
	emit := func(chunk []byte) error {
		return tunnelStreamChunk(gcm, requestID, chunk, false, write)
	}

	if kind == "chat" {
		if err := streamAuditFile(file, true, emit); err != nil {
			_ = tunnelStreamChunk(gcm, requestID, auditStreamErrorPayload("read chat audit: "+err.Error()), true, write)
			return
		}
		_ = tunnelStreamChunk(gcm, requestID, []byte(`{"done":true}`), true, write)
		return
	}

	if kind != "pty" {
		if err := streamAuditFile(file, false, emit); err != nil {
			_ = tunnelStreamChunk(gcm, requestID, auditStreamErrorPayload("read keylog audit: "+err.Error()), true, write)
			return
		}
		_ = tunnelStreamChunk(gcm, requestID, []byte(`{"done":true}`), true, write)
		return
	}

	// Decompress gzip and stream as asciinema v2 NDJSON. The parser tolerates
	// an incomplete trailing frame from a live writer.
	gr, gzErr := gzip.NewReader(file)
	if gzErr != nil {
		tunnelRespond(gcm, requestID, map[string]string{"error": "decompress: " + gzErr.Error()}, write)
		return
	}
	cols, rows := auditDimensions(dir)
	streamErr := streamPTYAudit(gr, cols, rows, emit)
	if closeErr := gr.Close(); closeErr != nil && streamErr == nil {
		streamErr = closeErr
	}
	if streamErr != nil {
		_ = tunnelStreamChunk(gcm, requestID, auditStreamErrorPayload("read PTY audit: "+streamErr.Error()), true, write)
		return
	}
	_ = tunnelStreamChunk(gcm, requestID, []byte(`{"done":true}`), true, write)
}
