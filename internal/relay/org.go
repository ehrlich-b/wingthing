package relay

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maxOrgInviteBatch = 100

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

	id := uuid.New().String()
	if err := s.Store.CreateOrgForOwnerLimited(id, req.Name, slug, user.ID, 1, 5); err != nil {
		if errors.Is(err, ErrOrgLimitReached) {
			writeError(w, http.StatusForbidden, "you can create up to 5 organizations")
			return
		}
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
		user = s.tokenUser(r)
	}
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
	orgID := r.PathValue("orgID")
	org, err := s.Store.GetOrgByID(orgID)
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
		user = s.tokenUser(r)
	}
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	orgID := r.PathValue("orgID")
	org, err := s.Store.GetOrgByID(orgID)
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
		// Include first passkey public key for wing allowlist
		creds, _ := s.Store.ListPasskeyCredentials(m.UserID)
		if len(creds) > 0 {
			entry["passkey_public_key"] = base64.StdEncoding.EncodeToString(creds[0].PublicKey)
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
	orgID := r.PathValue("orgID")
	org, err := s.Store.GetOrgByID(orgID)
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
	if len(req.Emails) > maxOrgInviteBatch {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d email addresses may be invited at once", maxOrgInviteBatch))
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
	emails := make([]string, 0, len(req.Emails))
	seenEmails := make(map[string]struct{}, len(req.Emails))
	for _, rawEmail := range req.Emails {
		if strings.TrimSpace(rawEmail) == "" {
			continue
		}
		email, err := normalizeBareEmail(strings.ToLower(rawEmail))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid email address")
			return
		}
		if _, duplicate := seenEmails[email]; duplicate {
			continue
		}
		seenEmails[email] = struct{}{}
		emails = append(emails, email)
	}
	var created []inviteResult
	for _, email := range emails {
		token := generateToken()
		id := uuid.New().String()
		if err := s.Store.CreateOrgInvite(id, org.ID, email, token, user.ID, inviteRole); err != nil {
			if errors.Is(err, ErrOrgInviteExists) {
				continue
			}
			if errors.Is(err, ErrOrgMutationUnauthorized) {
				writeError(w, http.StatusForbidden, "only owners and admins can invite")
				return
			}
			writeError(w, http.StatusInternalServerError, "create invite: "+err.Error())
			return
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
	subject := smtpHeaderValue("You're invited to " + orgName + " on wingthing")
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
	if !s.selfServicePlansEnabled() {
		writeError(w, http.StatusForbidden, "self-service plan changes are unavailable on this deployment")
		return
	}
	orgID := r.PathValue("orgID")
	org, err := s.Store.GetOrgByID(orgID)
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

	s.planMu.Lock()
	defer s.planMu.Unlock()

	existing, err := s.Store.GetActiveOrgSubscription(org.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read active subscription: "+err.Error())
		return
	}
	members, err := s.Store.ListOrgMembers(org.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list members: "+err.Error())
		return
	}
	if existing != nil {
		if req.Seats <= existing.Seats {
			writeError(w, http.StatusBadRequest, "contact support to reduce seats")
			return
		}
		entitlements := make([]*Entitlement, 0, len(members))
		for _, m := range members {
			entitlements = append(entitlements, &Entitlement{ID: uuid.New().String(), UserID: m.UserID, SubscriptionID: existing.ID})
		}
		granted, err := s.Store.ExpandOrgSubscription(existing.ID, org.ID, req.Seats, entitlements)
		if err != nil {
			if errors.Is(err, ErrOrgSeatsNotIncreased) {
				writeError(w, http.StatusBadRequest, "contact support to reduce seats")
				return
			}
			writeError(w, http.StatusInternalServerError, "expand org subscription: "+err.Error())
			return
		}
		if s.Bandwidth != nil {
			for _, userID := range granted {
				s.Bandwidth.InvalidateUser(userID)
			}
		}

		log.Printf("org %s seats increased: %d -> %d, granted=%d", org.Slug, existing.Seats, req.Seats, len(granted))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plan": existing.Plan, "seats": req.Seats})
		return
	}

	subID := uuid.New().String()
	sub := &Subscription{ID: subID, OrgID: &org.ID, Plan: req.Plan, Status: "active", Seats: req.Seats}
	entitlements := make([]*Entitlement, 0, len(members))
	for _, m := range members {
		entitlements = append(entitlements, &Entitlement{ID: uuid.New().String(), UserID: m.UserID, SubscriptionID: subID})
	}
	granted, err := s.Store.ActivateOrgSubscription(sub, org.ID, entitlements)
	if err != nil {
		if errors.Is(err, ErrActiveOrgSubscription) {
			writeError(w, http.StatusConflict, "organization subscription changed; retry the request")
			return
		}
		writeError(w, http.StatusInternalServerError, "activate org subscription: "+err.Error())
		return
	}
	if s.Bandwidth != nil {
		for _, userID := range granted {
			s.Bandwidth.InvalidateUser(userID)
		}
	}

	log.Printf("org %s upgraded: plan=%s seats=%d granted=%d", org.Slug, req.Plan, req.Seats, len(granted))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plan": req.Plan, "seats": req.Seats})
}

// handleOrgCancel cancels an org's subscription. POST /api/orgs/{slug}/cancel
func (s *Server) handleOrgCancel(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	orgID := r.PathValue("orgID")
	org, err := s.Store.GetOrgByID(orgID)
	if err != nil || org == nil {
		writeError(w, http.StatusNotFound, "org not found")
		return
	}
	if org.OwnerUserID != user.ID {
		writeError(w, http.StatusForbidden, "only the org owner can cancel")
		return
	}

	s.planMu.Lock()
	defer s.planMu.Unlock()

	sub, err := s.Store.GetActiveOrgSubscription(org.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read active subscription: "+err.Error())
		return
	}
	if sub == nil {
		writeError(w, http.StatusBadRequest, "no active subscription")
		return
	}

	affectedUsers, err := s.Store.CancelOrgSubscription(sub.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cancel subscription: "+err.Error())
		return
	}
	for _, uid := range affectedUsers {
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
	orgID := r.PathValue("orgID")
	targetUserID := r.PathValue("userID")

	org, err := s.Store.GetOrgByID(orgID)
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

	revoked, err := s.Store.RemoveOrgMemberAuthorized(org.ID, user.ID, targetUserID)
	if err != nil {
		if errors.Is(err, ErrOrgMutationUnauthorized) {
			writeError(w, http.StatusForbidden, "not authorized")
			return
		}
		if errors.Is(err, ErrOrgOwnerRemoval) {
			writeError(w, http.StatusBadRequest, "cannot remove org owner")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if revoked && s.Bandwidth != nil {
		s.Bandwidth.InvalidateUser(targetUserID)
	}
	s.refreshUserOrgSubs(targetUserID)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteOrg deletes an org. Only the owner can delete, and only if no active subscription.
func (s *Server) handleDeleteOrg(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	orgID := r.PathValue("orgID")
	org, err := s.Store.GetOrgByID(orgID)
	if err != nil || org == nil {
		writeError(w, http.StatusNotFound, "org not found")
		return
	}
	if org.OwnerUserID != user.ID {
		writeError(w, http.StatusForbidden, "only the org owner can delete")
		return
	}

	s.planMu.Lock()
	defer s.planMu.Unlock()

	sub, err := s.Store.GetActiveOrgSubscription(org.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read active subscription: "+err.Error())
		return
	}
	if sub != nil {
		writeError(w, http.StatusBadRequest, "cancel the subscription first")
		return
	}
	members, err := s.Store.ListOrgMembers(org.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Store.DeleteOrg(org.ID); err != nil {
		if errors.Is(err, ErrActiveOrgSubscription) {
			writeError(w, http.StatusBadRequest, "cancel the subscription first")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, member := range members {
		s.refreshUserOrgSubs(member.UserID)
	}
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
		// Invite state is consumed on the login host, so it must not be writable
		// by the app host or any other sibling subdomain.
		s.setAuthFlowCookie(w, "invite_token", token, "/", 3600)
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

	// Validate email BEFORE consuming the invite so a wrong-account login
	// doesn't burn the token.
	inv, err := s.Store.GetInviteByToken(token)
	if err != nil || inv == nil || inv.ClaimedAt != nil {
		http.Error(w, "invite already used or expired", http.StatusBadRequest)
		return
	}
	if user.Email == nil || !strings.EqualFold(*user.Email, inv.Email) {
		http.Error(w, "email mismatch", http.StatusForbidden)
		return
	}
	orgID, _, granted, err := s.Store.AcceptOrgInvite(token, user.ID, *user.Email, uuid.New().String())
	if err != nil {
		http.Error(w, "invite already used or expired", http.StatusBadRequest)
		return
	}
	if granted && s.Bandwidth != nil {
		s.Bandwidth.InvalidateUser(user.ID)
	}
	s.refreshUserOrgSubs(user.ID)
	s.expireAuthFlowCookie(w, "invite_token", "/")
	// Also remove the parent-domain cookie emitted by older releases. A split
	// deployment can otherwise keep presenting stale invite state after upgrade.
	if s.cookieDomain() != "" {
		s.expireCookie(w, "invite_token", "/")
	}

	http.Redirect(w, r, s.appURL()+"#account/"+orgID, http.StatusSeeOther)
}

// refreshUserOrgSubs updates a user's org subscriptions in the WingRegistry
// and sends org.changed so the browser re-fetches its wing list.
// Only does work if the user has active subscribers.
func (s *Server) refreshUserOrgSubs(userID string) {
	if s.Store == nil {
		return
	}
	var orgIDs []string
	orgs, err := s.Store.ListOrgsForUser(userID)
	if err != nil {
		log.Printf("refresh org subscriptions: %v", err)
		return
	}
	for _, org := range orgs {
		orgIDs = append(orgIDs, org.ID)
	}
	if s.Wings.UpdateUserOrgs(userID, orgIDs) {
		s.Wings.notify(userID, WingEvent{Type: "org.changed"})
	}
	// Broadcast org.changed to edges so they update their subscribers
	if s.IsLogin() && s.WingMap != nil {
		payload, err := json.Marshal(map[string]any{
			"type":    "org.changed",
			"user_id": userID,
		})
		if err != nil {
			log.Printf("marshal org changed event: %v", err)
			return
		}
		go s.broadcastToEdges(payload)
	}
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
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
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
		escapeHTML(org.ID),
	)

	if inv.ClaimedAt == nil {
		_, _ = fmt.Fprintf(w, `
<form method="POST" action="/api/orgs/%s/invites/%s/revoke" onsubmit="return confirm('Revoke this invite?')">
<button type="submit" class="btn btn-revoke">revoke</button>
</form>`, escapeHTML(org.ID), escapeHTML(inv.Token))
	}

	_, _ = fmt.Fprint(w, `
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
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
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
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
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
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
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
	orgID := r.PathValue("orgID")
	org, err := s.Store.GetOrgByID(orgID)
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
	revoked, err := s.Store.RevokeOrgInviteAuthorized(org.ID, token, user.ID)
	if err != nil {
		if errors.Is(err, ErrOrgMutationUnauthorized) {
			writeError(w, http.StatusForbidden, "only owners and admins can revoke invites")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !revoked {
		writeError(w, http.StatusNotFound, "pending invite not found")
		return
	}

	// If this was a form POST (from the invite status page), redirect back
	if r.Header.Get("Content-Type") != "application/json" {
		http.Redirect(w, r, s.appURL()+"#account/"+org.ID, http.StatusSeeOther)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
