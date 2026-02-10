package ws

import (
	"encoding/json"
	"testing"
)

func TestEnvelopeRouting(t *testing.T) {
	tests := []struct {
		name string
		msg  any
		want string
	}{
		{"register", WingRegister{Type: TypeWingRegister, MachineID: "m1"}, TypeWingRegister},
		{"heartbeat", WingHeartbeat{Type: TypeWingHeartbeat, MachineID: "m1"}, TypeWingHeartbeat},
		{"submit", TaskSubmit{Type: TypeTaskSubmit, TaskID: "t1", Prompt: "hi"}, TypeTaskSubmit},
		{"chunk", TaskChunk{Type: TypeTaskChunk, TaskID: "t1", Text: "hello"}, TypeTaskChunk},
		{"done", TaskDone{Type: TypeTaskDone, TaskID: "t1"}, TypeTaskDone},
		{"error", TaskErrorMsg{Type: TypeTaskError, TaskID: "t1", Error: "oops"}, TypeTaskError},
	}

	for _, tt := range tests {
		data, err := json.Marshal(tt.msg)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tt.name, err)
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			t.Fatalf("%s: unmarshal envelope: %v", tt.name, err)
		}
		if env.Type != tt.want {
			t.Errorf("%s: type = %q, want %q", tt.name, env.Type, tt.want)
		}
	}
}

func TestTaskSubmitRoundTrip(t *testing.T) {
	orig := TaskSubmit{
		Type:      TypeTaskSubmit,
		TaskID:    "rt-20260209-001",
		Prompt:    "summarize my git log",
		Skill:     "compress",
		Agent:     "ollama",
		Isolation: "standard",
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded TaskSubmit
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.TaskID != orig.TaskID {
		t.Errorf("task_id = %q, want %q", decoded.TaskID, orig.TaskID)
	}
	if decoded.Prompt != orig.Prompt {
		t.Errorf("prompt = %q, want %q", decoded.Prompt, orig.Prompt)
	}
	if decoded.Skill != orig.Skill {
		t.Errorf("skill = %q, want %q", decoded.Skill, orig.Skill)
	}
	if decoded.Agent != orig.Agent {
		t.Errorf("agent = %q, want %q", decoded.Agent, orig.Agent)
	}
}

func TestWingRegisterFields(t *testing.T) {
	reg := WingRegister{
		Type:       TypeWingRegister,
		MachineID:  "mac-A1B2",
		Agents:     []string{"claude", "ollama"},
		Skills:     []string{"compress", "scorer"},
		Labels:     []string{"gpu", "home"},
		Identities: []string{"bryan", "team-ml"},
	}

	data, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded WingRegister
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(decoded.Agents) != 2 || decoded.Agents[0] != "claude" {
		t.Errorf("agents = %v, want [claude, ollama]", decoded.Agents)
	}
	if len(decoded.Labels) != 2 || decoded.Labels[1] != "home" {
		t.Errorf("labels = %v, want [gpu, home]", decoded.Labels)
	}
	if len(decoded.Identities) != 2 || decoded.Identities[0] != "bryan" {
		t.Errorf("identities = %v, want [bryan, team-ml]", decoded.Identities)
	}
}
