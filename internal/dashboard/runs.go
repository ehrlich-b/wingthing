package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

const runPageSize = 100

type runRow struct {
	WingID             string `json:"wing_id"`
	ID                 string `json:"run_id"`
	SessionID          string `json:"session_id"`
	Agent              string `json:"agent"`
	Label              string `json:"label"`
	CWD                string `json:"cwd"`
	Status             string `json:"status"`
	FailureKind        string `json:"failure_kind"`
	CreatedAt          string `json:"created_at"`
	ParentID           string `json:"parent_id"`
	RequestKey         string `json:"idempotency_key"`
	Phase              string `json:"phase"`
	Isolation          string `json:"isolation"`
	TimeoutSeconds     int    `json:"timeout_seconds"`
	Cleanup            string `json:"cleanup"`
	OutputAvailability string `json:"output_availability"`
	Terminal           bool   `json:"terminal"`
	live               bool
	route              runRoute
}

func (r runRow) key() string { return r.WingID + ":" + r.ID }
func (r runRow) status() string {
	if r.FailureKind == "unknown_outcome" {
		return "unknown"
	}
	switch r.Status {
	case "pending", "running", "done", "failed", "timeout", "stopped":
		return r.Status
	}
	return "unknown"
}

type runRoute struct {
	name, wingID string
	remote       config.Remote
}
type runPage struct {
	Runs       []runRow `json:"runs"`
	Count      int      `json:"count"`
	WingID     string   `json:"wing_id"`
	Next       string   `json:"next_cursor"`
	Revision   string   `json:"snapshot_revision"`
	ObservedAt string   `json:"observed_at"`
	route      runRoute
	cursor     string
	err        string
	liveErr    string
}
type runDirectoryEntry struct {
	WingID  string `json:"wing_id"`
	Name    string `json:"name"`
	Control bool   `json:"mcp_control"`
	Host    string `json:"hostname"`
	Dir     string `json:"wingthing_dir"`
}
type runCall func(context.Context, string, map[string]any) (map[string]any, error)

func runDecode(data map[string]any, out any) error {
	if data == nil {
		return errors.New("missing control response")
	}
	if msg, ok := data["error"].(string); ok && msg != "" && data["run_id"] == nil {
		return errors.New(msg)
	}
	wire, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if len(wire) > 1024*1024 {
		return errors.New("history response exceeds 1 MiB")
	}
	return json.Unmarshal(wire, out)
}

// All history reads use the wing-owned authenticated aggregate adapter. The
// dashboard never owns an SSH pool, database, provider folder or launch retry.
func socketRunCall(ctx context.Context, dir string, use func(runCall) error) error {
	client, err := controlsocket.Dial(ctx, dir, controlsocket.Hello{Client: strings.TrimSpace(os.Getenv("WT_MCP_CLIENT")), Aggregate: true})
	if err != nil {
		return err
	}
	defer client.Close()
	return use(func(ctx context.Context, tool string, args map[string]any) (map[string]any, error) {
		wire, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		data, denied, err := client.Call(ctx, tool, wire)
		if err == nil && !denied && tool == "wing_list" {
			var directory struct {
				Wings []runDirectoryEntry `json:"wings"`
			}
			if decodeErr := runDecode(data, &directory); decodeErr != nil {
				return nil, decodeErr
			}
			pinned := false
			for _, entry := range directory.Wings {
				if entry.Name == "local" && entry.WingID == client.Welcome.WingID {
					pinned = true
				}
			}
			if !pinned {
				return nil, errors.New("history directory does not match authenticated local wing")
			}
		}
		if err == nil && denied {
			err = errors.New("history read denied")
		}
		return data, err
	})
}

