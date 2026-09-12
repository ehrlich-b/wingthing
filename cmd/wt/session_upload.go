package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ehrlich-b/wingthing/internal/fsutil"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

const (
	maxSessionUploadSize       = 25 << 20
	maxSessionUploadChunk      = 128 << 10
	maxActiveSessionUploads    = 8
	maxSessionUploadsPerSender = 2
	maxReservedSessionUpload   = 64 << 20
	sessionUploadExpiration    = 10 * time.Minute
)

type sessionUpload struct {
	id        string
	sessionID string
	name      string
	cwd       string
	userID    string
	senderPub string
	size      int64
	data      []byte
	updatedAt time.Time
	root      *os.Root
}

type sessionUploadRegistry struct {
	mu      sync.Mutex
	uploads map[string]*sessionUpload
	now     func() time.Time
}

func newSessionUploadRegistry() *sessionUploadRegistry {
	return &sessionUploadRegistry{
		uploads: make(map[string]*sessionUpload),
		now:     time.Now,
	}
}

var sessionUploads = newSessionUploadRegistry()

func validSessionUploadName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 ||
		filepath.IsAbs(name) || strings.ContainsAny(name, `/\\`) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return filepath.Base(name) == name
}

func sessionUploadTarget(req ws.TunnelRequest, sessionID string, sessions []ws.SessionInfo, userPaths []string) (ws.SessionInfo, error) {
	for _, session := range sessions {
		if session.SessionID != sessionID {
			continue
		}
		if session.UserID == "" || session.UserID != req.SenderUserID {
			return ws.SessionInfo{}, errors.New("session not found or not owned by caller")
		}
		if session.CWD == "" {
			return ws.SessionInfo{}, errors.New("session has no working directory")
		}
		session.CWD = canonicalSessionPath(session.CWD)
		if isMemberFiltered(req) {
			canonicalPaths := make([]string, 0, len(userPaths))
			for _, path := range userPaths {
				canonicalPaths = append(canonicalPaths, canonicalSessionPath(path))
			}
			if len(canonicalPaths) == 0 || !isUnderPaths(session.CWD, canonicalPaths) {
				return ws.SessionInfo{}, errors.New("session not found or not owned by caller")
			}
		}
		info, err := os.Stat(session.CWD)
		if err != nil || !info.IsDir() {
			return ws.SessionInfo{}, errors.New("session working directory is unavailable")
		}
		return session, nil
	}
	return ws.SessionInfo{}, errors.New("session not found or not owned by caller")
}

func (r *sessionUploadRegistry) begin(session ws.SessionInfo, req ws.TunnelRequest, name string, size int64) (*sessionUpload, error) {
	if !validSessionUploadName(name) {
		return nil, errors.New("invalid file name")
	}
	if size < 0 || size > maxSessionUploadSize {
		return nil, fmt.Errorf("file exceeds %d byte upload limit", maxSessionUploadSize)
	}

	root, err := os.OpenRoot(session.CWD)
	if err != nil {
		return nil, fmt.Errorf("open session directory: %w", err)
	}
	keepRoot := false
	defer func() {
		if !keepRoot {
			_ = root.Close()
		}
	}()
	if _, err := root.Lstat(name); err == nil {
		return nil, errors.New("a file with that name already exists")
	} else if !os.IsNotExist(err) {
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

	id, err := newSessionUploadID()
	if err != nil {
		return nil, fmt.Errorf("create upload ID: %w", err)
	}
	upload := &sessionUpload{
		id:        id,
		sessionID: session.SessionID,
		name:      name,
		cwd:       session.CWD,
		userID:    req.SenderUserID,
		senderPub: req.SenderPub,
		size:      size,
		updatedAt: r.now(),
		root:      root,
	}
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
	_ = upload.root.Close()
	return nil
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

func newSessionUploadID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func writeSessionUpload(cwd, name string, data []byte) (result error) {
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return fmt.Errorf("open session directory: %w", err)
	}
	defer func() { result = errors.Join(result, root.Close()) }()
	return writeSessionUploadRoot(root, name, data)
}

func writeSessionUploadRoot(root *os.Root, name string, data []byte) error {
	temporaryID, err := newSessionUploadID()
	if err != nil {
		return fmt.Errorf("create temporary upload name: %w", err)
	}
	temporaryName := ".wingthing-upload-" + temporaryID
	file, err := root.OpenFile(temporaryName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary uploaded file: %w", err)
	}
	defer func() { _ = root.Remove(temporaryName) }()
	linked := false
	committed := false
	defer func() {
		if linked && !committed {
			_ = root.Remove(name)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("protect uploaded file: %w", err)
	}
	if _, err := io.Copy(file, bytes.NewReader(data)); err != nil {
		_ = file.Close()
		return fmt.Errorf("write uploaded file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync uploaded file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close uploaded file: %w", err)
	}
	if err := root.Link(temporaryName, name); err != nil {
		if os.IsExist(err) {
			return errors.New("a file with that name already exists")
		}
		return fmt.Errorf("commit uploaded file: %w", err)
	}
	linked = true
	if err := root.Remove(temporaryName); err != nil {
		return fmt.Errorf("remove temporary uploaded file: %w", err)
	}
	if err := fsutil.SyncRoot(root); err != nil {
		return fmt.Errorf("persist uploaded file: %w", err)
	}
	committed = true
	return nil
}

func sessionUploadSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
