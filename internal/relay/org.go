package relay

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

var slugRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

func slugify(name string) string {
	s := strings.ToLower(name)
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, s)
	// Collapse multiple dashes
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	if len(s) < 3 {
		s = s + "-org"
	}
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// handleCreateOrg creates a new org. POST /api/orgs
func (s *Server) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	var req struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	slug := req.Slug
	if slug == "" {
		slug = slugify(req.Name)
	}
	if !slugRegexp.MatchString(slug) {
		writeError(w, http.StatusBadRequest, "invalid slug: must be 3-40 chars, lowercase alphanumeric with dashes")
		return
	}

	// Check uniqueness
	existing, _ := s.Store.GetOrgBySlug(slug)
	if existing != nil {
		writeError(w, http.StatusConflict, "slug already taken")
		return
	}

	id := uuid.New().String()
	if err := s.Store.CreateOrg(id, req.Name, slug, user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":   id,
		"name": req.Name,
		"slug": slug,
	})
}

// handleListOrgs lists the user's orgs. GET /api/orgs
func (s *Server) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	orgs, err := s.Store.ListOrgsForUser(user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, len(orgs))
	for i, o := range orgs {
		out[i] = map[string]any{
			"id":        o.ID,
			"name":      o.Name,
			"slug":      o.Slug,
			"max_seats": o.MaxSeats,
			"is_owner":  o.OwnerUserID == user.ID,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetOrg returns org details. GET /api/orgs/{slug}
func (s *Server) handleGetOrg(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	slug := r.PathValue("slug")
	org, err := s.Store.GetOrgBySlug(slug)
	if err != nil || org == nil {
		writeError(w, http.StatusNotFound, "org not found")
		return
	}
	if !s.Store.IsOrgMember(org.ID, user.ID) {
		writeError(w, http.StatusForbidden, "not a member")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":        org.ID,
		"name":      org.Name,
		"slug":      org.Slug,
		"max_seats": org.MaxSeats,
		"is_owner":  org.OwnerUserID == user.ID,
	})
}

// handleListOrgMembers lists members and pending invites. GET /api/orgs/{slug}/members
func (s *Server) handleListOrgMembers(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	slug := r.PathValue("slug")
	org, err := s.Store.GetOrgBySlug(slug)
	if err != nil || org == nil {
		writeError(w, http.StatusNotFound, "org not found")
		return
	}
	if !s.Store.IsOrgMember(org.ID, user.ID) {
		writeError(w, http.StatusForbidden, "not a member")
		return
	}

	members, err := s.Store.ListOrgMembers(org.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var memberList []map[string]any
	for _, m := range members {
		entry := map[string]any{
			"user_id": m.UserID,
			"role":    m.Role,
		}
		u, _ := s.Store.GetSocialUserByID(m.UserID)
		if u != nil {
			entry["display_name"] = u.DisplayName
			entry["email"] = u.Email
			entry["avatar_url"] = u.AvatarURL
		}
		memberList = append(memberList, entry)
	}

	invites, _ := s.Store.ListPendingInvites(org.ID)
	var inviteList []map[string]any
	for _, inv := range invites {
		inviteList = append(inviteList, map[string]any{
			"email":      inv.Email,
			"invited_by": inv.InvitedBy,
			"created_at": inv.CreatedAt,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"members": memberList,
		"invites": inviteList,
	})
}

// handleOrgInvite sends invite(s). POST /api/orgs/{slug}/invite
func (s *Server) handleOrgInvite(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	slug := r.PathValue("slug")
	org, err := s.Store.GetOrgBySlug(slug)
	if err != nil || org == nil {
		writeError(w, http.StatusNotFound, "org not found")
		return
	}
	// Only owner/admin can invite
	role := s.Store.GetOrgMemberRole(org.ID, user.ID)
	if role != "owner" && role != "admin" {
		writeError(w, http.StatusForbidden, "only owners and admins can invite")
		return
	}

	var req struct {
		Emails []string `json:"emails"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	var created []string
	for _, email := range req.Emails {
		email = strings.TrimSpace(strings.ToLower(email))
		if email == "" {
			continue
		}
		token := generateToken()
		id := uuid.New().String()
		if err := s.Store.CreateOrgInvite(id, org.ID, email, token, user.ID); err != nil {
			continue // skip dupes
		}
		// Send invite email if SMTP configured
		if s.Config.SMTPHost != "" {
			link := s.Config.BaseURL + "/invite/" + token
			s.sendInviteEmail(email, org.Name, link)
		}
		created = append(created, email)
	}

	writeJSON(w, http.StatusOK, map[string]any{"invited": created})
}

func (s *Server) sendInviteEmail(to, orgName, link string) {
	from := s.Config.SMTPFrom
	subject := "You're invited to " + orgName + " on wingthing"
	body := fmt.Sprintf("You've been invited to join %s on wingthing.\n\nClick here to accept:\n\n%s\n\nThis link does not expire.", orgName, link)
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s", from, to, subject, body)

	addr := s.Config.SMTPHost + ":" + s.Config.SMTPPort
	auth := smtp.PlainAuth("", s.Config.SMTPUser, s.Config.SMTPPass, s.Config.SMTPHost)
	if err := smtp.SendMail(addr, auth, from, []string{to}, []byte(msg)); err != nil {
		log.Printf("send invite email to %s: %v", to, err)
	}
}

// handleRemoveOrgMember removes a member. DELETE /api/orgs/{slug}/members/{userID}
func (s *Server) handleRemoveOrgMember(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	slug := r.PathValue("slug")
	targetUserID := r.PathValue("userID")

	org, err := s.Store.GetOrgBySlug(slug)
	if err != nil || org == nil {
		writeError(w, http.StatusNotFound, "org not found")
		return
	}

	// Only owner/admin can remove, or user can remove themselves
	role := s.Store.GetOrgMemberRole(org.ID, user.ID)
	if role != "owner" && role != "admin" && user.ID != targetUserID {
		writeError(w, http.StatusForbidden, "not authorized")
		return
	}
	// Can't remove the owner
	if targetUserID == org.OwnerUserID {
		writeError(w, http.StatusBadRequest, "cannot remove org owner")
		return
	}

	if err := s.Store.RemoveOrgMember(org.ID, targetUserID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAcceptInvite stores invite token and redirects to login. GET /invite/{token}
func (s *Server) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	// Store token in cookie so login flow can pick it up
	http.SetCookie(w, &http.Cookie{
		Name:     "invite_token",
		Value:    token,
		Path:     "/",
		Domain:   s.cookieDomain(),
		MaxAge:   3600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	// If already logged in, try to consume immediately
	user := s.sessionUser(r)
	if user != nil {
		email, orgID, err := s.Store.ConsumeOrgInvite(token)
		if err == nil {
			if user.Email != nil && strings.EqualFold(*user.Email, email) {
				s.Store.AddOrgMember(orgID, user.ID, "member")
				http.SetCookie(w, &http.Cookie{Name: "invite_token", Path: "/", MaxAge: -1})
				if s.Config.AppHost != "" {
					http.Redirect(w, r, "https://"+s.Config.AppHost+"/?invite=accepted", http.StatusSeeOther)
				} else {
					http.Redirect(w, r, "/?invite=accepted", http.StatusSeeOther)
				}
				return
			}
			userEmail := ""
			if user.Email != nil {
				userEmail = *user.Email
			}
			http.Error(w, "This invite was sent to "+email+", but you are logged in as "+userEmail, http.StatusForbidden)
			return
		}
	}

	// Not logged in — redirect to login
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
