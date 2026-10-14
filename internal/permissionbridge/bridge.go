// Package permissionbridge implements a finite, local permission-hook experiment.
// It is not connected to the wing's control interfaces.
package permissionbridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const MaxMessage = 128 << 10

type Input struct {
	Event      string         `json:"hook_event_name"`
	ProviderID string         `json:"session_id"`
	Tool       string         `json:"tool_name"`
	Arguments  map[string]any `json:"tool_input"`
	Directory  string         `json:"cwd"`
}

// ParseInput intentionally tolerates additional native metadata. Arguments use
// json.Number so canonicalization cannot round provider-supplied numbers.
func ParseInput(r io.Reader) (Input, error) {
	var in Input
	b, err := io.ReadAll(io.LimitReader(r, MaxMessage+1))
	if err != nil || len(b) > MaxMessage {
		return in, errors.New("invalid hook input size")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&in); err != nil {
		return in, err
	}
	if d.Decode(new(any)) != io.EOF {
		return in, errors.New("trailing hook input")
	}
	if in.Event != "PermissionRequest" || in.ProviderID == "" || in.Tool != "Bash" || !filepath.IsAbs(in.Directory) {
		return in, errors.New("incomplete Bash permission request")
	}
	command, ok := in.Arguments["command"].(string)
	if !ok || command == "" {
		return in, errors.New("missing Bash command")
	}
	return in, nil
}

// Digest covers the whole tool input, including any fields beyond command.
func Digest(in Input) string {
	b, _ := json.Marshal(struct {
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
		Directory string         `json:"directory"`
	}{in.Tool, in.Arguments, in.Directory})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type Binding struct {
	Session    string `json:"session"`
	ProviderID string `json:"provider_id"`
	RequestID  string `json:"request_id"`
	Digest     string `json:"digest"`
}

type Request struct {
	Binding
	Input    Input     `json:"input"`
	Deadline time.Time `json:"deadline"`
}

type Answer struct {
	Binding
	Decision string `json:"decision"`
}

// Hook emits only one request-specific decision. Every unavailable, expired or
// invalid exchange returns no decision, leaving native handling to the provider.
func Hook(ctx context.Context, socket, session string, in Input, out io.Writer) error {
	deadline, bounded := ctx.Deadline()
	if !bounded || session == "" {
		return errors.New("bounded session required")
	}
	if err := privateDirectory(filepath.Dir(socket)); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	req := Request{Binding: Binding{session, in.ProviderID, hex.EncodeToString(nonce[:]), Digest(in)}, Input: in, Deadline: deadline}
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.SetDeadline(deadline); err != nil {
		return err
	}
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return err
	}
	var answer Answer
	if err := decodeStrict(c, &answer); err != nil {
		return err
	}
	if ctx.Err() != nil || !time.Now().Before(deadline) || answer.Binding != req.Binding || !validDecision(answer.Decision) {
		return errors.New("invalid or expired answer")
	}
	decision := map[string]any{"behavior": answer.Decision}
	if answer.Decision == "deny" {
		decision["message"] = "This request was declined."
		decision["interrupt"] = false
	}
	return json.NewEncoder(out).Encode(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PermissionRequest", "decision": decision}})
}

func validDecision(s string) bool { return s == "allow" || s == "deny" }

type Attempt struct {
	// Binding is current or previous. Previous deliberately exercises replay.
	Binding     string `json:"binding"`
	Decision    string `json:"decision"`
	DelayMillis int    `json:"delay_ms"`
}

type Step struct {
	Digest   string    `json:"digest"`
	Attempts []Attempt `json:"attempts"`
}

type Script struct {
	Session    string `json:"session"`
	ProviderID string `json:"provider_id"`
	Steps      []Step `json:"steps"`
}

// Broker consumes a finite script and never exposes an unrestricted response
// endpoint. Logs retain IDs, digests and outcomes, never raw tool arguments.
type Broker struct {
	Script   Script
	Log      io.Writer
	mu       sync.Mutex
	next     int
	previous Binding
	seen     map[string]bool
}

func (b *Broker) Validate() error {
	if b.Script.Session == "" || b.Script.ProviderID == "" || len(b.Script.Steps) == 0 || len(b.Script.Steps) > 32 {
		return errors.New("finite session script required")
	}
	for _, step := range b.Script.Steps {
		digest, err := hex.DecodeString(step.Digest)
		if err != nil || len(digest) != sha256.Size || len(step.Attempts) > 8 {
			return errors.New("exact digest and bounded attempts required")
		}
		for _, a := range step.Attempts {
			if (a.Binding != "current" && a.Binding != "previous") || !validDecision(a.Decision) || a.DelayMillis < 0 || a.DelayMillis > 10000 {
				return errors.New("invalid scripted attempt")
			}
		}
	}
	return nil
}

func Listen(socket string) (net.Listener, error) {
	if err := privateDirectory(filepath.Dir(socket)); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		return nil, errors.New("socket path already exists")
	}
	l, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// Serve stops and drains request handlers when its context is canceled.
func (b *Broker) Serve(ctx context.Context, l net.Listener) error {
	if err := b.Validate(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { l.Close() })
	defer stop()
	var handlers sync.WaitGroup
	defer handlers.Wait()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		handlers.Add(1)
		go func() { defer handlers.Done(); defer c.Close(); b.handle(ctx, c) }()
	}
}

