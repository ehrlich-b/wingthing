package main

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

const (
	maxSessionUploadSize       = 25 << 20
	maxSessionUploadChunk      = 128 << 10
	maxActiveSessionUploads    = 8
	maxSessionUploadsPerSender = 2
	maxReservedSessionUpload   = 64 << 20
	maxSessionDownloadSize     = 100 << 20
	sessionFileStreamChunk     = 32 << 10
	sessionUploadExpiration    = 10 * time.Minute
	capabilitySessionRename    = "session.rename.v1"
	capabilitySessionUpload    = "session.file_upload.v1"
	capabilitySessionDownload  = "session.file_download.v1"
	capabilitySessionResume    = "session.provider_resume.v1"
	capabilitySessionExport    = "session.file_export.v1"
)

func browserSessionCapabilities(hasExport bool) []string {
	capabilities := []string{capabilitySessionRename, capabilitySessionUpload, capabilitySessionDownload, capabilitySessionResume}
	if hasExport {
		capabilities = append(capabilities, capabilitySessionExport)
	}
	return capabilities
}

type sessionFilePolicy struct {
	cwd           string
	writableRoots []string
	readOnlyRoots []string
	deny          []string
	denyWrite     []string
}

func canonicalPolicyPath(path string) string {
	path = filepath.Clean(path)
	current := path
	var suffix []string
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func loadSessionFilePolicy(session ws.SessionInfo, effectiveHome string) (sessionFilePolicy, error) {
	if session.CWD == "" || session.EggConfig == "" {
		return sessionFilePolicy{}, errors.New("session effective filesystem policy is unavailable")
	}
	cfg, err := egg.LoadEggConfigFromYAML(session.EggConfig)
	if err != nil {
		return sessionFilePolicy{}, fmt.Errorf("parse session filesystem policy: %w", err)
	}
	policy := sessionFilePolicy{cwd: canonicalPolicyPath(session.CWD)}
	for _, entry := range cfg.FS {
		mode, path, ok := strings.Cut(entry, ":")
		if !ok {
			mode, path = "rw", entry
		}
		if path == "~" {
			path = effectiveHome
		} else if strings.HasPrefix(path, "~/") {
			path = filepath.Join(effectiveHome, path[2:])
		} else if !filepath.IsAbs(path) {
			path = filepath.Join(policy.cwd, path)
		}
		path = canonicalPolicyPath(path)
		switch mode {
		case "deny":
			policy.deny = append(policy.deny, path)
		case "deny-write":
			policy.denyWrite = append(policy.denyWrite, path)
		case "rw", "":
			policy.writableRoots = append(policy.writableRoots, path)
		case "ro":
			policy.readOnlyRoots = append(policy.readOnlyRoots, path)
		}
	}
	if len(policy.writableRoots) == 0 {
		return sessionFilePolicy{}, errors.New("session policy has no writable data area")
	}
	return policy, nil
}

func (p sessionFilePolicy) writableRoot(path string) (string, bool) {
	path = canonicalPolicyPath(path)
	best := ""
	for _, root := range p.writableRoots {
		if isUnderPaths(path, []string{root}) && len(root) > len(best) {
			best = root
		}
	}
	if best == "" {
		return "", false
	}
	for _, root := range p.readOnlyRoots {
		if isUnderPaths(path, []string{root}) && len(root) >= len(best) {
			return "", false
		}
	}
	for _, rule := range p.deny {
		// deny:/ is only a selector for the Linux mount jail, where explicit
		// rw/ro roots are mounted back in. On other platforms it is an
		// effective deny and therefore wins.
		if runtime.GOOS == "linux" && filepath.Clean(rule) == string(filepath.Separator) {
			continue
		}
		if isUnderPaths(path, []string{rule}) {
			return "", false
		}
	}
	for _, rule := range p.denyWrite {
		if isUnderPaths(path, []string{rule}) {
			return "", false
		}
	}
	return best, best != ""
}

func (p sessionFilePolicy) uploadDirectory(userPaths []string) (string, error) {
	userPaths = canonicalPaths(userPaths)
	if _, ok := p.writableRoot(p.cwd); ok && (len(userPaths) == 0 || isUnderPaths(p.cwd, userPaths)) {
		return p.cwd, nil
	}
	best := ""
	for _, writable := range p.writableRoots {
		if len(userPaths) == 0 {
			if _, ok := p.writableRoot(writable); !ok {
				continue
			}
			info, err := os.Stat(writable)
			if err == nil && info.IsDir() && (best == "" || len(writable) > len(best)) {
				best = writable
			}
			continue
		}
		for _, allowed := range userPaths {
			candidate := ""
			switch {
			case isUnderPaths(allowed, []string{writable}):
				candidate = allowed
			case isUnderPaths(writable, []string{allowed}):
				candidate = writable
			}
			if candidate == "" {
				continue
			}
			if _, ok := p.writableRoot(candidate); !ok {
				continue
			}
			info, err := os.Stat(candidate)
			if err == nil && info.IsDir() && (best == "" || len(candidate) > len(best)) {
				best = candidate
			}
		}
	}
	if best == "" {
		return "", errors.New("session has no writable data area allowed for this user")
	}
	return best, nil
}

func resolveOwnedSessionFileTarget(req ws.TunnelRequest, sessionID string, sessions []ws.SessionInfo, userPaths []string, effectiveHome string) (ws.SessionInfo, sessionFilePolicy, error) {
	session, err := resolveOwnedActiveSession(req, sessionID, sessions, userPaths)
	if err != nil {
		return ws.SessionInfo{}, sessionFilePolicy{}, err
	}
	policy, err := loadSessionFilePolicy(session, effectiveHome)
	if err != nil {
		return ws.SessionInfo{}, sessionFilePolicy{}, err
	}
	return session, policy, nil
}

func resolveOwnedActiveSession(req ws.TunnelRequest, sessionID string, sessions []ws.SessionInfo, userPaths []string) (ws.SessionInfo, error) {
	userPaths = canonicalPaths(userPaths)
	for _, session := range sessions {
		if session.SessionID != sessionID {
			continue
		}
		if session.UserID == "" || session.UserID != req.SenderUserID {
			break
		}
		session.CWD = canonicalSessionPath(session.CWD)
		if !canAccessSessionPath(req, session.CWD, userPaths) {
			break
		}
		return session, nil
	}
	return ws.SessionInfo{}, errors.New("session not found or not owned by caller")
}

func validSessionFileName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || filepath.IsAbs(name) || strings.ContainsAny(name, `/\\`) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return filepath.Base(name) == name
}