func readRunPage(ctx context.Context, call runCall, route runRoute, cursor, revision string) (runPage, error) {
	page := runPage{route: route, cursor: cursor}
	data, err := call(ctx, "agent_list", map[string]any{"wing_id": route.wingID, "limit": runPageSize, "cursor": cursor})
	if err != nil {
		return page, err
	}
	if err := runDecode(data, &page); err != nil {
		return page, err
	}
	if page.Runs == nil || page.Count != len(page.Runs) || page.Count > runPageSize || page.WingID != route.wingID || page.Revision == "" || len(page.Next) > 4096 || page.Next != "" && (page.Count == 0 || page.Next == cursor) || cursor != "" && page.Revision != revision {
		return page, errors.New("incomplete or inconsistent history projection; refresh discovery")
	}
	seen := map[string]bool{}
	for i := range page.Runs {
		r := &page.Runs[i]
		if r.WingID != route.wingID || r.ID == "" || seen[r.ID] {
			return page, errors.New("invalid or duplicate history identity")
		}
		seen[r.ID] = true
		r.route = route
	}
	// Liveness is independent of a durable run's lifecycle. A failed live query
	// still leaves valid metadata discoverable, but never authorizes attach.
	sessions, err := readRunSessions(ctx, call, route.wingID)
	if err != nil {
		page.liveErr = err.Error()
	} else {
		for i := range page.Runs {
			page.Runs[i].live = sessions[page.Runs[i].SessionID]
		}
	}
	return page, nil
}
func readRunSessions(ctx context.Context, call runCall, wingID string) (map[string]bool, error) {
	data, err := call(ctx, "terminal_list", map[string]any{"wing_id": wingID})
	if err != nil {
		return nil, err
	}
	var list struct {
		WingID   string                   `json:"wing_id"`
		Sessions []eggclient.LocalSession `json:"sessions"`
	}
	if err := runDecode(data, &list); err != nil {
		return nil, err
	}
	if list.WingID != wingID || list.Sessions == nil {
		return nil, errors.New("incomplete live session projection")
	}
	live := map[string]bool{}
	for _, s := range list.Sessions {
		if s.ID != "" && s.PID > 0 && s.Status != "exited" {
			live[s.ID] = true
		}
	}
	return live, nil
}
func readRunDirectory(ctx context.Context, call runCall, remotes map[string]config.Remote) ([]runRoute, []string, error) {
	data, err := call(ctx, "wing_list", map[string]any{})
	if err != nil {
		return nil, nil, err
	}
	var directory struct {
		Wings         []runDirectoryEntry `json:"wings"`
		Count         int                 `json:"count"`
		RegistryError string              `json:"registry_error"`
	}
	if err := runDecode(data, &directory); err != nil {
		return nil, nil, err
	}
	if directory.Wings == nil || directory.Count == 0 || directory.Count != len(directory.Wings) || directory.Count > 32 {
		return nil, nil, errors.New("incomplete wing directory or more than 32 wings; use wt agent recover")
	}
	var routes []runRoute
	var unavailable []string
	seen := map[string]bool{}
	localCount := 0
	for _, e := range directory.Wings {
		if e.Name == "local" && e.Control && e.WingID != "" {
			localCount++
		}
		if !e.Control || e.WingID == "" {
			unavailable = append(unavailable, e.Name+": history unavailable (legacy SSH-only profile)")
			continue
		}
		if seen[e.WingID] {
			return nil, nil, errors.New("duplicate wing identity in directory")
		}
		seen[e.WingID] = true
		route := runRoute{name: e.Name, wingID: e.WingID}
		if e.Name != "local" {
			remote, ok := remotes[e.Name]
			if !ok || remote.WingID != e.WingID || remote.SSHTarget != e.Host || remote.WingthingDir != e.Dir {
				unavailable = append(unavailable, e.Name+": saved route changed or unverified; reopen wt")
				continue
			}
			route.remote = remote
		}
		routes = append(routes, route)
	}
	if localCount != 1 {
		return nil, nil, errors.New("incomplete local wing directory")
	}
	for name, remote := range remotes {
		if remote.WingID == "" {
			text := name + ": history unavailable (legacy SSH-only profile)"
			if !containsText(unavailable, text) {
				unavailable = append(unavailable, text)
			}
		}
	}
	if directory.RegistryError != "" {
		unavailable = append(unavailable, "Remembered wing registry unavailable: "+directory.RegistryError)
	}
	sort.Strings(unavailable)
	return routes, unavailable, nil
}
func containsText(items []string, text string) bool {
	for _, item := range items {
		if item == text {
			return true
		}
	}
	return false
}

