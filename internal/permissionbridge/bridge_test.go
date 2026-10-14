package permissionbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureInput(t *testing.T, command string) Input {
	t.Helper()
	return Input{Event: "PermissionRequest", ProviderID: "provider-fixture", Tool: "Bash", Arguments: map[string]any{"command": command, "description": "fixture"}, Directory: t.TempDir()}
}

func startFixtureBroker(t *testing.T, in Input, steps []Step) (string, *bytes.Buffer, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "pb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "broker.sock")
	l, err := Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	log := new(bytes.Buffer)
	b := &Broker{Script: Script{Session: "egg-fixture", ProviderID: in.ProviderID, Steps: steps}, Log: log}
	done := make(chan error, 1)
	go func() { done <- b.Serve(ctx, l) }()
	stop := func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
	return socket, log, stop
}

func callFixtureHook(t *testing.T, socket string, in Input, limit time.Duration) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	var out bytes.Buffer
	err := Hook(ctx, socket, "egg-fixture", in, &out)
	return out.String(), err
}

func TestPermissionBridgeAllowOnceAndDeny(t *testing.T) {
	for _, decision := range []string{"allow", "deny"} {
		t.Run(decision, func(t *testing.T) {
			in := fixtureInput(t, "echo fixture")
			socket, log, stop := startFixtureBroker(t, in, []Step{{Digest: Digest(in), Attempts: []Attempt{{Binding: "current", Decision: decision}}}})
			out, err := callFixtureHook(t, socket, in, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Output struct {
					Event    string         `json:"hookEventName"`
					Decision map[string]any `json:"decision"`
				} `json:"hookSpecificOutput"`
			}
			if json.Unmarshal([]byte(out), &wire) != nil || wire.Output.Event != "PermissionRequest" || wire.Output.Decision["behavior"] != decision {
				t.Fatalf("unexpected decision %s", out)
			}
			if _, ok := wire.Output.Decision["updatedPermissions"]; ok {
				t.Fatal("rule update emitted")
			}
			if _, ok := wire.Output.Decision["updatedInput"]; ok {
				t.Fatal("arguments rewritten")
			}
			if decision == "deny" && wire.Output.Decision["interrupt"] != false {
				t.Fatal("denial interrupts provider")
			}
			// The script is consumed: the same command under a fresh request cannot
			// inherit the prior allow/deny. This is a protocol test, not execution proof.
			out, err = callFixtureHook(t, socket, in, time.Second)
			if err == nil || out != "" {
				t.Fatalf("answer reused: %s %v", out, err)
			}
			stop()
			if strings.Count(log.String(), "accepted_") != 1 || strings.Contains(log.String(), "echo fixture") {
				t.Fatalf("unexpected log %s", log.String())
			}
		})
	}
}

func TestPermissionBridgeExpiryProducesNoDecision(t *testing.T) {
	in := fixtureInput(t, "echo expiry")
	socket, log, stop := startFixtureBroker(t, in, []Step{{Digest: Digest(in), Attempts: []Attempt{{Binding: "current", Decision: "allow", DelayMillis: 200}}}})
	started := time.Now()
	out, err := callFixtureHook(t, socket, in, 50*time.Millisecond)
	if err == nil || out != "" || time.Since(started) > time.Second {
		t.Fatalf("expiry failed: %q %v", out, err)
	}
	stop()
	if strings.Contains(log.String(), "accepted_") || !strings.Contains(log.String(), "expired_no_decision") {
		t.Fatalf("late answer accepted: %s", log.String())
	}
}

func TestPermissionBridgeStaleAndCompetingAnswers(t *testing.T) {
	in := fixtureInput(t, "echo race")
	steps := []Step{
		{Digest: Digest(in), Attempts: []Attempt{{Binding: "current", Decision: "deny"}}},
		{Digest: Digest(in), Attempts: []Attempt{{Binding: "previous", Decision: "allow"}, {Binding: "current", Decision: "deny", DelayMillis: 10}}},
		{Digest: Digest(in), Attempts: []Attempt{{Binding: "current", Decision: "allow"}, {Binding: "current", Decision: "deny"}}},
	}
	socket, log, stop := startFixtureBroker(t, in, steps)
	for i := 0; i < 3; i++ {
		out, err := callFixtureHook(t, socket, in, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && !strings.Contains(out, `"behavior":"deny"`) {
			t.Fatalf("stale allow won: %s", out)
		}
		if strings.Count(out, "hookSpecificOutput") != 1 {
			t.Fatalf("multiple answers %s", out)
		}
	}
	stop()
	if strings.Count(log.String(), "accepted_") != 3 || strings.Count(log.String(), "rejected_binding") != 1 || strings.Count(log.String(), "rejected_already_answered") != 1 {
		t.Fatalf("reservation counts: %s", log.String())
	}
	ids := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(log.String()), "\n") {
		var row struct {
			Binding
			Status string `json:"status"`
		}
		if json.Unmarshal([]byte(line), &row) != nil {
			t.Fatal(line)
		}
		if row.Status == "opened" {
			if ids[row.RequestID] {
				t.Fatal("request ID reused")
			}
			ids[row.RequestID] = true
		}
	}
	if len(ids) != 3 {
		t.Fatal("missing request identities")
	}
}

func TestPermissionBridgeHookRejectsUnboundAnswers(t *testing.T) {
	for _, field := range []string{"session", "provider", "id", "digest", "decision"} {
		t.Run(field, func(t *testing.T) {
			in := fixtureInput(t, "echo invalid")
			dir, err := os.MkdirTemp("", "pb-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "b.sock")
			l, err := Listen(socket)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				c, err := l.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				var req Request
				if decodeStrict(c, &req) != nil {
					return
				}
				a := Answer{req.Binding, "allow"}
				switch field {
				case "session":
					a.Session = "other"
				case "provider":
					a.ProviderID = "other"
				case "id":
					a.RequestID = "old"
				case "digest":
					a.Digest = "changed"
				case "decision":
					a.Decision = "allow-all"
				}
				_ = json.NewEncoder(c).Encode(a)
			}()
			out, err := callFixtureHook(t, socket, in, time.Second)
			<-done
			if err == nil || out != "" {
				t.Fatalf("unbound answer accepted: %q %v", out, err)
			}
		})
	}
}

