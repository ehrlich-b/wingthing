package relay

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
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
	if s.requireToken(w, r) == "" {
		return // requireToken already wrote 401
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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

	deviceCode := uuid.New().String()
	userCode := generateUserCode(6)
	expiresAt := time.Now().Add(deviceCodeExpiry)

	if err := s.Store.CreateDeviceCodeWithKey(deviceCode, userCode, req.WingID, req.PublicKey, expiresAt); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// In dev/local mode, auto-claim so login works without OAuth
	if s.DevMode {
		devUser, err := s.Store.CreateUserDev()
		if err == nil {
			s.Store.ClaimDeviceCode(deviceCode, devUser.ID)
		}
	} else if s.LocalMode && s.localUser != nil {
		s.Store.ClaimDeviceCode(deviceCode, s.localUser.ID)
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

	// Also store in device_tokens for backward compat with social API auth
	s.Store.CreateDeviceToken(token, *dc.UserID, dc.DeviceID, nil)

	s.Store.AppendAudit(*dc.UserID, "jwt_issued", strPtr(fmt.Sprintf("device=%s", dc.DeviceID)))

	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_at": exp.Unix(),
	})
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

	s.Store.AppendAudit(user.ID, "device_claimed", strPtr(fmt.Sprintf("code=%s user_code=%s", dc.Code, userCode)))

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
		return "https://" + s.Config.AppHost + "/"
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
	t.Execute(w, data)
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

	// Delete old token, issue new one
	if err := s.Store.DeleteToken(req.Token); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	newToken := uuid.New().String()
	if err := s.Store.CreateDeviceToken(newToken, userID, deviceID, nil); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.Store.AppendAudit(userID, "token_refreshed", strPtr(fmt.Sprintf("device=%s", deviceID)))

	writeJSON(w, http.StatusOK, map[string]any{
		"token":      newToken,
		"expires_at": 0,
	})
}

// Helpers

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func generateUserCode(n int) string {
	b := make([]byte, n)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(userCodeChars))))
		b[i] = userCodeChars[idx.Int64()]
	}
	return string(b)
}

// requireUser checks session cookie first, then Bearer token.
// Returns userID or writes 401 and returns empty string.
func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) string {
	if u := s.sessionUser(r); u != nil {
		return u.ID
	}
	return s.requireToken(w, r)
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
			return claims.Subject
		}
	}

	// Fall back to DB token
	userID, _, err := s.Store.ValidateToken(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid or expired token")
		return ""
	}
	return userID
}

func strPtr(s string) *string {
	return &s
}
