package relay

import "database/sql"

// A durable proof never substitutes for the current provider's verified
// enrollment response. It only establishes which account owns this subject.
func (s *RelayStore) hasGoogleSignInProof(userID, subject string) (bool, error) {
	var proved bool
	err := s.db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM signin_identity_proofs p JOIN user_identities i
		ON i.provider = p.provider AND i.provider_id = p.provider_id AND i.user_id = p.user_id
		WHERE p.provider = 'google' AND p.provider_id = ? AND p.user_id = ?)`, subject, userID).Scan(&proved)
	return proved, err
}

func recordSignInProof(tx *sql.Tx, userID, provider, subject, email, provenance string) error {
	_, err := tx.Exec(`INSERT INTO signin_identity_proofs (user_id, provider, provider_id, verified_email, provenance)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(provider, provider_id) DO UPDATE SET
		verified_email = excluded.verified_email, proved_at = CURRENT_TIMESTAMP
		WHERE signin_identity_proofs.user_id = excluded.user_id`, userID, provider, subject, email, provenance)
	return err
}

// Save the current verified enrollment even if this account cannot own that
// contact address. The immediate transaction makes the contact ownership check
// and update atomic; all unrelated database errors still abort the login.
func (s *RelayStore) saveGoogleSignIn(user *User, subject, email, provenance string) error {
	userID := user.ID
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT email FROM users WHERE email IS NOT NULL AND id <> ?
        UNION ALL SELECT provider_id FROM user_identities WHERE provider = 'email' AND user_id <> ?`, userID, userID)
	if err != nil {
		return err
	}
	collision := false
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			rows.Close()
			return err
		}
		stored, err := normalizeRecoveryEmail(address)
		if err == nil && stored == email {
			collision = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var proved bool
	if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM signin_identity_proofs
        WHERE provider = 'google' AND provider_id = ? AND user_id = ?)`, subject, userID).Scan(&proved); err != nil {
		return err
	}
	if collision && !proved {
		return ErrAccountRecoveryRefused
	}
	if err := upsertUser(tx, user); err != nil {
		return err
	}
	// Recheck subject ownership inside the write transaction as well: a
	// concurrent removal must not authorize a session from a stale lookup.
	var owner string
	if err := tx.QueryRow("SELECT user_id FROM user_identities WHERE provider = 'google' AND provider_id = ?", subject).Scan(&owner); err != nil {
		return err
	}
	if owner != userID {
		return ErrIdentityInUse
	}
	if !collision {
		if _, err := tx.Exec("UPDATE users SET email = ? WHERE id = ?", email, userID); err != nil {
			return err
		}
	}
	if provenance != "" {
		if err := recordSignInProof(tx, userID, "google", subject, email, provenance); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO user_oauth_enrollment (user_id, verified_email) VALUES (?, ?)
		ON CONFLICT(user_id) DO UPDATE SET verified_email = excluded.verified_email, checked_at = CURRENT_TIMESTAMP`, userID, email); err != nil {
		return err
	}
	return tx.Commit()
}

// Missing rows retain legacy enrollment behavior. A recorded NULL is an
// explicit denial and must never fall back to a stale contact address.
func (s *RelayStore) oauthEnrollment(userID string) (email *string, known bool, err error) {
	err = s.db.QueryRow("SELECT verified_email FROM user_oauth_enrollment WHERE user_id = ?", userID).Scan(&email)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	return email, err == nil, err
}

func (s *RelayStore) saveVerifiedEnrollment(userID, email string) error {
	_, err := s.db.Exec(`INSERT INTO user_oauth_enrollment (user_id, verified_email) VALUES (?, ?)
        ON CONFLICT(user_id) DO UPDATE SET verified_email = excluded.verified_email, checked_at = CURRENT_TIMESTAMP`, userID, email)
	return err
}
