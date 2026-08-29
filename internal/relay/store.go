package relay

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type RelayStore struct {
	db *sql.DB
}

var (
	ErrActivePersonalSubscription = errors.New("user already has an active personal subscription")
	ErrActiveOrgSubscription      = errors.New("organization has an active subscription")
	ErrDeviceUserCodeExists       = errors.New("device user code already exists")
	ErrOrgInviteExists            = errors.New("organization invite already exists")
	ErrOrgLimitReached            = errors.New("organization ownership limit reached")
	ErrOrgMutationUnauthorized    = errors.New("organization mutation is not authorized")
	ErrOrgOwnerRemoval            = errors.New("organization owner cannot be removed")
	ErrOrgSeatsNotIncreased       = errors.New("organization seats were not increased")
)

const roostWingServiceUserID = "roost-wing-service"

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

// MCPClientRegistration is durable Dynamic Client Registration state. Claude Code keeps
// client IDs across MCP logout/remove operations, so registrations must outlive a roost restart.
type MCPClientRegistration struct {
	ClientID     string
	ClientName   string
	RedirectURIs []string
	ExpiresAt    time.Time
}

func (s *RelayStore) SaveMCPClientRegistration(reg MCPClientRegistration) error {
	redirectURIs, err := json.Marshal(reg.RedirectURIs)
	if err != nil {
		return fmt.Errorf("marshal MCP client redirect URIs: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO mcp_oauth_clients (client_id, client_name, redirect_uris, expires_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(client_id) DO UPDATE SET
		   client_name = excluded.client_name,
		   redirect_uris = excluded.redirect_uris,
		   expires_at = excluded.expires_at`,
		reg.ClientID, reg.ClientName, string(redirectURIs), reg.ExpiresAt.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return fmt.Errorf("save MCP client registration: %w", err)
	}
	return nil
}

// SaveMCPClientRegistrationLimited prunes expired registrations, checks the
// durable capacity, and installs the new public client in one transaction.
// Keeping these operations together prevents concurrent anonymous DCR calls
// from racing past the resource bound.
func (s *RelayStore) SaveMCPClientRegistrationLimited(reg MCPClientRegistration, now time.Time, limit int) (bool, error) {
	redirectURIs, err := json.Marshal(reg.RedirectURIs)
	if err != nil {
		return false, fmt.Errorf("marshal MCP client redirect URIs: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin MCP client registration: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }
	formattedNow := now.UTC().Format("2006-01-02 15:04:05")
	if _, err := tx.Exec("DELETE FROM mcp_oauth_clients WHERE expires_at <= ?", formattedNow); err != nil {
		rollback()
		return false, fmt.Errorf("delete expired MCP client registrations: %w", err)
	}
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM mcp_oauth_clients").Scan(&count); err != nil {
		rollback()
		return false, fmt.Errorf("count MCP client registrations: %w", err)
	}
	if limit <= 0 || count >= limit {
		rollback()
		return false, nil
	}
	if _, err := tx.Exec(
		`INSERT INTO mcp_oauth_clients (client_id, client_name, redirect_uris, expires_at)
		 VALUES (?, ?, ?, ?)`,
		reg.ClientID, reg.ClientName, string(redirectURIs), reg.ExpiresAt.UTC().Format("2006-01-02 15:04:05"),
	); err != nil {
		rollback()
		return false, fmt.Errorf("save MCP client registration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit MCP client registration: %w", err)
	}
	return true, nil
}

func (s *RelayStore) GetMCPClientRegistration(clientID string, now time.Time) (*MCPClientRegistration, error) {
	row := s.db.QueryRow(
		`SELECT client_id, client_name, redirect_uris, expires_at
		 FROM mcp_oauth_clients WHERE client_id = ? AND expires_at > ?`,
		clientID, now.UTC().Format("2006-01-02 15:04:05"),
	)
	var reg MCPClientRegistration
	var redirectURIs string
	if err := row.Scan(&reg.ClientID, &reg.ClientName, &redirectURIs, &reg.ExpiresAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get MCP client registration: %w", err)
	}
	if err := json.Unmarshal([]byte(redirectURIs), &reg.RedirectURIs); err != nil {
		return nil, fmt.Errorf("parse MCP client redirect URIs: %w", err)
	}
	return &reg, nil
}

func (s *RelayStore) CountMCPClientRegistrations(now time.Time) (int, error) {
	if _, err := s.db.Exec(
		"DELETE FROM mcp_oauth_clients WHERE expires_at <= ?",
		now.UTC().Format("2006-01-02 15:04:05"),
	); err != nil {
		return 0, fmt.Errorf("delete expired MCP client registrations: %w", err)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM mcp_oauth_clients").Scan(&count); err != nil {
		return 0, fmt.Errorf("count MCP client registrations: %w", err)
	}
	return count, nil
}

func OpenRelay(dsn string) (*RelayStore, error) {
	configuredDSN, err := configureRelayDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("configure db: %w", err)
	}
	db, err := sql.Open("sqlite", configuredDSN)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// Every connection to the special :memory: DSN otherwise gets an unrelated
	// database. A single connection preserves the caller's expected store.
	if dsn == ":memory:" {
		db.SetMaxOpenConns(1)
	}
	if err := execRelayPragmaWithBusyRetry(db, "PRAGMA journal_mode=WAL", 5*time.Second); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	s := &RelayStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func execRelayPragmaWithBusyRetry(db *sql.DB, statement string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		_, err := db.Exec(statement)
		if err == nil {
			return nil
		}
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != 5 || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// configureRelayDSN applies connection-scoped SQLite guarantees to every
// pooled connection, not just whichever connection happens to run OpenRelay's
// initial PRAGMAs. Immediate transactions make read-check-write store methods
// atomic across processes sharing the database.
func configureRelayDSN(dsn string) (string, error) {
	queryStart := strings.IndexByte(dsn, '?')
	query := ""
	separator := "?"
	if queryStart >= 0 {
		query = dsn[queryStart+1:]
		separator = "&"
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return "", fmt.Errorf("parse SQLite DSN query: %w", err)
	}

	params := url.Values{}
	params.Add("_pragma", "foreign_keys(1)")
	hasBusyTimeout := false
	for _, pragma := range values["_pragma"] {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(pragma)), "busy_timeout") {
			hasBusyTimeout = true
			break
		}
	}
	if !hasBusyTimeout {
		params.Add("_pragma", "busy_timeout(5000)")
	}
	if values.Get("_txlock") == "" {
		params.Set("_txlock", "immediate")
	}
	return dsn + separator + params.Encode(), nil
}

func (s *RelayStore) Close() error {
	return s.db.Close()
}

func (s *RelayStore) CreateUser(id string) error {
	_, err := s.db.Exec(
		"INSERT INTO users (id, provider, provider_id, display_name) VALUES (?, 'device', ?, ?)",
		id, id, id,
	)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func (s *RelayStore) CreateDeviceCode(code, userCode, deviceID string, expiresAt time.Time) error {
	return s.createDeviceCode(code, userCode, deviceID, "", expiresAt)
}

func (s *RelayStore) CreateDeviceCodeWithKey(code, userCode, deviceID, publicKey string, expiresAt time.Time) error {
	return s.createDeviceCode(code, userCode, deviceID, publicKey, expiresAt)

}

// createDeviceCode reserves the short, human-entered user code and stores the
// opaque device code in one immediate transaction. user_code is intentionally
// not a durable UNIQUE column because expired rows are retained for audit and
// the same short code may safely be reused after expiry.
func (s *RelayStore) createDeviceCode(code, userCode, deviceID, publicKey string, expiresAt time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin device code creation: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	var exists int
	if err := tx.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM device_codes WHERE user_code = ? AND expires_at > ?)",
		userCode, now,
	).Scan(&exists); err != nil {
		rollback()
		return fmt.Errorf("check device user code: %w", err)
	}
	if exists != 0 {
		rollback()
		return ErrDeviceUserCodeExists
	}
	if _, err := tx.Exec(
		"INSERT INTO device_codes (code, user_code, device_id, public_key, expires_at) VALUES (?, ?, ?, ?, ?)",
		code, userCode, deviceID, publicKey, expiresAt.UTC().Format("2006-01-02 15:04:05"),
	); err != nil {
		rollback()
		return fmt.Errorf("create device code: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit device code creation: %w", err)
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
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("count claimed device codes: %w", err)
	}
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

// ExchangeClaimedDeviceCode stores a token and consumes its approved device
// code in one immediate transaction. A device code is a one-time credential:
// concurrent or repeated polls must never mint multiple usable bearer tokens.
// If token creation fails, the transaction restores the device code so the
// client can safely retry.
func (s *RelayStore) ExchangeClaimedDeviceCode(code, token, userID, deviceID string, expiresAt *time.Time) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin device code exchange: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	result, err := tx.Exec(
		`DELETE FROM device_codes
		 WHERE code = ? AND claimed = 1 AND user_id = ? AND device_id = ? AND expires_at > ?`,
		code, userID, deviceID, now,
	)
	if err != nil {
		rollback()
		return false, fmt.Errorf("consume device code: %w", err)
	}
	consumed, err := result.RowsAffected()
	if err != nil {
		rollback()
		return false, fmt.Errorf("count consumed device codes: %w", err)
	}
	if consumed != 1 {
		rollback()
		return false, nil
	}
	if _, err := tx.Exec(
		"INSERT INTO device_tokens (token, user_id, device_id, expires_at) VALUES (?, ?, ?, ?)",
		token, userID, deviceID, expiresAt,
	); err != nil {
		rollback()
		return false, fmt.Errorf("create device token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit device code exchange: %w", err)
	}
	return true, nil
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

// RotateDeviceToken replaces an existing token without creating a window in
// which neither the old nor new credential exists.
func (s *RelayStore) RotateDeviceToken(oldToken, newToken, userID, deviceID string, expiresAt *time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin token rotation: %w", err)
	}
	result, err := tx.Exec(
		"DELETE FROM device_tokens WHERE token = ? AND user_id = ? AND device_id = ?",
		oldToken, userID, deviceID,
	)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete old device token: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("count deleted device tokens: %w", err)
	}
	if deleted != 1 {
		_ = tx.Rollback()
		return fmt.Errorf("old device token is no longer valid")
	}
	if _, err := tx.Exec(
		"INSERT INTO device_tokens (token, user_id, device_id, expires_at) VALUES (?, ?, ?, ?)",
		newToken, userID, deviceID, expiresAt,
	); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("create replacement device token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit token rotation: %w", err)
	}
	return nil
}

// Session methods

func (s *RelayStore) CreateSession(token, userID string, expiresAt time.Time) error {
	_, err := s.db.Exec(
		"INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)",
		token, userID, expiresAt.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (s *RelayStore) GetSession(token string) (*User, error) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	row := s.db.QueryRow(
		`SELECT u.id, u.provider, u.provider_id, u.display_name, u.avatar_url, u.email, u.tier, u.is_pro, u.created_at
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token = ? AND s.expires_at > ?`,
		token, now,
	)
	var u User
	var isPro int
	err := row.Scan(&u.ID, &u.Provider, &u.ProviderID, &u.DisplayName, &u.AvatarURL, &u.Email, &u.Tier, &isPro, &u.CreatedAt)
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
	var email string
	err := s.db.QueryRow(
		`UPDATE magic_links SET used = 1
		 WHERE token = ? AND used = 0 AND expires_at > ?
		 RETURNING email`,
		token, now,
	).Scan(&email)
	if err != nil {
		// Keep the public failure deliberately indistinguishable across unknown,
		// expired, and already-used tokens. The conditional UPDATE is the
		// one-shot boundary: concurrent consumers cannot both return an email.
		return "", fmt.Errorf("invalid or expired magic link")
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

// CreateUserDev creates a dev-mode social user if one doesn't exist.
func (s *RelayStore) CreateUserDev() (*User, error) {
	u, err := s.GetUserByProvider("dev", "dev")
	if err != nil {
		return nil, err
	}
	if u != nil {
		return u, nil
	}
	u = &User{
		ID:          "test-user",
		Provider:    "dev",
		ProviderID:  "dev",
		DisplayName: "dev",
	}
	if err := s.UpsertUser(u); err != nil {
		return nil, err
	}
	return u, nil
}

// CreateLocalUser creates the local-mode user and a non-expiring device token.
// Repeated and concurrent calls return the same token.
func (s *RelayStore) CreateLocalUser() (*User, string, error) {
	return s.createBuiltinUser("local", "local", "local", "local", "local")
}

// CreateServiceUser creates a service user for the roost wing goroutine.
// Idempotent: returns existing user + token if already created.
func (s *RelayStore) CreateServiceUser() (*User, string, error) {
	return s.createBuiltinUser(roostWingServiceUserID, "service", "roost-wing", "roost-wing", "roost-wing")
}

func (s *RelayStore) createBuiltinUser(id, provider, providerID, displayName, deviceID string) (*User, string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, "", fmt.Errorf("begin built-in user creation: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }

	var u User
	var isPro int
	err = tx.QueryRow(
		`SELECT id, provider, provider_id, display_name, avatar_url, email, tier, is_pro, created_at
		 FROM users WHERE provider = ? AND provider_id = ?`,
		provider, providerID,
	).Scan(&u.ID, &u.Provider, &u.ProviderID, &u.DisplayName, &u.AvatarURL, &u.Email, &u.Tier, &isPro, &u.CreatedAt)
	if err == sql.ErrNoRows {
		u = User{ID: id, Provider: provider, ProviderID: providerID, DisplayName: displayName}
		if _, err := tx.Exec(
			`INSERT INTO users (id, provider, provider_id, display_name)
			 VALUES (?, ?, ?, ?)`,
			u.ID, u.Provider, u.ProviderID, u.DisplayName,
		); err != nil {
			rollback()
			return nil, "", fmt.Errorf("create built-in user: %w", err)
		}
	} else if err != nil {
		rollback()
		return nil, "", fmt.Errorf("get built-in user: %w", err)
	} else {
		u.IsPro = isPro != 0
	}

	var token string
	err = tx.QueryRow(
		`SELECT token FROM device_tokens
		 WHERE user_id = ? AND device_id = ?
		 ORDER BY created_at, token LIMIT 1`,
		u.ID, deviceID,
	).Scan(&token)
	if err == sql.ErrNoRows {
		token = generateToken()
		if _, err := tx.Exec(
			"INSERT INTO device_tokens (token, user_id, device_id) VALUES (?, ?, ?)",
			token, u.ID, deviceID,
		); err != nil {
			rollback()
			return nil, "", fmt.Errorf("create built-in device token: %w", err)
		}
	} else if err != nil {
		rollback()
		return nil, "", fmt.Errorf("get built-in device token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, "", fmt.Errorf("commit built-in user creation: %w", err)
	}
	return &u, token, nil
}

// GetDeviceCodeByUserCode finds a device code by user_code.
func (s *RelayStore) GetDeviceCodeByUserCode(userCode string) (*DeviceCodeRow, error) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	rows, err := s.db.Query(
		"SELECT code, user_code, user_id, device_id, public_key, created_at, expires_at, claimed FROM device_codes WHERE user_code = ? AND expires_at > ? LIMIT 2",
		userCode, now,
	)
	if err != nil {
		return nil, fmt.Errorf("get device code by user_code: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get device code by user_code: %w", err)
		}
		return nil, nil
	}
	var dc DeviceCodeRow
	if err := rows.Scan(&dc.Code, &dc.UserCode, &dc.UserID, &dc.DeviceID, &dc.PublicKey, &dc.CreatedAt, &dc.ExpiresAt, &dc.Claimed); err != nil {
		return nil, fmt.Errorf("get device code by user_code: %w", err)
	}
	if rows.Next() {
		return nil, fmt.Errorf("ambiguous device user code")
	}
	if err := rows.Err(); err != nil {
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

// User represents an authenticated user (via OAuth, magic link, or local mode).
type User struct {
	ID          string
	Provider    string
	ProviderID  string
	DisplayName string
	AvatarURL   *string
	Email       *string
	Tier        string // "free", "pro", "team"
	IsPro       bool
	CreatedAt   time.Time
	OrgIDs      []string          // transient: populated by session cache on edge nodes
	OrgRoles    map[string]string // transient: org ID to authenticated role on edge nodes
}

// Org represents an organization that can share wings among members.
type Org struct {
	ID          string
	Name        string
	Slug        string
	OwnerUserID string
	MaxSeats    int
	CreatedAt   time.Time
}

// OrgMember represents a user's membership in an org.
type OrgMember struct {
	OrgID     string
	UserID    string
	Role      string // "owner", "admin", "member"
	CreatedAt time.Time
}

// OrgInvite represents a pending invite to an org.
type OrgInvite struct {
	ID        string
	OrgID     string
	Email     string
	Token     string
	InvitedBy string
	Role      string
	CreatedAt time.Time
	ClaimedAt *time.Time
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *RelayStore) UpsertUser(u *User) error {
	_, err := s.db.Exec(
		`INSERT INTO users (id, provider, provider_id, display_name, avatar_url, is_pro)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   display_name = excluded.display_name,
		   avatar_url = excluded.avatar_url,
		   is_pro = excluded.is_pro`,
		u.ID, u.Provider, u.ProviderID, u.DisplayName, u.AvatarURL, boolToInt(u.IsPro),
	)
	if err != nil {
		return fmt.Errorf("upsert social user: %w", err)
	}
	return nil
}

func (s *RelayStore) GetUserByProvider(provider, providerID string) (*User, error) {
	row := s.db.QueryRow(
		"SELECT id, provider, provider_id, display_name, avatar_url, email, tier, is_pro, created_at FROM users WHERE provider = ? AND provider_id = ?",
		provider, providerID,
	)
	var u User
	var isPro int
	err := row.Scan(&u.ID, &u.Provider, &u.ProviderID, &u.DisplayName, &u.AvatarURL, &u.Email, &u.Tier, &isPro, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get social user by provider: %w", err)
	}
	u.IsPro = isPro != 0
	return &u, nil
}

func (s *RelayStore) GetOrCreateUserByEmail(email string) (*User, error) {
	u, err := s.GetUserByProvider("email", email)
	if err != nil {
		return nil, err
	}
	if u != nil {
		return u, nil
	}
	u = &User{
		ID:          generateToken(),
		Provider:    "email",
		ProviderID:  email,
		DisplayName: email,
	}
	if err := s.UpsertUser(u); err != nil {
		return nil, err
	}
	return u, nil
}

// GetUserByID returns a user by their ID.
func (s *RelayStore) GetUserByID(id string) (*User, error) {
	row := s.db.QueryRow(
		"SELECT id, provider, provider_id, display_name, avatar_url, email, tier, is_pro, created_at FROM users WHERE id = ?",
		id,
	)
	var u User
	var isPro int
	err := row.Scan(&u.ID, &u.Provider, &u.ProviderID, &u.DisplayName, &u.AvatarURL, &u.Email, &u.Tier, &isPro, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get social user by id: %w", err)
	}
	u.IsPro = isPro != 0
	return &u, nil
}

// UpdateUserEmail sets the email for a user.
func (s *RelayStore) UpdateUserEmail(userID, email string) error {
	_, err := s.db.Exec("UPDATE users SET email = ? WHERE id = ?", email, userID)
	return err
}

// ClearUserEmail removes a provider address that can no longer be verified.
// This is authorization state when a private roost has an enrollment list, so
// callers must not leave a previously allowed address in place after a denied
// provider callback.
func (s *RelayStore) ClearUserEmail(userID string) error {
	_, err := s.db.Exec("UPDATE users SET email = NULL WHERE id = ?", userID)
	return err
}

// UpdateUserTier sets the tier for a user.
func (s *RelayStore) UpdateUserTier(userID, tier string) error {
	_, err := s.db.Exec("UPDATE users SET tier = ? WHERE id = ?", tier, userID)
	return err
}

// GetUserByEmail returns a user by email.
func (s *RelayStore) GetUserByEmail(email string) (*User, error) {
	row := s.db.QueryRow(
		"SELECT id, provider, provider_id, display_name, avatar_url, email, tier, is_pro, created_at FROM users WHERE email = ?",
		email,
	)
	var u User
	var isPro int
	err := row.Scan(&u.ID, &u.Provider, &u.ProviderID, &u.DisplayName, &u.AvatarURL, &u.Email, &u.Tier, &isPro, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get social user by email: %w", err)
	}
	u.IsPro = isPro != 0
	return &u, nil
}

// --- Org CRUD ---

// CreateOrg creates a one-seat org and adds the owner as an owner member.
func (s *RelayStore) CreateOrg(id, name, slug, ownerUserID string) error {
	return s.CreateOrgWithSeats(id, name, slug, ownerUserID, 1)
}

// CreateOrgWithSeats creates an org and its owner membership atomically.
func (s *RelayStore) CreateOrgWithSeats(id, name, slug, ownerUserID string, maxSeats int) error {
	if maxSeats < 1 {
		return fmt.Errorf("max seats must be at least 1")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	_, err = tx.Exec(
		"INSERT INTO orgs (id, name, slug, owner_user_id, max_seats) VALUES (?, ?, ?, ?, ?)",
		id, name, slug, ownerUserID, maxSeats,
	)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("create org: %w", err)
	}
	_, err = tx.Exec(
		"INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'owner')",
		id, ownerUserID,
	)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("add owner member: %w", err)
	}
	return tx.Commit()
}

// CreateOrgForOwnerLimited creates an organization only if the owner remains
// below limit. The count and inserts share one immediate transaction so
// concurrent requests cannot exceed the per-owner product limit.
func (s *RelayStore) CreateOrgForOwnerLimited(id, name, slug, ownerUserID string, maxSeats, limit int) error {
	if maxSeats < 1 {
		return fmt.Errorf("max seats must be at least 1")
	}
	if limit < 1 {
		return fmt.Errorf("organization limit must be at least 1")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin limited org creation: %w", err)
	}
	var owned int
	if err := tx.QueryRow("SELECT COUNT(*) FROM orgs WHERE owner_user_id = ?", ownerUserID).Scan(&owned); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("count owned organizations: %w", err)
	}
	if owned >= limit {
		_ = tx.Rollback()
		return ErrOrgLimitReached
	}
	if _, err := tx.Exec(
		"INSERT INTO orgs (id, name, slug, owner_user_id, max_seats) VALUES (?, ?, ?, ?, ?)",
		id, name, slug, ownerUserID, maxSeats,
	); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("create org: %w", err)
	}
	if _, err := tx.Exec(
		"INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'owner')",
		id, ownerUserID,
	); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("add owner member: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit limited org creation: %w", err)
	}
	return nil
}

// DeleteOrg removes an org and all its members, invites, and scoped labels.
// The active-subscription check lives in the same immediate transaction as the
// deletion so callers cannot race an upgrade between a preflight check and the
// destructive write.
func (s *RelayStore) DeleteOrg(orgID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	var activeSubscriptions int
	if err := tx.QueryRow(
		"SELECT COUNT(*) FROM subscriptions WHERE org_id = ? AND status = 'active'", orgID,
	).Scan(&activeSubscriptions); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("check active org subscriptions: %w", err)
	}
	if activeSubscriptions > 0 {
		_ = tx.Rollback()
		return ErrActiveOrgSubscription
	}
	if _, err := tx.Exec("DELETE FROM org_invites WHERE org_id = ?", orgID); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete org invites: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM org_members WHERE org_id = ?", orgID); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete org members: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM labels WHERE scope_type = 'org' AND scope_id = ?", orgID); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete org labels: %w", err)
	}
	_, err = tx.Exec("DELETE FROM orgs WHERE id = ?", orgID)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete org: %w", err)
	}
	return tx.Commit()
}

// GetOrgBySlug returns an org by slug.
func (s *RelayStore) GetOrgBySlug(slug string) (*Org, error) {
	row := s.db.QueryRow(
		"SELECT id, name, slug, owner_user_id, max_seats, created_at FROM orgs WHERE slug = ?",
		slug,
	)
	var o Org
	err := row.Scan(&o.ID, &o.Name, &o.Slug, &o.OwnerUserID, &o.MaxSeats, &o.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get org by slug: %w", err)
	}
	return &o, nil
}

// GetOrgByID returns an org by ID.
func (s *RelayStore) GetOrgByID(id string) (*Org, error) {
	row := s.db.QueryRow(
		"SELECT id, name, slug, owner_user_id, max_seats, created_at FROM orgs WHERE id = ?",
		id,
	)
	var o Org
	err := row.Scan(&o.ID, &o.Name, &o.Slug, &o.OwnerUserID, &o.MaxSeats, &o.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get org by id: %w", err)
	}
	return &o, nil
}

// ResolveOrg resolves an org reference (ID or slug) for a specific user.
// It tries ID first, then slug. If multiple orgs match the slug, it returns
// an error listing the ambiguous matches.
func (s *RelayStore) ResolveOrg(ref, userID string) (*Org, error) {
	// Try by ID first — but only if the user is a member
	org, err := s.GetOrgByID(ref)
	if err != nil {
		return nil, err
	}
	if org != nil {
		if s.IsOrgMember(org.ID, userID) {
			return org, nil
		}
		return nil, nil // org exists but user is not a member
	}

	// Try by slug — find all orgs with this slug that the user belongs to
	rows, err := s.db.Query(
		`SELECT o.id, o.name, o.slug, o.owner_user_id, o.max_seats, o.created_at
		 FROM orgs o JOIN org_members m ON o.id = m.org_id
		 WHERE o.slug = ? AND m.user_id = ?`, ref, userID)
	if err != nil {
		return nil, fmt.Errorf("resolve org: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var matches []*Org
	for rows.Next() {
		var o Org
		if err := rows.Scan(&o.ID, &o.Name, &o.Slug, &o.OwnerUserID, &o.MaxSeats, &o.CreatedAt); err != nil {
			return nil, fmt.Errorf("resolve org scan: %w", err)
		}
		matches = append(matches, &o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("resolve org rows: %w", err)
	}

	if len(matches) == 0 {
		return nil, nil
	}
	if len(matches) == 1 {
		return matches[0], nil
	}

	// Ambiguous — build helpful error
	msg := fmt.Sprintf("ambiguous org %q, did you mean:", ref)
	for _, m := range matches {
		msg += fmt.Sprintf("\n  %s: %s", m.Name, m.ID)
	}
	return nil, fmt.Errorf("%s", msg)
}

// ListOrgsForUser returns all orgs a user belongs to.
func (s *RelayStore) ListOrgsForUser(userID string) ([]*Org, error) {
	rows, err := s.db.Query(
		`SELECT o.id, o.name, o.slug, o.owner_user_id, o.max_seats, o.created_at
		 FROM orgs o JOIN org_members m ON o.id = m.org_id
		 WHERE m.user_id = ? ORDER BY o.name`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list orgs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []*Org
	for rows.Next() {
		var o Org
		if err := rows.Scan(&o.ID, &o.Name, &o.Slug, &o.OwnerUserID, &o.MaxSeats, &o.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, &o)
	}
	return result, rows.Err()
}

// AddOrgMember adds a user to an org, enforcing max_seats.
func (s *RelayStore) AddOrgMember(orgID, userID, role string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	var existingRole string
	err = tx.QueryRow(
		"SELECT role FROM org_members WHERE org_id = ? AND user_id = ?", orgID, userID,
	).Scan(&existingRole)
	if err == nil {
		return tx.Commit()
	}
	if err != sql.ErrNoRows {
		_ = tx.Rollback()
		return fmt.Errorf("check org member: %w", err)
	}
	var count, maxSeats int
	err = tx.QueryRow("SELECT COUNT(*) FROM org_members WHERE org_id = ?", orgID).Scan(&count)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	err = tx.QueryRow("SELECT max_seats FROM orgs WHERE id = ?", orgID).Scan(&maxSeats)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if count >= maxSeats {
		_ = tx.Rollback()
		return fmt.Errorf("org has reached max seats (%d)", maxSeats)
	}
	_, err = tx.Exec(
		"INSERT OR IGNORE INTO org_members (org_id, user_id, role) VALUES (?, ?, ?)",
		orgID, userID, role,
	)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("add org member: %w", err)
	}
	return tx.Commit()
}

// RemoveOrgMemberAndEntitlement removes a membership and any grant from the
// org's active subscription in one transaction. It returns whether a grant was
// removed so callers can invalidate cached entitlement state.
func (s *RelayStore) RemoveOrgMemberAndEntitlement(orgID, userID string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin org member removal: %w", err)
	}
	revoked, err := removeOrgMemberAndEntitlementTx(tx, orgID, userID)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit org member removal: %w", err)
	}
	return revoked, nil
}

// RemoveOrgMemberAuthorized rechecks the actor's current organization role in
// the same immediate transaction as the removal. This prevents a cached or
// preflight admin role from surviving a concurrent demotion.
func (s *RelayStore) RemoveOrgMemberAuthorized(orgID, actorUserID, targetUserID string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin authorized org member removal: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }

	var ownerUserID string
	if err := tx.QueryRow("SELECT owner_user_id FROM orgs WHERE id = ?", orgID).Scan(&ownerUserID); err != nil {
		rollback()
		if err == sql.ErrNoRows {
			return false, ErrOrgMutationUnauthorized
		}
		return false, fmt.Errorf("load organization owner: %w", err)
	}
	if targetUserID == ownerUserID {
		rollback()
		return false, ErrOrgOwnerRemoval
	}
	if actorUserID != targetUserID {
		authorized, err := orgManagerAuthorizedInTx(tx, orgID, actorUserID)
		if err != nil {
			rollback()
			return false, err
		}
		if !authorized {
			rollback()
			return false, ErrOrgMutationUnauthorized
		}
	}

	revoked, err := removeOrgMemberAndEntitlementTx(tx, orgID, targetUserID)
	if err != nil {
		rollback()
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit authorized org member removal: %w", err)
	}
	return revoked, nil
}

func removeOrgMemberAndEntitlementTx(tx *sql.Tx, orgID, userID string) (bool, error) {
	if _, err := tx.Exec("DELETE FROM org_members WHERE org_id = ? AND user_id = ?", orgID, userID); err != nil {
		return false, fmt.Errorf("remove org member: %w", err)
	}
	var subID string
	err := tx.QueryRow(
		"SELECT id FROM subscriptions WHERE org_id = ? AND status = 'active'", orgID,
	).Scan(&subID)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load org subscription: %w", err)
	}
	result, err := tx.Exec("DELETE FROM entitlements WHERE user_id = ? AND subscription_id = ?", userID, subID)
	if err != nil {
		return false, fmt.Errorf("delete org entitlement: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count removed org entitlements: %w", err)
	}
	tier, err := userTierInTx(tx, userID)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec("UPDATE users SET tier = ? WHERE id = ?", tier, userID); err != nil {
		return false, fmt.Errorf("update user tier: %w", err)
	}
	return removed > 0, nil
}

func orgManagerAuthorizedInTx(tx *sql.Tx, orgID, userID string) (bool, error) {
	var authorized int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM org_members
		 WHERE org_id = ? AND user_id = ? AND role IN ('owner', 'admin')`,
		orgID, userID,
	).Scan(&authorized); err != nil {
		return false, fmt.Errorf("authorize organization manager: %w", err)
	}
	return authorized == 1, nil
}

// ListOrgMembers returns all members of an org with their user info.
func (s *RelayStore) ListOrgMembers(orgID string) ([]*OrgMember, error) {
	rows, err := s.db.Query(
		"SELECT org_id, user_id, role, created_at FROM org_members WHERE org_id = ? ORDER BY created_at",
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list org members: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []*OrgMember
	for rows.Next() {
		var m OrgMember
		if err := rows.Scan(&m.OrgID, &m.UserID, &m.Role, &m.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, &m)
	}
	return result, rows.Err()
}

// IsOrgMember returns true if the user is a member of the org.
func (s *RelayStore) IsOrgMember(orgID, userID string) bool {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = ?", orgID, userID).Scan(&count)
	return err == nil && count > 0
}

// GetOrgMemberRole returns the role of a user in an org, or "" if not a member.
func (s *RelayStore) GetOrgMemberRole(orgID, userID string) string {
	var role string
	err := s.db.QueryRow("SELECT role FROM org_members WHERE org_id = ? AND user_id = ?", orgID, userID).Scan(&role)
	if err != nil {
		return ""
	}
	return role
}

// CreateOrgInvite creates a pending invite.
func (s *RelayStore) CreateOrgInvite(id, orgID, email, token, invitedBy, role string) error {
	if role == "" {
		role = "member"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin org invite: %w", err)
	}
	authorized, err := orgManagerAuthorizedInTx(tx, orgID, invitedBy)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if !authorized {
		_ = tx.Rollback()
		return ErrOrgMutationUnauthorized
	}
	var existing int
	if err := tx.QueryRow(
		"SELECT COUNT(*) FROM org_invites WHERE org_id = ? AND email = ?", orgID, email,
	).Scan(&existing); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("check existing org invite: %w", err)
	}
	if existing > 0 {
		_ = tx.Rollback()
		return ErrOrgInviteExists
	}
	_, err = tx.Exec(
		"INSERT INTO org_invites (id, org_id, email, token, invited_by, role) VALUES (?, ?, ?, ?, ?, ?)",
		id, orgID, email, token, invitedBy, role,
	)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("create org invite: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit org invite: %w", err)
	}
	return nil
}

