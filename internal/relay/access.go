package relay

import "fmt"

// wingRegistrySummary returns a compact debug string of all connected wings.
func (s *Server) wingRegistrySummary() string {
	all := s.Wings.All()
	if len(all) == 0 {
		return "[empty]"
	}
	result := fmt.Sprintf("[%d wings:", len(all))
	for _, w := range all {
		result += fmt.Sprintf(" %s(conn=%s,user=%s)", w.WingID, summaryPrefix(w.ID), summaryPrefix(w.UserID))
	}
	return result + "]"
}

func summaryPrefix(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

// canAccessWing returns true if userID can use this wing.
// userOrgIDs are the user's org memberships from their session (works on edge nodes without DB).
func (s *Server) canAccessWing(userID string, wing *ConnectedWing, userOrgIDs ...[]string) bool {
	if !s.roostUserIDAllowed(userID) {
		return false
	}
	// Roost mode makes the appliance's embedded service wing shared. External
	// personal wings may also register with the gateway, but they keep ordinary
	// owner/org isolation instead of inheriting appliance-wide access.
	if s.RoostMode && wing.UserID == roostWingServiceUserID {
		return true
	}

	// Owner always has access
	if wing.UserID == userID {
		return true
	}

	if wing.OrgID != "" {
		// Check via session org IDs (works on edge nodes)
		if len(userOrgIDs) > 0 {
			for _, oid := range userOrgIDs[0] {
				if oid == wing.OrgID {
					return true
				}
			}
		}
		// Check via store (login node)
		if s.Store != nil {
			if s.Store.IsOrgMember(wing.OrgID, userID) {
				return true
			}
		}
	}

	return false
}

// listAccessibleWings returns all wings the user can access.
func (s *Server) listAccessibleWings(userID string) []*ConnectedWing {
	all := s.Wings.All()
	var result []*ConnectedWing
	for _, w := range all {
		if s.canAccessWing(userID, w) {
			result = append(result, w)
		}
	}
	return result
}

// findAccessibleWing finds the first wing the user can access.
func (s *Server) findAccessibleWing(userID string) *ConnectedWing {
	all := s.Wings.All()
	for _, w := range all {
		if s.canAccessWing(userID, w) {
			return w
		}
	}
	return nil
}

// isWingOwner returns true if userID owns this wing (personal or org owner/admin).
func (s *Server) isWingOwner(userID string, wing *ConnectedWing) bool {
	if wing.UserID == userID {
		return true
	}
	if wing.OrgID != "" && s.Store != nil {
		role := s.Store.GetOrgMemberRole(wing.OrgID, userID)
		return role == "owner" || role == "admin"
	}
	return false
}

// findWingByWingID finds a connected wing by wing_id that the user can access.
func (s *Server) findWingByWingID(userID, wingID string) *ConnectedWing {
	wing := s.Wings.FindByID(wingID)
	if wing != nil && s.canAccessWing(userID, wing) {
		return wing
	}
	return nil
}

// findAnyWingByWingID finds a connected wing by wing_id without access check.
// Used for routing and tunnel dispatch — the wing itself handles authz via E2E tunnel.
func (s *Server) findAnyWingByWingID(wingID string) *ConnectedWing {
	return s.Wings.FindByID(wingID)
}
