package relay

const (
	RelayPolicyLegacy     = "legacy"
	RelayPolicyDirectFree = "direct-free"
)

type RelayAccess struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

// selfServicePlansEnabled reports whether the historical, billing-free plan
// mutation endpoints are part of this deployment. They remain available to
// legacy and self-hosted installs for compatibility, but must not let users on
// the public direct-free tier grant themselves hosted-relay access.
func (s *Server) selfServicePlansEnabled() bool {
	return s.LocalMode || s.RoostMode || s.Config.RelayPolicy != RelayPolicyDirectFree
}

func (s *Server) relayAccess(userID string) RelayAccess {
	if s.LocalMode {
		return RelayAccess{Allowed: true, Reason: "self-hosted"}
	}
	if !s.roostUserIDAllowed(userID) {
		return RelayAccess{Allowed: false, Reason: "roost-enrollment-required"}
	}
	if s.RoostMode {
		return RelayAccess{Allowed: true, Reason: "self-hosted"}
	}
	if s.Config.RelayPolicy != RelayPolicyDirectFree {
		return RelayAccess{Allowed: true, Reason: "legacy-policy"}
	}
	// Edge nodes open a local store for process plumbing, but account truth
	// lives on the login node. Prefer its synchronized decision whenever the
	// cache is installed; consulting the empty edge DB would incorrectly turn
	// every Pro and temporary-migration user into direct-only free.
	if s.EntitlementCache != nil {
		return s.EntitlementCache.GetRelayAccess(userID)
	}
	if s.Store != nil {
		if s.Store.IsUserPro(userID) {
			return RelayAccess{Allowed: true, Reason: "pro"}
		}
		user, _ := s.Store.GetUserByID(userID)
		if user != nil && !s.Config.RelayMigrationBefore.IsZero() && !user.CreatedAt.After(s.Config.RelayMigrationBefore) {
			return RelayAccess{Allowed: true, Reason: "temporary-migration"}
		}
		return RelayAccess{Allowed: false, Reason: "direct-only-free"}
	}
	return RelayAccess{Allowed: false, Reason: "entitlement-unavailable"}
}