// ConsumeOrgInvite validates a token, marks it claimed, returns email, orgID, and role.
func (s *RelayStore) ConsumeOrgInvite(token string) (string, string, string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", "", "", fmt.Errorf("begin tx: %w", err)
	}
	var email, orgID, role string
	err = tx.QueryRow(
		"SELECT email, org_id, role FROM org_invites WHERE token = ? AND claimed_at IS NULL",
		token,
	).Scan(&email, &orgID, &role)
	if err != nil {
		_ = tx.Rollback()
		return "", "", "", fmt.Errorf("invalid or already claimed invite")
	}
	result, err := tx.Exec(
		"UPDATE org_invites SET claimed_at = datetime('now') WHERE token = ? AND claimed_at IS NULL",
		token,
	)
	if err != nil {
		_ = tx.Rollback()
		return "", "", "", fmt.Errorf("consume invite: %w", err)
	}
	consumed, err := result.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return "", "", "", fmt.Errorf("count consumed invites: %w", err)
	}
	if consumed != 1 {
		_ = tx.Rollback()
		return "", "", "", fmt.Errorf("invalid or already claimed invite")
	}
	if err := tx.Commit(); err != nil {
		return "", "", "", err
	}
	return email, orgID, role, nil
}

// AcceptOrgInvite consumes an invite, installs the membership, and grants an
// available seat from the org's active subscription in one transaction. A
// full org or failed entitlement grant must not burn the invite or leave a
// partially installed membership.
func (s *RelayStore) AcceptOrgInvite(token, userID, expectedEmail, entitlementID string) (string, string, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", "", false, fmt.Errorf("begin invite acceptance: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }
	var email, orgID, role string
	if err := tx.QueryRow(
		"SELECT email, org_id, role FROM org_invites WHERE token = ? AND claimed_at IS NULL", token,
	).Scan(&email, &orgID, &role); err != nil {
		rollback()
		return "", "", false, fmt.Errorf("invalid or already claimed invite")
	}
	if !strings.EqualFold(email, expectedEmail) {
		rollback()
		return "", "", false, fmt.Errorf("invite email mismatch")
	}
	var alreadyMember int
	if err := tx.QueryRow(
		"SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = ?", orgID, userID,
	).Scan(&alreadyMember); err != nil {
		rollback()
		return "", "", false, fmt.Errorf("check existing org membership: %w", err)
	}
	if alreadyMember == 0 {
		var count, maxSeats int
		if err := tx.QueryRow("SELECT COUNT(*) FROM org_members WHERE org_id = ?", orgID).Scan(&count); err != nil {
			rollback()
			return "", "", false, fmt.Errorf("count org members: %w", err)
		}
		if err := tx.QueryRow("SELECT max_seats FROM orgs WHERE id = ?", orgID).Scan(&maxSeats); err != nil {
			rollback()
			return "", "", false, fmt.Errorf("load org seat limit: %w", err)
		}
		if count >= maxSeats {
			rollback()
			return "", "", false, fmt.Errorf("org has reached max seats (%d)", maxSeats)
		}
		if _, err := tx.Exec(
			"INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, ?)", orgID, userID, role,
		); err != nil {
			rollback()
			return "", "", false, fmt.Errorf("add org member: %w", err)
		}
	}
	granted := false
	var subID string
	var seats int
	err = tx.QueryRow(
		"SELECT id, seats FROM subscriptions WHERE org_id = ? AND status = 'active'", orgID,
	).Scan(&subID, &seats)
	if err != nil && err != sql.ErrNoRows {
		rollback()
		return "", "", false, fmt.Errorf("load org subscription: %w", err)
	}
	if err == nil {
		var used, alreadyEntitled int
		if err := tx.QueryRow("SELECT COUNT(*) FROM entitlements WHERE subscription_id = ?", subID).Scan(&used); err != nil {
			rollback()
			return "", "", false, fmt.Errorf("count org entitlements: %w", err)
		}
		if err := tx.QueryRow(
			"SELECT COUNT(*) FROM entitlements WHERE subscription_id = ? AND user_id = ?", subID, userID,
		).Scan(&alreadyEntitled); err != nil {
			rollback()
			return "", "", false, fmt.Errorf("check org entitlement: %w", err)
		}
		if alreadyEntitled == 0 && used < seats {
			if entitlementID == "" {
				rollback()
				return "", "", false, fmt.Errorf("entitlement ID is required")
			}
			if _, err := tx.Exec(
				"INSERT INTO entitlements (id, user_id, subscription_id) VALUES (?, ?, ?)", entitlementID, userID, subID,
			); err != nil {
				rollback()
				return "", "", false, fmt.Errorf("grant org entitlement: %w", err)
			}
			if _, err := tx.Exec("UPDATE users SET tier = 'pro' WHERE id = ?", userID); err != nil {
				rollback()
				return "", "", false, fmt.Errorf("update user tier: %w", err)
			}
			granted = true
		}
	}
	result, err := tx.Exec("UPDATE org_invites SET claimed_at = datetime('now') WHERE token = ? AND claimed_at IS NULL", token)
	if err != nil {
		rollback()
		return "", "", false, fmt.Errorf("consume invite: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		rollback()
		return "", "", false, fmt.Errorf("invite was already consumed")
	}
	if err := tx.Commit(); err != nil {
		return "", "", false, fmt.Errorf("commit invite acceptance: %w", err)
	}
	return orgID, role, granted, nil
}

