package dashboard

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
)

func TestWorklistKeepsRowsInPlaceUntilRefresh(t *testing.T) {
	s := fixture()
	before := s.rows()
	s.selected = "local:one"
	s.selectionTouched = true
	m := s.machines["work"]
	m.sessions[0].Status, m.sessions[0].State = "blocked", "needs_input"
	m.sessions[0].StateCursor++
	s.machines["work"] = m
	s.reconcile()
	for i, r := range s.rows() {
		if r.key() != before[i].key() {
			t.Fatal("background update reordered the work list")
		}
	}
	if s.selected != "local:one" {
		t.Fatal("background update moved selection")
	}
	s.handle("R")
	if s.rows()[1].key() != "work:one" || s.selected != "local:one" {
		t.Fatal("refresh did not reconcile attention while preserving target")
	}
}
func TestSearchLeavesWingForMatchesAndEscapeRestoresTarget(t *testing.T) {
	s := fixture()
	m := s.machines["local"]
	p := session("pin", "Coordinator", "/app", "idle")
	p.Metadata = sessionrole.Metadata{Role: "coordinator", Pinned: true}
	m.sessions = append(m.sessions, p)
	s.machines["local"] = m
	s.selected = "local:pin"
	s.reconcile()
	drive(s, "/review")
	if s.selected != "local:two" {
		t.Fatal("search kept wing instead of its matching work")
	}
	drive(s, "\x1b")
	if s.selected != "local:pin" || s.filter != "" {
		t.Fatal("Escape lost the pre-search target")
	}
	s.machineFilter = "work"
	s.reconcile()
	if !s.rows()[0].Pinned {
		t.Fatal("machine scope hid wing")
	}
}
func TestStaleSessionCannotAttachUntilSuccessfulInventory(t *testing.T) {
	s := fixture()
	m := s.machines["work"]
	m.err = "identity changed"
	s.machines["work"] = m
	s.selected = "work:one"
	for _, key := range []string{"enter", "v"} {
		if a, _ := s.handle(key); a != nil {
			t.Fatal("attached cached identity")
		}
	}
	inv := newInventory(nil)
	inv.apply(s, inventoryResult{name: "work", sessions: []eggclient.LocalSession{session("one", "remote", "/app", "working")}, updated: time.Now()})
	if a, _ := s.handle("enter"); a == nil || a.row.ID != "one" || a.row.machine != "work" {
		t.Fatal("successful retry lost exact attachment target")
	}
}
func TestInventoryFailureDatesAttemptWithoutRefreshingCache(t *testing.T) {
	s := fixture()
	success := time.Unix(1000, 0)
	retry := success.Add(14 * time.Minute)
	m := s.machines["work"]
	m.updated = success
	s.machines["work"] = m
	newInventory(nil).apply(s, inventoryResult{name: "work", err: fmt.Errorf("connection timed out"), updated: retry})
	m = s.machines["work"]
	if m.updated != success || m.attempted != retry {
		t.Fatal("failed attempt dated cached sessions as fresh")
	}
	text := connection(m, retry)
	if !strings.Contains(text, "last seen 14m ago") || !strings.Contains(text, "retried 0s ago") {
		t.Fatal(text)
	}
}
func TestMachinePickerKeepsLastSavedMachineVisible(t *testing.T) {
	s := &state{machines: map[string]machine{"local": {}}}
	for i := 0; i < 25; i++ {
		s.machines[fmt.Sprintf("Studio-%02d", i)] = machine{}
	}
	s.mode, s.picker = machinePicker, 26
	text := strings.Join(renderedLines(t, render(s, 80, 24, time.Now()), 80, 24), "\n")
	if !strings.Contains(text, "> Studio-24") {
		t.Fatal("selected saved machine scrolled offscreen")
	}
}
func TestLongTitlePreservesIdentityAndRecovery(t *testing.T) {
	s := fixture()
	m := s.machines["work"]
	m.sessions[0].Name = strings.Repeat("Repair DNS ", 30)
	s.machines["work"] = m
	s.selected = "work:one"
	lines := renderedLines(t, render(s, 80, 24, time.Now()), 80, 24)
	if !strings.Contains(lines[18], "work") || !strings.Contains(lines[20], "i details") {
		t.Fatal("long title hid target or recovery")
	}
	s.handle("i")
	full := strings.Join(overlayLines(s, time.Now()), "\n")
	if !strings.Contains(full, m.sessions[0].Name) {
		t.Fatal("full title unavailable in details")
	}
}
func TestActionMenuAndStopKeepExactTarget(t *testing.T) {
	s := fixture()
	s.selected = "work:one"
	s.handle(".")
	if s.mode != actionMenu {
		t.Fatal("actions did not open")
	}
	s.handle("x")
	s.machines["work"] = machine{sessions: []eggclient.LocalSession{session("replacement", "remote", "/app", "idle")}}
	s.reconcile()
	if a, _ := s.handle("enter"); a != nil {
		t.Fatal("default stop action was destructive")
	}
	s.selected = "local:one"
	s.handle(".")
	s.handle("x")
	a, _ := s.handle("y")
	if a == nil || a.row.key() != "local:one" {
		t.Fatal("confirmed stop lost captured target")
	}
}
func TestNewSessionDraftSurvivesCancel(t *testing.T) {
	s := fixture()
	drive(s, "n\x15codex\r\x15/selected/workspace\x1b")
	drive(s, "n\x15codex\r")
	if s.mode != newCWD || s.input != "/selected/workspace" {
		t.Fatal("cancel discarded launch draft")
	}
}
func TestActionNoticesNeverEchoRemoteDiagnostics(t *testing.T) {
	for _, message := range []string{"failed /tmp/session-abcdef.log", "error /Users/me/private", "rpc denied on abcdef123456", "{\"result\":\"YmxvYg==\"}", "error /opt/provider.log"} {
		notice := safeNotice(message)
		if strings.ContainsAny(notice, "/{}") || strings.Contains(notice, "abcdef") {
			t.Fatalf("diagnostic became chrome: %q", notice)
		}
	}
}