type runsResult struct {
	next        bool
	pages       []runPage
	unavailable []string
	err         error
	target      *runRow
	content     []string
}
type runHistory struct {
	pages            map[string]runPage
	selected, filter string
	pageWing         string
	unavailable      []string
	err              string
	loading          bool
	requested        bool
	next             bool
	target           runRow
	content          []string // Explicit result view only; erased as soon as the overlay closes.
	recovery         bool     // Details overlay shows recovery steps; reset whenever details open or close.
	results          chan runsResult
	workers          sync.WaitGroup
}

func (s *state) history() *runHistory {
	if s.runs == nil {
		s.runs = &runHistory{pages: map[string]runPage{}, results: make(chan runsResult)}
	}
	return s.runs
}

// current is the target run as its machine last reported it; found is false
// when the latest page no longer lists it.
func (h *runHistory) current() (r runRow, found bool) {
	r = h.target
	for _, run := range h.pages[r.WingID].Runs {
		if run.ID == r.ID && run.SessionID == r.SessionID && run.route == r.route {
			r, found = run, true
		}
	}
	return r, found
}

func (h *runHistory) rows() []runRow {
	var rows []runRow
	query := strings.ToLower(h.filter)
	for _, page := range h.pages {
		for _, r := range page.Runs {
			if query == "" || strings.Contains(strings.ToLower(strings.Join([]string{r.key(), r.SessionID, r.route.name, r.Agent, r.Label, r.CWD, r.status(), r.ParentID, r.Cleanup}, " ")), query) {
				rows = append(rows, r)
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt != rows[j].CreatedAt {
			return rows[i].CreatedAt > rows[j].CreatedAt
		}
		return rows[i].key() < rows[j].key()
	})
	return rows
}
func (h *runHistory) selection() (runRow, bool) {
	rows := h.rows()
	for _, r := range rows {
		if r.key() == h.selected {
			return r, true
		}
	}
	if len(rows) > 0 {
		return rows[0], true
	}
	return runRow{}, false
}
func (h *runHistory) reconcile() {
	if r, ok := h.selection(); ok {
		h.selected = r.key()
		h.pageWing = r.WingID
	} else {
		h.selected = ""
	}
}
func (h *runHistory) move(delta int) {
	rows := h.rows()
	for i, r := range rows {
		if r.key() == h.selected {
			h.selected = rows[max(0, min(len(rows)-1, i+delta))].key()
			return
		}
	}
	h.reconcile()
}
func (h *runHistory) apply(result runsResult) {
	h.loading = false
	if result.target != nil {
		h.target = *result.target
		h.content = result.content
		if result.err != nil {
			h.content = []string{resultFailureNotice(result.err.Error())}
		}
		return
	}
	if result.err != nil {
		h.err = result.err.Error()
		return
	}
	h.err = ""
	if result.unavailable != nil {
		h.unavailable = result.unavailable
	}
	for _, page := range result.pages {
		if page.err != "" {
			old := h.pages[page.WingID]
			old.WingID, old.route, old.err = page.WingID, page.route, page.err
			h.pages[page.WingID] = old
		} else {
			h.pages[page.WingID] = page
		}
	}
	// A removed route is retained as stale metadata and cannot be attached.
	if !result.next {
		found := map[string]bool{}
		for _, p := range result.pages {
			found[p.WingID] = true
		}
		for id, p := range h.pages {
			if !found[id] {
				p.err = "route unavailable; retained metadata stale"
				h.pages[id] = p
			}
		}
	}
	h.reconcile()
}
func (h *runHistory) request(ctx context.Context, dir string, remotes map[string]config.Remote, result *runRow) {
	if h.loading {
		return
	}
	h.loading = true
	h.requested = false
	next := h.next
	old := h.pagingPage()
	selected := runRow{route: old.route}
	h.workers.Add(1)
	go func() {
		defer h.workers.Done()
		parentCtx := ctx
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		reply := runsResult{target: result, next: next}
		reply.err = socketRunCall(ctx, dir, func(call runCall) error {
			routes, unavailable, err := readRunDirectory(ctx, call, remotes)
			if err != nil {
				return err
			}
			if !next && result == nil {
				reply.unavailable = append([]string{}, unavailable...)
			}
			for _, route := range routes {
				if result != nil {
					if route != result.route {
						continue
					}
					reply.content, err = readRunResult(ctx, call, *result)
					return err
				}
				if next && route != selected.route {
					continue
				}
				cursor, revision := "", ""
				if next {
					cursor, revision = old.Next, old.Revision
				}
				page, err := readRunPage(ctx, call, route, cursor, revision)
				if err != nil {
					page = runPage{WingID: route.wingID, route: route, err: err.Error()}
				}
				reply.pages = append(reply.pages, page)
			}
			if (next || result != nil) && len(reply.pages) == 0 {
				return errors.New("saved exact wing route is unavailable")
			}
			return nil
		})
		select {
		case h.results <- reply:
		case <-parentCtx.Done():
		}
	}()
}
func readRunResult(ctx context.Context, call runCall, r runRow) ([]string, error) {
	data, err := call(ctx, "agent_result", map[string]any{"wing_id": r.WingID, "run_id": r.ID, "max_chars": 8192})
	if err != nil {
		return nil, err
	}
	var result struct {
		WingID      string  `json:"wing_id"`
		ID          string  `json:"run_id"`
		Ready       bool    `json:"ready"`
		Status      string  `json:"status"`
		FailureKind string  `json:"failure_kind"`
		Truncated   bool    `json:"truncated"`
		Output      *string `json:"output"`
	}
	if err := runDecode(data, &result); err != nil {
		return nil, err
	}
	if result.WingID != r.WingID || result.ID != r.ID {
		return nil, errors.New("result identity mismatch")
	}
	if !result.Ready {
		return []string{"Result not ready; outcome remains " + r.status()}, nil
	}
	switch result.Status {
	case "done", "failed", "timeout", "stopped":
	default:
		return nil, errors.New("result readiness lacks a terminal lifecycle")
	}
	receipt := runRow{Status: result.Status, FailureKind: result.FailureKind}.status()
	if result.Output == nil {
		return []string{"Terminal result: " + receipt, "Output unavailable"}, nil
	}
	// Preserve newlines as layout while removing all terminal and bidi controls.
	text := *result.Output
	if len(text) > 32768 || utf8.RuneCountInString(text) > 8192 {
		return nil, errors.New("result exceeds bounded view")
	}
	lines := []string{"Terminal receipt: " + receipt + " (cancellation is not completion)"}
	if result.Truncated {
		lines = append(lines, "Output truncated to the requested 8192-character view")
	}
	return append(lines, strings.Split(cleanExceptNewlines(text), "\n")...), nil
}
func cleanExceptNewlines(text string) string {
	parts := strings.Split(text, "\n")
	for i := range parts {
		parts[i] = clean(parts[i])
	}
	return strings.Join(parts, "\n")
}

func (s *state) runHandle(key string) (*action, bool) {
	h := s.history()
	if s.mode == runDetails || s.mode == runResult {
		switch key {
		case "escape", "q", "enter", "i":
			s.mode = navigate
			h.content = nil
			h.recovery = false
		case "r":
			if s.mode == runDetails {
				h.recovery = !h.recovery
				s.overlayOffset = 0
			}
		case "o":
			if s.mode == runDetails && !h.loading {
				s.mode = runResult
				s.overlayOffset = 0
				h.content = nil
				return &action{kind: runResult, run: h.target}, false
			}
		case "up", "k":
			s.overlayOffset = max(0, s.overlayOffset-1)
		case "down", "j":
			s.overlayOffset = min(s.overlayLimit, s.overlayOffset+1)
		case "pagedown":
			s.overlayOffset = min(s.overlayLimit, s.overlayOffset+5)
		case "pageup":
			s.overlayOffset = max(0, s.overlayOffset-5)
		case "home":
			s.overlayOffset = 0
		}
		return nil, false
	}
	if s.mode == runFilter {
		if key == "escape" {
			h.filter = s.previous
			s.mode = navigate
			h.reconcile()
			return nil, false
		}
		if key == "enter" {
			s.mode = navigate
			return nil, false
		}
		switch key {
		case "backspace":
			r := []rune(s.input)
			if len(r) > 0 {
				s.input = string(r[:len(r)-1])
			}
		case "ctrl-u":
			s.input = ""
		default:
			if len([]rune(key)) == 1 && clean(key) == key {
				s.input += key
			}
		}
		h.filter = s.input
		h.reconcile()
		return nil, false
	}
	switch key {
	case "q":
		return nil, true
	case "h", "escape":
		s.recent = false
		s.message = ""
	case "up", "k", "left":
		h.move(-1)
	case "down", "j", "right":
		h.move(1)
	case "/":
		s.mode = runFilter
		s.previous = h.filter
		s.input = h.filter
	case "R":
		h.requested = true
		h.next = false
	case "N":
		if h.pagingPage().Next != "" && !h.loading {
			h.next = true
			h.requested = true
		}
	case "?":
		s.mode = help
		s.overlayOffset = 0
	case "enter", "i", "o":
		if r, ok := h.selection(); ok {
			h.target = r
			h.recovery = false
			s.overlayOffset = 0
			if key == "o" && !h.loading {
				s.mode = runResult
				h.content = nil
				return &action{kind: runResult, run: r}, false
			}
			if key == "enter" && r.live && h.err == "" && h.pages[r.WingID].err == "" {
				return &action{kind: runAttach, run: r, attach: true}, false
			}
			s.mode = runDetails
		}
	}
	return nil, false
}
func humanTime(value string) string {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "Not reported"
	}
	return t.Local().Format("Jan 2, 2006 3:04 PM MST")
}
func (s *state) runOverlay() []string {
	h := s.history()
	r := h.target
	title := s.runTitle(r)
	if s.mode == runResult {
		lines := []string{title, "Result / Read only", ""}
		if h.loading {
			return append(lines, "Reading the result...")
		}
		return append(lines, h.content...)
	}
	p := h.pages[r.WingID]
	r, found := h.current()
	live := found && r.live
	stale := p.err != "" || h.err != ""
	if h.recovery {
		return runRecoveryLines(title, r)
	}
	label, _ := runStatus(r)
	when := "Started: "
	if r.status() == "pending" {
		when = "Queued: "
	}
	lines := []string{title, "", "Ran: " + runWhat(r), "Result: " + label, when + humanTime(r.CreatedAt), "Checked: " + humanTime(p.ObservedAt)}
	if r.Isolation == "privileged" {
		lines = append(lines, "! Sandbox off. This run had full access to your account.")
	} else if label := isolationLabel(r.Isolation); label != "Not reported" {
		lines = append(lines, "Sandbox: "+label)
	}
	if r.TimeoutSeconds > 0 {
		lines = append(lines, fmt.Sprintf("Time limit: %d seconds", r.TimeoutSeconds))
	}
	if runNeedsRecovery(r) && r.Cleanup != "" {
		lines = append(lines, "Cleanup: "+cleanupLabel(r.Cleanup))
	}
	lines = append(lines, "")
	if stale {
		lines = append(lines, "History is offline. This is the last saved state.")
	}
	keys := "o result"
	if runNeedsRecovery(r) {
		keys += "   r recovery steps"
	}
	return append(lines, "Next: "+runNextStep(r, live && !stale), "", keys)
}

