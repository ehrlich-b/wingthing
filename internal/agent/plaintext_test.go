package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func plaintextProviders() map[string]Agent {
	return map[string]Agent{
		"ollama":   NewOllama("", 0),
		"gemini":   NewGemini("", 0),
		"hermes":   NewHermes(0),
		"opencode": NewOpenCode(0),
	}
}

func TestPlaintextPreservesBytes(t *testing.T) {
	for name, provider := range plaintextProviders() {
		t.Run(name, func(t *testing.T) {
			for _, text := range []string{"first\n\nlast", "\n\n", "first\r\n\r\nlast\r", "", "hello 世界\n", strings.Repeat("x", maxProviderLine+1)} {
				stream := runFakeProvider(t, provider, text)
				if err := stream.Err(); err != nil {
					t.Fatal(err)
				}
				if got := stream.Text(); got != text {
					t.Fatalf("output (%d bytes) = %.80q, want (%d bytes) %.80q", len(got), got, len(text), text)
				}
			}
		})
	}
}

// The child waits on a control pipe after every write, so each fragment proves
// delivery before a newline, before the next UTF-8 byte, and before child exit.
func TestPlaintextStreamsPartialBytes(t *testing.T) {
	for name, provider := range plaintextProviders() {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var ack func()
			stream, err := provider.Run(ctx, "prompt", RunOpts{CmdFactory: func(ctx context.Context, _ string, _ []string) (*exec.Cmd, error) {
				cmd := exec.CommandContext(ctx, "sh", "-c", `printf partial; read -r ack <&3; printf '\342'; read -r ack <&3; printf '\202\254\n\nlast'; read -r ack <&3`)
				ackRead, ackWrite, err := os.Pipe()
				if err != nil {
					return nil, err
				}
				cmd.ExtraFiles = []*os.File{ackRead}
				ack = func() {
					if _, err := ackWrite.Write([]byte("ok\n")); err != nil {
						t.Fatal(err)
					}
				}
				t.Cleanup(func() { _ = ackRead.Close(); _ = ackWrite.Close() })
				return cmd, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"partial", "\xe2", "\x82\xac\n\nlast"} {
				var got string
				for len(got) < len(want) {
					chunk, ok := stream.Next()
					if !ok {
						t.Fatalf("stream closed awaiting %q; received %q: %v", want, got, stream.Err())
					}
					got += chunk.Text
				}
				if got != want {
					t.Fatalf("fragment = %q, want %q", got, want)
				}
				ack()
			}
			if _, ok := stream.Next(); ok {
				t.Fatal("unexpected extra output")
			}
			if err := stream.Err(); err != nil {
				t.Fatal(err)
			}
			if got := stream.Text(); got != "partial€\n\nlast" {
				t.Fatalf("text = %q", got)
			}
		})
	}
}

func TestPlaintextFailurePreservesOutputAndDiagnostics(t *testing.T) {
	for name, provider := range plaintextProviders() {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err := provider.Run(ctx, "prompt", RunOpts{CmdFactory: func(ctx context.Context, _ string, _ []string) (*exec.Cmd, error) {
				return exec.CommandContext(ctx, "sh", "-c", `printf 'partial\n\nlast'; printf 'local diagnostic' >&2; exit 7`), nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			for {
				if _, ok := stream.Next(); !ok {
					break
				}
			}
			if stream.Text() != "partial\n\nlast" {
				t.Fatalf("text = %q", stream.Text())
			}
			var exitErr *exec.ExitError
			if err := stream.Err(); !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 || !strings.Contains(err.Error(), "local diagnostic") {
				t.Fatalf("error = %v, want exit 7 and stderr", err)
			}
		})
	}
}

func TestPlaintextCancellation(t *testing.T) {
	for name, provider := range plaintextProviders() {
		t.Run(name, func(t *testing.T) {
			for _, blocked := range []string{"read", "send"} {
				t.Run(blocked, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					// The read case leaves a descendant holding stdout open. The send
					// case fills the stream channel with no consumer.
					script := "printf ready; sleep 60 & wait"
					if blocked == "send" {
						script = "while :; do printf '" + strings.Repeat("x", 8192) + "'; done"
					}
					stream, err := provider.Run(ctx, "prompt", RunOpts{CmdFactory: func(ctx context.Context, _ string, _ []string) (*exec.Cmd, error) {
						return exec.CommandContext(ctx, "sh", "-c", script), nil
					}})
					if err != nil {
						t.Fatal(err)
					}
					if blocked == "read" {
						var got string
						for len(got) < len("ready") {
							chunk, ok := stream.Next()
							if !ok {
								t.Fatalf("stream closed before ready: %v", stream.Err())
							}
							got += chunk.Text
						}
						if got != "ready" {
							t.Fatalf("text = %q", got)
						}
					} else {
						deadline := time.Now().Add(2 * time.Second)
						for len(stream.ch) < cap(stream.ch) {
							if time.Now().After(deadline) {
								t.Fatal("stream channel did not fill")
							}
							time.Sleep(time.Millisecond)
						}
					}
					cancel()
					deadline := time.Now().Add(2 * time.Second)
					for {
						stream.mu.Lock()
						done := stream.done
						stream.mu.Unlock()
						if done {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("cancellation did not finish within two seconds")
						}
						time.Sleep(time.Millisecond)
					}
					if stream.Err() == nil {
						t.Fatal("cancellation lost its error")
					}
					for {
						if _, ok := stream.Next(); !ok {
							break
						}
					}
				})
			}
		})
	}
}

type plaintextReadError struct{ err error }

func (r plaintextReadError) Read(p []byte) (int, error) {
	return copy(p, "final\r\n\nbytes"), r.err
}

func TestPlaintextReadError(t *testing.T) {
	failure := errors.New("read failed")
	for _, readErr := range []error{io.EOF, failure} {
		var got string
		err := readProviderPlaintext(context.Background(), plaintextReadError{readErr}, func(text string) { got += text })
		if got != "final\r\n\nbytes" {
			t.Fatalf("text = %q", got)
		}
		if (readErr == io.EOF && err != nil) || (readErr == failure && !errors.Is(err, failure)) {
			t.Fatalf("error = %v for read error %v", err, readErr)
		}
	}
}