// GetInviteByToken returns an invite by its token (claimed or not).
func (s *RelayStore) GetInviteByToken(token string) (*OrgInvite, error) {
	row := s.db.QueryRow(
		"SELECT id, org_id, email, token, invited_by, role, created_at, claimed_at FROM org_invites WHERE token = ?",
		token,
	)
	var inv OrgInvite
	err := row.Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Token, &inv.InvitedBy, &inv.Role, &inv.CreatedAt, &inv.ClaimedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get invite by token: %w", err)
	}
	return &inv, nil
}

// RevokeOrgInvite deletes a pending invite only when it belongs to orgID. It
// returns false when the token is absent, already claimed, or belongs to a
// different org so callers cannot authorize one tenant and mutate another.
func (s *RelayStore) RevokeOrgInvite(orgID, token string) (bool, error) {
	result, err := s.db.Exec(
		"DELETE FROM org_invites WHERE org_id = ? AND token = ? AND claimed_at IS NULL",
		orgID, token,
	)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

// RevokeOrgInviteAuthorized rechecks the actor's current role in the same
// transaction as the tenant-scoped delete.
func (s *RelayStore) RevokeOrgInviteAuthorized(orgID, token, actorUserID string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin authorized invite revocation: %w", err)
	}
	authorized, err := orgManagerAuthorizedInTx(tx, orgID, actorUserID)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if !authorized {
		_ = tx.Rollback()
		return false, ErrOrgMutationUnauthorized
	}
	result, err := tx.Exec(
		"DELETE FROM org_invites WHERE org_id = ? AND token = ? AND claimed_at IS NULL",
		orgID, token,
	)
	if err != nil {
		_ = tx.Rollback()
		return false, fmt.Errorf("revoke organization invite: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return false, fmt.Errorf("count revoked organization invites: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit authorized invite revocation: %w", err)
	}
	return rows == 1, nil
}

// ListPendingInvites returns unclaimed invites for an org.
func (s *RelayStore) ListPendingInvites(orgID string) ([]*OrgInvite, error) {
	rows, err := s.db.Query(
		"SELECT id, org_id, email, token, invited_by, role, created_at FROM org_invites WHERE org_id = ? AND claimed_at IS NULL ORDER BY created_at",
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list pending invites: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []*OrgInvite
	for rows.Next() {
		var inv OrgInvite
		if err := rows.Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Token, &inv.InvitedBy, &inv.Role, &inv.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, &inv)
	}
	return result, rows.Err()
}

// SetOrgMaxSeats updates the max seat count for an org.
func (s *RelayStore) SetOrgMaxSeats(orgID string, seats int) error {
	_, err := s.db.Exec("UPDATE orgs SET max_seats = ? WHERE id = ?", seats, orgID)
	return err
}

// UpdateSubscriptionSeats updates the seat count on a subscription.
func (s *RelayStore) UpdateSubscriptionSeats(subID string, seats int) error {
	_, err := s.db.Exec("UPDATE subscriptions SET seats = ?, updated_at = datetime('now') WHERE id = ?", seats, subID)
	return err
}

// CountOrgsOwnedByUser returns the number of orgs a user has created.
func (s *RelayStore) CountOrgsOwnedByUser(userID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM orgs WHERE owner_user_id = ?", userID).Scan(&count)
	return count, err
}

// CountOrgMembers returns the number of members in an org.
func (s *RelayStore) CountOrgMembers(orgID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM org_members WHERE org_id = ?", orgID).Scan(&count)
	return count, err
}

// --- Subscriptions + Entitlements ---

type Subscription struct {
	ID                   string
	UserID               *string
	OrgID                *string
	Plan                 string
	Status               string
	Seats                int
	StripeSubscriptionID *string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type Entitlement struct {
	ID             string
	UserID         string
	SubscriptionID string
	CreatedAt      time.Time
}

func (s *RelayStore) CreateSubscription(sub *Subscription) error {
	_, err := s.db.Exec(
		`INSERT INTO subscriptions (id, user_id, org_id, plan, status, seats, stripe_subscription_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sub.ID, sub.UserID, sub.OrgID, sub.Plan, sub.Status, sub.Seats, sub.StripeSubscriptionID,
	)
	return err
}

func (s *RelayStore) GetActivePersonalSubscription(userID string) (*Subscription, error) {
	row := s.db.QueryRow(
		`SELECT id, user_id, org_id, plan, status, seats, stripe_subscription_id, created_at, updated_at
		 FROM subscriptions
		 WHERE user_id = ? AND org_id IS NULL AND status = 'active'
		 ORDER BY created_at, id LIMIT 1`,
		userID,
	)
	var sub Subscription
	err := row.Scan(&sub.ID, &sub.UserID, &sub.OrgID, &sub.Plan, &sub.Status, &sub.Seats, &sub.StripeSubscriptionID, &sub.CreatedAt, &sub.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get personal subscription: %w", err)
	}
	return &sub, nil
}

func (s *RelayStore) GetActiveOrgSubscription(orgID string) (*Subscription, error) {
	row := s.db.QueryRow(
		`SELECT id, user_id, org_id, plan, status, seats, stripe_subscription_id, created_at, updated_at
		 FROM subscriptions
		 WHERE org_id = ? AND status = 'active'
		 ORDER BY created_at, id LIMIT 1`,
		orgID,
	)
	var sub Subscription
	err := row.Scan(&sub.ID, &sub.UserID, &sub.OrgID, &sub.Plan, &sub.Status, &sub.Seats, &sub.StripeSubscriptionID, &sub.CreatedAt, &sub.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get org subscription: %w", err)
	}
	return &sub, nil
}

func (s *RelayStore) UpdateSubscriptionStatus(subID, status string) error {
	_, err := s.db.Exec("UPDATE subscriptions SET status = ?, updated_at = datetime('now') WHERE id = ?", status, subID)
	return err
}

func (s *RelayStore) CreateEntitlement(ent *Entitlement) error {
	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO entitlements (id, user_id, subscription_id) VALUES (?, ?, ?)",
		ent.ID, ent.UserID, ent.SubscriptionID,
	)
	return err
}

// ActivateSubscription installs a subscription, its first entitlement, and
// the denormalized user tier as one transaction. Callers must not expose a
// successful upgrade if only part of that state reached disk.
func (s *RelayStore) ActivateSubscription(sub *Subscription, ent *Entitlement) error {
	if sub == nil || ent == nil || sub.UserID == nil || *sub.UserID == "" || sub.OrgID != nil {
		return fmt.Errorf("personal subscription and entitlement must identify one user")
	}
	if sub.ID == "" || sub.Plan == "" || sub.Status != "active" || sub.Seats < 1 {
		return fmt.Errorf("personal subscription must be a valid active plan")
	}
	if ent.UserID != *sub.UserID || ent.SubscriptionID != sub.ID || ent.ID == "" {
		return fmt.Errorf("personal subscription and entitlement do not match")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin subscription activation: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }
	var active int
	if err := tx.QueryRow(
		"SELECT COUNT(*) FROM subscriptions WHERE user_id = ? AND org_id IS NULL AND status = 'active'",
		*sub.UserID,
	).Scan(&active); err != nil {
		rollback()
		return fmt.Errorf("check active personal subscription: %w", err)
	}
	if active > 0 {
		rollback()
		return ErrActivePersonalSubscription
	}
	if _, err := tx.Exec(
		`INSERT INTO subscriptions (id, user_id, org_id, plan, status, seats, stripe_subscription_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sub.ID, sub.UserID, sub.OrgID, sub.Plan, sub.Status, sub.Seats, sub.StripeSubscriptionID,
	); err != nil {
		rollback()
		return fmt.Errorf("create subscription: %w", err)
	}
	if _, err := tx.Exec(
		"INSERT INTO entitlements (id, user_id, subscription_id) VALUES (?, ?, ?)",
		ent.ID, ent.UserID, ent.SubscriptionID,
	); err != nil {
		rollback()
		return fmt.Errorf("create entitlement: %w", err)
	}
	if _, err := tx.Exec("UPDATE users SET tier = 'pro' WHERE id = ?", ent.UserID); err != nil {
		rollback()
		return fmt.Errorf("update user tier: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit subscription activation: %w", err)
	}
	return nil
}

// EnsurePersonalSubscription atomically creates a personal subscription and
// entitlement, or repairs the entitlement on the user's existing active
// subscription. It returns the effective subscription and whether it was
// newly created.
func (s *RelayStore) EnsurePersonalSubscription(sub *Subscription, ent *Entitlement) (*Subscription, bool, error) {
	if sub == nil || ent == nil || sub.UserID == nil || *sub.UserID == "" || sub.OrgID != nil {
		return nil, false, fmt.Errorf("personal subscription and entitlement must identify one user")
	}
	if sub.ID == "" || sub.Plan == "" || sub.Status != "active" || sub.Seats < 1 {
		return nil, false, fmt.Errorf("personal subscription must be a valid active plan")
	}
	if ent.ID == "" || ent.UserID != *sub.UserID || ent.SubscriptionID != sub.ID {
		return nil, false, fmt.Errorf("personal subscription and entitlement do not match")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, fmt.Errorf("begin personal subscription ensure: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }

	effective := &Subscription{}
	err = tx.QueryRow(
		`SELECT id, user_id, org_id, plan, status, seats, stripe_subscription_id, created_at, updated_at
		 FROM subscriptions
		 WHERE user_id = ? AND org_id IS NULL AND status = 'active'
		 ORDER BY created_at, id LIMIT 1`,
		*sub.UserID,
	).Scan(
		&effective.ID, &effective.UserID, &effective.OrgID, &effective.Plan,
		&effective.Status, &effective.Seats, &effective.StripeSubscriptionID,
		&effective.CreatedAt, &effective.UpdatedAt,
	)
	created := false
	if err == sql.ErrNoRows {
		if _, err := tx.Exec(
			`INSERT INTO subscriptions (id, user_id, org_id, plan, status, seats, stripe_subscription_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			sub.ID, sub.UserID, sub.OrgID, sub.Plan, sub.Status, sub.Seats, sub.StripeSubscriptionID,
		); err != nil {
			rollback()
			return nil, false, fmt.Errorf("create subscription: %w", err)
		}
		effective = sub
		created = true
	} else if err != nil {
		rollback()
		return nil, false, fmt.Errorf("get personal subscription: %w", err)
	}

	if err := ensureEntitlementInTx(tx, ent.ID, ent.UserID, effective.ID); err != nil {
		rollback()
		return nil, false, err
	}
	if _, err := tx.Exec("UPDATE users SET tier = 'pro' WHERE id = ?", ent.UserID); err != nil {
		rollback()
		return nil, false, fmt.Errorf("update user tier: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit personal subscription ensure: %w", err)
	}
	return effective, created, nil
}

// GrantEntitlement atomically installs an entitlement and reflects it in the
// user's compatibility tier field.
func (s *RelayStore) GrantEntitlement(ent *Entitlement) error {
	if ent == nil || ent.ID == "" || ent.UserID == "" || ent.SubscriptionID == "" {
		return fmt.Errorf("entitlement must identify a user and subscription")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin entitlement grant: %w", err)
	}
	var subscriptionUserID, subscriptionOrgID *string
	var status string
	if err := tx.QueryRow(
		"SELECT user_id, org_id, status FROM subscriptions WHERE id = ?", ent.SubscriptionID,
	).Scan(&subscriptionUserID, &subscriptionOrgID, &status); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("load entitlement subscription: %w", err)
	}
	if status != "active" {
		_ = tx.Rollback()
		return fmt.Errorf("cannot grant entitlement from an inactive subscription")
	}
	if subscriptionUserID != nil && *subscriptionUserID != ent.UserID {
		_ = tx.Rollback()
		return fmt.Errorf("personal subscription belongs to another user")
	}
	if subscriptionOrgID != nil {
		var member int
		if err := tx.QueryRow(
			"SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = ?", *subscriptionOrgID, ent.UserID,
		).Scan(&member); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("check entitlement org member: %w", err)
		}
		if member == 0 {
			_ = tx.Rollback()
			return fmt.Errorf("cannot grant organization entitlement to a non-member")
		}
	}
	if (subscriptionUserID == nil) == (subscriptionOrgID == nil) {
		_ = tx.Rollback()
		return fmt.Errorf("subscription must belong to exactly one user or organization")
	}
	if err := ensureEntitlementInTx(tx, ent.ID, ent.UserID, ent.SubscriptionID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec("UPDATE users SET tier = 'pro' WHERE id = ?", ent.UserID); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("update user tier: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit entitlement grant: %w", err)
	}
	return nil
}

// ActivateOrgSubscription creates a team subscription, updates the seat limit,
// and grants the initial member entitlements as one transaction.
func (s *RelayStore) ActivateOrgSubscription(sub *Subscription, orgID string, entitlements []*Entitlement) ([]string, error) {
	if sub == nil || sub.OrgID == nil || *sub.OrgID != orgID || sub.UserID != nil {
		return nil, fmt.Errorf("organization subscription must identify the target organization and no user")
	}
	if sub.ID == "" || sub.Plan == "" || sub.Status != "active" || sub.Seats < 1 {
		return nil, fmt.Errorf("organization subscription must be a valid active plan")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin org subscription activation: %w", err)
	}
	var active int
	if err := tx.QueryRow(
		"SELECT COUNT(*) FROM subscriptions WHERE org_id = ? AND status = 'active'", orgID,
	).Scan(&active); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("check active org subscription: %w", err)
	}
	if active > 0 {
		_ = tx.Rollback()
		return nil, ErrActiveOrgSubscription
	}
	if _, err := tx.Exec(
		`INSERT INTO subscriptions (id, user_id, org_id, plan, status, seats, stripe_subscription_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sub.ID, sub.UserID, sub.OrgID, sub.Plan, sub.Status, sub.Seats, sub.StripeSubscriptionID,
	); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("create org subscription: %w", err)
	}
	if err := setOrgMaxSeatsInTx(tx, orgID, sub.Seats); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	granted, err := grantEntitlementsInTx(tx, orgID, sub.ID, entitlements, sub.Seats)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit org subscription activation: %w", err)
	}
	return granted, nil
}

// ExpandOrgSubscription changes the subscription and org seat limits together
// with any newly available member grants.
func (s *RelayStore) ExpandOrgSubscription(subID, orgID string, seats int, entitlements []*Entitlement) ([]string, error) {
	if seats < 1 {
		return nil, fmt.Errorf("organization seats must be at least 1")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin org subscription expansion: %w", err)
	}
	result, err := tx.Exec(
		`UPDATE subscriptions SET seats = ?, updated_at = datetime('now')
		 WHERE id = ? AND org_id = ? AND status = 'active' AND seats < ?`,
		seats, subID, orgID, seats,
	)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("update subscription seats: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("count updated subscriptions: %w", err)
	}
	if updated != 1 {
		_ = tx.Rollback()
		return nil, ErrOrgSeatsNotIncreased
	}
	if err := setOrgMaxSeatsInTx(tx, orgID, seats); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	var used int
	if err := tx.QueryRow("SELECT COUNT(*) FROM entitlements WHERE subscription_id = ?", subID).Scan(&used); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("count org subscription entitlements: %w", err)
	}
	granted, err := grantEntitlementsInTx(tx, orgID, subID, entitlements, seats-used)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit org subscription expansion: %w", err)
	}
	return granted, nil
}

func setOrgMaxSeatsInTx(tx *sql.Tx, orgID string, seats int) error {
	if seats < 1 {
		return fmt.Errorf("org seats must be at least 1")
	}
	result, err := tx.Exec("UPDATE orgs SET max_seats = ? WHERE id = ?", seats, orgID)
	if err != nil {
		return fmt.Errorf("update org seats: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count updated orgs: %w", err)
	}
	if updated != 1 {
		return fmt.Errorf("org not found")
	}
	return nil
}

func grantEntitlementsInTx(tx *sql.Tx, orgID, subscriptionID string, entitlements []*Entitlement, capacity int) ([]string, error) {
	if capacity < 0 {
		return nil, fmt.Errorf("entitlement capacity must not be negative")
	}
	granted := make([]string, 0, min(len(entitlements), capacity))
	for _, ent := range entitlements {
		if len(granted) >= capacity {
			break
		}
		if ent == nil {
			return nil, fmt.Errorf("entitlement must not be nil")
		}
		if ent.ID == "" || ent.UserID == "" || ent.SubscriptionID != subscriptionID {
			return nil, fmt.Errorf("entitlement for user %s does not match organization subscription", ent.UserID)
		}
		var member int
		if err := tx.QueryRow(
			"SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = ?", orgID, ent.UserID,
		).Scan(&member); err != nil {
			return nil, fmt.Errorf("check org member %s: %w", ent.UserID, err)
		}
		if member == 0 {
			return nil, fmt.Errorf("cannot grant organization entitlement to non-member %s", ent.UserID)
		}
		result, err := tx.Exec(
			"INSERT OR IGNORE INTO entitlements (id, user_id, subscription_id) VALUES (?, ?, ?)",
			ent.ID, ent.UserID, ent.SubscriptionID,
		)
		if err != nil {
			return nil, fmt.Errorf("create entitlement for user %s: %w", ent.UserID, err)
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("count entitlements created for user %s: %w", ent.UserID, err)
		}
		if inserted == 0 {
			var existing int
			if err := tx.QueryRow(
				"SELECT COUNT(*) FROM entitlements WHERE user_id = ? AND subscription_id = ?",
				ent.UserID, subscriptionID,
			).Scan(&existing); err != nil {
				return nil, fmt.Errorf("verify entitlement for user %s: %w", ent.UserID, err)
			}
			if existing == 0 {
				return nil, fmt.Errorf("entitlement ID %s is already in use", ent.ID)
			}
			continue
		}
		if _, err := tx.Exec("UPDATE users SET tier = 'pro' WHERE id = ?", ent.UserID); err != nil {
			return nil, fmt.Errorf("update tier for user %s: %w", ent.UserID, err)
		}
		granted = append(granted, ent.UserID)
	}
	return granted, nil
}

func ensureEntitlementInTx(tx *sql.Tx, id, userID, subscriptionID string) error {
	if _, err := tx.Exec(
		"INSERT OR IGNORE INTO entitlements (id, user_id, subscription_id) VALUES (?, ?, ?)",
		id, userID, subscriptionID,
	); err != nil {
		return fmt.Errorf("create entitlement: %w", err)
	}
	var exists int
	if err := tx.QueryRow(
		"SELECT COUNT(*) FROM entitlements WHERE user_id = ? AND subscription_id = ?",
		userID, subscriptionID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("verify entitlement: %w", err)
	}
	if exists == 0 {
		return fmt.Errorf("entitlement ID %s is already in use", id)
	}
	return nil
}

// CancelOrgSubscription cancels the subscription, removes its grants, and
// recomputes compatibility tiers atomically. It returns users whose cached
// entitlement state should be invalidated.
func (s *RelayStore) CancelOrgSubscription(subID string) ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin org subscription cancellation: %w", err)
	}
	rows, err := tx.Query("SELECT user_id FROM entitlements WHERE subscription_id = ?", subID)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("list subscription entitlements: %w", err)
	}
	var userIDs []string
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			_ = rows.Close()
			_ = tx.Rollback()
			return nil, fmt.Errorf("scan subscription entitlement: %w", err)
		}
		userIDs = append(userIDs, userID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		_ = tx.Rollback()
		return nil, fmt.Errorf("iterate subscription entitlements: %w", err)
	}
	if err := rows.Close(); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("close subscription entitlements: %w", err)
	}
	result, err := tx.Exec("UPDATE subscriptions SET status = 'canceled', updated_at = datetime('now') WHERE id = ? AND status = 'active'", subID)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("cancel subscription: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("count canceled subscriptions: %w", err)
	}
	if updated != 1 {
		_ = tx.Rollback()
		return nil, fmt.Errorf("active subscription not found")
	}
	if _, err := tx.Exec("DELETE FROM entitlements WHERE subscription_id = ?", subID); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("delete subscription entitlements: %w", err)
	}
	for _, userID := range userIDs {
		tier, err := userTierInTx(tx, userID)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if _, err := tx.Exec("UPDATE users SET tier = ? WHERE id = ?", tier, userID); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("update user tier: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit org subscription cancellation: %w", err)
	}
	return userIDs, nil
}

