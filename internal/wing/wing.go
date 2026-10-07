package wing

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/cipher"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	agentpkg "github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	directpkg "github.com/ehrlich-b/wingthing/internal/direct"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/procinfo"
	relaypkg "github.com/ehrlich-b/wingthing/internal/relay"
	"github.com/ehrlich-b/wingthing/internal/tunnel"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/fsnotify/fsnotify"
	pionwebrtc "github.com/pion/webrtc/v4"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
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
	defer cmdutil.CloseWithLog("preview file", file)
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
			if removeErr := cmdutil.RemoveIfExists(path); removeErr != nil {
				log.Printf("pty session %s: remove rejected preview: %v", sessionID, removeErr)
			}
		}
		return
	}
	jsonBytes, err := marshalPreviewFile(data)
	if err != nil {
		if errors.Is(err, errPreviewTooLarge) {
			log.Printf("pty session %s: discard preview: %v", sessionID, err)
			if removeErr := cmdutil.RemoveIfExists(path); removeErr != nil {
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
	if err := cmdutil.RemoveIfExists(path); err != nil {
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
		defer cmdutil.CloseWithLog("preview watcher", watcher)
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

func browserRequestOffset(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// watchBrowserRequests polls for new lines in the browser-requests file and forwards them as PTYBrowserOpen messages.
func watchBrowserRequests(ctx context.Context, path, sessionID string, lastOffset int64, write ws.PTYWriteFunc) {
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
				cmdutil.CloseWithLog("browser request file", f)
				continue
			}
			if info.Size() < lastOffset {
				lastOffset = 0
				pending = ""
				discarding = false
			}
			if info.Size() == lastOffset {
				cmdutil.CloseWithLog("browser request file", f)
				continue
			}
			if unread := info.Size() - lastOffset; unread > maxBrowserRequestReadBytes {
				lastOffset = info.Size() - maxBrowserRequestReadBytes
				pending = ""
				discarding = true // the retained window may begin in the middle of a line
			}
			if _, err := f.Seek(lastOffset, io.SeekStart); err != nil {
				cmdutil.CloseWithLog("browser request file", f)
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
				ws.WritePTYMessage(write, ws.PTYBrowserOpen{Type: ws.TypePTYBrowserOpen, SessionID: sessionID, URL: line})
			})
		case <-ctx.Done():
			return
		}
	}
}

// wingCfgMu serializes tunnel-driven wing.yaml mutations. Tunnel requests run
// on concurrent goroutines; unsynchronized admin edits could race each other
// into a corrupt config.
var wingCfgMu sync.Mutex

// killSessionsViolatingACLs checks active sessions and kills any that no longer
// have access under the current path ACLs.
func killSessionsViolatingACLs(cfg *config.Config, paths config.PathList, home string) {
	sessions := ListAliveEggSessions(cfg)
	for _, s := range sessions {
		dir := filepath.Join(cfg.Dir, "eggs", s.SessionID)
		email := eggclient.ReadEggOwnerEmail(dir)
		if email == "" {
			continue // pre-ACL session or admin — leave it
		}
		// Re-check if this user still has access to the session's CWD
		userPaths := wingpolicy.ResolvePathStrings(paths.PathsForUser(email, "member"), home)
		if len(userPaths) == 0 || !wingpolicy.IsUnderPaths(s.CWD, userPaths) {
			log.Printf("ACL revoke: killing session %s (user=%s cwd=%s)", s.SessionID, email, s.CWD)
			eggclient.KillOrphanEgg(cfg, s.SessionID)
		}
	}
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
		ws.WritePTYMessage(write, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sessionID, Data: encrypted, ViewerID: viewerID})
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
		ws.WritePTYMessage(write, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sessionID, Data: encrypted, ViewerID: viewerID})
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
		ws.WritePTYMessage(write, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sessionID, Data: encrypted, Compressed: isCompressed, ViewerID: viewerID})
		totalCompressed += len(compressed)
		sent = end
		chunks++
	}
	log.Printf("pty session %s: replayed %d bytes (gzip %d, %d chunks)", sessionID, len(raw), totalCompressed, chunks)
}

func sendReplayChunked(sessionID string, raw []byte, gcm cipher.AEAD, write ws.PTYWriteFunc) {
	sendReplayChunkedTagged(sessionID, "", raw, gcm, write)
}

