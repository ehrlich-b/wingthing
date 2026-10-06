package localmcp

// The conversation mailbox carries MCP requests from a sandboxed parent's
// injected stdio client to its host broker through regular files in the
// parent's already writable workspace. It is data-only, with same-owner
// workspace trust: any process able to write that directory can act as the
// mailbox client or forge client-side files. That includes the parent's own
// children, which run in the same workspace under the same owner, and any
// other same-owner process with workspace write access. It does not
// authenticate a writer process and is not a sealed caller transport.
// Authority comes only from the host broker's protected registration, never
// from these files, and is bounded to the registered owner's task tree.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	conversationMailboxVersion       = 1
	conversationMailboxRequestBytes  = 1 << 20
	conversationMailboxResponseBytes = 4 << 20
	conversationMailboxReadyBytes    = 8 << 10
	conversationMailboxPendingLimit  = 32
	conversationMailboxEntryLimit    = 4*conversationMailboxPendingLimit + 8
	conversationMailboxReadyMaxAge   = 3 * time.Second
	conversationMailboxRequestMaxAge = 10 * time.Minute
	conversationMailboxReadyFile     = "host-ready.json"
	conversationMailboxConcurrency   = 8
)

var conversationMailboxPoll = 50 * time.Millisecond

// How long a client waits for a broker heartbeat before reporting the host
// unavailable. Before publication nothing was sent; after publication the
// outcome is unconfirmed and the request is never republished.
var conversationMailboxReadyWait = 30 * time.Second

type conversationMailboxReady struct {
	Version           int    `json:"version"`
	Epoch             string `json:"epoch"`
	ConversationID    string `json:"conversation_id"`
	SessionID         string `json:"session_id"`
	ProviderSessionID string `json:"provider_session_id"`
	HostReady         bool   `json:"host_ready"`
	ObservedAt        int64  `json:"observed_at"`
	Reason            string `json:"reason,omitempty"`
}

type conversationMailboxRequest struct {
	Version   int             `json:"version"`
	ID        string          `json:"request_id"`
	Epoch     string          `json:"epoch"`
	CreatedAt int64           `json:"created_at"`
	Payload   json.RawMessage `json:"payload"`
}

// The payload is an MCP method and its params only. Identity, grants, paths
// and bounds are never accepted from a mailbox envelope.
type conversationMailboxCall struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type conversationMailboxResponse struct {
	Version           int             `json:"version"`
	ID                string          `json:"request_id"`
	Epoch             string          `json:"epoch"`
	ConversationID    string          `json:"conversation_id"`
	SessionID         string          `json:"session_id"`
	ProviderSessionID string          `json:"provider_session_id"`
	Dispatched        bool            `json:"dispatched"`
	Outcome           string          `json:"outcome"`
	Payload           json.RawMessage `json:"payload,omitempty"`
	Error             string          `json:"error,omitempty"`
}

