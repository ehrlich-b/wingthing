package localmcp

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/fleet"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

func TestContextPhoneFleetTypedRefusal(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Dir: dir, WingID: "home-wing"}
	wc := &config.WingConfig{WingID: "home-wing", PhoneFleet: true, PhoneFleetExcludeContexts: []string{"work"}}
	service := &wingsession.Service{Config: cfg, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wc} }}
	owner, err := config.EnsureLocalOwner(dir, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveRemotes(dir, map[string]config.Remote{"slide": {SSHTarget: "must-not-contact", Context: "work", WingID: "work-wing", WingthingDir: "/work/state", ControlSocket: "/work/state/control.sock", ControlVersion: "v1"}}); err != nil {
		t.Fatal(err)
	}
	for op, raw := range map[string]string{
		"session_inventory": `{"include_remotes":true}`,
		"session_search":    `{"include_remotes":true,"query":"private"}`,
		"session_action":    `{"origin_wing_id":"work-wing","session_id":"full-id","operation":"terminal_read"}`,
		"session_launch":    `{"origin_wing_id":"work-wing","operation":"terminal_start","arguments":{}}`,
	} {
		_, err := OwnerFleetControl(t.Context(), service, owner.ID, op, json.RawMessage(raw))
		var refusal *PhoneFleetContextRefusal
		if !errors.As(err, &refusal) || refusal.Context != "work" || refusal.Remote != "slide" {
			t.Fatalf("%s: wanted typed context refusal, got %T %v", op, err, err)
		}
	}
	// Attachment admission and an existing attachment recheck must fail before
	// either tries to open a control socket or SSH forward.
	id := fleet.Identity{OriginWingID: "work-wing", SessionID: "full-id"}
	_, err = OwnerFleetAttach(t.Context(), service, owner.ID, id, true, false)
	var refusal *PhoneFleetContextRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("attachment admitted: %T %v", err, err)
	}
	remotes, err := config.LoadRemotes(dir)
	if err != nil {
		t.Fatal(err)
	}
	remote := remotes["slide"]
	attachment := &FleetAttachment{Identity: id, sessions: service, owner: owner.ID, remote: &remote, Route: fleet.Route{Remote: "slide"}}
	if err := attachment.Check(t.Context(), false); !errors.As(err, &refusal) {
		t.Fatalf("live attachment not revoked: %T %v", err, err)
	}
	wc.PhoneFleetExcludeContexts = nil
	if err := checkPhoneFleetContexts(dir, wc, "work-wing", false); err != nil {
		t.Fatal("SSH remote context alone broadened exclusions", err)
	}
}