// CancelPersonalSubscription cancels one user's active personal subscription,
// removes its grant, and recomputes the compatibility tier atomically. The
// user constraint prevents a stale or forged subscription ID from canceling
// another account's subscription.
func (s *RelayStore) CancelPersonalSubscription(subID, userID string) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin personal subscription cancellation: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }
	result, err := tx.Exec(
		"UPDATE subscriptions SET status = 'canceled', updated_at = datetime('now') WHERE id = ? AND user_id = ? AND status = 'active'",
		subID, userID,
	)
	if err != nil {
		rollback()
		return "", fmt.Errorf("cancel personal subscription: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		rollback()
		return "", fmt.Errorf("count canceled personal subscriptions: %w", err)
	}
	if updated != 1 {
		rollback()
		return "", fmt.Errorf("active personal subscription not found")
	}
	if _, err := tx.Exec("DELETE FROM entitlements WHERE user_id = ? AND subscription_id = ?", userID, subID); err != nil {
		rollback()
		return "", fmt.Errorf("delete personal entitlement: %w", err)
	}
	tier, err := userTierInTx(tx, userID)
	if err != nil {
		rollback()
		return "", err
	}
	if _, err := tx.Exec("UPDATE users SET tier = ? WHERE id = ?", tier, userID); err != nil {
		rollback()
		return "", fmt.Errorf("update user tier: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit personal subscription cancellation: %w", err)
	}
	return tier, nil
}

