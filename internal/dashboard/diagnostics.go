package dashboard

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

type diagnosticField struct{ label, flag, value string }

func (s *state) isOverlay() bool {
	return s.mode == confirmStop || s.mode == confirmTakeover || s.mode == help || s.mode == details || s.mode == runDetails || s.mode == runResult || s.mode == machineDetails || s.mode == diagnostics || s.mode == diagnosticInput || s.mode == diagnosticReport
}

type diagnosticState struct {
	title    string
	fields   []diagnosticField
	step     int
	args     []string
	lines    []string
	loading  bool
	sequence uint64
}

func (s *state) scrollOverlay(key string) {
	switch key {
	case "up", "k":
		s.overlayOffset = max(0, s.overlayOffset-1)
	case "down", "j":
		s.overlayOffset = min(s.overlayLimit, s.overlayOffset+1)
	case "pageup":
		s.overlayOffset = max(0, s.overlayOffset-5)
	case "pagedown":
		s.overlayOffset = min(s.overlayLimit, s.overlayOffset+5)
	case "home":
		s.overlayOffset = 0
	case "end":
		s.overlayOffset = s.overlayLimit
	}
}

func (s *state) diagnosticHandle(key string) (*action, bool) {
	d := &s.diagnostic
	if s.mode == diagnosticReport {
		if key == "escape" || key == "q" || key == "enter" {
			s.mode = diagnostics
		} else {
			s.scrollOverlay(key)
		}
		return nil, false
	}
	if key == "escape" || s.mode == diagnostics && key == "q" {
		s.mode, s.message = navigate, ""
		return nil, false
	}
	if s.mode == diagnostics {
		if d.loading {
			return nil, false
		}
		switch key {
		case "1", "2":
			d.title = "Launch readiness and egg reach"
			d.args = []string{"doctor", "launch-plan", "--unattended"}
			d.fields = []diagnosticField{
				{"Route: terminal_start, agent_start or agent_run", "--route", "agent_run"},
				{"Agent (blank for terminal_start)", "--agent", s.defaultAgent},
				{"Existing workspace on this machine", "--cwd", s.defaultCWD},
				{"Explicit provider HOME", "--home", ""},
				{"Allowed provider HOME root", "--allowed-home-root", ""},
				{"Absolute executable path (never invoked)", "--executable", ""},
				{"Policy file (blank explicitly selects built-in defaults)", "--config", ""},
				{"Policy base (blank uses workspace)", "--policy-base", ""},
				{"Host HOME for inheritance (blank uses provider HOME)", "--merge-home", ""},
			}
		case "3":
			d.title, d.args = "Inspect a completed copy safely", []string{"run-inspect"}
			d.fields = []diagnosticField{{"Completed snapshot inside this workspace", "--snapshot", ""}}
		default:
			return nil, false
		}
		d.step, d.lines = 0, nil
		s.mode, s.input, s.message = diagnosticInput, d.fields[0].value, ""
		return nil, false
	}
	if key == "enter" {
		f := &d.fields[d.step]
		if strings.TrimSpace(s.input) == "" && f.flag != "--agent" && f.flag != "--config" && f.flag != "--policy-base" && f.flag != "--merge-home" {
			s.message = "An explicit value is required"
			return nil, false
		}
		f.value = s.input
		d.step++
		if d.step < len(d.fields) {
			s.input, s.message = d.fields[d.step].value, ""
			return nil, false
		}
		args := append([]string{}, d.args...)
		for _, f := range d.fields {
			if f.value != "" {
				args = append(args, f.flag, f.value)
			}
		}
		d.loading, s.mode, s.overlayOffset, s.message = true, diagnosticReport, 0, ""
		d.sequence++
		return &action{kind: diagnosticReport, args: args}, false
	}
	switch key {
	case "ctrl-u":
		s.input = ""
	case "backspace":
		runes := []rune(s.input)
		if len(runes) > 0 {
			s.input = string(runes[:len(runes)-1])
		}
	default:
		if runes := []rune(key); len(runes) == 1 && !unicode.IsControl(runes[0]) {
			s.input += key
		}
	}
	s.message = ""
	return nil, false
}