func TestRunRecoveryPreservesSavedRouteAndClient(t *testing.T) {
	t.Setenv("WT_MCP_CLIENT", "personal")
	s := fixture()
	s.recent, s.mode = true, runDetails
	r := runRow{WingID: "studio", ID: "run", SessionID: "egg", Status: "failed", FailureKind: "unknown_outcome", RequestKey: "original", Isolation: "privileged", route: runRoute{name: "Studio", wingID: "studio", remote: config.Remote{SSHTarget: "studio-demo", WTBinary: "/usr/local/bin/wt", WingthingDir: "/workspace/state", WingID: "studio"}}}
	s.history().target = r
	s.history().pages["studio"] = runPage{Runs: []runRow{r}}
	text := strings.Join(s.runOverlay(), "\n")
	for _, want := range []string{"Result: ? Unknown", "Sandbox off", "full access to your account", "check its work before you run it again", "r recovery steps"} {
		if !strings.Contains(text, want) {
			t.Errorf("details lost %q", want)
		}
	}
	s.handle("r")
	text = strings.Join(s.runOverlay(), "\n")
	for _, want := range []string{"Recovery steps", "never sends it again", "On Studio, run:", "  " + runRecoveryCommand(), "wt agent recover --client personal"} {
		if !strings.Contains(text, want) {
			t.Errorf("recovery lost %q", want)
		}
	}
	// Recovery names the owning machine; it never prints saved route flags, IDs or shell quoting.
	for _, banned := range []string{"--remote", "studio-demo", "'", "In a terminal, run:"} {
		if strings.Contains(text, banned) {
			t.Errorf("recovery printed %q", banned)
		}
	}
	s.handle("r")
	if strings.Contains(strings.Join(s.runOverlay(), "\n"), "Recovery steps") {
		t.Fatal("r did not return to details")
	}
	if s.history().target.RequestKey != "original" {
		t.Fatal("receipt lost original request")
	}
}

