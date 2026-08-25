package relay

import "time"

const (
	RelayPolicyLegacy     = "legacy"
	RelayPolicyDirectFree = "direct-free"
)

// DefaultRelayGrandfatherBefore is the hosted migration boundary. Accounts
// created before this instant keep temporary relay parity when the hosted
// direct-free policy is enabled. Operators can override it explicitly.
var DefaultRelayGrandfatherBefore = time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)

type RelayAccess struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

func (s *Server) relayAccess(userID string) RelayAccess {
	if s.LocalMode || s.RoostMode {
		return RelayAccess{Allowed: true, Reason: "self-hosted"}
	}
	if s.Config.RelayPolicy != RelayPolicyDirectFree {
		return RelayAccess{Allowed: true, Reason: "legacy-policy"}
	}
	// Edge nodes open a local store for process plumbing, but account truth
	// lives on the login node. Prefer its synchronized decision whenever the
	// cache is installed; consulting the empty edge DB would incorrectly turn
	// every Pro and grandfathered user into direct-only free.
	if s.EntitlementCache != nil {
		return s.EntitlementCache.GetRelayAccess(userID)
	}
	if s.Store != nil {
		if s.Store.IsUserPro(userID) {
			return RelayAccess{Allowed: true, Reason: "pro"}
		}
		user, _ := s.Store.GetUserByID(userID)
		if user != nil && !s.Config.RelayGrandfatherBefore.IsZero() && !user.CreatedAt.After(s.Config.RelayGrandfatherBefore) {
			return RelayAccess{Allowed: true, Reason: "temporary-grandfather"}
		}
		return RelayAccess{Allowed: false, Reason: "direct-only-free"}
	}
	return RelayAccess{Allowed: false, Reason: "entitlement-unavailable"}
}
