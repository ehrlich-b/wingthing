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

// DB returns the underlying database connection.
func (s *RelayStore) DB() *sql.DB { return s.db }

type DeviceCodeRow struct {
	Code      string
	UserCode  string
	UserID    *string
	DeviceID  string
	PublicKey *string
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

func (s *RelayStore) CreateDeviceCodeWithKey(code, userCode, deviceID, publicKey string, expiresAt time.Time) error {
	_, err := s.db.Exec(
		"INSERT INTO device_codes (code, user_code, device_id, public_key, expires_at) VALUES (?, ?, ?, ?, ?)",
		code, userCode, deviceID, publicKey, expiresAt.UTC().Format("2006-01-02 15:04:05"),
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
		"SELECT code, user_code, user_id, device_id, public_key, created_at, expires_at, claimed FROM device_codes WHERE code = ?",
		code,
	)
	var dc DeviceCodeRow
	err := row.Scan(&dc.Code, &dc.UserCode, &dc.UserID, &dc.DeviceID, &dc.PublicKey, &dc.CreatedAt, &dc.ExpiresAt, &dc.Claimed)
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

// Session methods

func (s *RelayStore) CreateSession(token, socialUserID string, expiresAt time.Time) error {
	_, err := s.db.Exec(
		"INSERT INTO sessions (token, social_user_id, expires_at) VALUES (?, ?, ?)",
		token, socialUserID, expiresAt.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (s *RelayStore) GetSession(token string) (*SocialUser, error) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	row := s.db.QueryRow(
		`SELECT u.id, u.provider, u.provider_id, u.display_name, u.avatar_url, u.is_pro, u.created_at
		 FROM sessions s JOIN social_users u ON u.id = s.social_user_id
		 WHERE s.token = ? AND s.expires_at > ?`,
		token, now,
	)
	var u SocialUser
	var isPro int
	err := row.Scan(&u.ID, &u.Provider, &u.ProviderID, &u.DisplayName, &u.AvatarURL, &isPro, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	u.IsPro = isPro != 0
	return &u, nil
}

func (s *RelayStore) DeleteSession(token string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE token = ?", token)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// Magic link methods

func (s *RelayStore) CreateMagicLink(id, email, token string, expiresAt time.Time) error {
	_, err := s.db.Exec(
		"INSERT INTO magic_links (id, email, token, expires_at) VALUES (?, ?, ?, ?)",
		id, email, token, expiresAt.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return fmt.Errorf("create magic link: %w", err)
	}
	return nil
}

func (s *RelayStore) ConsumeMagicLink(token string) (string, error) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	var email string
	err = tx.QueryRow(
		"SELECT email FROM magic_links WHERE token = ? AND used = 0 AND expires_at > ?",
		token, now,
	).Scan(&email)
	if err != nil {
		tx.Rollback()
		return "", fmt.Errorf("invalid or expired magic link")
	}
	if _, err := tx.Exec("UPDATE magic_links SET used = 1 WHERE token = ?", token); err != nil {
		tx.Rollback()
		return "", fmt.Errorf("consume magic link: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return email, nil
}

// GetRelayConfig reads a value from the relay_config table.
func (s *RelayStore) GetRelayConfig(key string) (string, error) {
	var val string
	err := s.db.QueryRow("SELECT value FROM relay_config WHERE key = ?", key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get relay config %s: %w", key, err)
	}
	return val, nil
}

// SetRelayConfig writes a value to the relay_config table.
func (s *RelayStore) SetRelayConfig(key, value string) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO relay_config (key, value) VALUES (?, ?)",
		key, value,
	)
	if err != nil {
		return fmt.Errorf("set relay config %s: %w", key, err)
	}
	return nil
}

// CreateSocialUserDev creates a dev-mode social user if one doesn't exist.
func (s *RelayStore) CreateSocialUserDev() (*SocialUser, error) {
	u, err := s.GetSocialUserByProvider("dev", "dev")
	if err != nil {
		return nil, err
	}
	if u != nil {
		return u, nil
	}
	u = &SocialUser{
		ID:          "test-user",
		Provider:    "dev",
		ProviderID:  "dev",
		DisplayName: "dev",
	}
	if err := s.UpsertSocialUser(u); err != nil {
		return nil, err
	}
	return u, nil
}

// CreateLocalUser creates the local-mode user and a non-expiring device token.
// Returns the social user, device token string, and whether the user already existed.
func (s *RelayStore) CreateLocalUser() (*SocialUser, string, error) {
	const localUserID = "local"

	u, err := s.GetSocialUserByProvider("local", "local")
	if err != nil {
		return nil, "", err
	}
	if u == nil {
		u = &SocialUser{
			ID:          localUserID,
			Provider:    "local",
			ProviderID:  "local",
			DisplayName: "local",
		}
		if err := s.UpsertSocialUser(u); err != nil {
			return nil, "", err
		}
		// Also create a relay user for device token validation
		_ = s.CreateUser(localUserID)
	}

	// Check for existing device token
	var existing string
	err = s.db.QueryRow(
		"SELECT token FROM device_tokens WHERE user_id = ? AND device_id = 'local'",
		localUserID,
	).Scan(&existing)
	if err == nil {
		return u, existing, nil
	}

	// Create non-expiring device token
	token := generateToken()
	if err := s.CreateDeviceToken(token, localUserID, "local", nil); err != nil {
		return nil, "", fmt.Errorf("create local device token: %w", err)
	}
	return u, token, nil
}

// GetDeviceCodeByUserCode finds a device code by user_code.
func (s *RelayStore) GetDeviceCodeByUserCode(userCode string) (*DeviceCodeRow, error) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	row := s.db.QueryRow(
		"SELECT code, user_code, user_id, device_id, public_key, created_at, expires_at, claimed FROM device_codes WHERE user_code = ? AND expires_at > ?",
		userCode, now,
	)
	var dc DeviceCodeRow
	err := row.Scan(&dc.Code, &dc.UserCode, &dc.UserID, &dc.DeviceID, &dc.PublicKey, &dc.CreatedAt, &dc.ExpiresAt, &dc.Claimed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device code by user_code: %w", err)
	}
	return &dc, nil
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
