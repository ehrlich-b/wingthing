package agent

import (
	"strings"
	"testing"
)

func TestAssistantTextBlocks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{
		{"mixed", `[{"type":"text","text":"first\n\n"},{"type":"tool_use","id":"tool","text":"hidden"},{"type":"text","text":"last"}]`, "first\n\nlast"},
		{"empty_and_nontext", `[{"type":"text","text":""},{"type":"thinking","text":"hidden"},{"type":"tool_use"}]`, ""},
		{"whitespace_and_unicode", `[{"type":"text","text":" café\n"},{"type":"tool_use"},{"type":"text","text":"世界 "}]`, " café\n世界 "},
		{"repeated_text", `[{"type":"text","text":"same"},{"type":"text","text":"same"}]`, "samesame"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := `{"type":"assistant","message":{"content":` + tc.content + `}}`
			got, ok := parseStreamEvent(line)
			if got != tc.want || ok != (tc.want != "") {
				t.Fatalf("parse = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.want != "")
			}
		})
	}
}

func TestProviderRetainsAssistantTextBlocks(t *testing.T) {
	for _, provider := range []struct {
		name  string
		agent Agent
	}{
		{"claude", NewClaude(0)},
		{"cursor", NewCursor(0)},
	} {
		t.Run(provider.name, func(t *testing.T) {
			lines := []string{
				`{"type":"assistant","message":{"content":[{"type":"text","text":"first\n\n"},{"type":"tool_use","id":"tool","text":"hidden"},{"type":"text","text":"last"}]}}`,
				`{"type":"user","message":{"content":[{"type":"tool_result","text":"hidden"}]}}`,
				`{"type":"assistant","message":{"content":[{"type":"text","text":"\nnext"}]}}`,
				`{"type":"result","result":"first\n\nlast\nnext","input_tokens":12,"output_tokens":7}`,
			}
			stream := runFakeProvider(t, provider.agent, strings.Join(lines, "\n"))
			if err := stream.Err(); err != nil {
				t.Fatal(err)
			}
			if got, want := stream.Text(), "first\n\nlast\nnext"; got != want {
				t.Fatalf("text = %q, want %q", got, want)
			}
			if input, output := stream.Tokens(); input != 12 || output != 7 {
				t.Fatalf("tokens = (%d, %d), want (12, 7)", input, output)
			}
		})
	}
}

func TestProviderTextDeltasAreEmittedOnce(t *testing.T) {
	for _, provider := range []struct {
		name  string
		agent Agent
	}{
		{"claude", NewClaude(0)},
		{"cursor", NewCursor(0)},
	} {
		t.Run(provider.name, func(t *testing.T) {
			lines := []string{
				`{"type":"content_block_delta","delta":{"type":"text_delta","text":"first"}}`,
				`{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{}"}}`,
				`{"type":"content_block_delta","delta":{"type":"text_delta","text":"\nlast"}}`,
				`{"type":"result","result":"first\nlast"}`,
			}
			stream := runFakeProvider(t, provider.agent, strings.Join(lines, "\n"))
			if err := stream.Err(); err != nil {
				t.Fatal(err)
			}
			if got, want := stream.Text(), "first\nlast"; got != want {
				t.Fatalf("text = %q, want %q", got, want)
			}
		})
	}
}
