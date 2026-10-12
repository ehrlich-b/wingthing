package dashboard

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/config"
	_ "modernc.org/sqlite"
)

// The state directory belongs to the local OS viewer. Store only lifecycle
// cursors, never provider output, and never use these receipts as authority.
type attentionStore struct{ *sql.DB }

func openAttentionStore(dir string) (*attentionStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "navigator-attention.db")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		"PRAGMA busy_timeout=3000",
		`CREATE TABLE IF NOT EXISTS read_cursors (
		 identity TEXT PRIMARY KEY, completion INTEGER NOT NULL,
		 seen INTEGER NOT NULL, blocked INTEGER NOT NULL, cursor INTEGER NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &attentionStore{db}, nil
}

func attentionKey(key attentionIdentity) string {
	// Runtime generations prevent old on-screen acknowledgements; reopening
	// the same route must find its durable cursor irrespective of generation.
	wire, _ := json.Marshal(struct {
		Machine, Session, LocalDir string
		Profile                    config.Remote
	}{key.machine, key.session, key.localDir, key.profile})
	return fmt.Sprintf("%x", sha256.Sum256(wire))
}

func (db *attentionStore) load(key attentionIdentity) (attentionRecord, bool, error) {
	var r attentionRecord
	err := db.QueryRow("SELECT completion, seen, blocked, cursor FROM read_cursors WHERE identity = ?", attentionKey(key)).Scan(&r.completion, &r.seen, &r.blocked, &r.cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

func (db *attentionStore) save(key attentionIdentity, r attentionRecord, reset bool) error {
	statement := `INSERT INTO read_cursors VALUES (?, ?, ?, ?, ?)
	 ON CONFLICT(identity) DO UPDATE SET
	 completion=max(completion, excluded.completion), seen=max(seen, excluded.seen),
	 blocked=max(blocked, excluded.blocked), cursor=max(cursor, excluded.cursor)`
	if reset {
		statement = "INSERT OR REPLACE INTO read_cursors VALUES (?, ?, ?, ?, ?)"
	}
	// Atomic monotonic merges keep an older navigator from overwriting a
	// newer observation or acknowledgement made by another navigator.
	_, err := db.Exec(statement, attentionKey(key), r.completion, r.seen, r.blocked, r.cursor)
	return err
}
