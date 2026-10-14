package permissions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func brokerFixture(t *testing.T, duration time.Duration, configure ...func(*Broker)) (*Broker, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "wp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "p.sock")
	b := New("egg", "provider", "Studio", "developer")
	b.Duration = duration
	for _, setup := range configure {
		setup(b)
	}
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
	t.Run("parallel invocations", func(t *testing.T) {
		b, path := brokerFixture(t, time.Second)
		out, done := startHook(t, b, path, b.Token, input("echo first"))
		first := waitRequest(t, b)
		out2, done2 := startHook(t, b, path, b.Token, input("echo second"))
		end := time.Now().Add(time.Second)
		for len(b.List()) != 2 && time.Now().Before(end) {
			time.Sleep(time.Millisecond)
		}
		rows := b.List()
		if len(rows) != 2 || rows[0].RequestID != first.RequestID || rows[0].Digest != first.Digest {
			t.Fatal("parallel call replaced existing display")
		}
		if _, err := b.Respond(response(rows[1], "deny")); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Respond(response(first, "allow")); err != nil {
			t.Fatal(err)
		}
		if <-done != nil || <-done2 != nil || out.Len() == 0 || out2.Len() == 0 {
			t.Fatal("parallel reply lost")
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
			if _, err := l.Decide(c, a, auth, auth, deliver); err == nil {
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
	out, err := (Ledger{Dir: l.Dir}).Decide(stored.Response, a, auth, auth, deliver)
	if err != nil || out.State != "delivered" || sends.Load() != 1 {
		t.Fatalf("retry: %+v %v sends=%d", out, err, sends.Load())
	}
	changed := stored.Response
	changed.Decision = "allow"
	if _, err = l.Decide(changed, a, auth, auth, deliver); err == nil {
		t.Fatal("changed decision accepted")
	}
	t.Run("audit failure", func(t *testing.T) {
		l := Ledger{Dir: t.TempDir()}
		os.Mkdir(filepath.Join(l.Dir, "audit.jsonl"), 0700)
		calls := 0
		for i := 0; i < 2; i++ {
			out, err := l.Decide(r, a, auth, auth, func() (Outcome, error) { calls++; return Outcome{}, nil })
			if err == nil || calls != 0 || out.State == "unconfirmed" {
				t.Fatalf("audit failure delivered or reported uncertainty: %+v %v", out, err)
			}
			entries, err := filepath.Glob(filepath.Join(l.Dir, "*.json"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("audit failure reserved a decision or attempt: %v %v", entries, err)
			}
		}
		if err := os.Remove(filepath.Join(l.Dir, "audit.jsonl")); err != nil {
			t.Fatal(err)
		}
		out, err := l.Decide(r, a, auth, auth, func() (Outcome, error) {
			calls++
			return Outcome{State: "delivered", Decision: r.Decision, AttemptID: r.AttemptID}, nil
		})
		if err != nil || out.State != "delivered" || calls != 1 {
			t.Fatalf("repaired audit could not accept fresh delivery: %+v %v calls=%d", out, err, calls)
		}
	})
	t.Run("crash and lost reply", func(t *testing.T) {
		l := Ledger{Dir: t.TempDir()}
		calls := 0
		send := func() (Outcome, error) { calls++; return Outcome{}, errors.New("lost connection") }
		if _, err := l.Decide(r, a, auth, auth, send); err == nil {
			t.Fatal("missing lost reply")
		}
		out, err := (Ledger{Dir: l.Dir}).Decide(r, a, auth, auth, send)
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
		}, auth, func() (Outcome, error) { calls++; return Outcome{}, nil })
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
	if _, err := l.Decide(r, a, auth, auth, send); err != nil {
		t.Fatal(err)
	}
	r.RequestID = "second"
	if _, err := l.Decide(r, a, auth, auth, send); err == nil || calls != 1 {
		t.Fatal("attempt moved to another request")
	}
}

func TestPermissionDeadRequestRefusedBeforeReservation(t *testing.T) {
	for _, dead := range []string{"expired", "answered", "replaced", "disconnected"} {
		t.Run(dead, func(t *testing.T) {
			duration := time.Second
			if dead == "expired" {
				duration = 40 * time.Millisecond
			}
			b, path := brokerFixture(t, duration)
			_, done := startHook(t, b, path, b.Token, input("echo original"))
			req := waitRequest(t, b)
			b.mu.Lock()
			p := b.requests[req.RequestID]
			switch dead {
			case "answered":
				p.answered = true
			case "replaced":
				delete(b.requests, req.RequestID)
			case "disconnected":
				p.conn.Close()
			}
			b.mu.Unlock()
			if dead == "disconnected" || dead == "expired" {
				<-done
			}
			l := Ledger{Dir: t.TempDir()}
			answer := response(req, "allow")
			sends := 0
			_, err := l.Decide(answer, Actor{"owner", "tui", "tui"}, func() error { return nil }, func() error { return b.Check(answer) }, func() (Outcome, error) { sends++; return b.Respond(answer) })
			if err == nil || sends != 0 {
				t.Fatal("dead request reserved or delivered")
			}
			records, _ := filepath.Glob(filepath.Join(l.Dir, "*.json"))
			if len(records) != 0 {
				t.Fatal("dead request persisted as a decision")
			}
			audit, err := os.ReadFile(filepath.Join(l.Dir, "audit.jsonl"))
			if err != nil || !strings.Contains(string(audit), `"authorization":"refused"`) || strings.Contains(string(audit), `"authorization":"allowed"`) || strings.Contains(string(audit), `"state":"unconfirmed"`) {
				t.Fatalf("refusal audit: %s %v", audit, err)
			}
			p.conn.Close()
		})
	}
}

func TestPermissionAuditAppendsDeliveryOutcome(t *testing.T) {
	for _, state := range []string{"delivered", "unconfirmed", "failed"} {
		t.Run(state, func(t *testing.T) {
			l := Ledger{Dir: t.TempDir()}
			r := Response{WingID: "wing", SessionID: "egg", RequestID: "request", Digest: "digest", AttemptID: "attempt", Decision: "allow"}
			auth := func() error { return nil }
			out, err := l.Decide(r, Actor{"owner", "tui", "tui"}, auth, auth, func() (Outcome, error) {
				return Outcome{State: state, Decision: r.Decision, AttemptID: r.AttemptID}, nil
			})
			if err != nil || out.State != state {
				t.Fatal(out, err)
			}
			data, err := os.ReadFile(filepath.Join(l.Dir, "audit.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
			if len(lines) != 3 {
				t.Fatalf("want intent, reservation and delivery lines: %s", data)
			}
			var intent, first, last record
			if json.Unmarshal(lines[0], &intent) != nil || intent.Phase != "intent" || intent.Outcome.State != "failed" || json.Unmarshal(lines[1], &first) != nil || json.Unmarshal(lines[2], &last) != nil || first.Phase != "reservation" || first.Outcome.State != "failed" || last.Phase != "delivery" || last.Outcome.State != state || last.Response != r {
				t.Fatalf("incorrect delivery receipt: %s", data)
			}
		})
	}
}

func TestPermissionLedgerPersistFailuresNeverInventDelivery(t *testing.T) {
	for _, state := range []string{"delivered", "failed", "unconfirmed"} {
		for failAt := 1; failAt <= 5; failAt++ {
			for _, afterWrite := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/write-%d/after-%t", state, failAt, afterWrite), func(t *testing.T) {
					l := Ledger{Dir: t.TempDir()}
					r := Response{WingID: "wing", SessionID: "egg", RequestID: "request", Digest: "digest", AttemptID: "attempt", Decision: "allow"}
					a := Actor{"owner", "tui", "tui"}
					writes, sends := 0, 0
					injected := errors.New("injected storage failure")
					l.writeRecord = func(path string, value any) error {
						writes++
						if writes == failAt && !afterWrite {
							return injected
						}
						if err := l.persistFile(path, value); err != nil {
							return err
						}
						if writes == failAt {
							return injected
						}
						return nil
					}
					auth := func() error { return nil }
					deliver := func() (Outcome, error) {
						sends++
						return Outcome{State: state, Decision: r.Decision, AttemptID: r.AttemptID}, nil
					}
					out, err := l.Decide(r, a, auth, auth, deliver)
					if !errors.Is(err, injected) {
						t.Fatalf("fault was not reached: %+v %v", out, err)
					}
					if failAt <= 3 && (sends != 0 || out.State != "failed") {
						t.Fatalf("pre-delivery failure: %+v sends=%d", out, sends)
					}
					audit, err := os.ReadFile(filepath.Join(l.Dir, "audit.jsonl"))
					if err != nil {
						t.Fatal(err)
					}
					for _, line := range bytes.Split(bytes.TrimSpace(audit), []byte{'\n'}) {
						var entry record
						if err := json.Unmarshal(line, &entry); err != nil {
							t.Fatal(err)
						}
						if entry.Phase == "reservation" {
							files, _ := filepath.Glob(filepath.Join(l.Dir, "*.json"))
							if len(files) != 2 || failAt <= 2 {
								t.Fatalf("audit claimed an unpersisted reservation: %s", audit)
							}
						}
					}
					beforeRetry := sends
					reopened := Ledger{Dir: l.Dir}
					retry, err := reopened.Decide(r, a, auth, auth, deliver)
					if err != nil {
						t.Fatal(err)
					}
					if failAt <= 3 && !(failAt == 1 && !afterWrite) && (retry.State != "failed" || sends != 0) {
						t.Fatalf("undelivered retry: %+v sends=%d", retry, sends)
					}
					if failAt >= 4 && (retry.State != state || sends != beforeRetry) {
						t.Fatalf("receipt lost or resent: %+v sends=%d", retry, sends)
					}
				})
			}
		}
	}
}

func TestPermissionLedgerPreparationCrashAndReservationAuditFailure(t *testing.T) {
	for _, stopAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("crash-after-write-%d", stopAt), func(t *testing.T) {
			l := Ledger{Dir: t.TempDir()}
			r := Response{WingID: "wing", SessionID: "egg", RequestID: "request", Digest: "digest", AttemptID: "attempt", Decision: "deny"}
			a := Actor{"owner", "tui", "tui"}
			auth := func() error { return nil }
			writes, sends := 0, 0
			l.writeRecord = func(path string, value any) error {
				if err := l.persistFile(path, value); err != nil {
					return err
				}
				writes++
				if writes == stopAt {
					panic("simulated crash during preparation")
				}
				return nil
			}
			deliver := func() (Outcome, error) { sends++; return Outcome{}, nil }
			func() {
				defer func() {
					if recover() == nil {
						t.Error("crash injection was not reached")
					}
				}()
				l.Decide(r, a, auth, auth, deliver)
			}()
			out, err := (Ledger{Dir: l.Dir}).Decide(r, a, auth, auth, deliver)
			if err != nil || out.State != "failed" || sends != 0 {
				t.Fatalf("crashed preparation became delivery: %+v %v sends=%d", out, err, sends)
			}
		})
	}
	t.Run("reservation audit failure", func(t *testing.T) {
		l := Ledger{Dir: t.TempDir()}
		r := Response{WingID: "wing", SessionID: "egg", RequestID: "request", Digest: "digest", AttemptID: "attempt", Decision: "allow"}
		a := Actor{"owner", "tui", "tui"}
		writes, sends := 0, 0
		l.writeRecord = func(path string, value any) error {
			if err := l.persistFile(path, value); err != nil {
				return err
			}
			writes++
			if writes == 2 {
				audit := filepath.Join(l.Dir, "audit.jsonl")
				if err := os.Rename(audit, filepath.Join(l.Dir, "intent.jsonl")); err != nil {
					return err
				}
				return os.Mkdir(audit, 0700)
			}
			return nil
		}
		auth := func() error { return nil }
		deliver := func() (Outcome, error) { sends++; return Outcome{}, nil }
		if out, err := l.Decide(r, a, auth, auth, deliver); err == nil || out.State != "failed" || sends != 0 {
			t.Fatalf("failed reservation audit delivered: %+v %v sends=%d", out, err, sends)
		}
		out, err := (Ledger{Dir: l.Dir}).Decide(r, a, auth, auth, deliver)
		if err != nil || out.State != "failed" || sends != 0 {
			t.Fatalf("retry invented delivery: %+v %v sends=%d", out, err, sends)
		}
	})
}

func TestPermissionLedgerFailureReceiptPersistsWhenOneRepairWriteFails(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		firstRepair := 4
		for failAt := firstRepair; failAt <= firstRepair+1; failAt++ {
			for _, afterWrite := range []bool{false, true} {
				t.Run(fmt.Sprintf("revoked-%t/write-%d/after-%t", revoked, failAt, afterWrite), func(t *testing.T) {
					l := Ledger{Dir: t.TempDir()}
					r := Response{WingID: "wing", SessionID: "egg", RequestID: "request", Digest: "digest", AttemptID: "attempt", Decision: "allow"}
					a := Actor{"owner", "tui", "tui"}
					writes, checks, sends := 0, 0, 0
					injected := errors.New("injected failure")
					l.writeRecord = func(path string, value any) error {
						writes++
						if writes == failAt && !afterWrite {
							return injected
						}
						if err := l.persistFile(path, value); err != nil {
							return err
						}
						if writes == failAt || (!revoked && writes == 3) {
							return injected
						}
						return nil
					}
					auth := func() error {
						checks++
						if revoked && checks == 2 {
							return errors.New("revoked")
						}
						return nil
					}
					live := func() error { return nil }
					deliver := func() (Outcome, error) { sends++; return Outcome{}, nil }
					out, err := l.Decide(r, a, auth, live, deliver)
					if !errors.Is(err, injected) || out.State != "failed" || sends != 0 {
						t.Fatalf("failed repair: %+v %v sends=%d", out, err, sends)
					}
					out, err = (Ledger{Dir: l.Dir}).Decide(r, a, auth, live, deliver)
					if err != nil || out.State != "failed" || sends != 0 {
						t.Fatalf("retry lost non-delivery receipt: %+v %v sends=%d", out, err, sends)
					}
				})
			}
		}
	}
}

func TestPermissionLedgerPersistentWriteFailureUsesAuditReceipt(t *testing.T) {
	for _, state := range []string{"delivered", "failed", "unconfirmed"} {
		for _, failAt := range []int{3, 4} {
			for _, afterWrite := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/write-%d/after-%t", state, failAt, afterWrite), func(t *testing.T) {
					l := Ledger{Dir: t.TempDir()}
					r := Response{WingID: "wing", SessionID: "egg", RequestID: "request", Digest: "digest", AttemptID: "attempt", Decision: "allow"}
					a := Actor{"owner", "tui", "tui"}
					writes, sends := 0, 0
					injected := errors.New("persistent storage failure")
					l.writeRecord = func(path string, value any) error {
						writes++
						if writes > failAt || (writes == failAt && !afterWrite) {
							return injected
						}
						if err := l.persistFile(path, value); err != nil {
							return err
						}
						if writes == failAt {
							return injected
						}
						return nil
					}
					auth := func() error { return nil }
					deliver := func() (Outcome, error) {
						sends++
						return Outcome{State: state, Decision: r.Decision, AttemptID: r.AttemptID}, nil
					}
					if _, err := l.Decide(r, a, auth, auth, deliver); !errors.Is(err, injected) {
						t.Fatal(err)
					}
					beforeRetry := sends
					out, err := (Ledger{Dir: l.Dir}).Decide(r, a, auth, auth, deliver)
					want := state
					if failAt == 3 {
						want = "failed"
					}
					if err != nil || out.State != want || sends != beforeRetry {
						t.Fatalf("lost known outcome or resent: %+v %v sends=%d want=%s", out, err, sends, want)
					}
				})
			}
		}
	}
}
