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
		`SELECT u.id, u.provider, u.provider_id, u.display_name, u.avatar_url, u.email, u.tier, u.is_pro, u.created_at
		 FROM sessions s JOIN social_users u ON u.id = s.social_user_id
		 WHERE s.token = ? AND s.expires_at > ?`,
		token, now,
	)
	var u SocialUser
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

// SocialUser represents an authenticated user (via OAuth, magic link, or local mode).
type SocialUser struct {
	ID          string
	Provider    string
	ProviderID  string
	DisplayName string
	AvatarURL   *string
	Email       *string
	Tier        string // "free", "pro", "team"
	IsPro       bool
	CreatedAt   time.Time
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
	CreatedAt time.Time
	ClaimedAt *time.Time
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *RelayStore) UpsertSocialUser(u *SocialUser) error {
	_, err := s.db.Exec(
		`INSERT INTO social_users (id, provider, provider_id, display_name, avatar_url, is_pro)
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

func (s *RelayStore) GetSocialUserByProvider(provider, providerID string) (*SocialUser, error) {
	row := s.db.QueryRow(
		"SELECT id, provider, provider_id, display_name, avatar_url, email, tier, is_pro, created_at FROM social_users WHERE provider = ? AND provider_id = ?",
		provider, providerID,
	)
	var u SocialUser
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

func (s *RelayStore) GetOrCreateSocialUserByEmail(email string) (*SocialUser, error) {
	u, err := s.GetSocialUserByProvider("email", email)
	if err != nil {
		return nil, err
	}
	if u != nil {
		return u, nil
	}
	u = &SocialUser{
		ID:          generateToken(),
		Provider:    "email",
		ProviderID:  email,
		DisplayName: email,
	}
	if err := s.UpsertSocialUser(u); err != nil {
		return nil, err
	}
	return u, nil
}

// GetSocialUserByID returns a user by their ID.
func (s *RelayStore) GetSocialUserByID(id string) (*SocialUser, error) {
	row := s.db.QueryRow(
		"SELECT id, provider, provider_id, display_name, avatar_url, email, tier, is_pro, created_at FROM social_users WHERE id = ?",
		id,
	)
	var u SocialUser
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
	_, err := s.db.Exec("UPDATE social_users SET email = ? WHERE id = ?", email, userID)
	return err
}

// UpdateUserTier sets the tier for a user.
func (s *RelayStore) UpdateUserTier(userID, tier string) error {
	_, err := s.db.Exec("UPDATE social_users SET tier = ? WHERE id = ?", tier, userID)
	return err
}

// GetSocialUserByEmail returns a user by email.
func (s *RelayStore) GetSocialUserByEmail(email string) (*SocialUser, error) {
	row := s.db.QueryRow(
		"SELECT id, provider, provider_id, display_name, avatar_url, email, tier, is_pro, created_at FROM social_users WHERE email = ?",
		email,
	)
	var u SocialUser
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

// CreateOrg creates a new org and adds the owner as an owner member.
func (s *RelayStore) CreateOrg(id, name, slug, ownerUserID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	_, err = tx.Exec(
		"INSERT INTO orgs (id, name, slug, owner_user_id, max_seats) VALUES (?, ?, ?, ?, 1)",
		id, name, slug, ownerUserID,
	)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("create org: %w", err)
	}
	_, err = tx.Exec(
		"INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'owner')",
		id, ownerUserID,
	)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("add owner member: %w", err)
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
	defer rows.Close()
	var result []*Org
	for rows.Next() {
		var o Org
		if err := rows.Scan(&o.ID, &o.Name, &o.Slug, &o.OwnerUserID, &o.MaxSeats, &o.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, &o)
	}
	return result, nil
}

// AddOrgMember adds a user to an org, enforcing max_seats.
func (s *RelayStore) AddOrgMember(orgID, userID, role string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	var count, maxSeats int
	err = tx.QueryRow("SELECT COUNT(*) FROM org_members WHERE org_id = ?", orgID).Scan(&count)
	if err != nil {
		tx.Rollback()
		return err
	}
	err = tx.QueryRow("SELECT max_seats FROM orgs WHERE id = ?", orgID).Scan(&maxSeats)
	if err != nil {
		tx.Rollback()
		return err
	}
	if count >= maxSeats {
		tx.Rollback()
		return fmt.Errorf("org has reached max seats (%d)", maxSeats)
	}
	_, err = tx.Exec(
		"INSERT OR IGNORE INTO org_members (org_id, user_id, role) VALUES (?, ?, ?)",
		orgID, userID, role,
	)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("add org member: %w", err)
	}
	return tx.Commit()
}

// RemoveOrgMember removes a user from an org.
func (s *RelayStore) RemoveOrgMember(orgID, userID string) error {
	_, err := s.db.Exec("DELETE FROM org_members WHERE org_id = ? AND user_id = ?", orgID, userID)
	return err
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
	defer rows.Close()
	var result []*OrgMember
	for rows.Next() {
		var m OrgMember
		if err := rows.Scan(&m.OrgID, &m.UserID, &m.Role, &m.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, &m)
	}
	return result, nil
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
func (s *RelayStore) CreateOrgInvite(id, orgID, email, token, invitedBy string) error {
	_, err := s.db.Exec(
		"INSERT INTO org_invites (id, org_id, email, token, invited_by) VALUES (?, ?, ?, ?, ?)",
		id, orgID, email, token, invitedBy,
	)
	if err != nil {
		return fmt.Errorf("create org invite: %w", err)
	}
	return nil
}

// ConsumeOrgInvite validates a token, marks it claimed, returns the invite info.
func (s *RelayStore) ConsumeOrgInvite(token string) (string, string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", "", fmt.Errorf("begin tx: %w", err)
	}
	var email, orgID string
	err = tx.QueryRow(
		"SELECT email, org_id FROM org_invites WHERE token = ? AND claimed_at IS NULL",
		token,
	).Scan(&email, &orgID)
	if err != nil {
		tx.Rollback()
		return "", "", fmt.Errorf("invalid or already claimed invite")
	}
	_, err = tx.Exec(
		"UPDATE org_invites SET claimed_at = datetime('now') WHERE token = ?",
		token,
	)
	if err != nil {
		tx.Rollback()
		return "", "", fmt.Errorf("consume invite: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return email, orgID, nil
}

// ListPendingInvites returns unclaimed invites for an org.
func (s *RelayStore) ListPendingInvites(orgID string) ([]*OrgInvite, error) {
	rows, err := s.db.Query(
		"SELECT id, org_id, email, token, invited_by, created_at FROM org_invites WHERE org_id = ? AND claimed_at IS NULL ORDER BY created_at",
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list pending invites: %w", err)
	}
	defer rows.Close()
	var result []*OrgInvite
	for rows.Next() {
		var inv OrgInvite
		if err := rows.Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Token, &inv.InvitedBy, &inv.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, &inv)
	}
	return result, nil
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
		"SELECT id, user_id, org_id, plan, status, seats, stripe_subscription_id, created_at, updated_at FROM subscriptions WHERE user_id = ? AND org_id IS NULL AND status = 'active'",
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
		"SELECT id, user_id, org_id, plan, status, seats, stripe_subscription_id, created_at, updated_at FROM subscriptions WHERE org_id = ? AND status = 'active'",
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

func (s *RelayStore) DeleteEntitlementByUserAndSub(userID, subID string) error {
	_, err := s.db.Exec("DELETE FROM entitlements WHERE user_id = ? AND subscription_id = ?", userID, subID)
	return err
}

func (s *RelayStore) DeleteEntitlementsBySub(subID string) ([]string, error) {
	rows, err := s.db.Query("SELECT user_id FROM entitlements WHERE subscription_id = ?", subID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var userIDs []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		userIDs = append(userIDs, uid)
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

func (s *RelayStore) BackfillProUsers() error {
	rows, err := s.db.Query(
		`SELECT id FROM social_users WHERE tier = 'pro'
		 AND id NOT IN (SELECT e.user_id FROM entitlements e JOIN subscriptions s ON e.subscription_id = s.id WHERE s.status = 'active')`,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	var userIDs []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return err
		}
		userIDs = append(userIDs, uid)
	}
	for _, uid := range userIDs {
		subID := "backfill-" + uid
		sub := &Subscription{ID: subID, UserID: &uid, Plan: "pro_monthly", Status: "active", Seats: 1}
		if err := s.CreateSubscription(sub); err != nil {
			continue
		}
		s.CreateEntitlement(&Entitlement{ID: "backfill-ent-" + uid, UserID: uid, SubscriptionID: subID})
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