// RevokeEntitlement atomically removes one grant and recalculates the user's
// compatibility tier from their remaining active entitlements.
func (s *RelayStore) RevokeEntitlement(userID, subID string) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin entitlement revoke: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM entitlements WHERE user_id = ? AND subscription_id = ?", userID, subID); err != nil {
		_ = tx.Rollback()
		return "", fmt.Errorf("delete entitlement: %w", err)
	}
	tier, err := userTierInTx(tx, userID)
	if err != nil {
		_ = tx.Rollback()
		return "", err
	}
	if _, err := tx.Exec("UPDATE users SET tier = ? WHERE id = ?", tier, userID); err != nil {
		_ = tx.Rollback()
		return "", fmt.Errorf("update user tier: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit entitlement revoke: %w", err)
	}
	return tier, nil
}

func userTierInTx(tx *sql.Tx, userID string) (string, error) {
	var count int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM entitlements e
		 JOIN subscriptions s ON e.subscription_id = s.id
		 WHERE e.user_id = ? AND s.status = 'active'`, userID,
	).Scan(&count); err != nil {
		return "", fmt.Errorf("count active entitlements: %w", err)
	}
	if count > 0 {
		return "pro", nil
	}
	return "free", nil
}

func (s *RelayStore) DeleteEntitlementByUserAndSub(userID, subID string) error {
	_, err := s.db.Exec("DELETE FROM entitlements WHERE user_id = ? AND subscription_id = ?", userID, subID)
	return err
}

func (s *RelayStore) DeleteEntitlementsBySub(subID string) ([]string, error) {
	rows, err := s.db.Query("SELECT user_id FROM entitlements WHERE subscription_id = ?", subID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var userIDs []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		userIDs = append(userIDs, uid)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	_, err = s.db.Exec("DELETE FROM entitlements WHERE subscription_id = ?", subID)
	return userIDs, err
}

func (s *RelayStore) CountEntitlementsBySub(subID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM entitlements WHERE subscription_id = ?", subID).Scan(&count)
	return count, err
}

func (s *RelayStore) IsUserPro(userID string) bool {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM entitlements e
		 JOIN subscriptions s ON e.subscription_id = s.id
		 WHERE e.user_id = ? AND s.status = 'active'`,
		userID,
	).Scan(&count)
	return err == nil && count > 0
}