func (b *Broker) handle(ctx context.Context, c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(12 * time.Second))
	closeOnCancel := context.AfterFunc(ctx, func() { c.Close() })
	defer closeOnCancel()
	var req Request
	if decodeStrict(c, &req) != nil {
		return
	}
	b.mu.Lock()
	if b.seen == nil {
		b.seen = make(map[string]bool)
	}
	remaining := time.Until(req.Deadline)
	if req.Session != b.Script.Session || req.ProviderID != b.Script.ProviderID || req.ProviderID != req.Input.ProviderID || req.Input.Event != "PermissionRequest" || req.Input.Tool != "Bash" || !filepath.IsAbs(req.Input.Directory) || req.RequestID == "" || len(req.RequestID) != 32 || req.Digest != Digest(req.Input) || b.seen[req.RequestID] || remaining <= 0 || remaining > 10*time.Second || b.next >= len(b.Script.Steps) || req.Digest != b.Script.Steps[b.next].Digest {
		b.logLocked(req.Binding, "rejected_request")
		b.mu.Unlock()
		return
	}
	if command, ok := req.Input.Arguments["command"].(string); !ok || command == "" {
		b.logLocked(req.Binding, "rejected_request")
		b.mu.Unlock()
		return
	}
	step := b.Script.Steps[b.next]
	b.next++
	previous := b.previous
	b.previous = req.Binding
	b.seen[req.RequestID] = true
	b.logLocked(req.Binding, "opened")
	b.mu.Unlock()
	requestCtx, cancel := context.WithDeadline(ctx, req.Deadline)
	defer cancel()
	_ = c.SetDeadline(req.Deadline)
	var winnerMu sync.Mutex
	answered := false
	var attempts sync.WaitGroup
	for _, attempt := range step.Attempts {
		attempts.Add(1)
		go func(a Attempt) {
			defer attempts.Done()
			timer := time.NewTimer(time.Duration(a.DelayMillis) * time.Millisecond)
			defer timer.Stop()
			select {
			case <-requestCtx.Done():
				// Still account for a scripted candidate after cancellation. A
				// reserved winner remains final even if shutdown races its loser.
			case <-timer.C:
			}
			binding := req.Binding
			if a.Binding == "previous" {
				binding = previous
			}
			winnerMu.Lock()
			defer winnerMu.Unlock()
			status := "accepted_" + a.Decision
			if binding != req.Binding {
				status = "rejected_binding"
			} else if answered {
				status = "rejected_already_answered"
			} else if requestCtx.Err() != nil || !time.Now().Before(req.Deadline) {
				status = "rejected_expired"
			} else {
				answered = true // reservation precedes delivery; never retry a lost reply
				if json.NewEncoder(c).Encode(Answer{binding, a.Decision}) != nil {
					status = "delivery_unconfirmed"
				}
			}
			b.mu.Lock()
			b.logLocked(binding, status)
			b.mu.Unlock()
		}(attempt)
	}
	attempts.Wait()
	winnerMu.Lock()
	won := answered
	winnerMu.Unlock()
	if !won {
		<-requestCtx.Done()
		b.mu.Lock()
		b.logLocked(req.Binding, "expired_no_decision")
		b.mu.Unlock()
	}
}

func (b *Broker) logLocked(binding Binding, status string) {
	if b.Log != nil {
		_ = json.NewEncoder(b.Log).Encode(struct {
			Binding
			Status    string    `json:"status"`
			Timestamp time.Time `json:"timestamp"`
		}{binding, status, time.Now().UTC()})
	}
}

func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("socket directory must be private")
	}
	return nil
}

func decodeStrict(r io.Reader, v any) error {
	d := json.NewDecoder(io.LimitReader(r, MaxMessage))
	d.DisallowUnknownFields()
	d.UseNumber()
	return d.Decode(v)
}
