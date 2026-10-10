package localmcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/agent"
)

func TestRunRecoveryTimeoutSteerKeepsParentAndPartialResult(t *testing.T) {
	f := newRunWingFixture(t, nil)
	parent := f.admit(t, "original request")
	<-f.submitted
	f.finish(t, parent, "safe partial Ω", agent.Timeout)
	f.wait(t, parent)
	f.restart(t)
	wire, _ := json.Marshal(map[string]any{"run_id": parent, "prompt": "finish remaining work", "idempotency_key": "timeout-followup"})
	receipt, err := f.server.toolAgentSteer(wire)
	if err != nil {
		t.Fatal(err)
	}
	child := receipt["run_id"].(string)
	if receipt["parent_id"] != parent || receipt["phase"] == nil || receipt["idempotency_key"] != "timeout-followup" {
		t.Fatalf("follow-up receipt lost its durable parent/state: %v", receipt)
	}
	if got := <-f.submitted; got != child {
		t.Fatalf("submitted %q, want %q", got, child)
	}
	f.mu.Lock()
	e := f.eggs[child]
	f.mu.Unlock()
	e.mu.Lock()
	prompt := e.prompt
	e.mu.Unlock()
	for _, part := range []string{"Prior request:\noriginal request", "Prior result:\nsafe partial Ω", "Prior error:\nEgg run turn ended: timeout.", "New direction:\nfinish remaining work"} {
		if !strings.Contains(prompt, part) {
			t.Fatalf("follow-up lost timeout context %q: %q", part, prompt)
		}
	}
	f.restart(t)
	retry, err := f.server.toolAgentSteer(wire)
	if err != nil || retry["run_id"] != child || retry["session_id"] != receipt["session_id"] || retry["parent_id"] != parent {
		t.Fatalf("retry changed admitted child: %v, %v", retry, err)
	}
	f.finish(t, child, "finished Ω", "")
	f.wait(t, child)
	result, err := f.server.toolAgentResult(runArgs(child))
	if err != nil || result["output"] != "finished Ω" || result["parent_id"] != parent || result["phase"] != "terminal" {
		t.Fatalf("child result: %v, %v", result, err)
	}
	f.restart(t)
	again, err := f.server.toolAgentResult(runArgs(child))
	if err != nil || !reflect.DeepEqual(result, again) {
		t.Fatalf("restart changed terminal child: %v, %v", again, err)
	}
	select {
	case id := <-f.submitted:
		t.Fatalf("restart/retry duplicated execution: %s", id)
	default:
	}
}

func TestRunRecoveryStopTerminalParentKeepsStartedChild(t *testing.T) {
	for _, kind := range []agent.ErrorKind{"", agent.Timeout} {
		t.Run(string(kind), func(t *testing.T) {
			f := newRunWingFixture(t, nil)
			parent := f.admit(t, "parent")
			<-f.submitted
			f.finish(t, parent, "prior result", kind)
			f.wait(t, parent)
			before, err := f.server.toolAgentResult(runArgs(parent))
			if err != nil {
				t.Fatal(err)
			}
			wire, _ := json.Marshal(map[string]any{"run_id": parent, "prompt": "child", "idempotency_key": "terminal-steer"})
			receipt, err := f.server.toolAgentSteer(wire)
			if err != nil {
				t.Fatal(err)
			}
			child := receipt["run_id"].(string)
			if id := <-f.submitted; id != child {
				t.Fatal("wrong child submitted")
			}
			if _, err := f.server.toolAgentStop(runArgs(parent)); err != nil {
				t.Fatal(err)
			}
			f.restart(t)
			f.finish(t, child, "child survived terminal parent stop", "")
			f.wait(t, child)
			result, err := f.server.toolAgentResult(runArgs(child))
			if err != nil || result["status"] != "done" || result["parent_id"] != parent {
				t.Fatalf("started child lost after terminal parent stop: %v, %v", result, err)
			}
			after, err := f.server.toolAgentResult(runArgs(parent))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("stop rewrote terminal parent: %v, %v", after, err)
			}
		})
	}
}
