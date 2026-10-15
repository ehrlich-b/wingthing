package relay

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

var ErrAccountRecoveryRefused = errors.New("account recovery requires an unambiguous verified email and provider identity")

// Normalize only case and surrounding whitespace. Dots, plus suffixes and
// domains remain significant; aliases are not proof of the stored address.
func normalizeRecoveryEmail(email string) (string, error) {
	email, err := normalizeBareEmail(email)
	return strings.ToLower(email), err
}

// An identity created before recovery shipped may belong to an empty account
// left by the outage. Refuse a collision for manual investigation rather than
// silently using that account or failing the unique email update with a 500.
// Already recovered identities continue to resolve to their GitHub user.
func (s *RelayStore) checkExistingRecoveryCollision(user *User, email string) error {
	if user.Provider == "github" {
		return nil
	}
	normalized, err := normalizeRecoveryEmail(email)
	if err != nil {
		return nil
	}
	rows, err := s.db.Query("SELECT email FROM users WHERE provider = 'github' AND email IS NOT NULL AND id <> ?", user.ID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var storedEmail string
		if err := rows.Scan(&storedEmail); err != nil {
			return err
		}
		stored, err := normalizeRecoveryEmail(storedEmail)
		if err == nil && stored == normalized {
			return fmt.Errorf("%w: user_id=%q", ErrAccountRecoveryRefused, user.ID)
		}
	}
	return rows.Err()
}

// Older magic-link users used the original spelling of the address as their
// provider ID and did not populate users.email. Preserve those identities,
// refusing legacy duplicates rather than choosing an arbitrary account.
func (s *RelayStore) getUserByNormalizedEmailIdentity(email string) (*User, error) {
	rows, err := s.db.Query("SELECT user_id, provider_id FROM user_identities WHERE provider = 'email'")
	if err != nil {
		return nil, err
	}
	var userID string
	for rows.Next() {
		var id, address string
		if err := rows.Scan(&id, &address); err != nil {
			_ = rows.Close()
			return nil, err
		}
		normalized, err := normalizeRecoveryEmail(address)
		if err == nil && normalized == email {
			if userID != "" && userID != id {
				_ = rows.Close()
				return nil, fmt.Errorf("%w: user_ids=%q", ErrAccountRecoveryRefused, []string{userID, id})
			}
			userID = id
		}
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return nil, iterationErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if userID == "" {
		return nil, nil
	}
	return s.GetUserByID(userID)
}

// RecoverGitHubUser records an additional login identity without moving any
// ownership or changing the user's original provider. Call only after checking
// enrollment against the current provider response. Immediate transactions and
// database uniqueness constraints serialize concurrent recovery attempts.
func (s *RelayStore) RecoverGitHubUser(provider, providerID, email string, verified bool) (*User, error) {
	if !verified || (provider != "google" && provider != "email") || providerID == "" {
		return nil, nil
	}
	normalized, err := normalizeRecoveryEmail(email)
	if err != nil {
		return nil, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// A concurrent callback may already have registered this identity. Resolve
	// it directly; never attach it to a second user on the basis of email.
	var userID string
	err = tx.QueryRow("SELECT user_id FROM user_identities WHERE provider = ? AND provider_id = ?", provider, providerID).Scan(&userID)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return s.GetUserByID(userID)
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	// Compare normalized addresses in Go so legacy mixed-case/whitespace data
	// uses exactly the same normalization as the incoming provider address.
	// Legacy magic-link addresses may exist only in their email identity.
	rows, err := tx.Query(`
		SELECT id, provider, email, 1 FROM users WHERE email IS NOT NULL
		UNION ALL
		SELECT u.id, u.provider, i.provider_id, 0
		FROM users u JOIN user_identities i ON i.user_id = u.id
		WHERE i.provider = 'email'`)
	if err != nil {
		return nil, err
	}
	var targetID string
	var hasGitHubMatch bool
	matches := make(map[string]struct{})
	for rows.Next() {
		var id, primaryProvider, storedEmail string
		var userEmail bool
		if err := rows.Scan(&id, &primaryProvider, &storedEmail, &userEmail); err != nil {
			_ = rows.Close()
			return nil, err
		}
		stored, err := normalizeRecoveryEmail(storedEmail)
		if err == nil && stored == normalized {
			matches[id] = struct{}{}
			if primaryProvider == "github" {
				hasGitHubMatch = true
			}
			// Email identities contribute ambiguity, but recovery still requires
			// a match to the GitHub user's stored email.
			if userEmail && primaryProvider == "github" {
				targetID = id
			}
		}
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return nil, iterationErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	// Preserve separate alternate-provider signups for users who never used
	// GitHub. A single non-GitHub match is outside account recovery's scope.
	if len(matches) == 0 || (len(matches) == 1 && !hasGitHubMatch) {
		return nil, nil
	}
	if len(matches) != 1 || targetID == "" {
		userIDs := make([]string, 0, len(matches))
		for id := range matches {
			userIDs = append(userIDs, id)
		}
		slices.Sort(userIDs)
		return nil, fmt.Errorf("%w: user_ids=%q", ErrAccountRecoveryRefused, userIDs)
	}
	var existingID string
	err = tx.QueryRow("SELECT provider_id FROM user_identities WHERE user_id = ? AND provider = ?", targetID, provider).Scan(&existingID)
	if err == nil {
		return nil, fmt.Errorf("%w: user_id=%q", ErrAccountRecoveryRefused, targetID)
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	if _, err := tx.Exec("INSERT INTO user_identities (provider, provider_id, user_id) VALUES (?, ?, ?)", provider, providerID, targetID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetUserByID(targetID)
}
