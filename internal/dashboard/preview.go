package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/sessionsearch"
)

type previewMessage struct{ speaker, text, timestamp string }
type sessionPreview struct {
	origin       string
	messages     []previewMessage
	update, next string
	observed     time.Time
	cursor       int64
	failed       bool
	loading      bool
}
type previewResult struct {
	target row
	serial uint64
	value  sessionPreview
	err    error
}
type previewRead struct {
	origin string
	cancel context.CancelFunc
	next   time.Time
	cursor int64
	serial uint64
}
type previews struct {
	reads     map[attentionIdentity]previewRead
	results   chan previewResult
	workers   sync.WaitGroup
	active    int
	serial    uint64
	readGates machineReads
}

// The event loop assigns one read gate per saved machine. Workers acquire it
// with their own context, so optional transcript imports cannot make this
// navigator's nonblocking lifecycle inventory lose its native evidence.
type machineReads map[string]chan struct{}

func (g machineReads) gate(machine string) chan struct{} {
	if g == nil {
		return nil
	}
	if g[machine] == nil {
		g[machine] = make(chan struct{}, 1)
	}
	return g[machine]
}

func acquireMachineRead(ctx context.Context, gate chan struct{}) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if gate == nil {
		return func() {}, nil
	}
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return nil, err
		}
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func newPreviews() *previews {
	return &previews{reads: map[attentionIdentity]previewRead{}, results: make(chan previewResult, 2)}
}
func (p *previews) stop() {
	for _, r := range p.reads {
		if r.cancel != nil {
			r.cancel()
		}
	}
}

// The selected conversation, coordinator and visible reports are read with
// at most two active workers and twelve retained targets. Obsolete
// workers are joined on exit, and cache entries disappear with their targets.
func (p *previews) request(ctx context.Context, s *state, wide bool, now time.Time, visible ...row) {
	wanted := map[attentionIdentity]row{}
	ordered := []row{}
	add := func(r row) {
		if _, ok := wanted[r.identity]; !ok && nativeSource(r.StateSource) && r.StateCursor > 0 {
			wanted[r.identity] = r
			ordered = append(ordered, r)
		}
	}
	if (s.mode == navigate || s.mode == filter || s.mode == details) && !s.recent {
		for _, r := range s.rows() {
			if r.Pinned {
				add(r)
				break
			}
		}
		if s.mode == details {
			for _, r := range s.rows() {
				if r.key() == s.target.key() && r.identity == s.target.identity && r.generation == s.target.generation && r.OriginWingID == s.target.OriginWingID {
					add(r)
					break
				}
			}
		} else if wide || len(visible) > 0 {
			if r, ok := s.selection(); ok {
				add(r)
			}
		}
		for _, r := range visible {
			if len(wanted) >= 12 {
				break
			}
			add(r)
		}

	}
	for key, value := range s.previews {
		r, ok := wanted[key]
		if !ok || value.origin != r.OriginWingID || accessDenied(s.machines[key.machine].err) {
			delete(s.previews, key)
		}
	}
	for key, read := range p.reads {
		if accessDenied(s.machines[key.machine].err) {
			delete(s.previews, key)
		}
		if r, ok := wanted[key]; !ok || s.stale(r.machine) || read.origin != r.OriginWingID {
			if read.cancel != nil {
				read.cancel()
			}
			delete(p.reads, key)
			if r, ok := wanted[key]; !ok || read.origin != r.OriginWingID {
				delete(s.previews, key)
			}
		}
	}
	for _, r := range ordered {
		key := r.identity
		if s.stale(r.machine) || s.machines[r.machine].loading {
			continue
		}
		read, known := p.reads[key]
		if known && (read.cancel != nil || (read.cursor == r.StateCursor && now.Before(read.next))) {
			continue
		}
		if p.active >= 2 {
			continue
		}
		if s.previews == nil {
			s.previews = map[attentionIdentity]sessionPreview{}
		}
		value := s.previews[key]
		value.loading = true
		value.origin = r.OriginWingID
		s.previews[key] = value
		p.active++
		p.serial++
		serial := p.serial
		reader := s.previewReader
		gate := p.readGates.gate(r.machine)
		readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		p.reads[key] = previewRead{cancel: cancel, cursor: r.StateCursor, serial: serial, origin: r.OriginWingID}
		p.workers.Add(1)
		go func(r row) {
			defer p.workers.Done()
			defer cancel()
			value, err := func() (sessionPreview, error) {
				release, err := acquireMachineRead(readCtx, gate)
				if err != nil {
					return sessionPreview{}, err
				}
				defer release()
				return reader(readCtx, r)
			}()
			result := previewResult{target: r, value: value, err: err, serial: serial}
			select {
			case p.results <- result:
			case <-ctx.Done():
			}
		}(r)
	}
}
func (p *previews) apply(s *state, result previewResult) {
	p.active = max(0, p.active-1)
	r := result.target
	read, ok := p.reads[r.identity]
	if !ok || read.serial != result.serial || r.identity != s.attentionIdentity(r.machine, r.ID) || r.generation != s.machines[r.machine].generation || s.stale(r.machine) {
		return
	}
	found := false
	for _, current := range s.machines[r.machine].sessions {
		if current.ID == r.ID && current.OriginWingID == r.OriginWingID {
			found = true
			break
		}
	}
	if !found {
		return
	}
	read.cancel = nil
	read.next = time.Now().Add(10 * time.Second)
	p.reads[r.identity] = read
	if s.previews == nil {
		s.previews = map[attentionIdentity]sessionPreview{}
	}
	if result.err != nil {
		// Do not retain protected text following a denied/failed transcript read.
		s.previews[r.identity] = sessionPreview{origin: r.OriginWingID, failed: true}
		return
	}
	s.previews[r.identity] = result.value
}

