package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const oversizedTestLine = 3 * 1024 * 1024

func sizedEvent(prefix, suffix string, size int) string {
	return prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
}

func captureAgentWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var warnings bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&warnings, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &warnings
}

func checkAgentWarning(t *testing.T, warnings *bytes.Buffer, provider string, size int) {
	t.Helper()
	var warning struct {
		Level      string `json:"level"`
		Message    string `json:"msg"`
		Provider   string `json:"provider"`
		ByteLength int    `json:"byte_length"`
		Limit      int    `json:"limit"`
	}
	decoder := json.NewDecoder(warnings)
	if err := decoder.Decode(&warning); err != nil {
		t.Fatalf("decode warning: %v", err)
	}
	if warning.Level != "WARN" || warning.Message != "agent event skipped" || warning.Provider != provider || warning.ByteLength != size || warning.Limit != maxProviderLine {
		t.Fatalf("warning = %+v", warning)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("extra warning = %v, error = %v", extra, err)
	}
}

func runFakeProvider(t *testing.T, provider Agent, data string) *Stream {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdout")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	stream, err := provider.Run(ctx, "test", RunOpts{
		CmdFactory: func(ctx context.Context, _ string, _ []string) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "cat", path), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, ok := stream.Next(); !ok {
			break
		}
	}
	return stream
}

func TestProviderStreamsOversizedEvents(t *testing.T) {
	codexResponse := `{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`
	codexResult := `{"type":"turn.completed","usage":{"input_tokens":123,"output_tokens":45}}`
	claudeResponse := `{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`
	claudeResult := `{"type":"result","input_tokens":123,"output_tokens":45}`
	claudeIntermediate := sizedEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","input":{"output":"`, `"}}]}}`, oversizedTestLine)
	claudeFinal := sizedEvent(`{"result":"`, `","type":"result","input_tokens":123,"output_tokens":45}`, oversizedTestLine)
	type providerCase struct {
		name         string
		provider     Agent
		intermediate string
		normal       string
		final        string
		wantText     string
		wantTokens   [2]int
	}
	cases := []providerCase{
		{
			name: "codex", provider: NewCodex(0),
			intermediate: sizedEvent(`{"type":"item.completed","item":{"type":"command_execution","aggregated_output":"`, `"}}`, oversizedTestLine),
			normal:       "garbage\n\n" + codexResponse + "\r\n" + codexResult,
			final:        sizedEvent(`{"item":{"text":"`, `","type":"agent_message"},"type":"item.completed"}`, oversizedTestLine) + "\n" + codexResult,
			wantText:     "done", wantTokens: [2]int{123, 45},
		},
		{
			name: "claude", provider: NewClaude(0), intermediate: claudeIntermediate,
			normal:   "garbage\n\n" + claudeResponse + "\r\n" + claudeResult,
			final:    claudeResponse + "\n" + claudeFinal,
			wantText: "done", wantTokens: [2]int{123, 45},
		},
		{
			name: "cursor", provider: NewCursor(0), intermediate: claudeIntermediate,
			normal:   "garbage\n\n" + claudeResponse + "\r\n" + claudeResult,
			final:    claudeResponse + "\n" + claudeFinal,
			wantText: "done", wantTokens: [2]int{123, 45},
		},
	}
	for _, plain := range []struct {
		name     string
		provider Agent
	}{
		{"gemini", NewGemini("", 0)},
		{"opencode", NewOpenCode(0)},
		{"ollama", NewOllama("", 0)},
		{"hermes", NewHermes(0)},
	} {
		cases = append(cases, providerCase{
			name: plain.name, provider: plain.provider,
			intermediate: strings.Repeat("x", oversizedTestLine),
			normal:       "\r\n\ndone\r", final: strings.Repeat("x", oversizedTestLine), wantText: "done\n",
		})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("normal", func(t *testing.T) {
				warnings := captureAgentWarnings(t)
				stream := runFakeProvider(t, tc.provider, tc.normal)
				if err := stream.Err(); err != nil {
					t.Fatal(err)
				}
				if stream.Text() != tc.wantText {
					t.Fatalf("text = %q, want %q", stream.Text(), tc.wantText)
				}
				input, output := stream.Tokens()
				if got := [2]int{input, output}; got != tc.wantTokens {
					t.Fatalf("tokens = %v, want %v", got, tc.wantTokens)
				}
				if warnings.Len() != 0 {
					t.Fatalf("normal stream warned: %s", warnings)
				}
			})
			t.Run("intermediate", func(t *testing.T) {
				warnings := captureAgentWarnings(t)
				stream := runFakeProvider(t, tc.provider, tc.intermediate+"\r\n"+tc.normal)
				if err := stream.Err(); err != nil {
					t.Fatal(err)
				}
				if stream.Text() != tc.wantText {
					t.Fatalf("text = %q, want %q", stream.Text(), tc.wantText)
				}
				input, output := stream.Tokens()
				if got := [2]int{input, output}; got != tc.wantTokens {
					t.Fatalf("tokens = %v, want %v", got, tc.wantTokens)
				}
				checkAgentWarning(t, warnings, tc.name, oversizedTestLine)
			})
			t.Run("final", func(t *testing.T) {
				warnings := captureAgentWarnings(t)
				stream := runFakeProvider(t, tc.provider, tc.final)
				err := stream.Err()
				if err == nil || !strings.Contains(err.Error(), "final") || !strings.Contains(err.Error(), tc.name) || !strings.Contains(err.Error(), fmt.Sprint(oversizedTestLine)) || strings.Contains(err.Error(), "Scanner") {
					t.Fatalf("final error = %v, want provider and original size", err)
				}
				checkAgentWarning(t, warnings, tc.name, oversizedTestLine)
			})
		})
	}
}