// HasPersonalSubscription returns true if the user has an active personal (non-org) subscription.
func (s *RelayStore) HasPersonalSubscription(userID string) bool {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM entitlements e
		 JOIN subscriptions s ON e.subscription_id = s.id
		 WHERE e.user_id = ? AND s.status = 'active' AND s.org_id IS NULL`,
		userID,
	).Scan(&count)
	return err == nil && count > 0
}

func (s *RelayStore) BackfillProUsers() error {
	rows, err := s.db.Query(
		`SELECT id FROM users WHERE tier = 'pro'
		 AND id NOT IN (SELECT e.user_id FROM entitlements e JOIN subscriptions s ON e.subscription_id = s.id WHERE s.status = 'active')`,
	)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var userIDs []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return err
		}
		userIDs = append(userIDs, uid)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, uid := range userIDs {
		subID := "backfill-" + uid
		sub := &Subscription{ID: subID, UserID: &uid, Plan: "pro_monthly", Status: "active", Seats: 1}
		ent := &Entitlement{ID: "backfill-ent-" + uid, UserID: uid, SubscriptionID: subID}
		if _, _, err := s.EnsurePersonalSubscription(sub, ent); err != nil {
			return fmt.Errorf("backfill pro user %s: %w", uid, err)
		}
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
		content, err := migrationsFS.ReadFile("migrations/" + f)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", f, err)
		}

		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", f, err)
		}
		// Check after acquiring the immediate write transaction. Two processes
		// may open the same fresh roost database together; checking beforehand
		// lets both decide to execute the same non-idempotent DDL.
		var applied int
		if err := tx.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", f).Scan(&applied); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("check migration %s: %w", f, err)
		}
		if applied > 0 {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				return fmt.Errorf("finish already-applied migration %s: %w", f, err)
			}
			continue
		}
		if _, err := tx.Exec(string(content)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("exec migration %s: %w", f, err)
		}
		if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", f); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", f, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", f, err)
		}
	}
	return nil
}

// --- Labels ---

// SetLabel upserts a label for a target (wing or session).
func (s *RelayStore) SetLabel(targetID, scopeType, scopeID, label string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin label update: %w", err)
	}
	var scopeExists int
	switch scopeType {
	case "org":
		err = tx.QueryRow("SELECT COUNT(*) FROM orgs WHERE id = ?", scopeID).Scan(&scopeExists)
	case "user":
		err = tx.QueryRow("SELECT COUNT(*) FROM users WHERE id = ?", scopeID).Scan(&scopeExists)
	default:
		_ = tx.Rollback()
		return fmt.Errorf("unsupported label scope %q", scopeType)
	}
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("check label scope: %w", err)
	}
	if scopeExists == 0 {
		_ = tx.Rollback()
		return fmt.Errorf("label scope no longer exists")
	}
	_, err = tx.Exec(
		`INSERT INTO labels (target_id, scope_type, scope_id, label) VALUES (?, ?, ?, ?)
		 ON CONFLICT(target_id, scope_type, scope_id) DO UPDATE SET label = excluded.label`,
		targetID, scopeType, scopeID, label,
	)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// DeleteLabel removes a label for a target at a given scope.
func (s *RelayStore) DeleteLabel(targetID, scopeType, scopeID string) error {
	_, err := s.db.Exec(
		"DELETE FROM labels WHERE target_id = ? AND scope_type = ? AND scope_id = ?",
		targetID, scopeType, scopeID,
	)
	return err
}

// SetLabelAuthorized applies a label only if the actor still owns the target
// or, for an organization-scoped target, is still an organization manager.
// Authorization and the write share one immediate transaction.
func (s *RelayStore) SetLabelAuthorized(targetID, scopeType, scopeID, actorUserID string, actorOwnsTarget bool, label string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin authorized label update: %w", err)
	}
	if err := authorizeLabelMutationInTx(tx, scopeType, scopeID, actorUserID, actorOwnsTarget); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO labels (target_id, scope_type, scope_id, label) VALUES (?, ?, ?, ?)
		 ON CONFLICT(target_id, scope_type, scope_id) DO UPDATE SET label = excluded.label`,
		targetID, scopeType, scopeID, label,
	); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("set authorized label: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit authorized label update: %w", err)
	}
	return nil
}

