package egg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const MaxSessionPromptBytes = 64 << 10

// PromptDelivery is transport evidence only. Release holds the input stream
// (and its writer lease when supported) until receipt observation finishes.
type PromptDelivery struct {
	BytesEnqueued int
	// NoInputAttempted is explicit sender evidence, never inferred from a
	// zero byte count or an error. Clear it before the first input Send.
	NoInputAttempted bool
	Release          func()
	Lost             <-chan error
}

type SessionPromptOptions struct {
	RequestID     string
	Input         string
	Timeout       time.Duration
	Read          func(context.Context, int64, int) (SessionView, error)
	Send          func(context.Context, string) (PromptDelivery, error)
	maxInputBytes int // Native run turns have a separate bound from session_prompt.
}

type SessionPromptResult struct {
	SessionID                   string `json:"session_id"`
	RequestID                   string `json:"request_id"`
	Status                      string `json:"status"`
	NativeReceiptObserved       bool   `json:"native_receipt_observed"`
	ProviderRequestAcknowledged bool   `json:"provider_request_acknowledged"`
	ReceiptKind                 string `json:"receipt_kind,omitempty"`
	Causality                   string `json:"causality"`
	TransportEnqueued           bool   `json:"transport_enqueued"`
	TransportBytesEnqueued      int    `json:"transport_bytes_enqueued"`
	DefinitelyNotSent           bool   `json:"definitely_not_sent"`
	ReservedCursor              int64  `json:"reserved_cursor"`
	ReceiptCursor               int64  `json:"receipt_cursor,omitempty"`
	Retried                     bool   `json:"retried"`
	Reason                      string `json:"reason,omitempty"`
}

type promptReservation struct {
	Version           int                 `json:"version"`
	RequestID         string              `json:"request_id"`
	SpecHash          string              `json:"spec_hash"`
	ProviderSessionID string              `json:"provider_session_id"`
	Phase             string              `json:"phase"`
	CreatedAt         string              `json:"created_at"`
	Result            SessionPromptResult `json:"result"`
}

func validateSessionPromptOptions(o SessionPromptOptions) error {
	if !validLifecycleID(o.RequestID) || len(o.RequestID) > 128 || !utf8.ValidString(o.RequestID) {
		return errors.New("request_id must be a non-empty opaque ID of at most 128 characters without path separators")
	}
	for _, r := range o.RequestID {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return errors.New("request_id must not contain whitespace or control characters")
		}
	}
	limit := o.maxInputBytes
	if limit == 0 {
		limit = MaxSessionPromptBytes
	}
	if strings.TrimSpace(o.Input) == "" || len(o.Input) > limit || !utf8.ValidString(o.Input) {
		return fmt.Errorf("input must be non-empty UTF-8 text of at most %d bytes", limit)
	}
	for _, r := range o.Input {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return errors.New("input contains unsupported terminal control characters; use newlines rather than carriage returns")
		}
	}
	if o.Timeout < 100*time.Millisecond || o.Timeout > time.Minute {
		return errors.New("timeout must be between 100ms and 60s")
	}
	if o.Read == nil || o.Send == nil {
		return errors.New("native lifecycle reader and transport sender are required")
	}
	return nil
}

// NativePromptReady requires a living exact-provider session and native
// foreground idle/completion evidence; it does not certify a visible composer.
func NativePromptReady(view SessionView) bool {
	return (view.Agent == "claude" || view.Agent == "codex") && validLifecycleID(view.ProviderSessionID) && view.ProcessAlive && view.Ready && view.StateSource == view.Agent+"_hook" && (view.State == "idle" || view.State == "completed")
}