func TestPermissionBridgeRequestReplayAndChangedInput(t *testing.T) {
	in := fixtureInput(t, "echo once")
	socket, log, stop := startFixtureBroker(t, in, []Step{{Digest: Digest(in), Attempts: []Attempt{{Binding: "current", Decision: "allow"}}}, {Digest: Digest(in)}})
	req := Request{Binding: Binding{"egg-fixture", in.ProviderID, strings.Repeat("a", 32), Digest(in)}, Input: in, Deadline: time.Now().Add(time.Second)}
	exchange := func(r Request) (Answer, error) {
		c, err := net.Dial("unix", socket)
		if err != nil {
			return Answer{}, err
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(time.Second))
		if err := json.NewEncoder(c).Encode(r); err != nil {
			return Answer{}, err
		}
		var a Answer
		err = decodeStrict(c, &a)
		return a, err
	}
	if _, err := exchange(req); err != nil {
		t.Fatal(err)
	}
	if _, err := exchange(req); err == nil {
		t.Fatal("replayed request accepted")
	}
	req.RequestID = strings.Repeat("b", 32)
	req.Input.Arguments["command"] = "echo changed"
	if _, err := exchange(req); err == nil {
		t.Fatal("changed command accepted with old digest")
	}
	stop()
	if strings.Count(log.String(), "rejected_request") != 2 {
		t.Fatalf("rejection counts: %s", log.String())
	}
}

func TestPermissionBridgeCanonicalInputAndInvalidRequests(t *testing.T) {
	a, err := ParseInput(strings.NewReader(`{"session_id":"p","hook_event_name":"PermissionRequest","cwd":"/fixture","tool_name":"Bash","tool_input":{"command":"echo x","timeout":9007199254740993},"permission_mode":"manual"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseInput(strings.NewReader(`{"tool_input":{"timeout":9007199254740993,"command":"echo x"},"tool_name":"Bash","cwd":"/fixture","hook_event_name":"PermissionRequest","session_id":"p"}`))
	if err != nil || Digest(a) != Digest(b) {
		t.Fatal("noncanonical digest")
	}
	b.Directory = "/other"
	if Digest(a) == Digest(b) {
		t.Fatal("directory not bound")
	}
	b = a
	b.Tool = "Read"
	if Digest(a) == Digest(b) {
		t.Fatal("tool not bound")
	}
	for _, raw := range []string{`{}`, `{"session_id":"p","hook_event_name":"PreToolUse","cwd":"/fixture","tool_name":"Bash","tool_input":{"command":"echo x"}}`, `{"session_id":"p","hook_event_name":"PermissionRequest","cwd":"relative","tool_name":"Bash","tool_input":{"command":"echo x"}}`, `{"session_id":"p","hook_event_name":"PermissionRequest","cwd":"/fixture","tool_name":"Read","tool_input":{"command":"echo x"}}`, `{"session_id":"p","hook_event_name":"PermissionRequest","cwd":"/fixture","tool_name":"Bash","tool_input":{"command":"echo x"}} {}`} {
		if _, err := ParseInput(strings.NewReader(raw)); err == nil {
			t.Fatalf("invalid hook accepted: %s", raw)
		}
	}
	if (&Broker{Script: Script{Session: "s", ProviderID: "p", Steps: []Step{{Digest: "*"}}}}).Validate() == nil {
		t.Fatal("wildcard script accepted")
	}
	if (&Broker{Script: Script{Session: "s", ProviderID: "p", Steps: []Step{{Digest: Digest(a), Attempts: []Attempt{{Binding: "current", Decision: "allow-all"}}}}}}).Validate() == nil {
		t.Fatal("allow-all accepted")
	}
}

func TestPermissionBridgeMissingCommandIsLoggedWithoutConsumingStep(t *testing.T) {
	for _, arguments := range []map[string]any{{}, {"command": ""}, {"command": 7}} {
		in := fixtureInput(t, "unused")
		in.Arguments = arguments
		log := new(bytes.Buffer)
		b := &Broker{Script: Script{Session: "egg-fixture", ProviderID: in.ProviderID,
			Steps: []Step{{Digest: Digest(in), Attempts: []Attempt{{Binding: "current", Decision: "allow"}}}}}, Log: log}
		server, client := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer server.Close()
			b.handle(context.Background(), server)
		}()
		req := Request{Binding: Binding{"egg-fixture", in.ProviderID, strings.Repeat("a", 32), Digest(in)},
			Input: in, Deadline: time.Now().Add(time.Second)}
		if err := json.NewEncoder(client).Encode(req); err != nil {
			t.Fatal(err)
		}
		var answer Answer
		if err := decodeStrict(client, &answer); err == nil {
			t.Fatal("commandless request received an answer")
		}
		client.Close()
		<-done
		var row struct {
			Binding
			Status string `json:"status"`
		}
		decoder := json.NewDecoder(log)
		if err := decoder.Decode(&row); err != nil || row.Binding != req.Binding || row.Status != "rejected_request" {
			t.Fatalf("missing bound rejection log: %+v, %v", row, err)
		}
		if b.next != 0 || len(b.seen) != 0 || decoder.Decode(new(any)) != io.EOF {
			t.Fatal("invalid request consumed the step or emitted extra log data")
		}
	}
}
