package relay

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type RelayStore struct {
	db *sql.DB
}

type DeviceCodeRow struct {
	Code      string
	UserCode  string
	UserID    *string
	DeviceID  string
	CreatedAt time.Time
	ExpiresAt time.Time
	Claimed   bool
}

func OpenRelay(dsn string) (*RelayStore, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	s := &RelayStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *RelayStore) Close() error {
	return s.db.Close()
}

func (s *RelayStore) CreateUser(id string) error {
	_, err := s.db.Exec("INSERT INTO users (id) VALUES (?)", id)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func (s *RelayStore) CreateDeviceCode(code, userCode, deviceID string, expiresAt time.Time) error {
	_, err := s.db.Exec(
		"INSERT INTO device_codes (code, user_code, device_id, expires_at) VALUES (?, ?, ?, ?)",
		code, userCode, deviceID, expiresAt.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return fmt.Errorf("create device code: %w", err)
	}
	return nil
}

func (s *RelayStore) ClaimDeviceCode(code, userID string) error {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	res, err := s.db.Exec(
		"UPDATE device_codes SET claimed = 1, user_id = ? WHERE code = ? AND claimed = 0 AND expires_at > ?",
		userID, code, now,
	)
	if err != nil {
		return fmt.Errorf("claim device code: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("device code not found, already claimed, or expired")
	}
	return nil
}

func (s *RelayStore) GetDeviceCode(code string) (*DeviceCodeRow, error) {
	row := s.db.QueryRow(
		"SELECT code, user_code, user_id, device_id, created_at, expires_at, claimed FROM device_codes WHERE code = ?",
		code,
	)
	var dc DeviceCodeRow
	err := row.Scan(&dc.Code, &dc.UserCode, &dc.UserID, &dc.DeviceID, &dc.CreatedAt, &dc.ExpiresAt, &dc.Claimed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device code: %w", err)
	}
	return &dc, nil
}

func (s *RelayStore) CreateDeviceToken(token, userID, deviceID string, expiresAt *time.Time) error {
	_, err := s.db.Exec(
		"INSERT INTO device_tokens (token, user_id, device_id, expires_at) VALUES (?, ?, ?, ?)",
		token, userID, deviceID, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("create device token: %w", err)
	}
	return nil
}

func (s *RelayStore) ValidateToken(token string) (userID string, deviceID string, err error) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	row := s.db.QueryRow(
		"SELECT user_id, device_id FROM device_tokens WHERE token = ? AND (expires_at IS NULL OR expires_at > ?)",
		token, now,
	)
	err = row.Scan(&userID, &deviceID)
	if err == sql.ErrNoRows {
		return "", "", fmt.Errorf("invalid or expired token")
	}
	if err != nil {
		return "", "", fmt.Errorf("validate token: %w", err)
	}
	return userID, deviceID, nil
}

func (s *RelayStore) DeleteToken(token string) error {
	_, err := s.db.Exec("DELETE FROM device_tokens WHERE token = ?", token)
	if err != nil {
		return fmt.Errorf("delete token: %w", err)
	}
	return nil
}

func (s *RelayStore) AppendAudit(userID, event string, detail *string) error {
	_, err := s.db.Exec(
		"INSERT INTO audit_log (user_id, event, detail) VALUES (?, ?, ?)",
		userID, event, detail,
	)
	if err != nil {
		return fmt.Errorf("append audit: %w", err)
	}
	return nil
}

func (s *RelayStore) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, f := range files {
		var applied int
		err := s.db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", f).Scan(&applied)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", f, err)
		}
		if applied > 0 {
			continue
		}

		content, err := migrationsFS.ReadFile("migrations/" + f)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", f, err)
		}

		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", f, err)
		}
		if _, err := tx.Exec(string(content)); err != nil {
			tx.Rollback()
			return fmt.Errorf("exec migration %s: %w", f, err)
		}
		if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", f); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", f, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", f, err)
		}
	}
	return nil
}
