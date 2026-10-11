package wingsession

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/store"
)

const DefaultRunInventoryWindow = 7 * 24 * 60 * 60
const MaxRunInventoryWindow = 30 * 24 * 60 * 60

type RunInventoryRequest struct {
	Limit                 int    `json:"limit,omitempty"`
	Cursor                string `json:"cursor,omitempty"`
	RequestKey            string `json:"idempotency_key,omitempty"`
	TerminalWindowSeconds int    `json:"terminal_window_seconds,omitempty"`
}

type RunInventory struct {
	WingID           string                   `json:"wing_id"`
	Runs             []store.RunInventoryItem `json:"runs"`
	Count            int                      `json:"count"`
	NextCursor       string                   `json:"next_cursor,omitempty"`
	SnapshotRevision string                   `json:"snapshot_revision"`
	ObservedAt       string                   `json:"observed_at"`
	TerminalSince    string                   `json:"terminal_since"`
}

type inventoryCursor struct {
	Version  int                        `json:"version"`
	Scope    string                     `json:"scope"`
	Issued   time.Time                  `json:"issued"`
	Since    time.Time                  `json:"since"`
	Position store.RunInventoryPosition `json:"position"`
}

// List is a store-backed read. It deliberately avoids the mutable manager map
// and egg RPCs, so committed admissions remain discoverable without client IDs.
func (m *Runs) List(ctx context.Context, a Authority, req RunInventoryRequest) (RunInventory, error) {
	return m.listInventoryAt(ctx, a, req, time.Now().UTC())
}

func (m *Runs) listInventoryAt(ctx context.Context, a Authority, req RunInventoryRequest, now time.Time) (RunInventory, error) {
	var out RunInventory
	if req.Limit == 0 {
		req.Limit = 100
	}
	if req.TerminalWindowSeconds == 0 {
		req.TerminalWindowSeconds = DefaultRunInventoryWindow
	}
	if req.Limit < 1 || req.Limit > 200 || len(req.Cursor) > 4096 || len(req.RequestKey) > 200 || req.TerminalWindowSeconds < 3600 || req.TerminalWindowSeconds > MaxRunInventoryWindow {
		return out, errors.New("inventory requires limit 1-200, cursor at most 4096 bytes, key at most 200 bytes, and terminal_window_seconds 3600-2592000")
	}
	wingID := m.service.Config.WingID
	if m.service.Policy != nil {
		if p := m.service.Policy(); p.Wing != nil && p.Wing.WingID != "" {
			wingID = p.Wing.WingID
		}
	}
	paths := append([]string{}, a.AllowedPaths...)
	sort.Strings(paths)
	scopeData, _ := json.Marshal(struct {
		State, WingID, Principal, UserID, Key string
		Enforce, Legacy                       bool
		Paths                                 []string
		Window                                int
	}{filepath.Clean(m.service.Config.DBPath()), wingID, a.Principal, a.UserID, req.RequestKey, a.EnforcePaths, a.LegacyLocalDefault, paths, req.TerminalWindowSeconds})
	scopeHash := sha256.Sum256(scopeData)
	scope := hex.EncodeToString(scopeHash[:])
	aead, err := m.inventoryCipher()
	if err != nil {
		return out, err
	}
	cursor := inventoryCursor{Version: 1, Scope: scope, Issued: now, Since: now.Add(-time.Duration(req.TerminalWindowSeconds) * time.Second)}
	if req.Cursor != "" {
		wire, err := base64.RawURLEncoding.DecodeString(req.Cursor)
		if err != nil || len(wire) < aead.NonceSize() {
			return out, store.ErrRunInventoryCursor
		}
		data, err := aead.Open(nil, wire[:aead.NonceSize()], wire[aead.NonceSize():], []byte("wingthing-run-inventory-v1"))
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Version != 1 || cursor.Scope != scope || now.Before(cursor.Issued) || !now.Before(cursor.Issued.Add(24*time.Hour)) || cursor.Since.IsZero() {
			return out, store.ErrRunInventoryCursor
		}
	}
	page, err := m.db.ListRunInventory(ctx, store.RunInventoryQuery{Scope: store.RunInventoryScope{Principal: a.Principal, UserID: a.UserID, LegacyLocalDefault: a.LegacyLocalDefault, EnforcePaths: a.EnforcePaths, AllowedPaths: a.AllowedPaths}, Limit: req.Limit, RequestKey: req.RequestKey, TerminalSince: cursor.Since, Position: cursor.Position, Continuing: req.Cursor != ""})
	if err != nil {
		return out, err
	}
	cursor.Position = page.Position
	// A revision identifies this admission boundary, not lifecycle changes or a
	// globally atomic fleet. It remains constant across all pages of one scan.
	revisionData, _ := json.Marshal(struct {
		Scope     string
		Issued    time.Time
		Watermark int64
	}{scope, cursor.Issued, page.Position.Watermark})
	revision := sha256.Sum256(revisionData)
	out = RunInventory{WingID: wingID, Runs: page.Runs, Count: len(page.Runs), ObservedAt: now.Format(time.RFC3339Nano), TerminalSince: cursor.Since.Format(time.RFC3339), SnapshotRevision: hex.EncodeToString(revision[:])}
	if page.More {
		data, err := json.Marshal(cursor)
		if err != nil {
			return RunInventory{}, err
		}
		nonce := make([]byte, aead.NonceSize())
		if _, err = rand.Read(nonce); err != nil {
			return RunInventory{}, err
		}
		out.NextCursor = base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, data, []byte("wingthing-run-inventory-v1")))
	}
	return out, nil
}

func (m *Runs) inventoryCipher() (cipher.AEAD, error) {
	key, err := auth.LoadPrivateKey(m.service.Config.Dir)
	if err != nil {
		return nil, errors.New("run inventory wing identity is unavailable")
	}
	material, err := hkdf.Key(sha256.New, key.Bytes(), nil, "wingthing-run-inventory-v1", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
