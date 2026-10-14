package permissions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/ehrlich-b/wingthing/internal/permissionbridge"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func brokerFixture(t *testing.T, duration time.Duration) (*Broker, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "wp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "p.sock")
	b := New("egg", "provider", "Studio", "developer")
	b.Duration = duration
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); _ = b.Serve(ctx, l) }()
	t.Cleanup(func() { cancel(); l.Close(); <-done })
	return b, path
}
func input(command string) permissionbridge.Input {
	return permissionbridge.Input{Event: "PermissionRequest", ProviderID: "provider", Tool: "Bash", Arguments: map[string]any{"command": command}, Directory: "/workspace"}
}
func startHook(t *testing.T, b *Broker, path, token string, in permissionbridge.Input) (*bytes.Buffer, <-chan error) {
	t.Helper()
	out := &bytes.Buffer{}
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		done <- Hook(ctx, path, b.SessionID, b.Generation, token, in, out)
	}()
	return out, done
}
func waitRequest(t *testing.T, b *Broker) Request {
	t.Helper()
	end := time.Now().Add(time.Second)
	for time.Now().Before(end) {
		if rows := b.List(); len(rows) > 0 {
			return rows[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no live request")
	return Request{}
}
func response(r Request, decision string) Response {
	return Response{WingID: "wing", SessionID: r.SessionID, RequestID: r.RequestID, Digest: r.Digest, AttemptID: ID(), Decision: decision}
}
func TestPermissionBrokerAllowDenyExpiryAuthenticationAndReplacement(t *testing.T) {
	for _, decision := range []string{"allow", "deny"} {
		t.Run(decision, func(t *testing.T) {
			b, path := brokerFixture(t, time.Second)
			out, done := startHook(t, b, path, b.Token, input("echo allowed"))
			r := waitRequest(t, b)
			answer := response(r, decision)
			result, err := b.Respond(answer)
			if err != nil || result.State != "delivered" {
				t.Fatalf("%+v %v", result, err)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			var actual struct {
				Output struct {
					Decision struct {
						Behavior  string `json:"behavior"`
						Interrupt bool   `json:"interrupt"`
					} `json:"decision"`
				} `json:"hookSpecificOutput"`
			}
			if json.Unmarshal(out.Bytes(), &actual) != nil || actual.Output.Decision.Behavior != decision || actual.Output.Decision.Interrupt {
				t.Fatal(out.String())
			}
			if _, err = b.Respond(answer); err == nil {
				t.Fatal("answer replayed")
			}
		})
	}
	t.Run("authentication", func(t *testing.T) {
		b, path := brokerFixture(t, time.Second)
		out, done := startHook(t, b, path, "wrong", input("echo harmless"))
		if <-done == nil || out.Len() != 0 || len(b.List()) != 0 {
			t.Fatal("bad token admitted")
		}
	})
	t.Run("expiry", func(t *testing.T) {
		b, path := brokerFixture(t, 40*time.Millisecond)
		out, done := startHook(t, b, path, b.Token, input("echo harmless"))
		r := waitRequest(t, b)
		if <-done == nil || out.Len() != 0 {
			t.Fatal("expiry emitted a decision")
		}
		if _, err := b.Respond(response(r, "allow")); err == nil {
			t.Fatal("expired answer admitted")
		}
	})
	t.Run("replacement", func(t *testing.T) {
		b, path := brokerFixture(t, time.Second)
		out, done := startHook(t, b, path, b.Token, input("echo first"))
		old := waitRequest(t, b)
		out2, done2 := startHook(t, b, path, b.Token, input("echo second"))
		if <-done == nil || out.Len() != 0 {
			t.Fatal("replaced hook emitted a decision")
		}
		current := waitRequest(t, b)
		if _, err := b.Respond(response(old, "allow")); err == nil {
			t.Fatal("stale answer delivered")
		}
		if _, err := b.Respond(response(current, "deny")); err != nil {
			t.Fatal(err)
		}
		if <-done2 != nil || out2.Len() == 0 {
			t.Fatal("replacement did not receive deny")
		}
	})
}
func TestPermissionLedgerRaceRetriesAuditFailureAndLostReply(t *testing.T) {
	l := Ledger{Dir: filepath.Join(t.TempDir(), "permissions")}
	r := Response{WingID: "wing", SessionID: "egg", RequestID: "request", Digest: "digest", AttemptID: "attempt", Decision: "deny"}
	a := Actor{"owner", "tui", "tui"}
	auth := func() error { return nil }
	var sends atomic.Int32
	deliver := func() (Outcome, error) {
		sends.Add(1)
		return Outcome{State: "delivered", Decision: r.Decision, AttemptID: r.AttemptID}, nil
	}
	var wg sync.WaitGroup
	var winners atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := r
			if i == 1 {
				c.AttemptID = "competing"
			}
			if _, err := l.Decide(c, a, auth, deliver); err == nil {
				winners.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if sends.Load() != 1 || winners.Load() != 1 {
		t.Fatalf("sends=%d winners=%d", sends.Load(), winners.Load())
	}
	// Find the stored winner and retry its identical envelope after reopening.
	entries, _ := filepath.Glob(filepath.Join(l.Dir, "[0-9a-f]*.json"))
	var stored record
	var chosen string
	for _, path := range entries {
		if !strings.HasPrefix(filepath.Base(path), "attempt-") {
			chosen = path
			break
		}
	}
	data, _ := os.ReadFile(chosen)
	if json.Unmarshal(data, &stored) != nil {
		t.Fatal("invalid ledger")
	}
	out, err := (Ledger{Dir: l.Dir}).Decide(stored.Response, a, auth, deliver)
	if err != nil || out.State != "delivered" || sends.Load() != 1 {
		t.Fatalf("retry: %+v %v sends=%d", out, err, sends.Load())
	}
	changed := stored.Response
	changed.Decision = "allow"
	if _, err = l.Decide(changed, a, auth, deliver); err == nil {
		t.Fatal("changed decision accepted")
	}
	t.Run("audit failure", func(t *testing.T) {
		l := Ledger{Dir: t.TempDir()}
		os.Mkdir(filepath.Join(l.Dir, "audit.jsonl"), 0700)
		calls := 0
		_, err := l.Decide(r, a, auth, func() (Outcome, error) { calls++; return Outcome{}, nil })
		if err == nil || calls != 0 {
			t.Fatal("audit failure delivered")
		}
	})
	t.Run("crash and lost reply", func(t *testing.T) {
		l := Ledger{Dir: t.TempDir()}
		calls := 0
		send := func() (Outcome, error) { calls++; return Outcome{}, errors.New("lost connection") }
		if _, err := l.Decide(r, a, auth, send); err == nil {
			t.Fatal("missing lost reply")
		}
		out, err := (Ledger{Dir: l.Dir}).Decide(r, a, auth, send)
		if err != nil || out.State != "unconfirmed" || calls != 1 {
			t.Fatalf("replayed uncertain decision: %+v %v %d", out, err, calls)
		}
	})
	t.Run("revoked while reserving", func(t *testing.T) {
		l := Ledger{Dir: t.TempDir()}
		checks, calls := 0, 0
		_, err := l.Decide(r, a, func() error {
			checks++
			if checks > 1 {
				return errors.New("revoked")
			}
			return nil
		}, func() (Outcome, error) { calls++; return Outcome{}, nil })
		if err == nil || calls != 0 {
			t.Fatal("revoked authority delivered")
		}
	})
}
func TestPermissionBrokerLostAckAndConcurrentReplacement(t *testing.T) {
	b, path := brokerFixture(t, 80*time.Millisecond)
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	json.NewEncoder(c).Encode(exchange{b.Token, b.SessionID, b.Generation, input("echo harmless")})
	r := waitRequest(t, b)
	done := make(chan Outcome, 1)
	go func() { out, _ := b.Respond(response(r, "allow")); done <- out }()
	var a reply
	if err = json.NewDecoder(c).Decode(&a); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if out := <-done; out.State != "unconfirmed" {
		t.Fatal(out)
	}
	// Replacement arriving immediately after Respond used to double-close done.
	out2, done2 := startHook(t, b, path, b.Token, input("echo second"))
	next := waitRequest(t, b)
	b.Respond(response(next, "deny"))
	if <-done2 != nil || out2.Len() == 0 {
		t.Fatal("second request lost")
	}
}

func TestPermissionBrokerHookDisconnectInvalidatesRequest(t *testing.T) {
	b, path := brokerFixture(t, time.Second)
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	json.NewEncoder(c).Encode(exchange{b.Token, b.SessionID, b.Generation, input("echo harmless")})
	r := waitRequest(t, b)
	c.Close()
	end := time.Now().Add(time.Second)
	for len(b.List()) != 0 && time.Now().Before(end) {
		time.Sleep(time.Millisecond)
	}
	if len(b.List()) != 0 {
		t.Fatal("disconnected hook still live")
	}
	if _, err = b.Respond(response(r, "allow")); err == nil {
		t.Fatal("answered a disconnected request")
	}
}

func TestPermissionDecisionAttemptCannotMoveToAnotherRequest(t *testing.T) {
	l := Ledger{Dir: t.TempDir()}
	a := Actor{"owner", "tui", "tui"}
	r := Response{WingID: "wing", SessionID: "egg", RequestID: "first", Digest: "digest", AttemptID: "one-attempt", Decision: "allow"}
	calls := 0
	send := func() (Outcome, error) {
		calls++
		return Outcome{State: "delivered", Decision: r.Decision, AttemptID: r.AttemptID}, nil
	}
	auth := func() error { return nil }
	if _, err := l.Decide(r, a, auth, send); err != nil {
		t.Fatal(err)
	}
	r.RequestID = "second"
	if _, err := l.Decide(r, a, auth, send); err == nil || calls != 1 {
		t.Fatal("attempt moved to another request")
	}
}
