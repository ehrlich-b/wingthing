package dashboard

import (
	"strings"
	"testing"
)

func TestDashboardStaleInventoryDisablesActionsUntilVerified(t *testing.T) {
	for _, key := range []string{"enter", "v", "t", "r", "n"} {
		t.Run(key, func(t *testing.T) {
			s := fixture()
			s.selected = "work:one"
			m := s.machines["work"]
			m.err = "SSH unavailable"
			s.machines["work"] = m
			if act, _ := s.handle(key); act != nil || s.mode != navigate || !strings.Contains(s.message, "cannot validate") {
				t.Fatal("stale inventory permitted an action", act, s.message)
			}
			s.selected = "local:one"
			if act, _ := s.handle("enter"); act == nil || act.row.key() != "local:one" {
				t.Fatal("remote outage disabled local attachment")
			}
			m.err = ""
			s.machines["work"] = m
			s.selected = "work:one"
			if act, _ := s.handle("enter"); act == nil || act.row.key() != "work:one" {
				t.Fatal("successful inventory did not restore exact attachment")
			}
		})
	}
}

func TestDashboardStaleStopConfirmationCannotDispatch(t *testing.T) {
	s := fixture()
	s.selected = "work:one"
	m := s.machines["work"]
	m.err = "SSH unavailable"
	s.machines["work"] = m
	if act, _ := s.handle("x"); act != nil || s.mode != confirmStop || s.target.key() != "work:one" {
		t.Fatal("stop confirmation lost the exact stale target")
	}
	if act, _ := s.handle("y"); act != nil || s.mode != navigate || !strings.Contains(s.message, "cannot validate") {
		t.Fatal("stale confirmation dispatched stop", act, s.message)
	}
	// Inventory may fail after the confirmation was opened on a healthy row.
	m.err = ""
	s.machines["work"] = m
	s.handle("x")
	m.err = "SSH unavailable"
	s.machines["work"] = m
	if act, _ := s.handle("y"); act != nil || !strings.Contains(s.message, "cannot validate") {
		t.Fatal("confirmation raced a failed inventory")
	}
}