// runNeedsRecovery is true only when a run ended without a readable result
// or may have left work behind; a finished run with its output needs none.
func runNeedsRecovery(r runRow) bool {
	switch r.status() {
	case "pending", "running":
		return false
	case "done":
		return r.OutputAvailability != "" && r.OutputAvailability != "available" && r.OutputAvailability != "pending"
	}
	return true
}

func runWhat(r runRow) string {
	what := agentLabel(r.Agent) + " in " + projectName(r.CWD)
	if r.route.name != "" {
		what += " on " + machineLabel(r.route.name)
	}
	return what
}

// runNextStep names exactly one thing the person can do next.
func runNextStep(r runRow, live bool) string {
	switch r.status() {
	case "unknown":
		return "check its work before you run it again."
	case "pending", "running":
		if live {
			return "press Esc, then Enter to open the live session."
		}
		if r.status() == "pending" {
			return "press o once it has run."
		}
		return "wait for it to finish, then press o for the result."
	case "done":
		if r.OutputAvailability != "" && r.OutputAvailability != "available" && r.OutputAvailability != "pending" {
			return "the output is gone. Press r for recovery steps."
		}
		return "press o to read the result."
	case "failed", "timeout":
		return "press o to see what went wrong."
	default:
		return "press o to read what it did before it stopped."
	}
}

