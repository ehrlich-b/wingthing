package relay

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

// Pin the credential selected at admission; revalidation must not fall back to
// another cookie, bearer token, or implicit local identity after revocation.
type ptyCredential struct {
	token   string
	session bool
	local   bool
}

func (s *Server) ptyCredentialValid(ctx context.Context, credential ptyCredential, userID string) bool {
	if credential.local {
		return s.LocalMode && s.localUser != nil && s.localUser.ID == userID
	}
	if credential.session {
		if s.IsEdge() {
			var result SessionValidation
			// Bypass the browser-session cache so logout and expiry take effect
			// on idle sockets within the revalidation interval.
			return s.remoteCredential(ctx, "/internal/sessions/", credential.token, &result) == nil && result.UserID == userID
		}
		if s.Store == nil {
			return false
		}
		user, err := s.Store.GetSession(credential.token)
		return err == nil && user != nil && user.ID == userID
	}
	claims, err := s.validateWingCredential(ctx, credential.token)
	return err == nil && claims.Subject == userID
}

func (s *Server) currentUserOrgContext(ctx context.Context, userID string) (userOrgContext, bool) {
	if s.IsEdge() {
		return s.remoteUserOrgContext(ctx, userID)
	}
	if s.Store == nil {
		if s.LocalMode && s.localUser != nil && s.localUser.ID == userID {
			result := userOrgContext{OrgIDs: s.localUser.OrgIDs, OrgRoles: s.localUser.OrgRoles}
			if s.localUser.Email != nil {
				result.Email = *s.localUser.Email
			}
			return result, true
		}
		return userOrgContext{}, false
	}
	orgs, err := s.Store.ListOrgsForUser(userID)
	if err != nil {
		return userOrgContext{}, false
	}
	result := userOrgContext{OrgRoles: make(map[string]string, len(orgs))}
	for _, org := range orgs {
		role := s.Store.GetOrgMemberRole(org.ID, userID)
		if role == "" {
			return userOrgContext{}, false
		}
		result.OrgIDs = append(result.OrgIDs, org.ID)
		result.OrgRoles[org.ID] = role
	}
	user, err := s.Store.GetUserByID(userID)
	if err != nil || user == nil {
		return userOrgContext{}, false
	}
	if user.Email != nil {
		result.Email = *user.Email
	}
	return result, true
}

// Close the socket when any authority granted at admission is withdrawn. This
// also removes controllers, pending attachments, viewers and tunnel requests
// through the handler's existing disconnect cleanup.
func orgAuthorityRevoked(previous, current userOrgContext) bool {
	for _, orgID := range previous.OrgIDs {
		present := false
		for _, currentID := range current.OrgIDs {
			if orgID == currentID {
				present = true
				break
			}
		}
		if !present || orgRoleAuthority(current.OrgRoles[orgID]) < orgRoleAuthority(previous.OrgRoles[orgID]) {
			return true
		}
	}
	return false
}

func orgRoleAuthority(role string) int {
	switch role {
	case "owner":
		return 3
	case "admin":
		return 2
	case "member":
		return 1
	default:
		return 0
	}
}

func revalidateSocketAuthorization(ctx context.Context, conn *websocket.Conn, interval time.Duration, changes <-chan WingEvent, validate func() bool) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case event := <-changes:
			if event.Type != "org.changed" {
				continue
			}
		}
		if !validate() {
			_ = conn.Close(websocket.StatusPolicyViolation, "authorization revoked")
			return
		}
	}
}
