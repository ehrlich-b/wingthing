package relay

import (
	"net/http"
)

// canAccessWing returns true if userID can use this wing.
func (s *Server) canAccessWing(userID string, wing *ConnectedWing) bool {
	// Owner always has access
	if wing.UserID == userID {
		return true
	}

	// Org mode: check membership (requires store, edge nodes may not have it)
	if wing.OrgID != "" && s.Store != nil {
		if s.Store.IsOrgMember(wing.OrgID, userID) {
			return true
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

// replayToWingEdge checks if a wing (by wingID) is on a remote edge node.
// If so, sets fly-replay header and returns true. Caller should return immediately.
func (s *Server) replayToWingEdge(w http.ResponseWriter, wingID string) bool {
	if s.Peers == nil || s.Config.FlyMachineID == "" {
		return false
	}
	if s.Wings.FindByID(wingID) != nil {
		return false
	}
	pw := s.Peers.FindWing(wingID)
	if pw != nil && pw.MachineID != s.Config.FlyMachineID {
		w.Header().Set("fly-replay", "instance="+pw.MachineID)
		return true
	}
	return false
}

// replayToWingEdgeByWingID checks if a wing (by wing ID) is on a remote edge.
func (s *Server) replayToWingEdgeByWingID(w http.ResponseWriter, wingID string) bool {
	if s.Peers == nil || s.Config.FlyMachineID == "" {
		return false
	}
	for _, wing := range s.Wings.All() {
		if wing.WingID == wingID {
			return false
		}
	}
	pw := s.Peers.FindByWingID(wingID)
	if pw != nil && pw.MachineID != s.Config.FlyMachineID {
		w.Header().Set("fly-replay", "instance="+pw.MachineID)
		return true
	}
	return false
}

// findWingByWingID finds a connected wing by wing_id that the user can access.
func (s *Server) findWingByWingID(userID, wingID string) *ConnectedWing {
	all := s.Wings.All()
	for _, w := range all {
		if w.WingID == wingID && s.canAccessWing(userID, w) {
			return w
		}
	}
	return nil
}

// findAnyWingByWingID finds a connected wing by wing_id without access check.
// Used for routing and tunnel dispatch — the wing itself handles authz via E2E tunnel.
func (s *Server) findAnyWingByWingID(wingID string) *ConnectedWing {
	all := s.Wings.All()
	for _, w := range all {
		if w.WingID == wingID {
			return w
		}
	}
	return nil
}



