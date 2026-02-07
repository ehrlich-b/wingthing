package agent

import (
	"context"
	"testing"
)

func TestStreamSetAndGetTokens(t *testing.T) {
	s := newStream(context.Background())
	s.SetTokens(500, 200)
	input, output := s.Tokens()
	if input != 500 {
		t.Errorf("input = %d, want 500", input)
	}
	if output != 200 {
		t.Errorf("output = %d, want 200", output)
	}
}

func TestStreamTokensDefaultZero(t *testing.T) {
	s := newStream(context.Background())
	input, output := s.Tokens()
	if input != 0 || output != 0 {
		t.Errorf("tokens = (%d, %d), want (0, 0)", input, output)
	}
}

func TestParseResultTokens(t *testing.T) {
	line := `{"type":"result","subtype":"success","cost_usd":0.01,"duration_ms":1234,"input_tokens":500,"output_tokens":200}`
	input, output, ok := parseResultTokens(line)
	if !ok {
		t.Fatal("expected ok")
	}
	if input != 500 {
		t.Errorf("input = %d, want 500", input)
	}
	if output != 200 {
		t.Errorf("output = %d, want 200", output)
	}
}

func TestParseResultTokensNonResult(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hello"}]}}`
	_, _, ok := parseResultTokens(line)
	if ok {
		t.Error("expected not ok for non-result event")
	}
}

func TestParseResultTokensGarbage(t *testing.T) {
	_, _, ok := parseResultTokens("not json")
	if ok {
		t.Error("expected not ok for garbage input")
	}
}