// Recovery steps sit behind r. They never resend the run and never print
// saved route flags or machine IDs.
func runRecoveryLines(title string, r runRow) []string {
	lines := []string{title, "Recovery steps", ""}
	if r.RequestKey == "" {
		lines = append(lines, "The original request was not saved. Check where you started it.")
	} else {
		lines = append(lines, "wt saved the original request. It never sends it again by itself.")
	}
	if r.Cleanup == "unknown" || r.Cleanup == "required" || r.Cleanup == "attention" {
		lines = append(lines, "Some processes may still run on "+machineLabel(r.route.name)+". Check before you clean up.")
	}
	where := "In a terminal, run:"
	if r.route.name != "local" {
		where = "On " + machineLabel(r.route.name) + ", run:"
	}
	return append(lines, "", "To list runs that need recovery:", where, "  "+runRecoveryCommand(), "", "To read a saved copy of a finished run:", "  wt run-inspect --snapshot FILE", "", "r back to details")
}

// runTitle tells runs apart: the run's own label, then its session name, then a short run ID.
func (s *state) runTitle(r runRow) string {
	if label := clean(r.Label); strings.TrimSpace(label) != "" {
		return label
	}
	if s.machines != nil {
		if m, ok := s.machines[r.route.name]; ok && r.SessionID != "" {
			for _, session := range m.sessions {
				if session.ID == r.SessionID {
					if name := (row{LocalSession: session}).name(); name != "Untitled session" {
						return name
					}
				}
			}
		}
	}
	id := clean(r.ID)
	for _, prefix := range []string{"run-", "run_"} {
		id = strings.TrimPrefix(id, prefix)
	}
	if len([]rune(id)) > 8 {
		id = string([]rune(id)[:8])
	}
	if id == "" {
		return agentLabel(r.Agent) + " run"
	}
	return "Run " + id
}
func runStatus(r runRow) (string, tone) {
	switch r.status() {
	case "running":
		return "* Working", accent
	case "pending":
		return "! Pending", caution
	case "done":
		return "+ Done", success
	case "failed":
		return "x Failed", failure
	case "timeout":
		return "x Timed out", caution
	case "stopped":
		return "- Stopped", secondary
	default:
		return "? Unknown", caution
	}
}