func validMailboxID(id string) bool {
	if len(id) != 32 || id != strings.ToLower(id) {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func newMailboxID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func mailboxRequestName(id string) string { return "request." + id + ".json" }

func mailboxResponseName(id string) string { return "response." + id + ".json" }

// mailboxRequestID returns the ID of a well-formed request artifact name.
func mailboxRequestID(name string) (string, bool) {
	id, ok := strings.CutPrefix(name, "request.")
	if !ok {
		return "", false
	}
	id, ok = strings.CutSuffix(id, ".json")
	return id, ok && validMailboxID(id)
}

func mailboxResponseID(name string) (string, bool) {
	id, ok := strings.CutPrefix(name, "response.")
	if !ok {
		return "", false
	}
	id, ok = strings.CutSuffix(id, ".json")
	return id, ok && validMailboxID(id)
}

func mailboxRead(root *os.Root, name string, maximum int64) ([]byte, error) {
	if maximum <= 0 {
		return nil, errors.New("mailbox artifact bound must be positive")
	}
	file, err := root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("mailbox artifact must be a regular file")
	}
	if info.Size() > maximum {
		return nil, errors.New("mailbox artifact exceeds bound")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if int64(len(data)) > maximum {
		return nil, errors.New("mailbox artifact exceeds bound")
	}
	return data, err
}

// mailboxWrite publishes one complete artifact with an exclusive private
// temporary file and an atomic rename inside the bound root.
func mailboxWrite(root *os.Root, name string, value any, maximum int) error {
	if maximum <= 0 {
		return errors.New("mailbox artifact bound must be positive")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > maximum {
		return errors.New("mailbox artifact exceeds bound")
	}
	id, err := newMailboxID()
	if err != nil {
		return err
	}
	temporary := ".publish-" + id
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(temporary) }()
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return root.Rename(temporary, name)
}

func decodeMailbox(data []byte, destination any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("mailbox envelope must contain one JSON value")
	}
	return nil
}

func mailboxEntries(root *os.Root, maximum int) ([]os.DirEntry, error) {
	if maximum <= 0 {
		return nil, errors.New("mailbox artifact count bound must be positive")
	}
	file, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	entries, err := file.ReadDir(maximum + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maximum {
		return nil, errors.New("mailbox artifact count exceeds bound")
	}
	return entries, nil
}

func readMailboxReady(root *os.Root, conversation, session string, now time.Time) (conversationMailboxReady, error) {
	var ready conversationMailboxReady
	data, err := mailboxRead(root, conversationMailboxReadyFile, conversationMailboxReadyBytes)
	if err != nil {
		return ready, err
	}
	if err := decodeMailbox(data, &ready); err != nil {
		return ready, err
	}
	if ready.Version != conversationMailboxVersion || !validMailboxID(ready.Epoch) || ready.ConversationID != conversation || ready.SessionID != session {
		return ready, errors.New("mailbox host binding differs from this parent execution")
	}
	age := now.Sub(time.Unix(ready.ObservedAt, 0))
	if !ready.HostReady || age > conversationMailboxReadyMaxAge || age < -2*time.Second {
		reason := "stale heartbeat"
		if ready.Reason != "" {
			reason = ready.Reason
		}
		return ready, fmt.Errorf("mailbox host is unavailable for this parent execution: %s", reason)
	}
	return ready, nil
}

// conversationMailboxClient is the in-sandbox side. It publishes at most one
// request per call and never republishes: after publication, loss of the
// response leaves the outcome unconfirmed. Tool-level request IDs (launch and
// prompt) are the only safe way to reconcile such an operation.
type conversationMailboxClient struct {
	dir          string
	conversation string
	session      string
}

var errMailboxUnconfirmed = errors.New("mailbox response unavailable; the request was published and its outcome is unconfirmed; do not resend it with a new request identity")

// mailboxCallError reports whether a failed call reached the host dispatcher.
// The MCP caller receives the same facts as structured error data, so it never
// has to parse prose to decide whether a retry could repeat an effect.
type mailboxCallError struct {
	dispatched string // "no", "yes" or "unknown"
	outcome    string
	err        error
}

func (e *mailboxCallError) Error() string { return e.err.Error() }

func (e *mailboxCallError) Unwrap() error { return e.err }

func notSent(err error) error {
	return &mailboxCallError{dispatched: "no", outcome: brokerOutcomeNotDispatched, err: err}
}

func unconfirmed(err error) error {
	return &mailboxCallError{dispatched: "unknown", outcome: brokerOutcomeUnconfirmed, err: err}
}

func (c conversationMailboxClient) exchange(ctx context.Context, call conversationMailboxCall) (conversationMailboxResponse, error) {
	var response conversationMailboxResponse
	payload, err := json.Marshal(call)
	if err != nil {
		return response, notSent(err)
	}
	if len(payload) > conversationMailboxRequestBytes-1024 {
		return response, notSent(errors.New("MCP request exceeds mailbox bound; nothing was sent"))
	}
	root, err := os.OpenRoot(c.dir)
	if err != nil {
		return response, notSent(err)
	}
	defer func() { _ = root.Close() }()
	ticker := time.NewTicker(conversationMailboxPoll)
	defer ticker.Stop()
	readyCtx, cancel := context.WithTimeout(ctx, conversationMailboxReadyWait)
	defer cancel()
	var ready conversationMailboxReady
	for {
		ready, err = readMailboxReady(root, c.conversation, c.session, time.Now())
		if err == nil {
			break
		}
		select {
		case <-readyCtx.Done():
			return response, notSent(fmt.Errorf("host mailbox not ready; nothing was sent: %w", err))
		case <-ticker.C:
		}
	}
	entries, err := mailboxEntries(root, conversationMailboxEntryLimit)
	if err != nil {
		return response, notSent(fmt.Errorf("nothing was sent: %w", err))
	}
	pending := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "request.") {
			pending++
		}
	}
	if pending >= conversationMailboxPendingLimit {
		return response, notSent(errors.New("parent mailbox pending request limit reached; nothing was sent"))
	}
	id, err := newMailboxID()
	if err != nil {
		return response, notSent(err)
	}
	if err := ctx.Err(); err != nil {
		return response, notSent(err)
	}
	name := mailboxRequestName(id)
	request := conversationMailboxRequest{Version: conversationMailboxVersion, ID: id, Epoch: ready.Epoch, CreatedAt: time.Now().Unix(), Payload: payload}
	if err := mailboxWrite(root, name, request, conversationMailboxRequestBytes); err != nil {
		if _, statErr := root.Lstat(name); errors.Is(statErr, os.ErrNotExist) {
			return response, notSent(fmt.Errorf("publish request; nothing was sent: %w", err))
		}
		return response, unconfirmed(fmt.Errorf("%w: %v", errMailboxUnconfirmed, err))
	}
	lastReady := time.Now()
	var restarted time.Time
	for {
		data, readErr := mailboxRead(root, mailboxResponseName(id), conversationMailboxResponseBytes)
		if readErr == nil {
			if err := decodeMailbox(data, &response); err != nil {
				return response, unconfirmed(fmt.Errorf("invalid mailbox response; delivery remains unconfirmed: %w", err))
			}
			if response.Version != conversationMailboxVersion || response.ID != id || response.ConversationID != c.conversation || response.SessionID != c.session {
				return response, unconfirmed(errors.New("mailbox response execution binding differs; delivery remains unconfirmed"))
			}
			_ = root.Remove(mailboxResponseName(id))
			return response, nil
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return response, unconfirmed(fmt.Errorf("%w: %v", errMailboxUnconfirmed, readErr))
		}
		current, readyErr := readMailboxReady(root, c.conversation, c.session, time.Now())
		if readyErr == nil {
			lastReady = time.Now()
		} else if time.Since(lastReady) > conversationMailboxReadyWait {
			return response, unconfirmed(fmt.Errorf("%w: %v", errMailboxUnconfirmed, readyErr))
		}
		// A new broker epoch rejects a still-pending request with a
		// not-dispatched response, and answers a journaled mutation through
		// bounded recovery. A claimed read is never journaled, so after a
		// grace period its outcome is reported as unconfirmed.
		if readyErr == nil && current.Epoch != request.Epoch {
			if restarted.IsZero() {
				restarted = time.Now()
			}
			if _, statErr := root.Lstat(name); errors.Is(statErr, os.ErrNotExist) && time.Since(restarted) > conversationMailboxReadyWait {
				return response, unconfirmed(fmt.Errorf("%w: host broker restarted after accepting the request", errMailboxUnconfirmed))
			}
		}
		select {
		case <-ctx.Done():
			return response, unconfirmed(errMailboxUnconfirmed)
		case <-ticker.C:
		}
	}
}