// The existing transcript command resolves the exact full session ID through
// its owner route. Output and process lifetime are bounded independently of
// provider output size; transport errors never become visible message text.
func readSessionPreview(ctx context.Context, r row) (sessionPreview, error) {
	dir, err := config.StateDir()
	if err != nil {
		return sessionPreview{}, err
	}
	if r.machine != "local" {
		profiles, err := config.LoadRemotes(dir)
		if err != nil {
			return sessionPreview{}, err
		}
		profile, exists := profiles[r.machine]
		if !exists || profile != r.identity.profile {
			return sessionPreview{}, errors.New("saved machine changed")
		}
	} else if r.identity.localDir != "" && dir != r.identity.localDir {
		return sessionPreview{}, errors.New("local state changed")
	}
	return loadSessionPreview(ctx, r, runCommand)
}

func loadSessionPreview(ctx context.Context, r row, command commandRunner) (sessionPreview, error) {
	read := func(after int64, limit int) (egg.SessionView, error) {
		var args []string
		if r.machine != "local" {
			if r.OriginWingID == "" {
				return egg.SessionView{}, errors.New("remote origin not reported")
			}
			args = remotepkg.ProfileArgs(r.identity.profile)
			arguments, _ := json.Marshal(map[string]any{"after_cursor": after, "limit": limit})
			args = append(args, "session", "action", "--json", "--wing", r.OriginWingID, "--session", r.ID, "--operation", "session_read", "--arguments", string(arguments))
		} else {
			args = []string{"session", "transcript", "--json", "--after-cursor", strconv.FormatInt(after, 10), "--limit", strconv.Itoa(limit), "--", r.ID}
		}
		var output previewOutput
		if err := command(ctx, args, remotepkg.IO{Out: &output, ErrOut: io.Discard}); err != nil {
			return egg.SessionView{}, err
		}
		var wire struct {
			Session   string          `json:"session"`
			Lifecycle egg.SessionView `json:"lifecycle"`
		}
		data := output.data
		if r.machine != "local" {
			var action struct {
				OriginWingID string          `json:"origin_wing_id"`
				SessionID    string          `json:"session_id"`
				Result       json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal(data, &action); err != nil {
				return egg.SessionView{}, err
			}
			if action.OriginWingID != r.OriginWingID || action.SessionID != r.ID {
				return egg.SessionView{}, errors.New("remote session identity changed")
			}
			data = action.Result
		}
		if err := json.Unmarshal(data, &wire); err != nil {
			return egg.SessionView{}, err
		}
		if wire.Session != r.ID || wire.Lifecycle.SessionID != r.ID {
			return egg.SessionView{}, errors.New("session identity changed")
		}
		return wire.Lifecycle, nil
	}
	head, err := read(0, 1)
	if err != nil {
		return sessionPreview{}, err
	}
	view, err := read(max(0, head.HeadCursor-30), 30)
	if err != nil {
		return sessionPreview{}, err
	}
	value := sessionPreview{origin: r.OriginWingID, observed: time.Now(), cursor: view.HeadCursor}
	if view.Report != nil {
		value.update = previewText(view.Report.Update)
		value.next = previewText(view.Report.NextAction)
	}
	for _, event := range view.Events {
		if (event.Type != "message" && event.Type != "prompt_submitted") || (event.Role != "user" && event.Role != "assistant") || strings.TrimSpace(event.Text) == "" {
			continue
		}
		speaker := agentLabel(r.Agent)
		if r.Pinned {
			speaker = "wing"
		}
		if event.Role == "user" {
			speaker = "You"
		}
		value.messages = append(value.messages, previewMessage{speaker: speaker, text: previewText(event.Text), timestamp: event.Timestamp})
	}
	if len(value.messages) > 3 {
		value.messages = value.messages[len(value.messages)-3:]
	}
	return value, nil
}

type previewOutput struct{ data []byte }

func (b *previewOutput) Write(data []byte) (int, error) {
	if len(data) > 512*1024-len(b.data) {
		return 0, errors.New("transcript exceeds preview limit")
	}
	b.data = append(b.data, data...)
	return len(data), nil
}
func previewText(text string) string {
	if len(text) > 4096 {
		text = text[:4096]
	}
	return clean(strings.Join(strings.Fields(sessionsearch.Redact(text)), " "))
}
func (s *state) preview(r row) sessionPreview {
	value := s.previews[r.identity]
	if value.origin != r.OriginWingID {
		return sessionPreview{}
	}
	return value
}
func (s *state) latestUpdate(r row) string {
	value := s.preview(r)
	if value.update != "" {
		return value.update
	}
	for i := len(value.messages) - 1; i >= 0; i-- {
		if value.messages[i].speaker != "You" {
			return value.messages[i].text
		}
	}
	return ""
}
func previewStamp(value string) string {
	if stamp, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return stamp.Local().Format("Jan 2, 15:04")
	}
	return ""
}
func (s *state) messageLines(r row, width int) []string {
	value := s.preview(r)
	if value.failed {
		return []string{"Messages unavailable. Enter opens the terminal."}
	}
	lines := []string{}
	if s.stale(r.machine) && !value.observed.IsZero() {
		lines = append(lines, "Saved "+value.observed.Local().Format("Jan 2, 15:04"), "")
	}
	if value.update != "" {
		lines = append(lines, "Last update", value.update, "")
	}
	// Newest first, as in the selected pane, so a short screen keeps the latest.
	for i := len(value.messages) - 1; i >= 0; i-- {
		message := value.messages[i]
		label := message.speaker
		if stamp := previewStamp(message.timestamp); stamp != "" {
			label += " · " + stamp
		}
		lines = append(lines, label)
		parts := wrap(message.text, width)
		if len(parts) > 3 {
			parts = parts[:3]
			parts[2] = clip(parts[2], max(0, width-3)) + "..."
		}
		lines = append(lines, parts...)
		lines = append(lines, "")
	}
	if len(lines) == 0 && value.loading {
		return []string{"Loading messages..."}
	}
	if len(lines) == 0 {
		return []string{"Nothing new yet.", "Enter opens the terminal."}
	}
	return lines
}