func cleanupLabel(value string) string {
	switch value {
	case "complete", "completed":
		return "Done"
	case "required", "attention":
		return "Needs check"
	case "none", "not_required":
		return "Not needed"
	default:
		return "Not checked"
	}
}
func resultFailureNotice(err string) string {
	lower := strings.ToLower(err)
	switch {
	case strings.Contains(lower, "denied"):
		return "Result unavailable. Access denied."
	case strings.Contains(lower, "identity"):
		return "Result identity changed. Check the original run before retrying."
	default:
		return "Result unavailable. Retry this exact run."
	}
}
func historyFailureNotice(name, err string) string {
	if strings.Contains(strings.ToLower(err), "denied") {
		return machineLabel(name) + " history read denied"
	}
	return machineLabel(name) + " history unavailable"
}

func (s *state) runUnavailableNotice(text string) string {
	name := strings.SplitN(text, ":", 2)[0]
	if _, ok := s.remotes[name]; ok {
		if strings.Contains(text, "legacy SSH-only profile") {
			return machineLabel(name) + " has no run history. Its SSH sessions remain available."
		}
		return machineLabel(name) + " run history is unavailable. Retry before starting another."
	}
	if strings.Contains(text, "legacy SSH-only profile") {
		return "Saved SSH profile has no run history. Other machines remain available."
	}
	return "A machine's run history is unavailable. Retry before starting another."
}