// DeleteLabelAuthorized applies the same commit-time authorization as
// SetLabelAuthorized before deleting the scoped label.
func (s *RelayStore) DeleteLabelAuthorized(targetID, scopeType, scopeID, actorUserID string, actorOwnsTarget bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin authorized label deletion: %w", err)
	}
	if err := authorizeLabelMutationInTx(tx, scopeType, scopeID, actorUserID, actorOwnsTarget); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(
		"DELETE FROM labels WHERE target_id = ? AND scope_type = ? AND scope_id = ?",
		targetID, scopeType, scopeID,
	); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete authorized label: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit authorized label deletion: %w", err)
	}
	return nil
}

func authorizeLabelMutationInTx(tx *sql.Tx, scopeType, scopeID, actorUserID string, actorOwnsTarget bool) error {
	switch scopeType {
	case "user":
		if !actorOwnsTarget || scopeID != actorUserID {
			return ErrOrgMutationUnauthorized
		}
		var exists int
		if err := tx.QueryRow("SELECT COUNT(*) FROM users WHERE id = ?", scopeID).Scan(&exists); err != nil {
			return fmt.Errorf("check label user scope: %w", err)
		}
		if exists != 1 {
			return fmt.Errorf("label scope no longer exists")
		}
		return nil
	case "org":
		var exists int
		if err := tx.QueryRow("SELECT COUNT(*) FROM orgs WHERE id = ?", scopeID).Scan(&exists); err != nil {
			return fmt.Errorf("check label organization scope: %w", err)
		}
		if exists != 1 {
			return fmt.Errorf("label scope no longer exists")
		}
		if actorOwnsTarget {
			return nil
		}
		authorized, err := orgManagerAuthorizedInTx(tx, scopeID, actorUserID)
		if err != nil {
			return err
		}
		if !authorized {
			return ErrOrgMutationUnauthorized
		}
		return nil
	default:
		return fmt.Errorf("unsupported label scope %q", scopeType)
	}
}

