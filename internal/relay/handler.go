package relay

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	deviceCodeExpiry = 15 * time.Minute
	userCodeChars    = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no I/O/0/1 for clarity
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAuthCheck(w http.ResponseWriter, r *http.Request) {
	userID := s.requireToken(w, r)
	if userID == "" {
		return // requireToken already wrote 401
	}
	resp := map[string]any{"ok": true, "user_id": userID}
	if u, _ := s.Store.GetUserByID(userID); u != nil {
		resp["display_name"] = u.DisplayName
		if u.Email != nil {
			resp["email"] = *u.Email
		}
		resp["provider"] = u.Provider
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAuthDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WingID    string `json:"wing_id"`
		PublicKey string `json:"public_key,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.WingID == "" {
		writeError(w, http.StatusBadRequest, "wing_id is required")
		return
	}

	expiresAt := time.Now().Add(deviceCodeExpiry)
	var deviceCode, userCode string
	for attempt := 0; attempt < 10; attempt++ {
		deviceCode = uuid.New().String()
		var err error
		userCode, err = generateUserCode(6)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "generate secure device code")
			return
		}
		err = s.Store.CreateDeviceCodeWithKey(deviceCode, userCode, req.WingID, req.PublicKey, expiresAt)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrDeviceUserCodeExists) {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		deviceCode = ""
	}
	if deviceCode == "" {
		writeError(w, http.StatusServiceUnavailable, "could not allocate a device login code; retry")
		return
	}

	// In dev/local mode, auto-claim so login works without OAuth
	if s.DevMode {
		devUser, err := s.Store.CreateUserDev()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := s.Store.ClaimDeviceCode(deviceCode, devUser.ID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else if s.LocalMode && s.localUser != nil {
		if err := s.Store.ClaimDeviceCode(deviceCode, s.localUser.ID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	verificationURL := fmt.Sprintf("%s/auth/claim?code=%s", s.Config.BaseURL, userCode)
	interval := 5
	if s.DevMode || s.LocalMode {
		interval = 1
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":      deviceCode,
		"user_code":        userCode,
		"verification_url": verificationURL,
		"expires_in":       int(deviceCodeExpiry.Seconds()),
		"interval":         interval,
	})
}

func (s *Server) handleAuthToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.DeviceCode == "" {
		writeError(w, http.StatusBadRequest, "device_code is required")
		return
	}

	dc, err := s.Store.GetDeviceCode(req.DeviceCode)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if dc == nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": "invalid_code"})
		return
	}
	if time.Now().After(dc.ExpiresAt) {
		writeJSON(w, http.StatusOK, map[string]string{"error": "expired_code"})
		return
	}
	if !dc.Claimed || dc.UserID == nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": "authorization_pending"})
		return
	}
	if !s.roostUserIDAllowed(*dc.UserID) {
		writeError(w, http.StatusForbidden, "this account is not enrolled in this roost")
		return
	}

	// Issue JWT instead of UUID token
	if s.jwtKey == nil {
		writeError(w, http.StatusInternalServerError, "jwt key not initialized")
		return
	}

	publicKey := ""
	if dc.PublicKey != nil {
		publicKey = *dc.PublicKey
	}

	token, exp, err := IssueWingJWT(s.jwtKey, *dc.UserID, publicKey, dc.DeviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "issue jwt: "+err.Error())
		return
	}

	// Also store in device_tokens for backward compat with social API auth.
	// Consuming the grant in the same transaction makes approval one-shot even
	// when two polling clients race.
	exchanged, err := s.Store.ExchangeClaimedDeviceCode(dc.Code, token, *dc.UserID, dc.DeviceID, &exp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store token: "+err.Error())
		return
	}
	if !exchanged {
		writeJSON(w, http.StatusOK, map[string]string{"error": "invalid_code"})
		return
	}

	if err := s.Store.AppendAudit(*dc.UserID, "jwt_issued", strPtr(fmt.Sprintf("device=%s", dc.DeviceID))); err != nil {
		log.Printf("audit jwt issuance: %v", err)
	}

	tokenResp := map[string]any{
		"token":      token,
		"expires_at": exp.Unix(),
	}
	if user, _ := s.Store.GetUserByID(*dc.UserID); user != nil {
		tokenResp["display_name"] = user.DisplayName
		if user.Email != nil {
			tokenResp["email"] = *user.Email
		}
		tokenResp["provider"] = user.Provider
	}
	writeJSON(w, http.StatusOK, tokenResp)
}

// handleAuthClaim handles POST /auth/claim — requires web session (OAuth).
func (s *Server) handleAuthClaim(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		// Not logged in via form POST — redirect to login
		userCode := r.FormValue("user_code")
		if userCode == "" {
			writeError(w, http.StatusBadRequest, "user_code is required")
			return
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(fmt.Sprintf("/auth/claim?code=%s", userCode)), http.StatusSeeOther)
		return
	}

	userCode := r.FormValue("user_code")
	if userCode == "" {
		writeError(w, http.StatusBadRequest, "user_code is required")
		return
	}

	dc, err := s.Store.GetDeviceCodeByUserCode(strings.ToUpper(userCode))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if dc == nil || dc.Claimed {
		s.renderClaimPage(w, userCode, "Invalid or expired code")
		return
	}

	if err := s.Store.ClaimDeviceCode(dc.Code, user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Device and user codes are bearer credentials until redemption. Do not
	// retain either value in the durable audit log, even though this claim makes
	// them one-shot; the non-secret device identifier is sufficient metadata.
	if err := s.Store.AppendAudit(user.ID, "device_claimed", strPtr(fmt.Sprintf("device=%s", dc.DeviceID))); err != nil {
		log.Printf("audit device claim: %v", err)
	}

	http.Redirect(w, r, s.appURL(), http.StatusSeeOther)
}

// handleClaimPage handles GET /auth/claim — shows the approve page.
func (s *Server) handleClaimPage(w http.ResponseWriter, r *http.Request) {
	userCode := r.URL.Query().Get("code")
	if userCode == "" {
		s.renderClaimPage(w, "", "No device code provided")
		return
	}

	user := s.sessionUser(r)
	if user == nil {
		// Redirect to login with next param to come back here
		http.Redirect(w, r, "/login?next="+url.QueryEscape(fmt.Sprintf("/auth/claim?code=%s", userCode)), http.StatusSeeOther)
		return
	}

	dc, err := s.Store.GetDeviceCodeByUserCode(strings.ToUpper(userCode))
	if err != nil || dc == nil {
		s.renderClaimPage(w, userCode, "Invalid or expired code")
		return
	}
	if dc.Claimed {
		http.Redirect(w, r, s.appURL(), http.StatusSeeOther)
		return
	}

	s.renderClaimPage(w, userCode, "")
}

func (s *Server) appURL() string {
	if s.Config.AppHost != "" {
		return s.browserOriginScheme() + "://" + s.Config.AppHost + "/"
	}
	return "/app/"
}

func (s *Server) renderClaimPage(w http.ResponseWriter, userCode, errMsg string) {
	data := struct {
		UserCode string
		Error    string
	}{
		UserCode: userCode,
		Error:    errMsg,
	}

	t := s.template(claimTmpl, "claim.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		log.Printf("render claim page: %v", err)
	}
}

func (s *Server) handleAuthRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}

	userID, deviceID, err := s.Store.ValidateToken(req.Token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	if !s.roostUserIDAllowed(userID) {
		writeError(w, http.StatusForbidden, "this account is not enrolled in this roost")
		return
	}

	newToken := uuid.New().String()
	if err := s.Store.RotateDeviceToken(req.Token, newToken, userID, deviceID, nil); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := s.Store.AppendAudit(userID, "token_refreshed", strPtr(fmt.Sprintf("device=%s", deviceID))); err != nil {
		log.Printf("audit token refresh: %v", err)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"token":      newToken,
		"expires_at": 0,
	})
}

// Helpers

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode JSON response: %v", err)
	}
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func generateUserCode(n int) (string, error) {
	return generateUserCodeFrom(rand.Reader, n)
}

func generateUserCodeFrom(reader io.Reader, n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("device code length must be positive")
	}
	b := make([]byte, n)
	for i := range b {
		idx, err := rand.Int(reader, big.NewInt(int64(len(userCodeChars))))
		if err != nil {
			return "", fmt.Errorf("read crypto randomness: %w", err)
		}
		b[i] = userCodeChars[idx.Int64()]
	}
	return string(b), nil
}

// requireToken extracts and validates a Bearer token (JWT or DB) from the Authorization header.
// Returns the userID or writes an error response and returns empty string.
func (s *Server) requireToken(w http.ResponseWriter, r *http.Request) string {
	auth := r.Header.Get("Authorization")
	var token string
	if strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimPrefix(auth, "Bearer ")
	} else if t := r.URL.Query().Get("token"); t != "" {
		token = t
	} else {
		writeError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
		return ""
	}

	// Try JWT first
	if s.JWTPubKey() != nil {
		if claims, err := ValidateWingJWT(s.JWTPubKey(), token); err == nil {
			if !s.roostUserIDAllowed(claims.Subject) {
				writeError(w, http.StatusForbidden, "this account is not enrolled in this roost")
				return ""
			}
			return claims.Subject
		}
	}

	// Fall back to DB token
	userID, _, err := s.Store.ValidateToken(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid or expired token")
		return ""
	}
	if !s.roostUserIDAllowed(userID) {
		writeError(w, http.StatusForbidden, "this account is not enrolled in this roost")
		return ""
	}
	return userID
}

func strPtr(s string) *string {
	return &s
}
