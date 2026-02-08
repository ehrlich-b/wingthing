package relay

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
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

func (s *Server) handleAuthDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MachineID string `json:"machine_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.MachineID == "" {
		writeError(w, http.StatusBadRequest, "machine_id is required")
		return
	}

	deviceCode := uuid.New().String()
	userCode := generateUserCode(6)
	expiresAt := time.Now().Add(deviceCodeExpiry)

	if err := s.Store.CreateDeviceCode(deviceCode, userCode, req.MachineID, expiresAt); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// TODO: rate limiting on this endpoint
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":      deviceCode,
		"user_code":        userCode,
		"verification_url": "/auth/claim",
		"expires_in":       int(deviceCodeExpiry.Seconds()),
		"interval":         5,
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
		// TODO: rate limiting — could return slow_down
		writeJSON(w, http.StatusOK, map[string]string{"error": "authorization_pending"})
		return
	}

	token := uuid.New().String()
	if err := s.Store.CreateDeviceToken(token, *dc.UserID, dc.DeviceID, nil); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.Store.AppendAudit(*dc.UserID, "token_issued", strPtr(fmt.Sprintf("device=%s", dc.DeviceID)))

	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_at": 0,
	})
}

func (s *Server) handleAuthClaim(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserCode string `json:"user_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.UserCode == "" {
		writeError(w, http.StatusBadRequest, "user_code is required")
		return
	}

	// Find device code by user_code
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	var code string
	err := s.Store.db.QueryRow(
		"SELECT code FROM device_codes WHERE user_code = ? AND claimed = 0 AND expires_at > ?",
		strings.ToUpper(req.UserCode), now,
	).Scan(&code)
	if err != nil {
		writeError(w, http.StatusNotFound, "invalid or expired user code")
		return
	}

	// Auto-create user for now (no OAuth)
	userID := uuid.New().String()
	if err := s.Store.CreateUser(userID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := s.Store.ClaimDeviceCode(code, userID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.Store.AppendAudit(userID, "device_claimed", strPtr(fmt.Sprintf("code=%s", code)))

	writeJSON(w, http.StatusOK, map[string]any{
		"claimed": true,
		"user_id": userID,
	})
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

func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	q := r.URL.Query().Get("q")

	var skills []*SkillRow
	var err error
	if q != "" {
		skills, err = s.Store.SearchSkills(q)
	} else {
		skills, err = s.Store.ListSkills(category)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type skillMeta struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Category    string `json:"category"`
		Agent       string `json:"agent"`
		Tags        string `json:"tags"`
		SHA256      string `json:"sha256"`
		Publisher   string `json:"publisher"`
	}

	out := make([]skillMeta, len(skills))
	for i, sk := range skills {
		out[i] = skillMeta{
			Name:        sk.Name,
			Description: sk.Description,
			Category:    sk.Category,
			Agent:       sk.Agent,
			Tags:        sk.Tags,
			SHA256:      sk.SHA256,
			Publisher:   sk.Publisher,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	sk, err := s.Store.GetSkill(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sk == nil {
		writeError(w, http.StatusNotFound, "skill not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"name":        sk.Name,
		"description": sk.Description,
		"category":    sk.Category,
		"agent":       sk.Agent,
		"tags":        sk.Tags,
		"content":     sk.Content,
		"sha256":      sk.SHA256,
		"publisher":   sk.Publisher,
	})
}

func (s *Server) handleGetSkillRaw(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	sk, err := s.Store.GetSkill(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sk == nil {
		writeError(w, http.StatusNotFound, "skill not found")
		return
	}

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(sk.Content))
}

// requireUser checks session cookie first, then Bearer token.
// Returns userID or writes 401 and returns empty string.
func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) string {
	if u := s.sessionUser(r); u != nil {
		return u.ID
	}
	return s.requireToken(w, r)
}

// requireToken extracts and validates a Bearer token from the Authorization header.
// Returns the userID or writes an error response and returns empty string.
func (s *Server) requireToken(w http.ResponseWriter, r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
		return ""
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	userID, _, err := s.Store.ValidateToken(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid or expired token")
		return ""
	}
	return userID
}

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	userID := s.requireToken(w, r)
	if userID == "" {
		return
	}

	var req struct {
		Text  string `json:"text"`
		Title string `json:"title"`
		Link  string `json:"link"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}

	if s.Embedder == nil {
		writeError(w, http.StatusInternalServerError, "no embedder available — server cannot create embeddings")
		return
	}

	post, err := CreatePost(s.Store, s.Embedder, PostParams{
		UserID: userID,
		Text:   req.Text,
		Title:  req.Title,
		Link:   req.Link,
		Mass:   1, // public API: mass always 1
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"post_id": post.ID,
		"visible": post.Visible,
	})
}

func (s *Server) handleVote(w http.ResponseWriter, r *http.Request) {
	userID := s.requireUser(w, r)
	if userID == "" {
		return
	}
	var req struct {
		PostID string `json:"post_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.PostID == "" {
		writeError(w, http.StatusBadRequest, "post_id is required")
		return
	}
	if err := s.Store.Upvote(userID, req.PostID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	count, _ := s.Store.GetUpvoteCount(req.PostID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "upvotes": count})
}

func (s *Server) handleComment(w http.ResponseWriter, r *http.Request) {
	userID := s.requireUser(w, r)
	if userID == "" {
		return
	}
	var req struct {
		PostID   string  `json:"post_id"`
		ParentID *string `json:"parent_id,omitempty"`
		Content  string  `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.PostID == "" || req.Content == "" {
		writeError(w, http.StatusBadRequest, "post_id and content are required")
		return
	}
	c := &SocialComment{
		ID:       uuid.New().String(),
		PostID:   req.PostID,
		UserID:   userID,
		ParentID: req.ParentID,
		Content:  req.Content,
	}
	if err := s.Store.CreateComment(c); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "comment_id": c.ID})
}

func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request) {
	postID := r.URL.Query().Get("post_id")
	if postID == "" {
		writeError(w, http.StatusBadRequest, "post_id query param required")
		return
	}
	comments, err := s.Store.ListCommentsByPost(postID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, comments)
}

func strPtr(s string) *string {
	return &s
}