// ResolveLabel returns the best label for a target: org-scoped first, then personal.
func (s *RelayStore) ResolveLabel(targetID, userID, orgID string) string {
	if orgID != "" {
		var label string
		err := s.db.QueryRow(
			"SELECT label FROM labels WHERE target_id = ? AND scope_type = 'org' AND scope_id = ?",
			targetID, orgID,
		).Scan(&label)
		if err == nil {
			return label
		}
	}
	var label string
	err := s.db.QueryRow(
		"SELECT label FROM labels WHERE target_id = ? AND scope_type = 'user' AND scope_id = ?",
		targetID, userID,
	).Scan(&label)
	if err == nil {
		return label
	}
	return ""
}

// ResolveLabels batch-resolves labels for multiple targets.
func (s *RelayStore) ResolveLabels(targetIDs []string, userID, orgID string) map[string]string {
	result := make(map[string]string, len(targetIDs))
	for _, id := range targetIDs {
		if label := s.ResolveLabel(id, userID, orgID); label != "" {
			result[id] = label
		}
	}
	return result
}

// --- Passkey Credentials ---

// PasskeyCredential represents a stored WebAuthn credential.
type PasskeyCredential struct {
	ID           string
	UserID       string
	CredentialID []byte
	PublicKey    []byte // raw P-256: 64 bytes (X||Y)
	SignCount    int
	Label        string
	CreatedAt    time.Time
}

// CreatePasskeyCredential stores a new passkey credential.
func (s *RelayStore) CreatePasskeyCredential(id, userID string, credentialID, publicKey []byte, label string) error {
	_, err := s.db.Exec(
		"INSERT INTO passkey_credentials (id, user_id, credential_id, public_key, label) VALUES (?, ?, ?, ?, ?)",
		id, userID, credentialID, publicKey, label,
	)
	if err != nil {
		return fmt.Errorf("create passkey credential: %w", err)
	}
	return nil
}

// ListPasskeyCredentials returns all passkey credentials for a user.
func (s *RelayStore) ListPasskeyCredentials(userID string) ([]*PasskeyCredential, error) {
	rows, err := s.db.Query(
		"SELECT id, user_id, credential_id, public_key, sign_count, label, created_at FROM passkey_credentials WHERE user_id = ? ORDER BY created_at",
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list passkey credentials: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []*PasskeyCredential
	for rows.Next() {
		var c PasskeyCredential
		if err := rows.Scan(&c.ID, &c.UserID, &c.CredentialID, &c.PublicKey, &c.SignCount, &c.Label, &c.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, &c)
	}
	return result, rows.Err()
}

// DeletePasskeyCredential removes a passkey credential by ID, scoped to user.
func (s *RelayStore) DeletePasskeyCredential(id, userID string) error {
	res, err := s.db.Exec("DELETE FROM passkey_credentials WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return fmt.Errorf("delete passkey credential: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("count deleted passkey credentials: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("passkey credential not found")
	}
	return nil
}

// UpdatePasskeySignCount increments the sign count for a credential.
func (s *RelayStore) UpdatePasskeySignCount(id string, count int) error {
	_, err := s.db.Exec("UPDATE passkey_credentials SET sign_count = ? WHERE id = ?", count, id)
	return err
}

// --- ntfy Config ---

// NtfyConfig holds a user's push notification settings.
type NtfyConfig struct {
	Topic  string
	Token  string
	Events string // comma-separated: "attention,exit"
}

// GetNtfyConfig returns the ntfy config for a user.
func (s *RelayStore) GetNtfyConfig(userID string) (NtfyConfig, error) {
	var cfg NtfyConfig
	err := s.db.QueryRow(
		"SELECT ntfy_topic, ntfy_token, ntfy_events FROM users WHERE id = ?", userID,
	).Scan(&cfg.Topic, &cfg.Token, &cfg.Events)
	return cfg, err
}

// SetNtfyConfig updates the ntfy config for a user.
func (s *RelayStore) SetNtfyConfig(userID string, cfg NtfyConfig) error {
	_, err := s.db.Exec(
		"UPDATE users SET ntfy_topic = ?, ntfy_token = ?, ntfy_events = ? WHERE id = ?",
		cfg.Topic, cfg.Token, cfg.Events, userID,
	)
	return err
}