func (s *state) runView(now time.Time, dimensions ...int) ([]string, []viewLine, int, []string) {
	width := 80
	if len(dimensions) > 0 {
		width = dimensions[0]
	}
	h := s.history()
	rows := h.rows()
	// Same masthead shape as the sessions list: count, machines, then the machine strip.
	title := "wt   Recent runs   " + plural(len(rows), "run") + "   " + plural(len(s.machines), "machine")
	if width >= 120 {
		title = "wt   " + now.Local().Format("Mon Jan 2 · 15:04") + "   Recent runs   " + plural(len(rows), "run") + "   " + plural(len(s.machines), "machine")
	}
	if width < 65 {
		title = "wt   Recent runs   " + plural(len(rows), "run")
	}
	header := []string{title, "Newest first   " + s.machineStrip()}
	var body []viewLine
	selected := 0
	if h.loading {
		body = append(body, viewLine{text: "Loading runs..."})
	}
	if h.err != "" {
		body = append(body, viewLine{text: "History is offline. Rows show the last saved state.", tone: caution})
	}
	for _, text := range h.unavailable {
		body = append(body, viewLine{text: s.runUnavailableNotice(text), tone: caution})
	}
	for _, p := range h.pages {
		if p.err != "" {
			body = append(body, viewLine{text: historyFailureNotice(p.route.name, p.err), tone: caution})
		}
		if p.liveErr != "" {
			body = append(body, viewLine{text: machineLabel(p.route.name) + " live sessions unavailable", tone: caution})
		}
	}
	name, project, agent, machineWidth, elapsed := min(40, max(22, width-58)), 13, 8, 14, 6
	if width < 80 {
		name = max(1, width-20)
		project, agent, machineWidth = 0, 0, 0
	}
	body = append(body, viewLine{text: "  " + field("Run", name) + " " + field("State", 12) + field("Project", project) + field("Agent", agent) + field("Machine", machineWidth) + field("Age", elapsed), tone: secondary})
	for _, r := range rows {
		created, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
		when := "?"
		if !created.IsZero() {
			when = age(int64(now.Sub(created) / time.Second))
		}
		label, role := runStatus(r)
		if h.err != "" || h.pages[r.WingID].err != "" {
			label = "? Last seen"
			role = caution
		}
		prefix := "  "
		isSelected := r.key() == h.selected
		if isSelected {
			prefix = "> "
			selected = len(body)
		}
		body = append(body, viewLine{text: prefix + field(s.runTitle(r), name) + " " + field(label, 12) + field(projectName(r.CWD), project) + field(agentLabel(r.Agent), agent) + field(machineLabel(r.route.name), machineWidth) + field(when, elapsed), selected: isSelected, statusAt: 2 + name, statusWidth: 12, statusTone: role})
	}
	if len(rows) == 0 {
		text := "No runs on the loaded pages"
		if h.err != "" || len(h.unavailable) > 0 {
			text = "Runs unavailable. Retry before starting another."
		}
		body = append(body, viewLine{text: text})
	}
	return header, body, selected, keyHints(s)
}

func verifyRunAttach(ctx context.Context, dir string, r runRow) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	remotes, err := config.LoadRemotes(dir)
	if err != nil {
		return err
	}
	return socketRunCall(ctx, dir, func(call runCall) error {
		routes, _, err := readRunDirectory(ctx, call, remotes)
		if err != nil {
			return err
		}
		for _, route := range routes {
			if route == r.route {
				live, err := readRunSessions(ctx, call, r.WingID)
				if err != nil {
					return err
				}
				if live[r.SessionID] {
					return nil
				}
				return errors.New("session is no longer live; open run metadata")
			}
		}
		return errors.New("saved exact route changed or unavailable; reopen wt")
	})
}
func runAttachArgs(r runRow) []string {
	args := []string{}
	if r.route.name != "local" {
		args = append(args, remotepkg.ProfileArgs(r.route.remote)...)
	}
	return append(args, "attach", "--", r.SessionID)
}

func staleRunPage(p runPage, err error) runPage { p.err = err.Error(); return p }

// runRecoveryCommand is shown only behind the details r key. It is run on the
// machine that owns the run, so it carries no saved route flags or IDs.
func runRecoveryCommand() string {
	command := "wt agent recover"
	if client := strings.TrimSpace(os.Getenv("WT_MCP_CLIENT")); client != "" {
		if strings.Trim(client, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-") == "" {
			command += " --client " + client
		} else {
			command += " --client " + remotepkg.ShellQuote(client)
		}
	}
	return command
}

// Filtering only affects visible rows; it must not make a continuation vanish.
func (h *runHistory) pagingPage() runPage {
	if r, ok := h.selection(); ok {
		return h.pages[r.WingID]
	}
	if p, ok := h.pages[h.pageWing]; ok {
		return p
	}
	ids := make([]string, 0, len(h.pages))
	for id := range h.pages {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if h.pages[id].Next != "" {
			return h.pages[id]
		}
	}
	return runPage{}
}
