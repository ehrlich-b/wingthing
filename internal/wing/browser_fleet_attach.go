package wing

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

const browserAttachmentIdle = 15 * time.Second
const maxBrowserAttachments = 32

type browserFleetEntry struct {
	mu     sync.Mutex
	id     string
	last   time.Time
	closed bool
	phone  *phoneSessions
}

// Browser attachment capabilities are bound to the authenticated owner, tab's
// encryption key and client-generated ID. A quiet/disconnected tab loses its
// writer; reads never reattach or replay a prior takeover.
type browserFleetAttachments struct {
	mu        sync.Mutex
	ctx       context.Context
	service   *wingsession.Service
	owner     string
	entries   map[string]*browserFleetEntry
	ledger    *browserAttachmentLedger
	ledgerErr error
}

func newBrowserFleetAttachments(ctx context.Context, service *wingsession.Service, owner string) *browserFleetAttachments {
	b := &browserFleetAttachments{ctx: ctx, service: service, owner: owner, entries: map[string]*browserFleetEntry{}}
	if owner != "" && service != nil && !service.SharedHost {
		b.ledger, b.ledgerErr = openBrowserAttachmentLedger(service.Config.Dir)
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				b.Close()
				return
			case now := <-ticker.C:
				b.expire(now)
			}
		}
	}()
	return b
}

func browserAttachmentKey(req ws.TunnelRequest) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(req.SenderUserID+"\x00"+req.SenderPub)))
}

func (b *browserFleetAttachments) Execute(ctx context.Context, req ws.TunnelRequest, operation string, raw json.RawMessage) (map[string]any, error) {
	if b.service == nil || b.owner == "" || req.SenderUserID != b.owner || req.SenderPub == "" || b.service.SharedHost {
		return nil, errors.New("personal owner browser attachment required")
	}
	switch operation {
	case "fleet_attach", "fleet_read", "fleet_input", "fleet_detach":
	default:
		return nil, errors.New("unsupported browser attachment operation")
	}
	var args struct {
		Sequence     uint64 `json:"sequence"`
		AttachmentID string `json:"attachment_id"`
		OriginWingID string `json:"origin_wing_id"`
		SessionID    string `json:"session_id"`
		Input        string `json:"input,omitempty"`
		ReadOnly     bool   `json:"read_only,omitempty"`
		Takeover     bool   `json:"takeover,omitempty"`
	}
	if len(raw) > 32<<10 {
		return nil, errors.New("attachment arguments exceed limit")
	}
	if err := localmcp.DecodePhoneFleetAttachment(string(raw), &args); err != nil {
		return nil, err
	}
	if len(args.AttachmentID) < 16 || len(args.AttachmentID) > 64 {
		return nil, errors.New("exact attachment_id required")
	}
	for _, c := range args.AttachmentID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return nil, errors.New("invalid attachment_id")
		}
	}
	if operation == "fleet_attach" && !args.ReadOnly && !args.Takeover {
		return nil, errors.New("choose read_only observation or explicit takeover")
	}
	if b.ledgerErr != nil || b.ledger == nil {
		return nil, errors.New("browser replay fence unavailable")
	}
	if args.Sequence == 0 || operation == "fleet_attach" && args.Sequence != 1 {
		return nil, errors.New("browser attachment sequence required")
	}
	key := browserAttachmentKey(req)
	b.mu.Lock()
	entry := b.entries[key]
	if entry != nil && operation == "fleet_attach" {
		b.mu.Unlock()
		return nil, errors.New("browser takeover already admitted; hand back before another attach")
	}
	if entry == nil && operation == "fleet_attach" {
		if len(b.entries) >= maxBrowserAttachments {
			b.mu.Unlock()
			return nil, errors.New("browser attachment limit reached")
		}
		if err := b.ledger.Consume(ctx, key, args.AttachmentID, args.Sequence, true); err != nil {
			b.mu.Unlock()
			return nil, err
		}
		entry = &browserFleetEntry{id: args.AttachmentID, last: time.Now(), phone: &phoneSessions{service: b.service, owner: b.owner}}
		b.entries[key] = entry
	}
	b.mu.Unlock()
	if entry == nil {
		return nil, errors.New("not attached; explicitly attach again")
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.closed || entry.id != args.AttachmentID {
		return nil, errors.New("attachment changed; refresh before acting")
	}
	if time.Since(entry.last) > browserAttachmentIdle {
		b.remove(key, entry)
		return nil, errors.New("attachment expired; explicitly attach again")
	}
	if operation != "fleet_attach" {
		if err := b.ledger.Consume(ctx, key, args.AttachmentID, args.Sequence, false); err != nil {
			return nil, err
		}
	}
	// All options and execution selectors pass through the same phone/runtime
	// contract, which rechecks exact identity, grants and verified remote pins.
	wire, _ := json.Marshal(map[string]any{"origin_wing_id": args.OriginWingID, "session_id": args.SessionID, "input": args.Input, "read_only": args.ReadOnly, "takeover": args.Takeover})
	callCtx := ctx
	if operation == "fleet_attach" {
		callCtx = b.ctx // readiness remains bounded by OwnerFleetAttach, lifetime by wing + idle lease
	}
	result, err := entry.phone.Execute(callCtx, "browser", operation, "", string(wire))
	if err != nil {
		b.remove(key, entry)
		return nil, err
	}
	entry.last = time.Now()
	response, ok := result.(map[string]any)
	if !ok {
		b.remove(key, entry)
		return nil, errors.New("attachment contract mismatch")
	}
	response["attachment_id"] = entry.id
	response["sequence"] = args.Sequence
	if operation == "fleet_detach" {
		b.remove(key, entry)
	}
	return response, nil
}

// Caller holds entry.mu; never hold the registry lock across stream cleanup.
func (b *browserFleetAttachments) remove(key string, entry *browserFleetEntry) {
	entry.closed = true
	entry.phone.Close("browser")
	b.mu.Lock()
	if b.entries[key] == entry {
		delete(b.entries, key)
	}
	b.mu.Unlock()
}
func (b *browserFleetAttachments) snapshot() map[string]*browserFleetEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]*browserFleetEntry, len(b.entries))
	for key, entry := range b.entries {
		out[key] = entry
	}
	return out
}
func (b *browserFleetAttachments) expire(now time.Time) {
	for key, entry := range b.snapshot() {
		if entry.mu.TryLock() {
			if !entry.closed && now.Sub(entry.last) > browserAttachmentIdle {
				b.remove(key, entry)
			}
			entry.mu.Unlock()
		}
	}
}
func (b *browserFleetAttachments) Close() {
	defer b.ledger.Close()
	for key, entry := range b.snapshot() {
		entry.mu.Lock()
		if !entry.closed {
			b.remove(key, entry)
		}
		entry.mu.Unlock()
	}
}