func TestSetupKeysRemainAtBottomAndAppearOnce(t *testing.T) {
	s := setupFixture()
	s.setupAgent = s.agents[0]
	for _, mode := range []mode{agentSetup, installConsent, providerReady, setupFailure} {
		s.mode = mode
		lines := renderedLines(t, render(s, 80, 24, time.Now()), 80, 24)
		text := strings.Join(lines, "\n")
		if strings.Count(text, "Ctrl-C quit") != 1 || strings.Count(text, "Esc back") != 1 || !strings.Contains(lines[23], "Esc back") {
			t.Fatal("setup hints duplicated or moved")
		}
	}
}

func TestUnknownInventoryNeverClaimsKnownZero(t *testing.T) {
	s := &state{machines: map[string]machine{"local": {err: "connection refused"}}}
	lines := renderedLines(t, render(s, 80, 24, time.Now()), 80, 24)
	if !strings.Contains(lines[0], "Sessions unavailable") || strings.Contains(lines[0], "0 sessions") {
		t.Fatal("failed inventory claimed zero sessions")
	}
	s.machines["local"] = machine{loading: true}
	lines = renderedLines(t, render(s, 80, 24, time.Now()), 80, 24)
	if !strings.Contains(lines[0], "Loading sessions") {
		t.Fatal("initial inventory looked complete")
	}
	s.machines["local"] = machine{updated: time.Now()}
	lines = renderedLines(t, render(s, 80, 24, time.Now()), 80, 24)
	if !strings.Contains(lines[0], "0 sessions") {
		t.Fatal("successful zero inventory lost its count")
	}
}

func TestScopedNewSessionUsesSelectedMachineOutsidePinnedWing(t *testing.T) {
	s := fixture()
	m := s.machines["local"]
	pin := session("pin", "Coordinator", "/coordinator", "idle")
	pin.Metadata = sessionrole.Metadata{Role: "coordinator", Pinned: true}
	m.sessions = append(m.sessions, pin)
	s.machines["local"] = m
	s.machines["Studio"] = machine{updated: time.Now()}
	s.machineFilter = "Studio"
	s.reconcile()
	if r, _ := s.selection(); !r.Pinned {
		t.Fatal("fixture did not preserve wing outside the empty scope")
	}
	s.handle("n")
	s.input = "codex"
	s.handle("enter")
	s.input = "/studio/project"
	action, _ := s.handle("enter")
	if action == nil || action.row.machine != "Studio" || action.cwd != "/studio/project" {
		t.Fatalf("new session targeted pin machine: %+v", action)
	}
}

func TestInitialInventoryHasRefreshBeforeEmptyStateIsKnown(t *testing.T) {
	s := &state{machines: map[string]machine{"local": {loading: true}}}
	lines := renderedLines(t, render(s, 80, 24, time.Now()), 80, 24)
	if !strings.Contains(lines[22], "R refresh") || strings.Contains(lines[22], "a install an agent") {
		t.Fatal("loading presented an unverified empty-state action")
	}
}

func TestInitialInventoryKeepsAttentionFirstBeforeUserNavigation(t *testing.T) {
	s := &state{machines: map[string]machine{"local": {sessions: []eggclient.LocalSession{session("local", "Local work", "/project", "working")}, updated: time.Now()}, "work": {}}}
	s.reconcile()
	newInventory(nil).apply(s, inventoryResult{name: "work", sessions: []eggclient.LocalSession{session("reply", "Needs a reply", "/project", "blocked")}, updated: time.Now()})
	if s.rows()[0].key() != "work:reply" {
		t.Fatal("first local response froze attention behind working rows")
	}
	s.handle("i")
	before := s.rows()
	m := s.machines["local"]
	m.sessions[0].State = "needs_input"
	m.sessions[0].Status = "blocked"
	m.sessions[0].StateCursor++
	s.machines["local"] = m
	s.reconcile()
	for i, r := range s.rows() {
		if r.key() != before[i].key() {
			t.Fatal("background change reordered rows after deliberate detail selection")
		}
	}
}
