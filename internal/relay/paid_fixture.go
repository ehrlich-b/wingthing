package relay

// paidFixtureEnabled gates display only. It cannot issue account entitlements,
// run providers, or expose a payment endpoint. The local server enforces loopback
// Host and Origin checks independently of this flag.
func (s *Server) paidFixtureEnabled() bool {
	return s.Config.PaidFixture && s.LocalMode && s.DevMode && !s.RoostMode && s.Config.NodeRole == ""
}