func TestReadProviderLinesBoundaries(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n", "", "\r"} {
		t.Run(fmt.Sprintf("ending_%q", ending), func(t *testing.T) {
			warnings := captureAgentWarnings(t)
			want := strings.Repeat("x", maxProviderLine)
			var lines []string
			err := readProviderLines(strings.NewReader("\nshort\r\n"+want+ending), "gemini", func(line string) { lines = append(lines, line) })
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(lines, []string{"", "short", want}) {
				t.Fatalf("line count = %d, want 3 intact lines", len(lines))
			}
			if warnings.Len() != 0 {
				t.Fatalf("line at cap warned: %s", warnings)
			}
		})
	}
}

func TestReadProviderLinesLaterFinalReplacesSkippedResponse(t *testing.T) {
	warnings := captureAgentWarnings(t)
	oversized := sizedEvent(`{"type":"item.completed","item":{"type":"agent_message","text":"`, `"}}`, oversizedTestLine)
	final := `{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`
	var lines []string
	if err := readProviderLines(strings.NewReader(oversized+"\n"+final), "codex", func(line string) { lines = append(lines, line) }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lines, []string{final}) {
		t.Fatalf("lines = %q, want final only", lines)
	}
	checkAgentWarning(t, warnings, "codex", oversizedTestLine)
}

func TestProviderOversizedFinalPreservesCommandError(t *testing.T) {
	warnings := captureAgentWarnings(t)
	line := sizedEvent(`{"type":"item.completed","item":{"type":"agent_message","text":"`, `"}}`, oversizedTestLine)
	path := filepath.Join(t.TempDir(), "stdout")
	if err := os.WriteFile(path, []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := NewCodex(0).Run(ctx, "test", RunOpts{
		CmdFactory: func(ctx context.Context, _ string, _ []string) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "sh", "-c", `cat "$1"; printf 'provider failed' >&2; exit 7`, "sh", path), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, ok := stream.Next(); !ok {
			break
		}
	}
	err = stream.Err()
	if err == nil || FailureKind(err) != ProviderExit || strings.Contains(err.Error(), "provider failed") || !strings.Contains(err.Error(), "status 7") || !strings.Contains(err.Error(), fmt.Sprint(oversizedTestLine)) {
		t.Fatalf("error = %v, want structured exit and oversized final size", err)
	}
	checkAgentWarning(t, warnings, "codex", oversizedTestLine)
}

func TestReadProviderLinesOversizedTextAndUsage(t *testing.T) {
	for _, tc := range []struct {
		provider string
		prefix   string
		suffix   string
	}{
		{"claude", `{"message":{"content":[{"text":"`, `","type":"text"}]},"type":"assistant"}`},
		{"cursor", `{"delta":{"text":"`, `","type":"text_delta"},"type":"content_block_delta"}`},
		{"codex", `{"padding":"`, `","type":"turn.completed","usage":{"input_tokens":123,"output_tokens":45}}`},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			warnings := captureAgentWarnings(t)
			line := sizedEvent(tc.prefix, tc.suffix, oversizedTestLine)
			err := readProviderLines(strings.NewReader(line), tc.provider, func(string) { t.Fatal("oversized line was passed to parser") })
			if err == nil || !strings.Contains(err.Error(), fmt.Sprint(oversizedTestLine)) {
				t.Fatalf("error = %v, want oversized final event", err)
			}
			checkAgentWarning(t, warnings, tc.provider, oversizedTestLine)
		})
	}
}

func TestEventMetadataIgnoresPayloadTypes(t *testing.T) {
	line := `{"item":{"text":"escaped \"type\":\"agent_message\" \\ tail","nested":{"type":"agent_message"},"type":"command_execution"},"type":"item.completed"}`
	var metadata eventMetadata
	for i := range line {
		metadata.read([]byte(line[i : i+1]))
	}
	if metadata.required("codex", true) != [2]bool{} {
		t.Fatal("tool output was classified as a final response")
	}
}

type failingProviderReader struct{ err error }

func (r failingProviderReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadProviderLinesReadError(t *testing.T) {
	want := errors.New("provider read failed")
	var lines []string
	r := io.MultiReader(strings.NewReader("first\nlast"), failingProviderReader{want})
	err := readProviderLines(r, "gemini", func(line string) { lines = append(lines, line) })
	if !errors.Is(err, want) || !reflect.DeepEqual(lines, []string{"first", "last"}) {
		t.Fatalf("lines = %q, error = %v", lines, err)
	}
}
