package relay

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type SocialEmbedding struct {
	ID           string
	UserID       string
	Link         *string
	Text         string
	Centerpoint  *string
	Slug         *string
	Embedding    []byte // raw float32 blob
	Embedding512 []byte
	Centroid512  []byte
	Effective512 []byte
	Kind         string // "post", "subscription", "anchor", "antispam"
	Visible      bool
	Mass         int
	Upvotes24h   int
	DecayedMass  float64
	Swallowed    bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
	PublishedAt  *time.Time
}

type PostAnchor struct {
	PostID     string
	AnchorID   string
	Similarity float64
}

type SocialComment struct {
	ID        string
	PostID    string
	UserID    string
	ParentID  *string
	Content   string
	IsBot     bool
	CreatedAt time.Time
}

type SocialUser struct {
	ID          string
	Provider    string
	ProviderID  string
	DisplayName string
	AvatarURL   *string
	IsPro       bool
	CreatedAt   time.Time
}

func (s *RelayStore) CreateSocialEmbedding(e *SocialEmbedding) error {
	// Format published_at as SQLite-compatible string (no timezone suffix)
	var pubAt interface{}
	if e.PublishedAt != nil {
		pubAt = e.PublishedAt.UTC().Format("2006-01-02 15:04:05")
	}
	_, err := s.db.Exec(
		`INSERT INTO social_embeddings (id, user_id, link, text, centerpoint, slug, embedding, embedding_512, centroid_512, effective_512, kind, visible, mass, upvotes_24h, decayed_mass, swallowed, published_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.UserID, e.Link, e.Text, e.Centerpoint, e.Slug, e.Embedding, e.Embedding512, e.Centroid512, e.Effective512, e.Kind,
		boolToInt(e.Visible), e.Mass, e.Upvotes24h, e.DecayedMass, boolToInt(e.Swallowed), pubAt,
	)
	if err != nil {
		return fmt.Errorf("create social embedding: %w", err)
	}
	return nil
}

func (s *RelayStore) GetSocialEmbedding(id string) (*SocialEmbedding, error) {
	row := s.db.QueryRow(
		`SELECT id, user_id, link, text, centerpoint, slug, embedding, embedding_512, centroid_512, effective_512, kind, visible, mass, upvotes_24h, decayed_mass, swallowed, created_at, updated_at, published_at
		 FROM social_embeddings WHERE id = ?`, id,
	)
	return scanSocialEmbedding(row)
}

func (s *RelayStore) GetSocialEmbeddingByLink(link string) (*SocialEmbedding, error) {
	row := s.db.QueryRow(
		`SELECT id, user_id, link, text, centerpoint, slug, embedding, embedding_512, centroid_512, effective_512, kind, visible, mass, upvotes_24h, decayed_mass, swallowed, created_at, updated_at, published_at
		 FROM social_embeddings WHERE link = ?`, link,
	)
	return scanSocialEmbedding(row)
}

func (s *RelayStore) GetSocialEmbeddingBySlug(slug string) (*SocialEmbedding, error) {
	row := s.db.QueryRow(
		`SELECT id, user_id, link, text, centerpoint, slug, embedding, embedding_512, centroid_512, effective_512, kind, visible, mass, upvotes_24h, decayed_mass, swallowed, created_at, updated_at, published_at
		 FROM social_embeddings WHERE slug = ?`, slug,
	)
	return scanSocialEmbedding(row)
}

func (s *RelayStore) ListAnchors() ([]*SocialEmbedding, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, link, text, centerpoint, slug, embedding, embedding_512, centroid_512, effective_512, kind, visible, mass, upvotes_24h, decayed_mass, swallowed, created_at, updated_at, published_at
		 FROM social_embeddings WHERE kind = 'anchor' AND visible = 1 ORDER BY created_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("list anchors: %w", err)
	}
	defer rows.Close()

	var out []*SocialEmbedding
	for rows.Next() {
		e, err := scanSocialEmbeddingRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *RelayStore) AssignPostAnchors(postID string, assignments []PostAnchor) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	for _, a := range assignments {
		if _, err := tx.Exec(
			"INSERT INTO post_anchors (post_id, anchor_id, similarity) VALUES (?, ?, ?)",
			postID, a.AnchorID, a.Similarity,
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("assign post anchor: %w", err)
		}
	}
	return tx.Commit()
}

func (s *RelayStore) ListPostsByAnchor(anchorID, sort string, limit int) ([]*SocialEmbedding, error) {
	var query string
	switch sort {
	case "hot":
		query = `SELECT p.id, p.user_id, p.link, p.text, p.centerpoint, p.slug, p.embedding, p.embedding_512, p.centroid_512, p.effective_512, p.kind, p.visible, p.mass, p.upvotes_24h, p.decayed_mass, p.swallowed, p.created_at, p.updated_at, p.published_at
			FROM social_embeddings p
			JOIN post_anchors pa ON pa.post_id = p.id
			WHERE pa.anchor_id = ? AND p.visible = 1 AND p.kind = 'post'
			ORDER BY (p.upvotes_24h / (1.0 + (julianday('now') - julianday(p.created_at)) * 2.0)) DESC
			LIMIT ?`
	case "rising":
		query = `SELECT p.id, p.user_id, p.link, p.text, p.centerpoint, p.slug, p.embedding, p.embedding_512, p.centroid_512, p.effective_512, p.kind, p.visible, p.mass, p.upvotes_24h, p.decayed_mass, p.swallowed, p.created_at, p.updated_at, p.published_at
			FROM social_embeddings p
			JOIN post_anchors pa ON pa.post_id = p.id
			WHERE pa.anchor_id = ? AND p.visible = 1 AND p.kind = 'post' AND p.created_at > datetime('now', '-48 hours')
			ORDER BY p.upvotes_24h DESC
			LIMIT ?`
	case "best":
		query = `SELECT p.id, p.user_id, p.link, p.text, p.centerpoint, p.slug, p.embedding, p.embedding_512, p.centroid_512, p.effective_512, p.kind, p.visible, p.mass, p.upvotes_24h, p.decayed_mass, p.swallowed, p.created_at, p.updated_at, p.published_at
			FROM social_embeddings p
			JOIN post_anchors pa ON pa.post_id = p.id
			WHERE pa.anchor_id = ? AND p.visible = 1 AND p.kind = 'post'
			ORDER BY pa.similarity * p.decayed_mass DESC
			LIMIT ?`
	default: // "new"
		query = `SELECT p.id, p.user_id, p.link, p.text, p.centerpoint, p.slug, p.embedding, p.embedding_512, p.centroid_512, p.effective_512, p.kind, p.visible, p.mass, p.upvotes_24h, p.decayed_mass, p.swallowed, p.created_at, p.updated_at, p.published_at
			FROM social_embeddings p
			JOIN post_anchors pa ON pa.post_id = p.id
			WHERE pa.anchor_id = ? AND p.visible = 1 AND p.kind = 'post'
			ORDER BY p.created_at DESC
			LIMIT ?`
	}

	rows, err := s.db.Query(query, anchorID, limit)
	if err != nil {
		return nil, fmt.Errorf("list posts by anchor: %w", err)
	}
	defer rows.Close()

	var out []*SocialEmbedding
	for rows.Next() {
		e, err := scanSocialEmbeddingRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *RelayStore) Upvote(userID, postID string) error {
	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO social_upvotes (user_id, post_id) VALUES (?, ?)",
		userID, postID,
	)
	if err != nil {
		return fmt.Errorf("upvote: %w", err)
	}
	return nil
}

func (s *RelayStore) GetUpvoteCount(postID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM social_upvotes WHERE post_id = ?", postID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("get upvote count: %w", err)
	}
	return count, nil
}

func (s *RelayStore) CreateComment(c *SocialComment) error {
	_, err := s.db.Exec(
		"INSERT INTO social_comments (id, post_id, user_id, parent_id, content, is_bot) VALUES (?, ?, ?, ?, ?, ?)",
		c.ID, c.PostID, c.UserID, c.ParentID, c.Content, boolToInt(c.IsBot),
	)
	if err != nil {
		return fmt.Errorf("create comment: %w", err)
	}
	return nil
}

func (s *RelayStore) ListCommentsByPost(postID string) ([]*SocialComment, error) {
	rows, err := s.db.Query(
		"SELECT id, post_id, user_id, parent_id, content, is_bot, created_at FROM social_comments WHERE post_id = ? ORDER BY created_at",
		postID,
	)
	if err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}
	defer rows.Close()

	var out []*SocialComment
	for rows.Next() {
		var c SocialComment
		var isBot int
		if err := rows.Scan(&c.ID, &c.PostID, &c.UserID, &c.ParentID, &c.Content, &isBot, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan comment: %w", err)
		}
		c.IsBot = isBot != 0
		out = append(out, &c)
	}
	return out, rows.Err()
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

func (s *RelayStore) GetSocialUser(id string) (*SocialUser, error) {
	row := s.db.QueryRow(
		"SELECT id, provider, provider_id, display_name, avatar_url, is_pro, created_at FROM social_users WHERE id = ?",
		id,
	)
	var u SocialUser
	var isPro int
	err := row.Scan(&u.ID, &u.Provider, &u.ProviderID, &u.DisplayName, &u.AvatarURL, &isPro, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get social user: %w", err)
	}
	u.IsPro = isPro != 0
	return &u, nil
}

func (s *RelayStore) GetSocialUserByProvider(provider, providerID string) (*SocialUser, error) {
	row := s.db.QueryRow(
		"SELECT id, provider, provider_id, display_name, avatar_url, is_pro, created_at FROM social_users WHERE provider = ? AND provider_id = ?",
		provider, providerID,
	)
	var u SocialUser
	var isPro int
	err := row.Scan(&u.ID, &u.Provider, &u.ProviderID, &u.DisplayName, &u.AvatarURL, &isPro, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get social user by provider: %w", err)
	}
	u.IsPro = isPro != 0
	return &u, nil
}

func (s *RelayStore) CheckRateLimit(userID, action string, isPro bool) (bool, error) {
	bucketSize := 2.0
	refillRate := 5.0 / 86400.0 // free: 5 tokens/day
	if isPro {
		bucketSize = 5.0
		refillRate = 1.0 / 300.0 // pro: 1 token per 5 min
	}

	now := time.Now().UTC()
	nowStr := now.Format("2006-01-02 15:04:05")

	var tokens float64
	var lastRefillStr string
	err := s.db.QueryRow(
		"SELECT tokens, last_refill FROM social_rate_limits WHERE user_id = ? AND action = ?",
		userID, action,
	).Scan(&tokens, &lastRefillStr)

	if err == sql.ErrNoRows {
		// First time: start with bucket full minus one token
		newTokens := bucketSize - 1.0
		_, err := s.db.Exec(
			"INSERT INTO social_rate_limits (user_id, action, tokens, last_refill) VALUES (?, ?, ?, ?)",
			userID, action, newTokens, nowStr,
		)
		if err != nil {
			return false, fmt.Errorf("init rate limit: %w", err)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("check rate limit: %w", err)
	}

	lastRefill, err := parseTime(lastRefillStr)
	if err != nil {
		return false, fmt.Errorf("parse last_refill: %w", err)
	}

	elapsed := now.Sub(lastRefill).Seconds()
	tokens += elapsed * refillRate
	if tokens > bucketSize {
		tokens = bucketSize
	}

	if tokens < 1.0 {
		// Update refill time but don't consume
		_, err = s.db.Exec(
			"UPDATE social_rate_limits SET tokens = ?, last_refill = ? WHERE user_id = ? AND action = ?",
			tokens, nowStr, userID, action,
		)
		if err != nil {
			return false, fmt.Errorf("update rate limit: %w", err)
		}
		return false, nil
	}

	tokens -= 1.0
	_, err = s.db.Exec(
		"UPDATE social_rate_limits SET tokens = ?, last_refill = ? WHERE user_id = ? AND action = ?",
		tokens, nowStr, userID, action,
	)
	if err != nil {
		return false, fmt.Errorf("update rate limit: %w", err)
	}
	return true, nil
}

func (s *RelayStore) DecayMasses() error {
	_, err := s.db.Exec(
		`UPDATE social_embeddings
		 SET decayed_mass = mass * exp(-0.023 * (julianday('now') - julianday(COALESCE(published_at, created_at))))
		 WHERE kind = 'post' AND visible = 1`,
	)
	if err != nil {
		return fmt.Errorf("decay masses: %w", err)
	}
	return nil
}

func (s *RelayStore) RefreshUpvotes24h() error {
	_, err := s.db.Exec(
		`UPDATE social_embeddings SET upvotes_24h = (
			SELECT COUNT(*) FROM social_upvotes
			WHERE social_upvotes.post_id = social_embeddings.id
			AND social_upvotes.created_at > datetime('now', '-24 hours')
		) WHERE kind = 'post'`,
	)
	if err != nil {
		return fmt.Errorf("refresh upvotes 24h: %w", err)
	}
	return nil
}

func (s *RelayStore) UpdateAnchorEffective(anchorID string, effective512 []byte) error {
	_, err := s.db.Exec(
		"UPDATE social_embeddings SET effective_512 = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		effective512, anchorID,
	)
	if err != nil {
		return fmt.Errorf("update anchor effective: %w", err)
	}
	return nil
}

func (s *RelayStore) UpdateAnchorCentroid(anchorID string, centroid512 []byte) error {
	_, err := s.db.Exec(
		"UPDATE social_embeddings SET centroid_512 = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		centroid512, anchorID,
	)
	if err != nil {
		return fmt.Errorf("update anchor centroid: %w", err)
	}
	return nil
}

// parsePublishedAt parses a nullable datetime string from SQLite into *time.Time.
func parsePublishedAt(s *string) *time.Time {
	if s == nil || *s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, *s); err == nil {
			return &t
		}
	}
	return nil
}

// scanSocialEmbedding scans a single row from *sql.Row.
func scanSocialEmbedding(row *sql.Row) (*SocialEmbedding, error) {
	var e SocialEmbedding
	var visible, swallowed int
	var publishedAt *string
	err := row.Scan(&e.ID, &e.UserID, &e.Link, &e.Text, &e.Centerpoint, &e.Slug, &e.Embedding, &e.Embedding512, &e.Centroid512, &e.Effective512,
		&e.Kind, &visible, &e.Mass, &e.Upvotes24h, &e.DecayedMass, &swallowed, &e.CreatedAt, &e.UpdatedAt, &publishedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan social embedding: %w", err)
	}
	e.Visible = visible != 0
	e.Swallowed = swallowed != 0
	e.PublishedAt = parsePublishedAt(publishedAt)
	return &e, nil
}

// scanSocialEmbeddingRow scans a single row from *sql.Rows.
func scanSocialEmbeddingRow(rows *sql.Rows) (*SocialEmbedding, error) {
	var e SocialEmbedding
	var visible, swallowed int
	var publishedAt *string
	err := rows.Scan(&e.ID, &e.UserID, &e.Link, &e.Text, &e.Centerpoint, &e.Slug, &e.Embedding, &e.Embedding512, &e.Centroid512, &e.Effective512,
		&e.Kind, &visible, &e.Mass, &e.Upvotes24h, &e.DecayedMass, &swallowed, &e.CreatedAt, &e.UpdatedAt, &publishedAt)
	if err != nil {
		return nil, fmt.Errorf("scan social embedding row: %w", err)
	}
	e.Visible = visible != 0
	e.Swallowed = swallowed != 0
	e.PublishedAt = parsePublishedAt(publishedAt)
	return &e, nil
}

func (s *RelayStore) CountPostsByAnchor(anchorID string) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM post_anchors pa
		 JOIN social_embeddings p ON p.id = pa.post_id
		 WHERE pa.anchor_id = ? AND p.visible = 1 AND p.kind = 'post'`,
		anchorID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count posts by anchor: %w", err)
	}
	return count, nil
}