// serveConversationMailboxClient is `wt mcp stdio --host-mailbox`. It needs no
// Wingthing state access: it forwards each MCP request to the host broker and
// writes back the broker's response with the caller's JSON-RPC ID.
func ServeConversationMailboxClient(ctx context.Context, in io.Reader, out io.Writer, dir, conversation, session string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return errors.New("--host-mailbox must be a clean absolute directory")
	}
	if err := eggclient.ValidateSessionID(conversation); err != nil {
		return fmt.Errorf("--conversation: %w", err)
	}
	if err := eggclient.ValidateSessionID(session); err != nil {
		return fmt.Errorf("--execution: %w", err)
	}
	client := conversationMailboxClient{dir: dir, conversation: conversation, session: session}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), conversationMailboxRequestBytes)
	encoder := json.NewEncoder(out)
	var encodeMu sync.Mutex
	var encodeErr error
	write := func(response localMCPResponse) {
		encodeMu.Lock()
		defer encodeMu.Unlock()
		if encodeErr == nil {
			encodeErr = encoder.Encode(response)
		}
	}
	slots := make(chan struct{}, conversationMailboxConcurrency)
	var calls sync.WaitGroup
	for scanner.Scan() {
		var request localMCPRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			write(localMCPResponse{JSONRPC: "2.0", Error: &localMCPError{Code: -32700, Message: "parse error"}})
			continue
		}
		if len(request.ID) == 0 {
			continue // notifications carry no response and need no host state
		}
		if request.JSONRPC != "2.0" || request.Method == "" {
			write(localMCPResponse{JSONRPC: "2.0", ID: request.ID, Error: &localMCPError{Code: -32600, Message: "invalid request"}})
			continue
		}
		if !acquireLocalMCPCallSlot(slots) {
			write(localMCPResponse{JSONRPC: "2.0", ID: request.ID, Error: &localMCPError{Code: -32000, Message: "too many concurrent mailbox calls; nothing was sent"}})
			continue
		}
		calls.Add(1)
		go func(request localMCPRequest) {
			defer calls.Done()
			defer func() { <-slots }()
			write(forwardMailboxCall(callCtx, client, request))
		}(request)
	}
	scanErr := scanner.Err()
	cancel()
	calls.Wait()
	if scanErr != nil {
		return fmt.Errorf("read MCP request: %w", scanErr)
	}
	return encodeErr
}

