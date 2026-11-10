package relay

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var (
	ErrLastSignInMethod     = errors.New("this is your only way in")
	ErrIdentityInUse        = errors.New("this sign-in method is already linked")
	ErrSignInMethodNotFound = errors.New("sign-in method not found")
)

type SignInMethod struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
}

func (s *RelayStore) ListSignInMethods(userID string) ([]SignInMethod, error) {
	rows, err := s.db.Query("SELECT provider, provider_id FROM user_identities WHERE user_id = ? ORDER BY provider", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	methods := []SignInMethod{}
	for rows.Next() {
		var m SignInMethod
		if err := rows.Scan(&m.Provider, &m.Label); err != nil {
			return nil, err
		}
		// OAuth subjects are opaque identifiers, not useful account labels.
		if m.Provider != "email" {
			m.Label = ""
		}
		methods = append(methods, m)
	}
	return methods, rows.Err()
}

// LinkSignInMethod is called only after the provider (or a consumed email
// token) has proved this identity. Never move an identity between accounts.
func (s *RelayStore) LinkSignInMethod(userID, provider, providerID string) error {
	return s.linkSignInMethod(userID, provider, providerID, "")
}

// LinkGoogleSignInMethod checks the verified address before counting Google
// as a working method, rejecting conflicts with another account's unique email
// or recovery identity in the same transaction that links the provider subject.
func (s *RelayStore) LinkGoogleSignInMethod(userID, providerID, email string) error {
	email, err := normalizeRecoveryEmail(email)
	if err != nil {
		return ErrAccountRecoveryRefused
	}
	return s.linkSignInMethod(userID, "google", providerID, email)
}

func (s *RelayStore) linkSignInMethod(userID, provider, providerID, email string) error {
	if (provider != "google" && provider != "email") || providerID == "" {
		return ErrAccountRecoveryRefused
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if provider == "email" {
		normalized, err := normalizeRecoveryEmail(providerID)
		if err != nil {
			return ErrAccountRecoveryRefused
		}
		providerID = normalized
		email = normalized
	}
	if email != "" {
		// Legacy email subjects can have mixed case or whitespace. Also reserve
		// another account's verified recovery address; linking must not make its
		// next login ambiguous or steal its recovery path.
		rows, err := tx.Query(`SELECT user_id, provider_id FROM user_identities WHERE provider = 'email'
			UNION ALL SELECT id, email FROM users WHERE email IS NOT NULL`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, address string
			if err := rows.Scan(&id, &address); err != nil {
				rows.Close()
				return err
			}
			stored, err := normalizeRecoveryEmail(address)
			if err == nil && stored == email && id != userID {
				rows.Close()
				return ErrIdentityInUse
			}
		}
		iterationErr := rows.Err()
		rows.Close()
		if iterationErr != nil {
			return iterationErr
		}
	}
	var owner, subject string
	err = tx.QueryRow("SELECT user_id, provider_id FROM user_identities WHERE provider = ? AND (provider_id = ? OR user_id = ?)", provider, providerID, userID).Scan(&owner, &subject)
	if err == nil {
		if owner == userID && subject == providerID {
			if email != "" && provider == "google" {
				if err := recordSignInProof(tx, userID, provider, providerID, email, "explicit_link"); err != nil {
					return err
				}
			}
			return tx.Commit()
		}
		return ErrIdentityInUse
	}
	if err != sql.ErrNoRows {
		return err
	}
	if _, err = tx.Exec("INSERT INTO user_identities (user_id, provider, provider_id) VALUES (?, ?, ?)", userID, provider, providerID); err != nil {
		return err
	}
	if email != "" && provider == "google" {
		if err := recordSignInProof(tx, userID, provider, providerID, email, "explicit_link"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// The immediate transaction serializes concurrent removals. Passkeys currently
// unlock machines; they are not account login methods and cannot prevent lockout.
func (s *RelayStore) RemoveSignInMethod(userID, provider string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count, exists int
	if err = tx.QueryRow("SELECT COUNT(*), COUNT(CASE WHEN provider = ? THEN 1 END) FROM user_identities WHERE user_id = ?", provider, userID).Scan(&count, &exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrSignInMethodNotFound
	}
	if count <= 1 {
		return ErrLastSignInMethod
	}
	if _, err = tx.Exec("DELETE FROM user_identities WHERE user_id = ? AND provider = ?", userID, provider); err != nil {
		return err
	}
	// The stored email must not recreate this access through magic links or
	// another provider with the same verified address. Keep explicit linking
	// available, and preserve this decision across subsequent links/removals.
	if _, err = tx.Exec("INSERT OR IGNORE INTO signin_recovery_disabled (user_id) VALUES (?)", userID); err != nil {
		return err
	}
	// Retire the original provider metadata as well, so it neither reserves a
	// removed subject nor causes later callbacks to restore the removed method.
	if _, err = tx.Exec(`UPDATE users SET (provider, provider_id) =
		(SELECT provider, provider_id FROM user_identities WHERE user_id = ? ORDER BY provider LIMIT 1)
		WHERE id = ? AND provider = ?`, userID, userID, provider); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *RelayStore) createSignInLink(token, userID, provider, email string) error {
	_, err := s.db.Exec("INSERT INTO signin_links (token, user_id, provider, email, expires_at) VALUES (?, ?, ?, ?, ?)", token, userID, provider, email, time.Now().Add(15*time.Minute).UTC().Format("2006-01-02 15:04:05"))
	return err
}

func (s *RelayStore) consumeSignInLink(token, provider string) (userID, email string, err error) {
	err = s.db.QueryRow(`UPDATE signin_links SET used = 1
		WHERE token = ? AND provider = ? AND used = 0 AND expires_at > ? RETURNING user_id, email`, token, provider, time.Now().UTC().Format("2006-01-02 15:04:05")).Scan(&userID, &email)
	return
}

func signInError(w http.ResponseWriter, err error) {
	code, status, message := "internal_error", http.StatusInternalServerError, "Could not update sign-in methods"
	switch {
	case errors.Is(err, ErrLastSignInMethod):
		code, status, message = "last_signin_method", http.StatusConflict, "This is your only way in"
	case errors.Is(err, ErrIdentityInUse):
		code, status, message = "identity_in_use", http.StatusConflict, "This sign-in method is already linked"
	case errors.Is(err, ErrAccountRecoveryRefused):
		code, status, message = "verification_required", http.StatusForbidden, "Sign-in method could not be verified"
	case errors.Is(err, ErrSignInMethodNotFound):
		code, status, message = "method_not_found", http.StatusNotFound, "Sign-in method not found"
	}
	writeJSON(w, status, map[string]string{"code": code, "error": message})
}

func (s *Server) handleSignInMethods(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	methods, err := s.Store.ListSignInMethods(user.ID)
	if err != nil {
		signInError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"methods": methods, "can_add_google": s.Config.GoogleClientID != "", "can_add_email": s.Config.SMTPHost != ""})
}

func (s *Server) handleSignInMethodDelete(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	if err := s.Store.RemoveSignInMethod(user.ID, r.PathValue("provider")); err != nil {
		signInError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSignInEmailAdd(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	if s.Config.SMTPHost == "" {
		writeError(w, http.StatusNotFound, "Email links are unavailable")
		return
	}
	email, err := normalizeRecoveryEmail(r.FormValue("email"))
	if err != nil || !s.roostEmailAllowed(email) {
		writeError(w, http.StatusBadRequest, "Use an enrolled email address")
		return
	}
	// The address is verified only when the recipient consumes this token.
	token := "link_" + generateToken()
	if err := s.Store.createSignInLink(token, user.ID, "email", email); err != nil {
		signInError(w, err)
		return
	}
	link := s.Config.BaseURL + "/auth/magic/verify?token=" + token
	body := fmt.Sprintf("Add %s as a sign-in method to the wingthing account %q (ID: %s):\n\n%s\n\nThis adds access to that account. Only continue if you requested this from Account settings. Otherwise ignore this email. The link expires in 15 minutes.", email, user.DisplayName, user.ID, link)
	if err := s.sendAuthEmail(email, "Add a wingthing sign-in method", body); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not send email link")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"sent": true})
}

func (s *Server) redirectToSignInMethods(w http.ResponseWriter, r *http.Request) {
	// Use the same app destination as login, including split-host relays.
	destination := "/app/#account"
	if s.Config.AppHost != "" {
		destination = s.browserOriginScheme() + "://" + s.Config.AppHost + "/#account"
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
}

func (s *Server) verifySignInEmail(w http.ResponseWriter, r *http.Request, token string) {
	userID, email, err := s.Store.consumeSignInLink(token, "email")
	if err != nil {
		http.Error(w, "invalid or expired link", http.StatusBadRequest)
		return
	}
	if !s.roostEmailAllowed(email) || !s.roostUserIDAllowed(userID) {
		http.Error(w, "account not enrolled", http.StatusForbidden)
		return
	}
	if err := s.Store.LinkSignInMethod(userID, "email", email); err != nil {
		signInError(w, err)
		return
	}
	// Adding a method need not replace the current browser's session.
	s.redirectToSignInMethods(w, r)
}

func isSignInLink(token string) bool { return strings.HasPrefix(token, "link_") }