func openBoundDirectoryRoot(path string) (*os.Root, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("directory is unavailable")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	directory, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	after, statErr := directory.Stat()
	_ = directory.Close()
	if statErr != nil || !os.SameFile(before, after) {
		_ = root.Close()
		return nil, errors.New("directory changed while opening it")
	}
	return root, nil
}

type sessionUpload struct {
	id, sessionID, name, destination, userID, senderPub string
	size                                                int64
	data                                                []byte
	updatedAt                                           time.Time
	root                                                *os.Root
}

type sessionUploadRegistry struct {
	mu      sync.Mutex
	uploads map[string]*sessionUpload
	now     func() time.Time
}

func newSessionUploadRegistry() *sessionUploadRegistry {
	return &sessionUploadRegistry{uploads: make(map[string]*sessionUpload), now: time.Now}
}

var sessionUploads = newSessionUploadRegistry()

func (r *sessionUploadRegistry) begin(session ws.SessionInfo, policy sessionFilePolicy, userPaths []string, req ws.TunnelRequest, name string, size int64) (*sessionUpload, error) {
	if !validSessionFileName(name) {
		return nil, errors.New("invalid file name")
	}
	if size < 0 || size > maxSessionUploadSize {
		return nil, fmt.Errorf("file exceeds %d byte upload limit", maxSessionUploadSize)
	}
	destination, err := policy.uploadDirectory(userPaths)
	if err != nil {
		return nil, err
	}
	if _, ok := policy.writableRoot(filepath.Join(destination, name)); !ok {
		return nil, errors.New("file name is denied by the session write policy")
	}
	root, err := openBoundDirectoryRoot(destination)
	if err != nil {
		return nil, fmt.Errorf("open upload destination: %w", err)
	}
	keepRoot := false
	defer func() {
		if !keepRoot {
			_ = root.Close()
		}
	}()
	if _, err := root.Lstat(name); err == nil {
		return nil, errors.New("a file with that name already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect upload destination: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepExpiredLocked()
	if len(r.uploads) >= maxActiveSessionUploads {
		return nil, errors.New("too many uploads are already in progress")
	}
	var senderUploads int
	var reserved int64
	for _, upload := range r.uploads {
		reserved += upload.size
		if upload.userID == req.SenderUserID && upload.senderPub == req.SenderPub {
			senderUploads++
		}
	}
	if senderUploads >= maxSessionUploadsPerSender {
		return nil, errors.New("too many uploads are already in progress for this client")
	}
	if reserved+size > maxReservedSessionUpload {
		return nil, errors.New("upload capacity is temporarily full")
	}
	id, err := newSessionFileID()
	if err != nil {
		return nil, fmt.Errorf("create upload ID: %w", err)
	}
	upload := &sessionUpload{id: id, sessionID: session.SessionID, name: name, destination: destination, userID: req.SenderUserID, senderPub: req.SenderPub, size: size, updatedAt: r.now(), root: root}
	r.uploads[id] = upload
	keepRoot = true
	return upload, nil
}

func (r *sessionUploadRegistry) append(id, userID, senderPub string, offset int64, chunk []byte) (int64, error) {
	if len(chunk) == 0 || len(chunk) > maxSessionUploadChunk {
		return 0, fmt.Errorf("upload chunk must contain 1 to %d bytes", maxSessionUploadChunk)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepExpiredLocked()
	upload, err := r.boundUploadLocked(id, userID, senderPub)
	if err != nil {
		return 0, err
	}
	if offset != int64(len(upload.data)) {
		return 0, errors.New("upload chunk offset does not match received data")
	}
	if int64(len(upload.data))+int64(len(chunk)) > upload.size {
		return 0, errors.New("upload exceeds declared size")
	}
	upload.data = append(upload.data, chunk...)
	upload.updatedAt = r.now()
	return int64(len(upload.data)), nil
}

func (r *sessionUploadRegistry) finish(id, userID, senderPub string) (*sessionUpload, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepExpiredLocked()
	upload, err := r.boundUploadLocked(id, userID, senderPub)
	if err != nil {
		return nil, err
	}
	if int64(len(upload.data)) != upload.size {
		return nil, fmt.Errorf("upload is incomplete: received %d of %d bytes", len(upload.data), upload.size)
	}
	delete(r.uploads, id)
	return upload, nil
}

func (r *sessionUploadRegistry) cancel(id, userID, senderPub string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	upload, err := r.boundUploadLocked(id, userID, senderPub)
	if err != nil {
		return err
	}
	delete(r.uploads, id)
	return upload.root.Close()
}

func (r *sessionUploadRegistry) boundUploadLocked(id, userID, senderPub string) (*sessionUpload, error) {
	upload := r.uploads[id]
	if upload == nil || upload.userID != userID || upload.senderPub != senderPub {
		return nil, errors.New("upload not found")
	}
	return upload, nil
}

func (r *sessionUploadRegistry) sweepExpiredLocked() {
	now := r.now()
	for id, upload := range r.uploads {
		if now.Sub(upload.updatedAt) > sessionUploadExpiration {
			delete(r.uploads, id)
			_ = upload.root.Close()
		}
	}
}

func newSessionFileID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func writeSessionFileRoot(root *os.Root, name string, source io.Reader) (sha string, size int64, result error) {
	temporaryID, err := newSessionFileID()
	if err != nil {
		return "", 0, err
	}
	temporaryName := ".wingthing-file-" + temporaryID
	file, err := root.OpenFile(temporaryName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("create temporary file: %w", err)
	}
	defer func() { _ = root.Remove(temporaryName) }()
	linked, committed := false, false
	defer func() {
		if linked && !committed {
			_ = root.Remove(name)
		}
	}()
	hash := sha256.New()
	size, err = io.Copy(io.MultiWriter(file, hash), source)
	if err != nil {
		_ = file.Close()
		return "", 0, fmt.Errorf("write file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", 0, fmt.Errorf("sync file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", 0, fmt.Errorf("close file: %w", err)
	}
	if err := root.Link(temporaryName, name); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", 0, errors.New("a file with that name already exists")
		}
		return "", 0, fmt.Errorf("commit file: %w", err)
	}
	linked = true
	if err := root.Remove(temporaryName); err != nil {
		return "", 0, fmt.Errorf("remove temporary file: %w", err)
	}
	rootDirectory, err := root.Open(".")
	if err != nil {
		return "", 0, fmt.Errorf("open destination directory: %w", err)
	}
	if err := rootDirectory.Sync(); err != nil {
		_ = rootDirectory.Close()
		return "", 0, fmt.Errorf("persist file: %w", err)
	}
	if err := rootDirectory.Close(); err != nil {
		return "", 0, fmt.Errorf("close destination directory: %w", err)
	}
	committed = true
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

var errSessionFileTooLarge = errors.New("file grew beyond the transfer limit")

type boundedSessionFileReader struct {
	reader    io.Reader
	remaining int64
}

func (r *boundedSessionFileReader) Read(buffer []byte) (int, error) {
	if r.remaining < 0 {
		return 0, errSessionFileTooLarge
	}
	limit := int64(len(buffer))
	if limit > r.remaining+1 {
		limit = r.remaining + 1
	}
	count, err := r.reader.Read(buffer[:limit])
	r.remaining -= int64(count)
	if r.remaining < 0 {
		return count, errSessionFileTooLarge
	}
	return count, err
}

func openSessionFile(session ws.SessionInfo, policy sessionFilePolicy, userPaths []string, requested string) (*os.File, string, os.FileInfo, error) {
	userPaths = canonicalPaths(userPaths)
	path := requested
	if path == "" {
		return nil, "", nil, errors.New("missing path")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(session.CWD, path)
	}
	path = canonicalSessionPath(path)
	if len(userPaths) > 0 && !isUnderPaths(path, userPaths) {
		return nil, "", nil, errors.New("file is outside current path policy")
	}
	rootPath, ok := policy.writableRoot(path)
	if !ok {
		return nil, "", nil, errors.New("file is outside the session writable data policy")
	}
	rel, err := filepath.Rel(rootPath, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, "", nil, errors.New("invalid file path")
	}
	file, err := openSessionFileNoFollow(rootPath, rel)
	if err != nil {
		return nil, "", nil, fmt.Errorf("open file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, "", nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, "", nil, errors.New("only regular files can be downloaded or exported")
	}
	if info.Size() < 0 || info.Size() > maxSessionDownloadSize {
		_ = file.Close()
		return nil, "", nil, fmt.Errorf("file exceeds %d byte limit", maxSessionDownloadSize)
	}
	return file, path, info, nil
}

func streamSessionFile(ctx context.Context, file *os.File, path string, info os.FileInfo, gcm cipher.AEAD, requestID string, write ws.PTYWriteFunc) error {
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(info.Name())))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	meta, _ := json.Marshal(map[string]any{"name": info.Name(), "size": info.Size(), "mime": contentType})
	if err := tunnelStreamChunk(gcm, requestID, meta, false, write); err != nil {
		return err
	}
	hash := sha256.New()
	var streamed int64
	buffer := make([]byte, sessionFileStreamChunk)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			streamed += int64(count)
			if streamed > maxSessionDownloadSize {
				return errSessionFileTooLarge
			}
			if streamed > info.Size() {
				return errors.New("file changed during download")
			}
			_, _ = hash.Write(buffer[:count])
			chunk, _ := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString(buffer[:count])})
			if err := tunnelStreamChunk(gcm, requestID, chunk, false, write); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	final, _ := json.Marshal(map[string]string{"sha256": hex.EncodeToString(hash.Sum(nil))})
	return tunnelStreamChunk(gcm, requestID, final, true, write)
}

func streamSessionFileError(gcm cipher.AEAD, requestID, message string, write ws.PTYWriteFunc) error {
	payload, err := json.Marshal(map[string]string{"error": message})
	if err != nil {
		return err
	}
	return tunnelStreamChunk(gcm, requestID, payload, true, write)
}

func exportSessionFile(source *os.File, info os.FileInfo, target config.ExportTarget, ownerID string) (string, int64, error) {
	if !validSessionFileName(info.Name()) {
		return "", 0, errors.New("invalid export file name")
	}
	rootInfo, err := os.Lstat(target.Path)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", 0, errors.New("export destination is unavailable")
	}
	root, err := os.OpenRoot(target.Path)
	if err != nil {
		return "", 0, fmt.Errorf("open export destination: %w", err)
	}
	defer func() { _ = root.Close() }()
	rootDirectory, err := root.Open(".")
	if err != nil {
		return "", 0, errors.New("export destination is unavailable")
	}
	openedRootInfo, statErr := rootDirectory.Stat()
	_ = rootDirectory.Close()
	if statErr != nil || !os.SameFile(rootInfo, openedRootInfo) {
		return "", 0, errors.New("export destination changed while opening it")
	}
	ownerDir := userHash(ownerID)
	if err := root.MkdirAll(ownerDir, 0o700); err != nil {
		return "", 0, fmt.Errorf("create owner export directory: %w", err)
	}
	ownerInfo, err := root.Lstat(ownerDir)
	if err != nil || !ownerInfo.IsDir() || ownerInfo.Mode()&os.ModeSymlink != 0 {
		return "", 0, errors.New("owner export directory is unavailable")
	}
	ownerDirectory, err := root.Open(ownerDir)
	if err != nil {
		return "", 0, errors.New("owner export directory is unavailable")
	}
	if err := ownerDirectory.Chmod(0o700); err != nil {
		_ = ownerDirectory.Close()
		return "", 0, fmt.Errorf("protect owner export directory: %w", err)
	}
	if err := ownerDirectory.Close(); err != nil {
		return "", 0, fmt.Errorf("close owner export directory: %w", err)
	}
	ownerRoot, err := root.OpenRoot(ownerDir)
	if err != nil {
		return "", 0, fmt.Errorf("open owner export directory: %w", err)
	}
	defer func() { _ = ownerRoot.Close() }()
	return writeSessionFileRoot(ownerRoot, info.Name(), &boundedSessionFileReader{reader: source, remaining: maxSessionDownloadSize})
}
