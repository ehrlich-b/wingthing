package wingpolicy

import (
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// IsMemberFiltered returns true if the tunnel request is from an org member (not owner/admin).
// Empty/unknown roles are treated as "member" (least privilege) when a user ID is present.
func IsMemberFiltered(req ws.TunnelRequest) bool {
	if req.SenderUserID == "" {
		return false
	}
	return req.SenderOrgRole != "owner" && req.SenderOrgRole != "admin"
}

// RequestAgainstWingConfig recomputes only the wing-local admin override from
// the relay-authenticated role. Mutation paths call this while holding
// wingCfgMu so a removed local admin cannot commit one last stale-snapshot edit
// after SIGHUP has revoked that override.
func RequestAgainstWingConfig(req ws.TunnelRequest, authenticatedOrgRole string, wingCfg *config.WingConfig) ws.TunnelRequest {
	req.SenderOrgRole = authenticatedOrgRole
	if wingCfg != nil && wingCfg.IsAdmin(req.SenderEmail) && IsMemberRole(req.SenderOrgRole) {
		req.SenderOrgRole = "admin"
	}
	return req
}

// CanSeeSession returns true if the request sender can view a session with the given owner.
func CanSeeSession(req ws.TunnelRequest, sessionUserID string) bool {
	if !IsMemberFiltered(req) {
		return true
	}
	return sessionUserID != "" && sessionUserID == req.SenderUserID
}

func CanAttachSession(userID, orgRole, sessionUserID string) bool {
	if orgRole == "owner" || orgRole == "admin" {
		return true
	}
	return userID != "" && sessionUserID != "" && userID == sessionUserID
}

func CanAccessSessionPath(req ws.TunnelRequest, sessionPath string, userPaths []string) bool {
	if !IsMemberFiltered(req) {
		return true
	}
	return len(userPaths) > 0 && IsUnderPaths(sessionPath, userPaths)
}

func RequestDirEntries(req ws.TunnelRequest, path string, userPaths []string) []ws.DirEntry {
	if IsMemberFiltered(req) && len(userPaths) == 0 {
		return nil
	}
	return GetDirEntries(path, userPaths)
}

func RequestProjects(req ws.TunnelRequest, projects []ws.WingProject, userPaths []string) []ws.WingProject {
	if len(userPaths) > 0 {
		return FilterProjectsExact(projects, userPaths)
	}
	if IsMemberFiltered(req) {
		return nil
	}
	return projects
}
