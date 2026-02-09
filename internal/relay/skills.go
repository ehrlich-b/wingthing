package relay

import (
	"database/sql"
	"fmt"
	"time"
)

type SkillRow struct {
	Name        string
	Description string
	Category    string
	Agent       string
	Tags        string
	Content     string
	SHA256      string
	Publisher   string
	SourceURL   string
	Weight      int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (s *RelayStore) CreateSkill(name, description, category, agent, tags, content, sha256, publisher, sourceURL string, weight int) error {
	// Use NULL for publisher to trigger database default when empty
	var publisherVal any = publisher
	if publisher == "" {
		publisherVal = nil
	}

	_, err := s.db.Exec(
		`INSERT INTO skills (name, description, category, agent, tags, content, sha256, publisher, source_url, weight)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   description = excluded.description,
		   category = excluded.category,
		   agent = excluded.agent,
		   tags = excluded.tags,
		   content = excluded.content,
		   sha256 = excluded.sha256,
		   publisher = COALESCE(excluded.publisher, publisher),
		   source_url = excluded.source_url,
		   weight = excluded.weight,
		   updated_at = CURRENT_TIMESTAMP`,
		name, description, category, agent, tags, content, sha256, publisherVal, sourceURL, weight,
	)
	if err != nil {
		return fmt.Errorf("create skill: %w", err)
	}
	return nil
}

func (s *RelayStore) GetSkill(name string) (*SkillRow, error) {
	row := s.db.QueryRow(
		"SELECT name, description, category, agent, tags, content, sha256, COALESCE(publisher, 'wingthing'), COALESCE(source_url, ''), weight, created_at, updated_at FROM skills WHERE name = ?",
		name,
	)
	var sk SkillRow
	err := row.Scan(&sk.Name, &sk.Description, &sk.Category, &sk.Agent, &sk.Tags, &sk.Content, &sk.SHA256, &sk.Publisher, &sk.SourceURL, &sk.Weight, &sk.CreatedAt, &sk.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get skill: %w", err)
	}
	return &sk, nil
}

func (s *RelayStore) ListSkills(category string) ([]*SkillRow, error) {
	var rows *sql.Rows
	var err error
	if category == "" {
		rows, err = s.db.Query(
			"SELECT name, description, category, agent, tags, sha256, COALESCE(publisher, 'wingthing'), COALESCE(source_url, ''), weight, created_at, updated_at FROM skills ORDER BY category ASC, weight DESC, name ASC",
		)
	} else {
		rows, err = s.db.Query(
			"SELECT name, description, category, agent, tags, sha256, COALESCE(publisher, 'wingthing'), COALESCE(source_url, ''), weight, created_at, updated_at FROM skills WHERE category = ? ORDER BY weight DESC, name ASC",
			category,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	defer rows.Close()

	var skills []*SkillRow
	for rows.Next() {
		var sk SkillRow
		if err := rows.Scan(&sk.Name, &sk.Description, &sk.Category, &sk.Agent, &sk.Tags, &sk.SHA256, &sk.Publisher, &sk.SourceURL, &sk.Weight, &sk.CreatedAt, &sk.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan skill: %w", err)
		}
		skills = append(skills, &sk)
	}
	return skills, rows.Err()
}

func (s *RelayStore) DeleteSkill(name string) error {
	_, err := s.db.Exec("DELETE FROM skills WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("delete skill: %w", err)
	}
	return nil
}

func (s *RelayStore) SearchSkills(query string) ([]*SkillRow, error) {
	like := "%" + query + "%"
	rows, err := s.db.Query(
		"SELECT name, description, category, agent, tags, sha256, COALESCE(publisher, 'wingthing'), COALESCE(source_url, ''), weight, created_at, updated_at FROM skills WHERE name LIKE ? OR description LIKE ? ORDER BY weight DESC, name ASC",
		like, like,
	)
	if err != nil {
		return nil, fmt.Errorf("search skills: %w", err)
	}
	defer rows.Close()

	var skills []*SkillRow
	for rows.Next() {
		var sk SkillRow
		if err := rows.Scan(&sk.Name, &sk.Description, &sk.Category, &sk.Agent, &sk.Tags, &sk.SHA256, &sk.Publisher, &sk.SourceURL, &sk.Weight, &sk.CreatedAt, &sk.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan skill: %w", err)
		}
		skills = append(skills, &sk)
	}
	return skills, rows.Err()
}
