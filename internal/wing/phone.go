package wing

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

type phoneAttachment struct {
	id        string
	client    *egg.Client
	stream    pb.Egg_SessionClient
	cancel    context.CancelFunc
	mu        sync.Mutex
	output    []byte
	ended     bool
	truncated bool
	terminal  *egg.VTerm
	done      chan struct{}
	closed    bool
}
type phoneSessions struct {
	service     *wingsession.Service
	owner       string
	mu          sync.Mutex
	attachments map[string]*phoneAttachment
	seen        map[string]map[string]int64
}

func (h *phoneSessions) authority() wingsession.Authority {
	return wingsession.Authority{UserID: h.owner, Role: "owner", Browser: true}
}
func (h *phoneSessions) PhoneCapabilities() []string { return []string{"thread", "history"} }
func (h *phoneSessions) Execute(ctx context.Context, connection, op, id, input string) (any, error) {
	if h.service == nil {
		return nil, errors.New("unavailable")
	}
	switch op {
	case "sessions":
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		sessions, e := h.service.List(ctx, h.authority())
		if e != nil {
			return nil, e
		}
		inventory := h.service.Inventory(h.service.Config)
		attention := map[string]bool{}
		for _, s := range inventory {
			attention[s.SessionID] = s.NeedsAttention
		}
		out := []map[string]any{}
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, s := range sessions {
			seen := h.seen[connection][s.ID]
			out = append(out, map[string]any{"id": s.ID, "name": s.Name, "agent": s.Agent, "status": s.Status, "blocked": attention[s.ID], "unread": s.TotalWritten > seen, "cwd": s.CWD, "role": s.Role, "pinned": s.Pinned})
		}
		return out, nil
	case "history":
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var page struct {
			After string `json:"after"`
		}
		if input != "" && json.Unmarshal([]byte(input), &page) != nil || len(page.After) > 128 {
			return nil, errors.New("invalid history cursor")
		}
		entries, err := os.ReadDir(filepath.Join(h.service.Config.Dir, "eggs"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		out := []map[string]any{}
		next, last, size := "", "", 0
		for _, entry := range entries {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || entry.Name() <= page.After {
				continue
			}
			dir := filepath.Join(h.service.Config.Dir, "eggs", entry.Name())
			if _, alive := eggclient.ReadAliveEggPID(dir); alive {
				continue
			}
			meta := eggclient.ReadEggMetaValues(dir)
			if len(meta) == 0 {
				continue
			}
			s := eggclient.LocalSession{ID: entry.Name(), Name: eggclient.ReadSessionName(dir), Principal: eggclient.ReadSessionPrincipal(dir), Agent: meta["agent"], CWD: meta["cwd"]}
			if !h.service.Owns(h.authority(), s) {
				continue
			}
			role, err := sessionrole.Read(dir)
			if err != nil {
				return nil, err
			}
			row := map[string]any{"id": s.ID, "name": s.Name, "agent": s.Agent, "status": "archived", "cwd": s.CWD, "role": role.Role, "pinned": role.Pinned, "blocked": false, "unread": false}
			encoded, err := json.Marshal(row)
			if err != nil || len(encoded) > 48<<10 {
				return nil, errors.New("history metadata too large")
			}
			if len(out) == 32 || size+len(encoded) > 48<<10 {
				next = last
				break
			}
			out = append(out, row)
			size += len(encoded)
			last = entry.Name()
		}
		return map[string]any{"sessions": out, "next_cursor": next}, nil
	case "thread":
		var page struct {
			After int64 `json:"after"`
		}
		if input != "" && json.Unmarshal([]byte(input), &page) != nil || page.After < 0 {
			return nil, errors.New("invalid thread cursor")
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		session, e := h.service.Resolve(ctx, h.authority(), id, true)
		if e != nil || session.ID != id {
			return nil, errors.New("exact owned session required")
		}
		return eggclient.LifecycleViewForSession(h.service.Config, session, page.After, 20)
	case "attach":
		h.detach(connection)
		session, e := h.service.Resolve(ctx, h.authority(), id, false)
		if e != nil {
			return nil, e
		}
		dir := filepath.Join(h.service.Config.Dir, "eggs", session.ID)
		client, e := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
		if e != nil {
			return nil, e
		}
		actx, cancel := context.WithCancel(ctx)
		// Attach is bound to the connection lifetime; the readiness timer only cancels on failure.
		timer := time.AfterFunc(5*time.Second, cancel)
		stream, e := h.service.Attach(actx, h.authority(), client, session.ID, egg.AttachOptions{Claim: true, Owner: "phone:" + connection, Rows: 30, Cols: 80}, nil)
		timer.Stop()
		if e != nil {
			cancel()
			client.Close()
			return nil, e
		}
		a := &phoneAttachment{id: session.ID, client: client, stream: stream, cancel: cancel, terminal: egg.NewVTerm(80, 30), done: make(chan struct{})}
		first, err := stream.Recv() // Attach already buffered this acknowledgment.
		if err != nil {
			cancel()
			_ = client.Close()
			_ = a.terminal.Close()
			return nil, err
		}
		if payload, ok := first.Payload.(*pb.SessionMsg_Output); ok {
			_, _ = a.terminal.Write(payload.Output)
			a.output = append(a.output, payload.Output...)
		}
		h.mu.Lock()
		h.attachments[connection] = a
		if h.seen[connection] == nil {
			h.seen[connection] = map[string]int64{}
		}
		h.seen[connection][session.ID] = session.TotalWritten
		h.mu.Unlock()
		go func() {
			defer close(a.done)
			for {
				msg, e := stream.Recv()
				a.mu.Lock()
				if e != nil {
					a.ended = true
					a.mu.Unlock()
					return
				}
				if output, ok := msg.Payload.(*pb.SessionMsg_Output); ok {
					_, _ = a.terminal.Write(output.Output)
					a.output = append(a.output, output.Output...)
					if len(a.output) > 64<<10 {
						a.output = a.output[len(a.output)-(64<<10):]
						a.truncated = true
					}
				}
				if _, ok := msg.Payload.(*pb.SessionMsg_ExitCode); ok {
					a.ended = true
				}
				a.mu.Unlock()
			}
		}()
		return map[string]bool{"ok": true}, nil
	case "read", "input":
		h.mu.Lock()
		a := h.attachments[connection]
		h.mu.Unlock()
		if a == nil || a.id != id {
			return nil, errors.New("not attached")
		}
		if op == "input" {
			if len(input) > 16<<10 {
				return nil, errors.New("too large")
			}
			return map[string]bool{"ok": true}, h.service.Input(a.stream, id, []byte(input))
		}
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			return nil, errors.New("attachment closed")
		}
		data := string(a.output)
		screen := a.terminal.ScreenText()
		a.output = nil
		ended, truncated := a.ended, a.truncated
		a.truncated = false
		a.mu.Unlock()
		h.mu.Lock()
		if h.seen[connection] == nil {
			h.seen[connection] = map[string]int64{}
		}
		h.seen[connection][id] += int64(len(data))
		h.mu.Unlock()
		return map[string]any{"output": data, "screen": screen, "ended": ended, "truncated": truncated}, nil
	case "stop":
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, e := h.service.Stop(ctx, h.authority(), id)
		return map[string]bool{"ok": e == nil}, e
	case "detach":
		h.detach(connection)
		return map[string]bool{"ok": true}, nil
	}
	return nil, errors.New("denied")
}
func (h *phoneSessions) Close(connection string) {
	h.detach(connection)
	h.mu.Lock()
	delete(h.seen, connection)
	h.mu.Unlock()
}
func (h *phoneSessions) detach(connection string) {
	h.mu.Lock()
	a := h.attachments[connection]
	delete(h.attachments, connection)
	h.mu.Unlock()
	if a != nil {
		a.cancel()
		_ = a.client.Close()
		<-a.done
		a.mu.Lock()
		a.closed = true
		_ = a.terminal.Close()
		a.mu.Unlock()
	}
}