// mailboxEnvelopeError maps a broker error envelope to the caller-visible
// dispatch state. Only an explicit, consistent not_dispatched is reported as
// unsent; missing, unconfirmed, unknown, or contradictory outcomes are
// unconfirmed and never reported as safe to resend.
func mailboxEnvelopeError(envelope conversationMailboxResponse) error {
	dispatched, outcome := "unknown", brokerOutcomeUnconfirmed
	switch {
	case !envelope.Dispatched && envelope.Outcome == brokerOutcomeNotDispatched:
		dispatched, outcome = "no", brokerOutcomeNotDispatched
	case envelope.Dispatched && envelope.Outcome == brokerOutcomeCompleted:
		dispatched, outcome = "yes", brokerOutcomeCompleted
	}
	return &mailboxCallError{dispatched: dispatched, outcome: outcome, err: errors.New(envelope.Error)}
}

func forwardMailboxCall(ctx context.Context, client conversationMailboxClient, request localMCPRequest) localMCPResponse {
	response := localMCPResponse{JSONRPC: "2.0", ID: request.ID}
	envelope, err := client.exchange(ctx, conversationMailboxCall{Method: request.Method, Params: request.Params})
	if err == nil && envelope.Error != "" {
		err = mailboxEnvelopeError(envelope)
	}
	if err != nil {
		state := &mailboxCallError{dispatched: "unknown", outcome: brokerOutcomeUnconfirmed}
		_ = errors.As(err, &state)
		response.Error = &localMCPError{Code: -32000, Message: "wingthing host mailbox: " + err.Error(), Data: map[string]any{
			"transport": "host_mailbox", "dispatched": state.dispatched, "outcome": state.outcome, "retry_safe": state.dispatched == "no",
		}}
		return response
	}
	var hosted localMCPResponse
	if err := json.Unmarshal(envelope.Payload, &hosted); err != nil {
		response.Error = &localMCPError{Code: -32000, Message: "wingthing host mailbox: invalid broker payload; the call was dispatched and its outcome is unconfirmed", Data: map[string]any{
			"transport": "host_mailbox", "dispatched": "yes", "outcome": brokerOutcomeUnconfirmed, "retry_safe": false,
		}}
		return response
	}
	response.Result, response.Error = hosted.Result, hosted.Error
	return response
}
