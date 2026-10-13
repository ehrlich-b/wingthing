package relay

import "testing"

func TestPaidFixtureRequiresExplicitLocalDevelopment(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		flag, local, dev, roost bool
		role                    string
		want                    bool
	}{
		{"default", false, true, true, false, "", false},
		{"hosted", true, false, true, false, "", false},
		{"ordinary-local", true, true, false, false, "", false},
		{"roost", true, true, true, true, "", false},
		{"edge", true, true, true, false, "edge", false},
		{"login", true, true, true, false, "login", false},
		{"fixture", true, true, true, false, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{Config: ServerConfig{PaidFixture: tc.flag, NodeRole: tc.role}, LocalMode: tc.local, DevMode: tc.dev, RoostMode: tc.roost}
			if got := s.paidFixtureEnabled(); got != tc.want {
				t.Fatalf("enabled = %v, want %v", got, tc.want)
			}
		})
	}
}