func (s *RelayStore) SumDecayedMassByAnchor(anchorID string) (float64, error) {
	var mass float64
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(p.decayed_mass), 0) FROM post_anchors pa
		 JOIN social_embeddings p ON p.id = pa.post_id
		 WHERE pa.anchor_id = ? AND p.visible = 1 AND p.kind = 'post'`,
		anchorID,
	).Scan(&mass)
	if err != nil {
		return 0, fmt.Errorf("sum decayed mass by anchor: %w", err)
	}
	return mass, nil
}

type topPost struct {
	Text string
	Link string
}

// TopPostsByAnchor returns a map of anchorID -> top post (text + link) by highest decayed_mass.
func (s *RelayStore) TopPostsByAnchor() (map[string]topPost, error) {
	rows, err := s.db.Query(
		`SELECT pa.anchor_id, p.text, COALESCE(p.link, ''), p.decayed_mass FROM post_anchors pa
		 JOIN social_embeddings p ON p.id = pa.post_id
		 WHERE p.visible = 1 AND p.kind = 'post'
		 ORDER BY pa.anchor_id, p.decayed_mass DESC`)
	if err != nil {
		return nil, fmt.Errorf("top posts by anchor: %w", err)
	}
	defer rows.Close()

	out := make(map[string]topPost)
	for rows.Next() {
		var anchorID, text, link string
		var mass float64
		if err := rows.Scan(&anchorID, &text, &link, &mass); err != nil {
			return nil, fmt.Errorf("scan top post: %w", err)
		}
		if _, ok := out[anchorID]; !ok {
			out[anchorID] = topPost{Text: text, Link: link}
		}
	}
	return out, rows.Err()
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
		ID:          uuid.New().String(),
		Provider:    "email",
		ProviderID:  email,
		DisplayName: email,
	}
	if err := s.UpsertSocialUser(u); err != nil {
		return nil, err
	}
	return u, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}
