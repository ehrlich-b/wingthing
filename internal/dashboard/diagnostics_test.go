package dashboard

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestDiagnosticsExplicitInputLiteralArgvAndReturnScope(t *testing.T) {
	s := fixture()
	s.defaultAgent, s.defaultCWD = "codex", "/workspace"
	s.selected, s.filter = "local:two", "keep-query"
	s.handle("d")
	s.handle("1")
	for _, value := range []string{"agent_run", "codex", "/workspace", "/provider", "/", "/tools/codex", "/policy literal; $(no-exec).yaml", "/workspace", "/provider"} {
		s.handle("ctrl-u")
		for _, r := range value {
			s.handle(string(r))
		}
		act, quit := s.handle("enter")
		if quit {
			t.Fatal("unexpected quit")
		}
		if act != nil {
			want := []string{"doctor", "launch-plan", "--unattended", "--route", "agent_run", "--agent", "codex", "--cwd", "/workspace", "--home", "/provider", "--allowed-home-root", "/", "--executable", "/tools/codex", "--config", "/policy literal; $(no-exec).yaml", "--policy-base", "/workspace", "--merge-home", "/provider"}
			if act.kind != diagnosticReport || !reflect.DeepEqual(act.args, want) || validateDiagnosticArgs(act.args) != nil {
				t.Fatal(act)
			}
		}
	}
	if !s.diagnostic.loading {
		t.Fatal("report was not started")
	}
	s.handle("escape")
	s.handle("escape")
	if s.mode != navigate || s.selected != "local:two" || s.filter != "keep-query" {
		t.Fatal("diagnostics changed session scope")
	}
}

func TestDiagnosticsRequiredFieldsCancellationAndBoundedSanitizedOutput(t *testing.T) {
	s := fixture()
	s.handle("d")
	s.handle("3")
	act, _ := s.handle("enter")
	if act != nil || s.mode != diagnosticInput || s.message == "" {
		t.Fatal("implicit snapshot accepted")
	}
	s.handle("escape")
	if s.mode != navigate {
		t.Fatal("cannot cancel")
	}
	var b diagnosticBuffer
	b.Write([]byte("\x1b[2Juntrusted\u202etext\nnext line"))
	text := strings.Join(diagnosticResult(&b, errors.New("refused")), "\n")
	if strings.ContainsAny(text, "\x1b\u202e") || !strings.Contains(text, "next line") || !strings.Contains(text, "Report refused") {
		t.Fatal(text)
	}
	for i := 0; i < 33; i++ {
		b.Write([]byte(strings.Repeat("x", 4096)))
	}
	if b.text.Len() > 128<<10 || !strings.Contains(strings.Join(diagnosticResult(&b, nil), "\n"), "exceeded") {
		t.Fatal("report bound failed")
	}
	if validateDiagnosticArgs([]string{"run", "prompt"}) == nil {
		t.Fatal("diagnostic action permitted provider execution")
	}
}

func TestMachineDetailsFreshnessTrustFailureAndRemovedProfile(t *testing.T) {
	for _, tc := range []struct{ err, want string }{
		{"REMOTE HOST IDENTIFICATION HAS CHANGED", "Identity changed"},
		{"Host key verification failed", "Identity changed"},
		{"remote version mismatch", "Update wt"},
		{"connection refused", "Connection refused"},
	} {
		s := fixture()
		s.mode = machineDetails
		s.target = row{machine: "remote"}
		s.machines["remote"] = machine{err: tc.err, updated: time.Now().Add(-time.Minute)}
		s.remotes = map[string]config.Remote{"remote": {SSHTarget: "fixture-remote", WingID: "remote-id", ControlVersion: "1", ControlSocket: "/fixture/socket", WingthingDir: "/fixture/state"}}
		text := strings.Join(overlayLines(s, time.Now()), "\n")
		for _, want := range []string{tc.want, "Last seen 1m ago", "Machine ID checked", "may be out of date"} {
			if want == "may be out of date" && tc.want != "Connection refused" {
				continue
			}
			if !strings.Contains(text, want) {
				t.Fatalf("missing %q: %s", want, text)
			}
		}
		delete(s.machines, "remote")
		if !strings.Contains(strings.Join(overlayLines(s, time.Now()), "\n"), "removed") {
			t.Fatal("removed route presented as current")
		}
	}
}

func TestMachineDetailsUnverifiedProfileAndPicker(t *testing.T) {
	s := fixture()
	s.remotes = map[string]config.Remote{"remote": {SSHTarget: "fixture-remote"}}
	s.machines["remote"] = machine{}
	s.handle("m")
	s.picker = len(s.machineNames())
	s.handle("i")
	if s.mode != machineDetails || !strings.Contains(strings.Join(s.machineLines(time.Now()), "\n"), "Machine ID not saved yet") {
		t.Fatal("legacy profile claimed verified")
	}
	s.handle("escape")
	if s.mode != navigate {
		t.Fatal("machine inspection cannot return")
	}
}

func TestRunReceiptRefreshKeepsOriginalKeyAndCurrentOutcome(t *testing.T) {
	s := fixture()
	s.recent, s.mode = true, runDetails
	h := s.history()
	r := runRow{WingID: "remote-id", ID: "same", SessionID: "execution", Status: "running", RequestKey: "original-key", route: runRoute{name: "remote", wingID: "remote-id"}}
	h.target = r
	r.Status, r.Terminal, r.Cleanup, r.OutputAvailability = "done", true, "unknown", "expired"
	h.pages[r.WingID] = runPage{Runs: []runRow{r}, ObservedAt: "2026-10-12T20:00:00Z"}
	text := strings.Join(s.runOverlay(), "\n")
	for _, want := range []string{"Result: + Done", "Time limit: None", "the output is gone", "Cleanup: Not checked", "Checked: "} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %s", want, text)
		}
	}
	s.handle("r")
	recovery := strings.Join(s.runOverlay(), "\n")
	for _, want := range []string{"saved the original request", "never sends it again", "may still run on remote", "On remote, run:"} {
		if !strings.Contains(recovery, want) {
			t.Fatalf("missing %s in %s", want, recovery)
		}
	}
	if h.target.RequestKey != "original-key" || h.target.ID != "same" || strings.Contains(text, "Result: * Working") {
		t.Fatal("open receipt stayed on old outcome")
	}
}

func TestRunReceiptPendingAdmissionIsNotUnknownCompletion(t *testing.T) {
	s := fixture()
	s.mode = runDetails
	r := runRow{WingID: "wing", ID: "run", SessionID: "egg", Status: "pending", Phase: "observing", RequestKey: "original"}
	s.history().target = r
	s.history().pages["wing"] = runPage{Runs: []runRow{r}}
	text := strings.Join(s.runOverlay(), "\n")
	if !strings.Contains(text, "Result: ! Pending") || !strings.Contains(text, "wait for it to start") || strings.Contains(text, "? Unknown") || strings.Contains(text, "check its work") {
		t.Fatal(text)
	}
}
