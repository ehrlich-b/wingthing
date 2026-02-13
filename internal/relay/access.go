package relay

import "strings"

// canAccessWing returns true if userID can use this wing.
func (s *Server) canAccessWing(userID string, wing *ConnectedWing) bool {
	// Owner always has access
	if wing.UserID == userID {
		return true
	}

	// Org mode: check membership
	if wing.OrgID != "" {
		org, _ := s.Store.GetOrgBySlug(wing.OrgID)
		if org != nil && s.Store.IsOrgMember(org.ID, userID) {
			return true
		}
	}

	// Allow list: check email
	if len(wing.AllowEmails) > 0 {
		user, _ := s.Store.GetUserByID(userID)
		if user != nil && user.Email != nil {
			for _, e := range wing.AllowEmails {
				if strings.EqualFold(e, *user.Email) {
					return true
				}
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

// canAccessSession returns true if userID can access this PTY session.
func (s *Server) canAccessSession(userID string, session *PTYSession) bool {
	if session.UserID == userID {
		return true
	}
	wing := s.Wings.FindByID(session.WingID)
	if wing == nil {
		return false
	}
	return s.canAccessWing(userID, wing)
}

// listAccessiblePTYSessions returns all PTY sessions the user can access.
func (s *Server) listAccessiblePTYSessions(userID string) []*PTYSession {
	all := s.PTY.All()
	var result []*PTYSession
	for _, sess := range all {
		if s.canAccessSession(userID, sess) {
			result = append(result, sess)
		}
	}
	return result
}

// listAccessibleChatSessions returns all chat sessions the user can access.
func (s *Server) listAccessibleChatSessions(userID string) []*ChatSession {
	all := s.Chat.All()
	var result []*ChatSession
	for _, sess := range all {
		if sess.UserID == userID {
			result = append(result, sess)
			continue
		}
		wing := s.Wings.FindByID(sess.WingID)
		if wing != nil && s.canAccessWing(userID, wing) {
			result = append(result, sess)
		}
	}
	return result
}
