package relay

import (
	"database/sql"
	"errors"
	"strings"
)

var ErrAccountRecoveryRefused = errors.New("account recovery requires an unambiguous verified email and provider identity")

// Normalize only case and surrounding whitespace. Dots, plus suffixes and
// domains remain significant; aliases are not proof of the stored address.
func normalizeRecoveryEmail(email string) (string, error) {
	email, err := normalizeBareEmail(email)
	return strings.ToLower(email), err
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
				return nil, ErrAccountRecoveryRefused
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
	rows, err := tx.Query("SELECT id, provider, email FROM users WHERE email IS NOT NULL")
	if err != nil {
		return nil, err
	}
	var targetID, targetProvider string
	matches := 0
	for rows.Next() {
		var id, primaryProvider, storedEmail string
		if err := rows.Scan(&id, &primaryProvider, &storedEmail); err != nil {
			_ = rows.Close()
			return nil, err
		}
		stored, err := normalizeRecoveryEmail(storedEmail)
		if err == nil && stored == normalized {
			matches++
			targetID, targetProvider = id, primaryProvider
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
	if matches == 0 {
		return nil, nil
	}
	if matches != 1 || targetProvider != "github" {
		return nil, ErrAccountRecoveryRefused
	}
	var existingID string
	err = tx.QueryRow("SELECT provider_id FROM user_identities WHERE user_id = ? AND provider = ?", targetID, provider).Scan(&existingID)
	if err == nil {
		return nil, ErrAccountRecoveryRefused
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