func lockPromptSession(ctx context.Context, dir string) (*os.File, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile("prompt.lock", os.O_CREATE|os.O_EXCL|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if errors.Is(err, os.ErrExist) || errors.Is(err, os.ErrNotExist) {
		// Darwin can return ENOENT to an O_CREAT opener racing the first
		// creation despite the entry already existing. Reopen the bound regular
		// inode without O_CREAT, which also avoids replacing a live lock.
		before, statErr := root.Lstat("prompt.lock")
		if statErr != nil {
			return nil, statErr
		}
		if !before.Mode().IsRegular() {
			return nil, errors.New("prompt lock is not a regular file")
		}
		f, err = root.OpenFile("prompt.lock", os.O_RDWR|unix.O_NOFOLLOW, 0600)
		if err == nil {
			after, statErr := f.Stat()
			if statErr != nil || !os.SameFile(before, after) {
				_ = f.Close()
				return nil, errors.New("prompt lock changed while opening")
			}
		}
	}
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err = ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func savePromptReservation(path string, r promptReservation) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err = atomicWritePrivate(path, append(data, '\n')); err != nil {
		return err
	}
	// Atomic rename alone is not a durable before-send reservation on power loss.
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func sessionPromptSpecHash(sessionID, providerID string, o SessionPromptOptions) (string, error) {
	spec, err := json.Marshal(struct {
		Session, Provider, Input string
		Timeout                  int64
	}{sessionID, providerID, o.Input, int64(o.Timeout)})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(spec)
	return hex.EncodeToString(digest[:]), nil
}

func promptPreflightReason(err error) string {
	message := strings.Join(strings.Fields(err.Error()), " ")
	var bounded strings.Builder
	for _, r := range message {
		if unicode.IsControl(r) {
			r = ' '
		}
		if bounded.Len()+utf8.RuneLen(r) > 512 {
			bounded.WriteString("…")
			break
		}
		bounded.WriteRune(r)
	}
	return "input was not attempted: " + bounded.String() + "; resolve this condition and submit with a new request_id"
}

// SubmitSessionPrompt reserves before sending and never resends an existing
// request. Receipt means matching exact-provider native human user text, not a
// request-specific provider acknowledgement (providers do not expose that ID).
// The caller supplies authorization/routing and the PTY transport; this primitive
// owns serialization, bounded waiting, durable reservation and replay semantics.
func SubmitSessionPrompt(ctx context.Context, dir string, o SessionPromptOptions) (SessionPromptResult, error) {
	result := SessionPromptResult{SessionID: filepath.Base(dir), RequestID: o.RequestID, Status: "unconfirmed", Causality: "unverified_without_provider_request_id"}
	if err := validateSessionPromptOptions(o); err != nil {
		return result, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	lock, err := lockPromptSession(waitCtx, dir)
	if err != nil {
		return result, err
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN); _ = lock.Close() }()
	key := sha256.Sum256([]byte(o.RequestID))
	path := filepath.Join(dir, "prompt."+hex.EncodeToString(key[:])+".json")
	var reservation promptReservation
	file, err := openBoundRegularFile(path)
	if err == nil {
		data, readErr := io.ReadAll(io.LimitReader(file, 16385))
		_ = file.Close()
		if readErr != nil {
			return result, readErr
		}
		if len(data) > 16384 {
			return result, errors.New("prompt reservation exceeds bound")
		}
		if err = json.Unmarshal(data, &reservation); err != nil {
			return result, errors.New("invalid prompt reservation")
		}
		if reservation.Version != 1 || reservation.RequestID != o.RequestID || !validLifecycleID(reservation.ProviderSessionID) || reservation.Result.SessionID != result.SessionID || reservation.Result.RequestID != o.RequestID {
			return result, errors.New("invalid prompt reservation")
		}
		// Replays retain the destination bound at reservation. A replacement
		// provider or unavailable native reader cannot invalidate a proven
		// terminal result or cause an existing request to be sent again.
		specHash, hashErr := sessionPromptSpecHash(result.SessionID, reservation.ProviderSessionID, o)
		if hashErr != nil {
			return result, hashErr
		}
		if reservation.SpecHash != specHash {
			return result, errors.New("request_id already reserved with a different prompt, destination, or wait bound")
		}
		result = reservation.Result
		result.Retried = true
		if result.NativeReceiptObserved || (result.Status == "not_sent" && result.DefinitelyNotSent && reservation.Phase == "not_sent") {
			return result, nil
		}
		result.Reason = "prior reservation may have delivered input; retry will inspect native history without resending"
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	} else {
		initial, readErr := o.Read(waitCtx, 0, 1)
		if readErr != nil {
			return result, readErr
		}
		// Drain the source head before reservation so an old matching user
		// record beyond the import batch cannot appear as a new receipt.
		for initial.HasMore {
			if err = waitCtx.Err(); err != nil {
				return result, err
			}
			next, readErr := o.Read(waitCtx, initial.Cursor, 200)
			if readErr != nil {
				return result, readErr
			}
			if next.HasMore && next.Cursor <= initial.Cursor {
				return result, errors.New("native event replay made no progress while reserving prompt")
			}
			initial = next
		}
		if (initial.Agent != "claude" && initial.Agent != "codex") || !validLifecycleID(initial.ProviderSessionID) {
			return result, errors.New("provider has no exact native prompt receipt adapter; use terminal_send for raw input")
		}
		if !NativePromptReady(initial) {
			return result, fmt.Errorf("native foreground readiness required: state=%s source=%s ready=%t process_alive=%t", initial.State, initial.StateSource, initial.Ready, initial.ProcessAlive)
		}
		if err = waitCtx.Err(); err != nil {
			return result, err
		}
		specHash, hashErr := sessionPromptSpecHash(result.SessionID, initial.ProviderSessionID, o)
		if hashErr != nil {
			return result, hashErr
		}
		result.ReservedCursor = initial.HeadCursor
		result.Reason = "reserved before input transport; delivery is not yet confirmed"
		reservation = promptReservation{Version: 1, RequestID: o.RequestID, SpecHash: specHash, ProviderSessionID: initial.ProviderSessionID, Phase: "reserved", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Result: result}
		if err = savePromptReservation(path, reservation); err != nil {
			return result, fmt.Errorf("persist before-send reservation: %w", err)
		}
		delivery, sendErr := o.Send(waitCtx, o.Input)
		if delivery.Release != nil {
			defer delivery.Release()
		}
		result.TransportBytesEnqueued = delivery.BytesEnqueued
		result.TransportEnqueued = sendErr == nil
		reservation.Phase = "transport_attempted"
		reservation.Result = result
		if sendErr != nil {
			result.Reason = "input transport returned an error; native receipt remains unconfirmed"
			if delivery.NoInputAttempted && delivery.BytesEnqueued == 0 {
				result.Status = "not_sent"
				result.DefinitelyNotSent = true
				result.Reason = promptPreflightReason(sendErr)
				reservation.Phase = "not_sent"
			}
			reservation.Result = result
			if err = savePromptReservation(path, reservation); err != nil {
				return result, err
			}
			return result, nil
		}
		if err = savePromptReservation(path, reservation); err != nil {
			return result, fmt.Errorf("persist transport evidence (reservation remains retry-safe): %w", err)
		}
		return waitForPromptReceipt(waitCtx, path, reservation, result, o, delivery.Lost)
	}
	return waitForPromptReceipt(waitCtx, path, reservation, result, o, nil)
}

func waitForPromptReceipt(ctx context.Context, path string, reservation promptReservation, result SessionPromptResult, o SessionPromptOptions, lost <-chan error) (SessionPromptResult, error) {
	scan := result.ReservedCursor
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	persist := func(reason string) (SessionPromptResult, error) {
		result.Reason = reason
		reservation.Result = result
		return result, savePromptReservation(path, reservation)
	}
	for {
		if ctx.Err() != nil {
			return persist("receipt wait ended; input may have been delivered; retry this request_id to inspect without resending")
		}
		view, err := o.Read(ctx, scan, 200)
		if err != nil {
			return persist("native transcript read unavailable; input receipt remains unconfirmed")
		}
		if view.ProviderSessionID != reservation.ProviderSessionID {
			return persist("provider identity changed; receipt cannot be attributed to the reserved destination")
		}
		for _, event := range view.Events {
			if event.Sequence > result.ReservedCursor && nativeHumanPromptReceipt(event, reservation.ProviderSessionID, o.Input) {
				result.NativeReceiptObserved = true
				result.Status = "native_receipt_observed"
				result.ReceiptKind = "exact_provider_user_text_match"
				if event.Source == "codex_hook" {
					result.ReceiptKind = "exact_provider_prompt_hook_match"
				}
				result.ReceiptCursor = event.Sequence
				reservation.Phase = "native_receipt_observed"
				return persist("matching human user prompt observed in exact provider transcript; concurrent identical human input cannot be causally distinguished")
			}
		}
		if view.Cursor > scan {
			scan = view.Cursor
		}
		if view.HasMore {
			continue
		}
		select {
		case <-ctx.Done():
			return persist("receipt wait ended; input may have been delivered; retry this request_id to inspect without resending")
		case <-lost:
			return persist("input connection was lost before native receipt confirmation; retry this request_id without resending")
		case <-ticker.C:
		}
	}
}

func nativeHumanPromptReceipt(event SessionEvent, providerID, input string) bool {
	if event.Source == "codex_hook" {
		return event.ProviderSessionID == providerID && event.Type == "prompt_submitted" && event.Text == input
	}
	if event.Source != "claude_transcript" || event.ProviderSessionID != providerID || event.Type != "message" || len(event.Raw) == 0 {
		return false
	}
	var record struct {
		Type           string `json:"type"`
		SessionID      string `json:"sessionId"`
		Meta           bool   `json:"isMeta"`
		Synthetic      bool   `json:"isSynthetic"`
		Summary        bool   `json:"isCompactSummary"`
		Sidechain      bool   `json:"isSidechain"`
		TranscriptOnly bool   `json:"isVisibleInTranscriptOnly"`
		Message        struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(event.Raw, &record) != nil || record.Type != "user" || record.SessionID != providerID || record.Message.Role != "user" || record.Meta || record.Synthetic || record.Summary || record.Sidechain || record.TranscriptOnly {
		return false
	}
	var text string
	if json.Unmarshal(record.Message.Content, &text) == nil {
		return text == input
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(record.Message.Content, &blocks) != nil || len(blocks) == 0 {
		return false
	}
	var parts []string
	for _, block := range blocks {
		if block.Type != "text" {
			return false
		}
		parts = append(parts, block.Text)
	}
	return strings.Join(parts, "\n") == input
}
