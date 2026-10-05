package wingpolicy

import (
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestRequestAgainstWingConfigRevalidatesLocalAdmin(t *testing.T) {
	req := ws.TunnelRequest{SenderUserID: "member-1", SenderEmail: "member@example.com", SenderOrgRole: "admin"}
	withoutOverride := RequestAgainstWingConfig(req, "member", &config.WingConfig{})
	if withoutOverride.SenderOrgRole != "member" {
		t.Fatalf("removed override retained stale role %q", withoutOverride.SenderOrgRole)
	}
	withOverride := RequestAgainstWingConfig(req, "member", &config.WingConfig{Admins: []string{"MEMBER@example.com"}})
	if withOverride.SenderOrgRole != "admin" {
		t.Fatalf("live override role = %q, want admin", withOverride.SenderOrgRole)
	}
}
