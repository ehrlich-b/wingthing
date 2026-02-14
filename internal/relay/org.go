package relay

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (s *Server) grantOrgEntitlement(orgID, userID string) {
	sub, _ := s.Store.GetActiveOrgSubscription(orgID)
	if sub == nil {
		return
	}
	used, _ := s.Store.CountEntitlementsBySub(sub.ID)
	if used >= sub.Seats {
		return
	}
	if err := s.Store.CreateEntitlement(&Entitlement{ID: uuid.New().String(), UserID: userID, SubscriptionID: sub.ID}); err != nil {
		log.Printf("grant org entitlement: %v", err)
		return
	}
	s.Store.UpdateUserTier(userID, "pro")
	if s.Bandwidth != nil {
		s.Bandwidth.InvalidateUser(userID)
	}
}

func (s *Server) revokeOrgEntitlement(orgID, userID string) {
	sub, _ := s.Store.GetActiveOrgSubscription(orgID)
	if sub == nil {
		return
	}
	s.Store.DeleteEntitlementByUserAndSub(userID, sub.ID)
	tier := "free"
	if s.Store.IsUserPro(userID) {
		tier = "pro"
	}
	s.Store.UpdateUserTier(userID, tier)
	if s.Bandwidth != nil {
		s.Bandwidth.InvalidateUser(userID)
	}
}

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
	owned, _ := s.Store.CountOrgsOwnedByUser(user.ID)
	if owned >= 5 {
		writeError(w, http.StatusForbidden, "you can create up to 5 organizations")
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
		entry := map[string]any{
			"id":        o.ID,
			"name":      o.Name,
			"slug":      o.Slug,
			"max_seats": o.MaxSeats,
			"is_owner":  o.OwnerUserID == user.ID,
		}
		sub, _ := s.Store.GetActiveOrgSubscription(o.ID)
		if sub != nil {
			used, _ := s.Store.CountEntitlementsBySub(sub.ID)
			entry["has_subscription"] = true
			entry["plan"] = sub.Plan
			entry["seats_total"] = sub.Seats
			entry["seats_used"] = used
		} else {
			entry["has_subscription"] = false
		}
		memberCount, _ := s.Store.CountOrgMembers(o.ID)
		entry["member_count"] = memberCount
		out[i] = entry
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
	resp := map[string]any{
		"id":        org.ID,
		"name":      org.Name,
		"slug":      org.Slug,
		"max_seats": org.MaxSeats,
		"is_owner":  org.OwnerUserID == user.ID,
	}
	sub, _ := s.Store.GetActiveOrgSubscription(org.ID)
	if sub != nil {
		used, _ := s.Store.CountEntitlementsBySub(sub.ID)
		resp["has_subscription"] = true
		resp["plan"] = sub.Plan
		resp["seats_total"] = sub.Seats
		resp["seats_used"] = used
	} else {
		resp["has_subscription"] = false
	}
	memberCount, _ := s.Store.CountOrgMembers(org.ID)
	resp["member_count"] = memberCount
	writeJSON(w, http.StatusOK, resp)
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
	role := s.Store.GetOrgMemberRole(org.ID, user.ID)
	if role == "" {
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
		u, _ := s.Store.GetUserByID(m.UserID)
		if u != nil {
			entry["display_name"] = u.DisplayName
			entry["email"] = u.Email
			entry["avatar_url"] = u.AvatarURL
		}
		memberList = append(memberList, entry)
	}

	invites, _ := s.Store.ListPendingInvites(org.ID)
	var inviteList []map[string]any
	isOwnerOrAdmin := role == "owner" || role == "admin"
	for _, inv := range invites {
		entry := map[string]any{
			"email":      inv.Email,
			"invited_by": inv.InvitedBy,
			"role":       inv.Role,
			"created_at": inv.CreatedAt,
		}
		if isOwnerOrAdmin {
			entry["link"] = s.Config.BaseURL + "/invite/" + inv.Token
		}
		inviteList = append(inviteList, entry)
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
		Role   string   `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	inviteRole := req.Role
	if inviteRole == "" {
		inviteRole = "member"
	}
	if inviteRole != "member" && inviteRole != "admin" {
		writeError(w, http.StatusBadRequest, "role must be member or admin")
		return
	}

	type inviteResult struct {
		Email string `json:"email"`
		Link  string `json:"link"`
	}
	var created []inviteResult
	for _, email := range req.Emails {
		email = strings.TrimSpace(strings.ToLower(email))
		if email == "" {
			continue
		}
		token := generateToken()
		id := uuid.New().String()
		if err := s.Store.CreateOrgInvite(id, org.ID, email, token, user.ID, inviteRole); err != nil {
			continue // skip dupes
		}
		link := s.Config.BaseURL + "/invite/" + token
		// Send invite email if SMTP configured
		if s.Config.SMTPHost != "" {
			s.sendInviteEmail(email, org.Name, link)
		}
		created = append(created, inviteResult{Email: email, Link: link})
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

// handleOrgUpgrade creates a team subscription for an org. POST /api/orgs/{slug}/upgrade
func (s *Server) handleOrgUpgrade(w http.ResponseWriter, r *http.Request) {
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
	if org.OwnerUserID != user.ID {
		writeError(w, http.StatusForbidden, "only the org owner can upgrade")
		return
	}

	var req struct {
		Plan  string `json:"plan"`
		Seats int    `json:"seats"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Plan == "" {
		req.Plan = "team_monthly"
	}
	if req.Seats < 1 {
		req.Seats = 5
	}

	existing, _ := s.Store.GetActiveOrgSubscription(org.ID)
	if existing != nil {
		if req.Seats <= existing.Seats {
			writeError(w, http.StatusBadRequest, "contact support to reduce seats")
			return
		}
		// Increase seats on existing subscription
		s.Store.UpdateSubscriptionSeats(existing.ID, req.Seats)
		s.Store.SetOrgMaxSeats(org.ID, req.Seats)

		// Grant entitlements to existing members who don't have one yet
		members, _ := s.Store.ListOrgMembers(org.ID)
		granted := 0
		for _, m := range members {
			used, _ := s.Store.CountEntitlementsBySub(existing.ID)
			if used >= req.Seats {
				break
			}
			if s.Store.CreateEntitlement(&Entitlement{ID: uuid.New().String(), UserID: m.UserID, SubscriptionID: existing.ID}) == nil {
				s.Store.UpdateUserTier(m.UserID, "pro")
				if s.Bandwidth != nil {
					s.Bandwidth.InvalidateUser(m.UserID)
				}
				granted++
			}
		}

		log.Printf("org %s seats increased: %d -> %d, granted=%d", org.Slug, existing.Seats, req.Seats, granted)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plan": existing.Plan, "seats": req.Seats})
		return
	}

	subID := uuid.New().String()
	sub := &Subscription{ID: subID, OrgID: &org.ID, Plan: req.Plan, Status: "active", Seats: req.Seats}
	if err := s.Store.CreateSubscription(sub); err != nil {
		writeError(w, http.StatusInternalServerError, "create subscription: "+err.Error())
		return
	}
	s.Store.SetOrgMaxSeats(org.ID, req.Seats)

	members, _ := s.Store.ListOrgMembers(org.ID)
	granted := 0
	for _, m := range members {
		if granted >= req.Seats {
			break
		}
		if s.Store.CreateEntitlement(&Entitlement{ID: uuid.New().String(), UserID: m.UserID, SubscriptionID: subID}) == nil {
			s.Store.UpdateUserTier(m.UserID, "pro")
			if s.Bandwidth != nil {
				s.Bandwidth.InvalidateUser(m.UserID)
			}
			granted++
		}
	}

	log.Printf("org %s upgraded: plan=%s seats=%d granted=%d", org.Slug, req.Plan, req.Seats, granted)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plan": req.Plan, "seats": req.Seats})
}

// handleOrgCancel cancels an org's subscription. POST /api/orgs/{slug}/cancel
func (s *Server) handleOrgCancel(w http.ResponseWriter, r *http.Request) {
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
	if org.OwnerUserID != user.ID {
		writeError(w, http.StatusForbidden, "only the org owner can cancel")
		return
	}

	sub, _ := s.Store.GetActiveOrgSubscription(org.ID)
	if sub == nil {
		writeError(w, http.StatusBadRequest, "no active subscription")
		return
	}

	s.Store.UpdateSubscriptionStatus(sub.ID, "canceled")
	affectedUsers, _ := s.Store.DeleteEntitlementsBySub(sub.ID)
	for _, uid := range affectedUsers {
		tier := "free"
		if s.Store.IsUserPro(uid) {
			tier = "pro"
		}
		s.Store.UpdateUserTier(uid, tier)
		if s.Bandwidth != nil {
			s.Bandwidth.InvalidateUser(uid)
		}
	}

	log.Printf("org %s subscription canceled, %d users affected", org.Slug, len(affectedUsers))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
	s.revokeOrgEntitlement(org.ID, targetUserID)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAcceptInvite shows invite info and accept button. GET /invite/{token}
func (s *Server) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	inv, _ := s.Store.GetInviteByToken(token)
	if inv == nil {
		http.Error(w, "invite not found", http.StatusNotFound)
		return
	}
	org, _ := s.Store.GetOrgByID(inv.OrgID)
	if org == nil {
		http.Error(w, "org not found", http.StatusNotFound)
		return
	}

	user := s.sessionUser(r)

	// If logged in and user is admin/owner of the invite's org, show admin status page
	if user != nil {
		role := s.Store.GetOrgMemberRole(org.ID, user.ID)
		if role == "owner" || role == "admin" {
			// Don't show admin page if the invite is actually for this user's email
			isForMe := user.Email != nil && strings.EqualFold(*user.Email, inv.Email)
			if !isForMe {
				s.renderInviteStatusPage(w, inv, org)
				return
			}
		}
	}

	// Not logged in — store token in cookie, show login prompt
	if user == nil {
		http.SetCookie(w, &http.Cookie{
			Name:     "invite_token",
			Value:    token,
			Path:     "/",
			Domain:   s.cookieDomain(),
			MaxAge:   3600,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		s.renderInviteLoginPage(w, inv, org)
		return
	}

	// Logged in — show accept button (or already-redeemed status)
	if inv.ClaimedAt != nil {
		s.renderInviteStatusPage(w, inv, org)
		return
	}

	// Check email match
	if user.Email == nil || !strings.EqualFold(*user.Email, inv.Email) {
		userEmail := ""
		if user.Email != nil {
			userEmail = *user.Email
		}
		s.renderInviteErrorPage(w, inv.Email, userEmail)
		return
	}

	s.renderInviteAcceptPage(w, inv, org)
}

// handleConsumeInvite processes the accept. POST /invite/{token}
func (s *Server) handleConsumeInvite(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	user := s.sessionUser(r)
	if user == nil {
		http.Redirect(w, r, "/invite/"+token, http.StatusSeeOther)
		return
	}

	email, orgID, invRole, err := s.Store.ConsumeOrgInvite(token)
	if err != nil {
		http.Error(w, "invite already used or expired", http.StatusBadRequest)
		return
	}
	if user.Email == nil || !strings.EqualFold(*user.Email, email) {
		http.Error(w, "email mismatch", http.StatusForbidden)
		return
	}

	s.Store.AddOrgMember(orgID, user.ID, invRole)
	s.grantOrgEntitlement(orgID, user.ID)
	http.SetCookie(w, &http.Cookie{Name: "invite_token", Path: "/", MaxAge: -1})

	org, _ := s.Store.GetOrgByID(orgID)
	slug := ""
	if org != nil {
		slug = org.Slug
	}
	appURL := "/"
	if s.Config.AppHost != "" {
		appURL = "https://" + s.Config.AppHost + "/"
	}
	http.Redirect(w, r, appURL+"#account/"+slug, http.StatusSeeOther)
}

func (s *Server) renderInviteStatusPage(w http.ResponseWriter, inv *OrgInvite, org *Org) {
	status := "pending"
	if inv.ClaimedAt != nil {
		status = "redeemed on " + inv.ClaimedAt.Format("Jan 2, 2006")
	}

	inviterName := inv.InvitedBy
	if u, _ := s.Store.GetUserByID(inv.InvitedBy); u != nil {
		inviterName = u.DisplayName
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>invite — %s</title>
<style>%s</style>
</head><body>
<div class="card">
<h2>invite to %s</h2>
<div class="row"><span class="key">email</span><span class="val">%s</span></div>
<div class="row"><span class="key">role</span><span class="val">%s</span></div>
<div class="row"><span class="key">invited by</span><span class="val">%s</span></div>
<div class="row"><span class="key">created</span><span class="val">%s</span></div>
<div class="row"><span class="key">status</span><span class="val %s">%s</span></div>
<div class="actions">
<a href="/app/#account/%s" class="btn btn-back">back to org</a>`,
		escapeHTML(org.Name),
		invitePageStyle,
		escapeHTML(org.Name),
		escapeHTML(inv.Email),
		escapeHTML(inv.Role),
		escapeHTML(inviterName),
		inv.CreatedAt.Format("Jan 2, 2006"),
		statusClass(inv.ClaimedAt),
		escapeHTML(status),
		escapeHTML(org.Slug),
	)

	if inv.ClaimedAt == nil {
		fmt.Fprintf(w, `
<form method="POST" action="/api/orgs/%s/invites/%s/revoke" onsubmit="return confirm('Revoke this invite?')">
<button type="submit" class="btn btn-revoke">revoke</button>
</form>`, escapeHTML(org.Slug), escapeHTML(inv.Token))
	}

	fmt.Fprint(w, `
</div>
</div>
</body></html>`)
}

func statusClass(claimedAt *time.Time) string {
	if claimedAt != nil {
		return "status-redeemed"
	}
	return "status-pending"
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

const invitePageStyle = `
body{font-family:'SF Mono','Fira Code',monospace;background:#1a1a2e;color:#eee;margin:0;display:flex;justify-content:center;padding:40px 16px}
.card{background:#16213e;border-radius:8px;padding:20px 28px;max-width:440px;width:100%}
h2{font-size:18px;margin:0 0 16px}
p{font-size:14px;color:#888;margin:8px 0}
.row{display:flex;gap:10px;padding:6px 0;font-size:14px}
.key{color:#888;min-width:80px;flex-shrink:0}
.val{word-break:break-all}
.status-pending{color:#f1c40f}
.status-redeemed{color:#2ecc71}
.actions{margin-top:16px;border-top:1px solid #0f3460;padding-top:12px;display:flex;gap:8px}
.btn{font-family:inherit;font-size:13px;padding:8px 16px;border:none;border-radius:4px;cursor:pointer;font-weight:600;text-decoration:none;display:inline-block}
.btn-accept{background:#e94560;color:#fff}
.btn-accept:hover{background:#ff6b81}
.btn-login{background:#e94560;color:#fff}
.btn-login:hover{background:#ff6b81}
.btn-back{background:#0f3460;color:#eee}
.btn-back:hover{background:#1e2a4a}
.btn-revoke{background:transparent;color:#e74c3c;border:1px solid #e74c3c}
.btn-revoke:hover{background:#e74c3c;color:#fff}
.error{color:#e74c3c;font-size:14px;margin:8px 0}
`

func (s *Server) renderInviteAcceptPage(w http.ResponseWriter, inv *OrgInvite, org *Org) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>join %s — wingthing</title><style>%s</style></head><body>
<div class="card">
<h2>join %s</h2>
<p>you've been invited to join <strong>%s</strong> as <strong>%s</strong></p>
<div class="actions">
<form method="POST" action="/invite/%s"><button type="submit" class="btn btn-accept">accept invite</button></form>
</div>
</div>
</body></html>`,
		escapeHTML(org.Name), invitePageStyle,
		escapeHTML(org.Name), escapeHTML(org.Name),
		escapeHTML(inv.Role), escapeHTML(inv.Token))
}

func (s *Server) renderInviteLoginPage(w http.ResponseWriter, inv *OrgInvite, org *Org) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>join %s — wingthing</title><style>%s</style></head><body>
<div class="card">
<h2>join %s</h2>
<p>you've been invited to join <strong>%s</strong> as <strong>%s</strong></p>
<p>log in to accept this invite</p>
<div class="actions">
<a href="/login?next=%s" class="btn btn-login">log in</a>
</div>
</div>
</body></html>`,
		escapeHTML(org.Name), invitePageStyle,
		escapeHTML(org.Name), escapeHTML(org.Name),
		escapeHTML(inv.Role),
		"/invite/"+escapeHTML(inv.Token))
}

func (s *Server) renderInviteErrorPage(w http.ResponseWriter, inviteEmail, userEmail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>invite — wingthing</title><style>%s</style></head><body>
<div class="card">
<h2>email mismatch</h2>
<p class="error">this invite was sent to <strong>%s</strong>, but you are logged in as <strong>%s</strong></p>
<p>log out and log in with the correct account to accept</p>
<div class="actions">
<form method="POST" action="/auth/logout"><button type="submit" class="btn btn-back">log out</button></form>
</div>
</div>
</body></html>`, invitePageStyle, escapeHTML(inviteEmail), escapeHTML(userEmail))
}

// handleRevokeInvite revokes a pending invite. POST /api/orgs/{slug}/invites/{token}/revoke
func (s *Server) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
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
	role := s.Store.GetOrgMemberRole(org.ID, user.ID)
	if role != "owner" && role != "admin" {
		writeError(w, http.StatusForbidden, "only owners and admins can revoke invites")
		return
	}

	token := r.PathValue("token")
	if err := s.Store.RevokeOrgInvite(token); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// If this was a form POST (from the invite status page), redirect back
	if r.Header.Get("Content-Type") != "application/json" {
		appURL := "/"
		if s.Config.AppHost != "" {
			appURL = "https://" + s.Config.AppHost + "/"
		}
		http.Redirect(w, r, appURL+"#account/"+slug, http.StatusSeeOther)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
