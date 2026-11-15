package relay

import "testing"

func TestRemediationRevokedMemberRoster(t *testing.T) {
	s, wing, owner, member, _ := setupAuthzServer(t)
	if len(s.listAccessibleWings(member)) != 1 {
		t.Fatal("member must initially see org wing")
	}
	if _, err := s.Store.RemoveOrgMemberAndEntitlement(wing.OrgID, member); err != nil {
		t.Fatal(err)
	}
	if len(s.listAccessibleWings(member)) != 0 {
		t.Fatal("revoked member still sees wing")
	}
	if len(s.listAccessibleWings(owner)) != 1 {
		t.Fatal("wing must remain online for owner")
	}
	t.Log("org membership revoked: same wing still connected, former member roster empty")
	s.Wings.Remove(wing.ID)
	if len(s.listAccessibleWings(owner)) != 0 {
		t.Fatal("disconnected wing still in online roster")
	}
	t.Log("wing disconnected: owner roster empty, with no offline metadata")
}