func (s *state) diagnosticLines() []string {
	d := s.diagnostic
	if s.mode == diagnostics {
		lines := []string{"Runs / diagnostics", "1  Explain launch readiness", "2  What can this egg touch?", "3  Inspect a completed copy safely", "", "These reports use explicit local inputs. They never invoke a provider or connect to a wing.", "A remote workspace is not inspected on this machine; run doctor on its owning host.", "Saved native/provider evidence remains available through the full doctor CLI.", "", "Terminal task: wt run PROMPT --agent AGENT (sandboxed by default)", "Parent tasks: agent_run with wing_id, existing allowed cwd and original idempotency_key.", "h in the navigator: durable run receipts, authorized results and recovery guidance."}
		if d.loading {
			lines = append(lines, "A read-only report is still running.")
		}
		return lines
	}
	lines := []string{d.title, "Local read-only report; catalog unattended args stay inside the configured boundary."}
	if s.mode == diagnosticInput {
		lines = append(lines, fmt.Sprintf("Input %d / %d", d.step+1, len(d.fields)))
		for _, f := range d.fields[:d.step] {
			value := f.value
			if value == "" {
				value = "explicit default"
			}
			lines = append(lines, f.flag+": "+value)
		}
		return append(lines, "", d.fields[d.step].label+": "+s.input+"_", s.message)
	}
	if d.loading {
		return append(lines, "Reading bounded report…")
	}
	return append(lines, d.lines...)
}

// A bounded sink prevents a large, valid snapshot report from filling the UI's
// memory. Only existing read-only CLI verbs can reach this path.
type diagnosticBuffer struct {
	text     strings.Builder
	overflow bool
}

func (b *diagnosticBuffer) Write(p []byte) (int, error) {
	const limit = 128 << 10
	n := len(p)
	if n > limit-b.text.Len() {
		b.overflow = true
		_, _ = b.text.Write(p[:max(0, limit-b.text.Len())])
		return n, nil
	}
	_, _ = b.text.Write(p)
	return n, nil
}
func diagnosticResult(b *diagnosticBuffer, err error) []string {
	if b.overflow {
		return []string{"Report exceeded the 128 KiB terminal view; use the same read-only CLI command for the full report."}
	}
	lines := strings.Split(cleanExceptNewlines(b.text.String()), "\n")
	if err != nil {
		lines = append([]string{"Report refused: " + clean(err.Error())}, lines...)
	}
	return lines
}
func validateDiagnosticArgs(args []string) error {
	if len(args) > 0 && args[0] == "run-inspect" || len(args) > 1 && args[0] == "doctor" && args[1] == "launch-plan" {
		return nil
	}
	return errors.New("diagnostics require an existing read-only report command")
}

func (s *state) machineLines(now time.Time) []string {
	name := s.target.machine
	m, exists := s.machines[name]
	lines := []string{"Machines / " + machineLabel(name), ""}
	if !exists {
		return append(lines, "Saved machine was removed. Select its current profile.")
	}
	lines = append(lines, "Inventory: "+connection(m, now), machineCount(m))
	if s.registryError != "" {
		lines = append(lines, "Saved machines could not be loaded. Remote actions are blocked.")
	}
	if s.stale(name) {
		lines = append(lines, "", routeCause(m.err), "Cached sessions do not prove this machine is empty.", "Verify this machine before attaching. R retries from the list.")
	}
	if name == "local" {
		return append(lines, "", "Route: Local", "", "Add a machine with a saved SSH profile:", "wt remote add Studio SSH_ALIAS")
	}
	r := s.remotes[name]
	verification := "not verified"
	if r.WingID != "" && r.ControlSocket != "" && r.ControlVersion != "" {
		verification = "Saved identity verified; checked on every connection"
	}
	return append(lines, "", "Route: SSH", "Identity: "+verification, "", "Check the saved profile in your terminal:", "wt remote ls", "", "An identity change requires manual verification.", "Never bypass SSH host-key checks.")
}

func machineCount(m machine) string {
	if accessDenied(m.err) {
		return "Sessions unavailable"
	}
	if m.updated.IsZero() {
		return "Sessions not reported"
	}
	label := plural(len(m.sessions), "session")
	if m.err != "" {
		label += " cached"
	}
	return label
}