func RunWingWithContext(options EntryOptions, ctx context.Context, sighupCh <-chan os.Signal, roostFlag, labelsFlag, convFlag, eggConfigFlag, orgFlag string, allowFlags []string, pathsFlag string, debug, audit, local, vte, sharedHost bool, tokenOverride *auth.DeviceToken) error {
	version := options.Version
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Load wing.yaml
	wingCfg, err := loadWingConfigForStart(cfg.Dir)
	if err != nil {
		return err
	}

	var releaseContext func()
	wingCfg.Context, releaseContext = config.FreezeContextConfig(cfg.Dir, wingCfg.Context)
	defer releaseContext()

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
	if options.SetPolicySource != nil {
		options.SetPolicySource(func() (*config.WingConfig, *egg.EggConfig) {
			wingCfgMu.Lock()
			defer wingCfgMu.Unlock()
			wingEggMu.Lock()
			defer wingEggMu.Unlock()
			return wingCfg.Clone(), wingEggCfg
		})
	}

	// Load privileged tool configs
	toolsDir := config.ResolveToolsDir(cfg.Dir, wingCfg.ToolsDir)
	wingTools, toolErr := config.LoadWingTools(toolsDir, wingCfg.Context)
	if toolErr != nil {
		log.Printf("wing: load tools: %v (continuing without tools)", toolErr)
	} else if len(wingTools) > 0 {
		log.Printf("wing: loaded %d tool(s) from %s", len(wingTools), toolsDir)
	}
	var wingToolsMu sync.Mutex

	// Resolve roost URL
	roostURL := roostFlag
	if local && roostURL == "" {
		roostURL = config.DefaultLocalRelayURL()
	}
	if roostURL == "" {
		roostURL = cfg.RoostURL
	}
	if roostURL == "" {
		roostURL = config.DefaultRelayURL()
	}
	var passkeyPolicyLive atomic.Value
	passkeyPolicyLive.Store(wingpolicy.PasskeyPolicyForRoost(wingpolicy.PasskeyRPURL(roostURL, os.Getenv("WT_BASE_URL"))))
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
	resolvedPaths := wingpolicy.ResolvePathStrings(cliPaths, home)
	rootDir := home
	if len(resolvedPaths) > 0 {
		rootDir = resolvedPaths[0]
	}

	// Scan only within explicitly configured paths. When no paths are configured,
	// preserve the legacy cwd discovery behavior. A detached daemon starts in the
	// user's home directory, so adding cwd to an explicit --paths scan would
	// disclose unrelated project names and paths to the coordinator.
	cwd, _ := os.Getwd()
	projects := wingpolicy.DiscoverWingProjects(resolvedPaths, cwd)

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
	fmt.Printf("open %s to start a terminal\n", wingpolicy.RoostBrowserURL(roostURL))

	// Reap dead egg directories on startup
	eggclient.ReapDeadEggs(cfg)

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
	directMCPAdmission := localmcp.NewMCPAdmissionState()

	var client *ws.Client // declared early so peerMgr.OnDC closure can capture it
	var directSrv *directpkg.Server

	// P2P: wire up DataChannel message routing when DCs open
	if peerMgr != nil {
		peerMgr.OnDC(func(senderPub, sessionID string, ident webrtcpkg.PeerIdentity, dc *pionwebrtc.DataChannel) {
			if strings.HasPrefix(dc.Label(), control.DirectChannelPrefix) {
				if ident.UserID == "" {
					log.Printf("[P2P] rejected direct MCP channel from %s: missing authenticated identity", cmdutil.ShortLogValue(senderPub))
					if err := dc.Close(); err != nil {
						log.Printf("[P2P] close rejected direct MCP channel: %v", err)
					}
					return
				}
				localmcp.ServeDirectMCPChannelWithPolicySource(version, cfg, home, sharedHost, directMCPAdmission, ident, dc, func() (*config.WingConfig, []config.AllowKey) {
					wingCfgMu.Lock()
					defer wingCfgMu.Unlock()
					return wingCfg.Clone(), append([]config.AllowKey(nil), allowedKeys...)
				}, func() *egg.EggConfig {
					wingEggMu.Lock()
					defer wingEggMu.Unlock()
					return wingEggCfg
				})
				return
			}
			if sessionID == "" {
				log.Printf("[P2P] DC opened with no session ID from %s", cmdutil.ShortLogValue(senderPub))
				return
			}
			if !ws.ValidSessionID(sessionID) {
				log.Printf("[P2P] rejected DC with invalid session ID %q from %s", sessionID, cmdutil.ShortLogValue(senderPub))
				if err := dc.Close(); err != nil {
					log.Printf("[P2P] close invalid session channel: %v", err)
				}
				return
			}
			// The label is client-controlled; a DataChannel feeds the session's
			// trusted input channel, so only the session owner's peer identity
			// may bind one. Anything else could inject input or kill a session
			// it does not own.
			owner := eggclient.ReadEggOwner(filepath.Join(cfg.Dir, "eggs", sessionID))
			if ident.UserID == "" || owner == "" || ident.UserID != owner {
				log.Printf("[P2P] rejected DC for session %s from %s: sender is not the session owner", sessionID, cmdutil.ShortLogValue(senderPub))
				if err := dc.Close(); err != nil {
					log.Printf("[P2P] close rejected session channel: %v", err)
				}
				return
			}
			boundController, canBind := browserDataChannelBinding(sessionID, senderPub, ident.UserID)
			if !canBind {
				_ = dc.Close()
				return
			}
			dcSessions.Store(sessionID, dc)
			log.Printf("[P2P] DC stored for session %s from %s", sessionID, cmdutil.ShortLogValue(senderPub))

			dc.OnMessage(browserDataChannelInputHandler(&dcSessions, sessionID, dc, boundController, client.PushPTYInput))
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
		if policy, ok := wingpolicy.PasskeyPolicyFromRegistration(msg); ok {
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
		daemonctl.WriteWingStatusForRoost(state, errMsg, roostURL)
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
		wingEggMu.Lock()
		currentEggCfg := wingEggCfg
		wingEggMu.Unlock()
		eggCfg, _, launchErr := eggclient.PrepareBrowserLaunch(sessionWingCfg, &start, home, sharedHost, currentEggCfg)
		if launchErr != nil {
			ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: launchErr.Error()})
			return
		}
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
			handlePTYSession(version, ctx, cfg, sessionWingCfg, start, sw.Write, input, eggCfg, debugLive.Load(), vte, &sessionAllowedKeys, passkeyCache, currentPasskeyPolicy(), authTTL, idleTimeout, sw, &dcSessions, sessionTools, sharedHost)
		} else {
			handlePTYSession(version, ctx, cfg, sessionWingCfg, start, write, input, eggCfg, debugLive.Load(), vte, &sessionAllowedKeys, passkeyCache, currentPasskeyPolicy(), authTTL, idleTimeout, nil, nil, sessionTools, sharedHost)
		}
	}

	client.OnTunnel = func(ctx context.Context, req ws.TunnelRequest, write ws.PTYWriteFunc) {
		tunnel.HandleTunnelRequest(tunnel.References{Version: version, WingCfg: wingCfg, WingCfgMu: &wingCfgMu, AllowedKeys: &allowedKeys, WingEggMu: &wingEggMu, WingEggCfg: &wingEggCfg, ListAliveEggSessions: ListAliveEggSessions, ResizeBrowserInput: resizeBrowserInput, KillSessionsViolatingACLs: killSessionsViolatingACLs, BrowserTools: func() []*config.ToolConfig {
			wingToolsMu.Lock()
			defer wingToolsMu.Unlock()
			return append([]*config.ToolConfig(nil), wingTools...)
		}}, ctx, cfg, req, write, passkeyCache, passkeyChallenges, currentPasskeyPolicy(), privKey, home, auditLive.Load(), debugLive.Load(), client, peerMgr, &dcSessions, sharedHost)
	}

	client.OnOrphanKill = func(ctx context.Context, sessionID string) {
		eggclient.KillOrphanEgg(cfg, sessionID)
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
					if err := egg.CheckLoaderReloadIsolation(cfg.Dir); err != nil {
						log.Printf("reload failed: %v", err)
						continue
					}
					newCfg, err := config.LoadWingConfig(cfg.Dir)
					if err != nil {
						log.Printf("reload failed: %v", err)
						continue
					}
					if err := localmcp.ValidateDirectMCPGrantConfig(newCfg); err != nil {
						log.Printf("reload failed: %v", err)
						continue
					}
					wingCfgMu.Lock()
					config.RetainContextConfig(newCfg, wingCfg.Context)
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
					wingCfg.Exports = newCfg.Exports

					// Hot-reload paths
					wingCfg.Paths = newCfg.Paths
					resolvedPaths = wingpolicy.ResolvePathStrings(newCfg.Paths.Strings(), home)
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
					if newTools, tErr := config.LoadWingTools(newToolsDir, newCfg.Context); tErr == nil {
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

	go localmcp.RunConversationWakeController(version, ctx, cfg, func() (*config.WingConfig, bool) {
		wingCfgMu.Lock()
		copyCfg := *wingCfg
		wingCfgMu.Unlock()
		return &copyCfg, sharedHost
	})

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
						cmdutil.CloseWithLog("idle-check egg client", ec)
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
						cmdutil.CloseWithLog("idle-reaper egg client", ec)
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
		defer cmdutil.CloseWithLog("direct server", directSrv)
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
	if err := localmcp.ValidateDirectMCPGrantConfig(wingCfg); err != nil {
		return nil, fmt.Errorf("load wing.yaml: %w", err)
	}
	return wingCfg, nil
}

func directMCPEnabled(hasPeerManager bool, wingCfg *config.WingConfig) bool {
	return hasPeerManager && wingCfg != nil && (wingCfg.DirectMCP == nil || !wingCfg.DirectMCP.Disabled)
}

// listAliveEggSessions scans ~/.wingthing/eggs/ for alive egg processes.
func ListAliveEggSessions(cfg *config.Config) []ws.SessionInfo {
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
		if !procinfo.OwnedProcessIsAlive(pid) {
			continue
		}

		// Alive — try to dial to confirm it's responsive
		sockPath := filepath.Join(dir, "egg.sock")
		tokenPath := filepath.Join(dir, "egg.token")
		ec, dialErr := egg.Dial(sockPath, tokenPath)
		if dialErr != nil {
			continue
		}
		statusCtx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		statusResponse, statusErr := ec.Status(statusCtx)
		cancel()
		cmdutil.CloseWithLog("egg health-check client", ec)
		var renderedConfig string
		if statusErr == nil {
			renderedConfig = statusResponse.RenderedConfig
		} else {
			// A verified live session process remains visible when the optional
			// Status RPC is transiently slow. A lazy dial plus Unavailable does
			// not prove a live egg, and a recycled PID must never revive stale
			// session metadata.
			if !eggclient.EggPidMatchesSession(pid, sessionID) || grpcstatus.Code(statusErr) == codes.Unavailable {
				continue
			}
			// Keep it visible so attach and ACL revocation still work; file
			// operations fail closed while the rendered policy is unavailable.
			log.Printf("egg: status unavailable for live session %s: %v", sessionID, statusErr)
		}

		agent, sessionCWD := eggclient.ReadEggMeta(dir)
		info := ws.SessionInfo{
			SessionID: sessionID,
			Name:      eggclient.ReadSessionName(dir),
			Agent:     agent,
			CWD:       sessionCWD,
			EggConfig: renderedConfig,
			UserID:    eggclient.ReadEggOwner(dir),
			Email:     eggclient.ReadEggOwnerEmail(dir),
		}
		link := eggclient.SessionConversationLink(cfg, sessionID)
		info.ConversationID, info.RootConversationID, info.ParentConversationID, info.ConversationRole = link.ConversationID, link.RootConversationID, link.ParentConversationID, link.ConversationRole
		info.Lifecycle = eggclient.SessionLifecycleSummary(context.Background(), cfg, sessionID)
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

func (p *pendingReattachAuths) detach(detach ws.PTYDetach) {
	for viewerID, pending := range p.byViewer {
		if (detach.ViewerID != "" && pending.attach.ViewerID == detach.ViewerID) || (detach.ControllerID != "" && pending.attach.ControllerID == detach.ControllerID) {
			delete(p.byViewer, viewerID)
		}
	}
	p.resetTimer()
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
		if !procinfo.OwnedProcessIsAlive(pid) {
			eggclient.CleanEggDir(dir)
			continue
		}

		// If a goroutine is already handling this session (survived the
		// reconnect), skip — don't create a duplicate subscriber or
		// goroutine, which would cause decrypt errors.
		if wsClient.HasPTYSession(sessionID) {
			log.Printf("egg: session %s already tracked, skipping", sessionID)
			continue
		}

		agent, _ := eggclient.ReadEggMeta(dir)
		prepareReclaimedEggIsolation(dir)

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
			cmdutil.CloseWithLog("duplicate reclaimed egg client", ec)
			log.Printf("egg: session %s became active during reclaim, skipping", sessionID)
			continue
		}
		go func(sid string, ec *egg.Client, dir string) {
			defer cleanup()
			defer cmdutil.CloseWithLog("reclaimed egg client", ec)
			handleReclaimedPTY(ctx, cfg, ec, sid, dir, write, input, wingCfg, allowedKeys, passkeyCache, passkeyPolicy, authTTL, tools)
		}(sessionID, ec, dir)
	}
}

// Preserve terminal access while withholding privileged authority from old
// sandboxes. The marker makes the required replacement visible to the owner.
func prepareReclaimedEggIsolation(dir string) bool {
	if !egg.HasCurrentControlIsolation(dir) {
		egg.MarkLegacyEggForReplacement(dir)
		return false
	}
	return true
}

// handleReclaimedPTY sets up I/O routing for a reclaimed (surviving) egg session.
func handleReclaimedPTY(ctx context.Context, cfg *config.Config, ec *egg.Client, sessionID, eggDir string, write ws.PTYWriteFunc, input <-chan []byte, wingCfg *config.WingConfig, allowedKeys []config.AllowKey, passkeyCache *auth.AuthCache, passkeyPolicy auth.PasskeyPolicy, authTTL time.Duration, tools []*config.ToolConfig) {
	reclaimAgent, reclaimCWD := eggclient.ReadEggMeta(eggDir)
	var mu sync.Mutex
	var gcm cipher.AEAD
	var activeStream pb.Egg_SessionClient
	var cancelStream context.CancelFunc
	privKey, privKeyErr := auth.LoadPrivateKey(cfg.Dir)
	if privKeyErr != nil {
		log.Printf("pty session %s: FATAL: load private key: %v (reclaim aborted)", sessionID, privKeyErr)
		ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: sessionID, ExitCode: 1, Error: "E2E encryption required but wing private key missing"})
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
	// via --tool-socket. Recover its capability through the host-only egg RPC;
	// generating a new secret would strand the surviving agent. Only sessions with tools
	// have a .tools dir; skip the rest.
	if len(tools) > 0 && prepareReclaimedEggIsolation(eggDir) {
		toolsDir := filepath.Join(eggDir, ".tools")
		if _, statErr := os.Stat(toolsDir); statErr == nil {
			toolSocketPath := filepath.Join(toolsDir, "tool.sock")
			toolCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			capability, capabilityErr := ec.ReclaimToolCapability(toolCtx)
			cancel()
			if capabilityErr != nil {
				log.Printf("pty session %s: reclaim tool capability unavailable: %v", sessionID, capabilityErr)
			} else if tl, tlErr := egg.NewToolListenerWithCapability(toolSocketPath, tools, capability, egg.ToolContext{Reclaimed: true}); tlErr != nil {
				log.Printf("pty session %s: reclaim tool listener failed: %v", sessionID, tlErr)
			} else {
				log.Printf("pty session %s: reclaim tool listener restarted (%d tools)", sessionID, len(tools))
				defer cmdutil.CloseWithLog("reclaimed egg tool listener", tl)
			}
		}
	}

	// Attach to existing egg session
	streamCtx, sCancel := context.WithCancel(ctx)
	stream, err := ec.AttachSessionWithOptions(streamCtx, sessionID, egg.AttachOptions{ReadOnly: config.Channel() == "preview", Owner: "wing:observer"})
	if err != nil {
		sCancel()
		log.Printf("pty session %s: reclaim attach failed: %v", sessionID, err)
		return
	}
	activeStream = stream
	cancelStream = sCancel
	if config.Channel() == "preview" {
		cancelStream = nil
		defer sCancel()
	}

	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()
	if reclaimCWD != "" {
		go watchPreviewFile(sessionCtx, reclaimCWD, sessionID, &mu, &gcm, write)
	}
	browserRequestsPath := filepath.Join(eggDir, "browser-requests")
	go watchBrowserRequests(sessionCtx, browserRequestsPath, sessionID, browserRequestOffset(browserRequestsPath), write)

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
				if config.Channel() == "preview" {
					continue
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
				ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: sessionID, ExitCode: int(p.ExitCode)})
				clearAttentionCooldown(sessionID)
				sessionCancel()
				return
			}
		}
	}()

	// Process input from browser
	go func() {
		defer releaseBrowserClient(sessionID, ec)
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
					ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "passkey timed out", SessionID: sessionID, ViewerID: pending.attach.ViewerID})
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
				rawKey, verifyErr := wingpolicy.VerifySubjectPasskey(allowedKeys, pending.attach.UserID, pending.challenge, authData, clientData, signature, passkeyPolicy)
				if verifyErr != nil {
					ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "invalid passkey", SessionID: sessionID, ViewerID: pending.attach.ViewerID})
					continue
				}
				token, tokenErr := auth.GenerateAuthToken()
				if tokenErr != nil {
					ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "auth token generation failed", SessionID: sessionID, ViewerID: pending.attach.ViewerID})
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
				if !wingpolicy.CanAttachSession(attach.UserID, attach.OrgRole, eggclient.ReadEggOwner(eggDir)) {
					ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "session not found or not owned by caller", SessionID: sessionID, ViewerID: attach.ViewerID})
					continue
				}
				clearAttentionCooldown(sessionID)
				if attach.PublicKey == "" {
					ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "client encryption key required", SessionID: sessionID, ViewerID: attach.ViewerID})
					continue
				}

				// Passkey auth gate — per-user check
				var attachAuthToken string
				attachSubject := wingpolicy.PasskeySubject(attach.UserID, attach.PublicKey)
				attachUserHasPasskey := len(wingpolicy.PasskeysForSubject(allowedKeys, attach.UserID)) > 0
				if wingCfg.Locked && (attachSubject == "" || !attachUserHasPasskey) {
					ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "not allowed by wing", SessionID: sessionID, ViewerID: attach.ViewerID})
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
						ws.WritePTYMessage(write, ws.PasskeyChallenge{
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
						ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: sessionID, ExitCode: 1, Error: "spectate not enabled", ViewerID: attach.ViewerID})
						continue
					}
					spectatorGCM, deriveErr := auth.DeriveSharedKey(privKey, attach.PublicKey, "wt-pty")
					if deriveErr != nil {
						ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "spectator encryption setup failed", SessionID: sessionID, ViewerID: attach.ViewerID})
						continue
					}
					specCtx, specCancel := context.WithCancel(ctx)
					specStream, specErr := ec.AttachSessionWithOptions(specCtx, sessionID, egg.AttachOptions{ReadOnly: true, Owner: "browser:observer"})
					if specErr != nil {
						specCancel()
						ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "spectator attach failed", SessionID: sessionID, ViewerID: attach.ViewerID})
						continue
					}
					bindBrowserViewer(sessionID, attach.ViewerID, ec, specCancel)
					ws.WritePTYMessage(write, ws.PTYStarted{
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
						defer browserViewers.Delete(sessionID + ":" + viewerID)
						for {
							msg, err := stream.Recv()
							if err != nil {
								return
							}
							switch payload := msg.Payload.(type) {
							case *pb.SessionMsg_Output:
								sendPTYOutputTagged(sessionID, viewerID, payload.Output, g, write)
							case *pb.SessionMsg_ExitCode:
								ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: sessionID, ExitCode: int(payload.ExitCode), ViewerID: viewerID})
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
					ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "client encryption setup failed", SessionID: sessionID})
					continue
				}
				log.Printf("pty session %s: re-keyed E2E for reattach", sessionID)
				newStreamCtx, newSCancel := context.WithCancel(ctx)
				newStream, reErr := ec.AttachSessionWithOptions(newStreamCtx, sessionID, egg.AttachOptions{Claim: true, Takeover: attach.Takeover, Owner: "browser:" + attach.UserID, Rows: attach.Rows, Cols: attach.Cols})
				if reErr != nil {
					newSCancel()
					log.Printf("pty session %s: reattach to egg failed: %v", sessionID, reErr)
					ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: reErr.Error(), SessionID: sessionID, ControllerID: attach.ControllerID})
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

				// Activate new key + stream, start new output goroutine.
				mu.Lock()
				gcm = newGCM
				activeStream = newStream
				cancelStream = newSCancel
				mu.Unlock()
				bindBrowserInput(sessionID, attach.ControllerID, attach.PublicKey, attach.UserID, ec, newStream, newSCancel)

				// Send pty.started so browser can derive key.
				reclaimIdleState.mu.Lock()
				reclaimIdleState.connected = true
				reclaimIdleState.mu.Unlock()
				{
					started := ws.PTYStarted{Type: ws.TypePTYStarted, SessionID: sessionID, PublicKey: wingPubKeyB64, ControllerID: attach.ControllerID}
					if attachAuthToken != "" {
						started.AuthToken = attachAuthToken
					}
					ws.WritePTYMessage(write, started)
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

				go func() {
					var lastHadBell bool
					for {
						msg, err := newStream.Recv()
						if err != nil {
							browserStreamEnded(sessionID, attach.ControllerID, err, write)
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
							if config.Channel() != "preview" && hasBell(p.Output) {
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
							if config.Channel() == "preview" {
								return
							}
							log.Printf("pty session %s: exited with code %d", sessionID, p.ExitCode)
							ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: sessionID, ExitCode: int(p.ExitCode)})
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
					if config.Channel() == "preview" {
						continue reclaimInputLoop
					}
					sessionCancel()
					return
				}

			case ws.TypePTYDetach:
				var detach ws.PTYDetach
				if json.Unmarshal(data, &detach) == nil {
					pendingAuth.detach(detach)
					detachBrowserAttachment(sessionID, detach)
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
					if config.Channel() == "preview" {
						if err := resizeBrowserStream(ctx, sessionID, msg.ControllerID, currentStream, uint32(msg.Rows), uint32(msg.Cols)); err != nil {
							log.Printf("browser resize: %v", err)
						}
						continue
					}
					if err := currentStream.Send(&pb.SessionMsg{SessionId: sessionID, Payload: &pb.SessionMsg_Resize{Resize: &pb.Resize{Rows: uint32(msg.Rows), Cols: uint32(msg.Cols)}}}); err != nil {
						log.Printf("pty session %s: send resize to reclaimed egg: %v", sessionID, err)
						if config.Channel() == "preview" {
							continue reclaimInputLoop
						}
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
func handlePTYSession(version string, ctx context.Context, cfg *config.Config, wingCfg *config.WingConfig, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte, eggCfg *egg.EggConfig, debug, vte bool, allowedKeysPtr *[]config.AllowKey, passkeyCache *auth.AuthCache, passkeyPolicy auth.PasskeyPolicy, authTTL time.Duration, idleTimeout time.Duration, sw *webrtcpkg.SwappableWriter, dcSessions *sync.Map, tools []*config.ToolConfig, sharedHost bool) {
	allowedKeys := *allowedKeysPtr
	if start.PublicKey == "" {
		ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "E2E client key required"})
		return
	}
	subject := wingpolicy.PasskeySubject(start.UserID, start.PublicKey)
	userHasPasskey := len(wingpolicy.PasskeysForSubject(allowedKeys, start.UserID)) > 0
	if wingCfg.Locked && (subject == "" || !userHasPasskey) {
		log.Printf("pty session %s: locked wing rejected user without a locally approved passkey", start.SessionID)
		ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "not allowed by wing"})
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
			ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "challenge generation failed"})
			return
		}

		ws.WritePTYMessage(write, ws.PasskeyChallenge{
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
					ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "invalid passkey response"})
					return
				}

				// Decode assertion fields
				authData, _ := base64.StdEncoding.DecodeString(resp.AuthenticatorData)
				clientJSON, _ := base64.StdEncoding.DecodeString(resp.ClientDataJSON)
				sig, _ := base64.StdEncoding.DecodeString(resp.Signature)

				matchedRawKey, verifyErr := wingpolicy.VerifySubjectPasskey(allowedKeys, start.UserID, challenge, authData, clientJSON, sig, passkeyPolicy)
				if verifyErr != nil {
					ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "invalid passkey signature"})
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
				ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "passkey authentication timed out"})
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
		ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "E2E encryption required but wing private key missing"})
		return
	}
	wingPubKeyB64 = base64.StdEncoding.EncodeToString(privKey.PublicKey().Bytes())
	if start.PublicKey != "" {
		derived, deriveErr := auth.DeriveSharedKey(privKey, start.PublicKey, "wt-pty")
		if deriveErr != nil {
			log.Printf("pty session %s: FATAL: derive shared key: %v", start.SessionID, deriveErr)
			ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "E2E key exchange failed"})
			return
		}
		gcm = derived
		log.Printf("pty session %s: E2E encryption enabled", start.SessionID)
	}

	hostHome, _ := os.UserHomeDir()
	identity := eggclient.BrowserEggIdentity(wingCfg, start, hostHome, sharedHost)
	toolOpts := eggclient.SpawnEggOpts{}
	toolListener, toolErr := eggclient.PrepareBrowserTools(cfg, start.SessionID, tools, &toolOpts, identity)
	if toolErr != nil {
		log.Printf("pty session %s: %v", start.SessionID, toolErr)
		ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: toolErr.Error()})
		return
	}
	if toolListener != nil {
		defer cmdutil.CloseWithLog("egg tool listener", toolListener)
	}

	// Spawn a per-session egg
	sharedAllowedPaths := wingpolicy.CanonicalPaths(wingpolicy.PathsForRequest(wingCfg.Paths, start.Email, start.OrgRole, hostHome))
	providerResumeID := ""
	var releaseProviderResume func(bool)
	providerResumeSpawned := false
	if start.ResumeSessionID != "" {
		var resumeErr error
		providerResumeID, start.CWD, releaseProviderResume, resumeErr = eggclient.PrepareBrowserResume(cfg, wingCfg, start, sharedAllowedPaths, sharedHost)
		if resumeErr != nil {
			ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: resumeErr.Error()})
			return
		}
		defer func() { releaseProviderResume(providerResumeSpawned) }()
	}
	resumeArgs, resumePrincipal, resumeBindingErr := localmcp.PrepareConversationResumeMCP(version, cfg, wingCfg, start, eggCfg, sharedHost, &toolOpts)
	if resumeBindingErr != nil {
		ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: resumeBindingErr.Error()})
		return
	}
	ec, err := eggclient.SpawnEgg(cfg, start.SessionID, start.Agent, eggCfg, uint32(start.Rows), uint32(start.Cols), start.CWD, debug, vte, eggCfg.Trace,
		identity, idleTimeout, eggclient.SpawnEggOpts{
			ResumeSessionID: providerResumeID, ResumeSourceSessionID: start.ResumeSessionID,
			ProviderReserved: providerResumeID != "", ToolNames: toolOpts.ToolNames, ToolSocketPath: toolOpts.ToolSocketPath,
			Principal: resumePrincipal, AgentArgs: resumeArgs,
			ProtectedWriteTargets: toolOpts.ProtectedWriteTargets, OmitBrowserBridge: toolOpts.OmitBrowserBridge,
		})
	if err != nil {
		eggDir := filepath.Join(cfg.Dir, "eggs", start.SessionID)
		crashInfo := eggclient.ReadEggCrashInfo(eggDir)
		log.Printf("pty session %s: spawn egg failed: %v", start.SessionID, err)
		// If no crash info from the child (e.g. pre-flight check failed before
		// child was spawned), use the spawn error directly — it contains the
		// actionable fix instructions.
		if strings.Contains(crashInfo, "no log available") || strings.Contains(crashInfo, "empty log") {
			crashInfo = err.Error()
		}
		ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: crashInfo})
		return
	}
	defer cmdutil.CloseWithLog("PTY egg client", ec)
	providerResumeSpawned = providerResumeID != ""
	if err := localmcp.InheritConversationExecution(cfg, start.ResumeSessionID, start.SessionID); err != nil {
		killCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_ = ec.Kill(killCtx, start.SessionID)
		cancel()
		ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "persist resumed conversation identity: " + err.Error()})
		return
	}

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

	// Attach to egg session stream
	streamCtx, sCancel := context.WithCancel(ctx)
	stream, err := eggclient.AttachBrowserController(streamCtx, ec, start.SessionID, egg.AttachOptions{Claim: true, Owner: "browser:" + start.UserID}, toolListener, start.UserID)
	writerConfirmed := err == nil
	if err != nil {
		log.Printf("pty: egg attach failed: %v", err)
		if config.Channel() != "preview" {
			sCancel()
			ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1})
			return
		}
		ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, SessionID: start.SessionID, ControllerID: start.ControllerID, Message: err.Error()})
		if grpcstatus.Code(err) != codes.FailedPrecondition {
			sCancel()
			return
		}
		// Another surface may claim a newly visible egg before the browser's
		// initial attach. Keep its bridge alive for a deliberate takeover.
		stream, err = ec.AttachSessionWithOptions(streamCtx, start.SessionID, egg.AttachOptions{ReadOnly: true, Owner: "wing:pending-browser"})
		if err != nil {
			sCancel()
			return
		}
		idleState.mu.Lock()
		idleState.connected = false
		idleState.mu.Unlock()
	}
	activeStream = stream
	cancelStream = sCancel

	if writerConfirmed {
		bindBrowserInput(start.SessionID, start.ControllerID, start.PublicKey, start.UserID, ec, stream, sCancel)
		// Notify browser
		ws.WritePTYMessage(write, ws.PTYStarted{
			Type:                 ws.TypePTYStarted,
			SessionID:            start.SessionID,
			ControllerID:         start.ControllerID,
			Agent:                start.Agent,
			PublicKey:            wingPubKeyB64,
			CWD:                  start.CWD,
			AuthToken:            start.AuthToken,
			ResumedFromSessionID: start.ResumeSessionID,
		})
	}

	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()
	if config.Channel() == "preview" {
		if err := watchPreviewBrowserEgg(sessionCtx, ec, start.SessionID, start.Agent, start.CWD, idleState, write, sessionCancel); err != nil {
			ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, SessionID: start.SessionID, Message: err.Error()})
			return
		}
	}

	// Watch for .wt-preview file in agent working directory
	if start.CWD != "" {
		go watchPreviewFile(sessionCtx, start.CWD, start.SessionID, &mu, &gcm, write)
	}

	// Watch for browser open requests from the shim
	go watchBrowserRequests(sessionCtx, filepath.Join(cfg.Dir, "eggs", start.SessionID, "browser-requests"), start.SessionID, 0, write)

	// Read output from egg -> encrypt -> send to browser
	go func() {
		var lastHadBell bool
		for {
			msg, err := stream.Recv()
			if err != nil {
				browserStreamEnded(start.SessionID, start.ControllerID, err, write)
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
				if config.Channel() != "preview" && hasBell(p.Output) {
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
				if config.Channel() == "preview" {
					return
				}
				log.Printf("pty session %s: exited with code %d", start.SessionID, p.ExitCode)
				ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: int(p.ExitCode)})
				clearAttentionCooldown(start.SessionID)
				sessionCancel()
				return
			}
		}
	}()

	// Process input from browser -> decrypt -> send to egg
	go func() {
		defer releaseBrowserClient(start.SessionID, ec)
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
					ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "passkey timed out", SessionID: start.SessionID, ViewerID: pending.attach.ViewerID})
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
				rawKey, verifyErr := wingpolicy.VerifySubjectPasskey(allowedKeys, pending.attach.UserID, pending.challenge, authData, clientData, signature, passkeyPolicy)
				if verifyErr != nil {
					ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "invalid passkey", SessionID: start.SessionID, ViewerID: pending.attach.ViewerID})
					continue
				}
				token, tokenErr := auth.GenerateAuthToken()
				if tokenErr != nil {
					ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "auth token generation failed", SessionID: start.SessionID, ViewerID: pending.attach.ViewerID})
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
				if !wingpolicy.CanAttachSession(attach.UserID, attach.OrgRole, start.UserID) {
					ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "session not found or not owned by caller", SessionID: start.SessionID, ViewerID: attach.ViewerID})
					continue
				}
				clearAttentionCooldown(start.SessionID)
				if attach.PublicKey == "" {
					ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "client encryption key required", SessionID: start.SessionID, ViewerID: attach.ViewerID})
					continue
				}

				attachSubject := wingpolicy.PasskeySubject(attach.UserID, attach.PublicKey)
				attachUserHasPasskey := len(wingpolicy.PasskeysForSubject(allowedKeys, attach.UserID)) > 0
				if wingCfg.Locked && (attachSubject == "" || !attachUserHasPasskey) {
					ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "not allowed by wing", SessionID: start.SessionID, ViewerID: attach.ViewerID})
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
							ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "challenge generation failed", SessionID: start.SessionID, ViewerID: attach.ViewerID})
							continue
						}
						ws.WritePTYMessage(write, ws.PasskeyChallenge{
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
						ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: 1, Error: "spectate not enabled", ViewerID: attach.ViewerID})
						continue
					}
					// Derive spectator-specific E2E key (independent of controller)
					spectatorGCM, deriveErr := auth.DeriveSharedKey(privKey, attach.PublicKey, "wt-pty")
					if deriveErr != nil {
						log.Printf("pty session %s: spectator key derive failed: %v", start.SessionID, deriveErr)
						ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "spectator encryption setup failed", SessionID: start.SessionID, ViewerID: attach.ViewerID})
						continue
					}
					log.Printf("pty session %s: spectator E2E enabled (viewer=%s)", start.SessionID, attach.ViewerID)

					// Open independent egg stream (gets replay + live cursor)
					specCtx, specCancel := context.WithCancel(ctx)
					specStream, specErr := ec.AttachSessionWithOptions(specCtx, start.SessionID, egg.AttachOptions{ReadOnly: true, Owner: "browser:observer"})
					if specErr != nil {
						specCancel()
						log.Printf("pty session %s: spectator attach to egg failed: %v", start.SessionID, specErr)
						ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "spectator attach failed", SessionID: start.SessionID, ViewerID: attach.ViewerID})
						continue
					}

					// Send pty.started only after the independent stream exists.
					bindBrowserViewer(start.SessionID, attach.ViewerID, ec, specCancel)
					ws.WritePTYMessage(write, ws.PTYStarted{
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
						defer browserViewers.Delete(start.SessionID + ":" + viewerID)
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
								ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: int(p.ExitCode), ViewerID: viewerID})
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
					ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: "client encryption setup failed", SessionID: start.SessionID})
					continue
				}
				log.Printf("pty session %s: re-keyed E2E for reattach", start.SessionID)
				newStreamCtx, newSCancel := context.WithCancel(ctx)
				newStream, reErr := eggclient.AttachBrowserController(newStreamCtx, ec, start.SessionID, egg.AttachOptions{Claim: true, Takeover: attach.Takeover, Owner: "browser:" + attach.UserID, Rows: attach.Rows, Cols: attach.Cols}, toolListener, attach.UserID)
				if reErr != nil {
					newSCancel()
					log.Printf("pty session %s: reattach to egg failed: %v", start.SessionID, reErr)
					ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, Message: reErr.Error(), SessionID: start.SessionID, ControllerID: attach.ControllerID})
					continue
				}

				mu.Lock()
				gcm = nil
				if cancelStream != nil {
					cancelStream()
				}
				mu.Unlock()

				// Activate new key + stream, start new output goroutine.
				mu.Lock()
				gcm = newGCM
				activeStream = newStream
				cancelStream = newSCancel
				mu.Unlock()
				bindBrowserInput(start.SessionID, attach.ControllerID, attach.PublicKey, attach.UserID, ec, newStream, newSCancel)

				// Send pty.started so browser can derive key and the relay can
				// promote this pending controller.
				idleState.mu.Lock()
				idleState.connected = true
				idleState.mu.Unlock()
				ws.WritePTYMessage(write, ws.PTYStarted{
					ControllerID: attach.ControllerID,
					Type:         ws.TypePTYStarted,
					SessionID:    start.SessionID,
					Agent:        start.Agent,
					PublicKey:    wingPubKeyB64,
					AuthToken:    attachAuthToken,
				})

				// Read replay (first message) and send to browser in chunks.
				if newGCM != nil {
					replayMsg, rErr := newStream.Recv()
					if rErr == nil {
						if replay, ok := replayMsg.Payload.(*pb.SessionMsg_Output); ok && len(replay.Output) > 0 {
							sendReplayChunked(start.SessionID, replay.Output, newGCM, write)
						}
					}
				}

				go func() {
					var lastHadBell bool
					for {
						msg, err := newStream.Recv()
						if err != nil {
							browserStreamEnded(start.SessionID, attach.ControllerID, err, write)
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
							if config.Channel() != "preview" && hasBell(p.Output) {
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
							if config.Channel() == "preview" {
								return
							}
							log.Printf("pty session %s: exited with code %d", start.SessionID, p.ExitCode)
							ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID, ExitCode: int(p.ExitCode)})
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
					if config.Channel() == "preview" {
						continue inputLoop
					}
					sessionCancel()
					return
				}

			case ws.TypePTYDetach:
				var detach ws.PTYDetach
				if json.Unmarshal(data, &detach) == nil {
					pendingAuth.detach(detach)
					detachBrowserAttachment(start.SessionID, detach)
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
					if config.Channel() == "preview" {
						if err := resizeBrowserStream(ctx, start.SessionID, msg.ControllerID, currentStream, uint32(msg.Rows), uint32(msg.Cols)); err != nil {
							log.Printf("browser resize: %v", err)
						}
						continue
					}
					if err := currentStream.Send(&pb.SessionMsg{
						SessionId: start.SessionID,
						Payload: &pb.SessionMsg_Resize{Resize: &pb.Resize{
							Rows: uint32(msg.Rows),
							Cols: uint32(msg.Cols),
						}},
					}); err != nil {
						log.Printf("pty session %s: send resize to egg: %v", start.SessionID, err)
						if config.Channel() == "preview" {
							continue inputLoop
						}
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

func appendHostedRelayPolicyAudit(cfg *config.Config, operation string) (result error) {
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(cfg.Dir, "policy-audit.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { result = cmdutil.CloseAndJoin("hosted relay policy audit", file, result) }()
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
