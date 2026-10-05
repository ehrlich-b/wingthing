package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestMailbox(t *testing.T) (string, *os.Root) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return dir, root
}

func publishTestReady(t *testing.T, root *os.Root, conversation, session, epoch string, ready bool) {
	t.Helper()
	value := conversationMailboxReady{Version: 1, Epoch: epoch, ConversationID: conversation, SessionID: session, ProviderSessionID: "provider", HostReady: ready, ObservedAt: time.Now().Unix()}
	if err := mailboxWrite(root, conversationMailboxReadyFile, value, conversationMailboxReadyBytes); err != nil {
		t.Fatal(err)
	}
}

func requestArtifacts(t *testing.T, root *os.Root) []string {
	t.Helper()
	entries, err := mailboxEntries(root, conversationMailboxEntryLimit)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if _, ok := mailboxRequestID(entry.Name()); ok {
			names = append(names, entry.Name())
		}
	}
	return names
}

func TestHostMailboxExchangeRoundTripsOneRequest(t *testing.T) {
	dir, root := openTestMailbox(t)
	epoch, _ := newMailboxID()
	publishTestReady(t, root, "conv", "exec", epoch, true)
	client := conversationMailboxClient{dir: dir, conversation: "conv", session: "exec"}
	done := make(chan conversationMailboxResponse, 1)
	errs := make(chan error, 1)
	go func() {
		response, err := client.exchange(context.Background(), conversationMailboxCall{Method: "tools/list"})
		done <- response
		errs <- err
	}()
	var request conversationMailboxRequest
	deadline := time.Now().Add(5 * time.Second)
	for {
		names := requestArtifacts(t, root)
		if len(names) == 1 {
			data, err := mailboxRead(root, names[0], conversationMailboxRequestBytes)
			if err != nil {
				t.Fatal(err)
			}
			if err := decodeMailbox(data, &request); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("client never published a request")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var call conversationMailboxCall
	if err := decodeMailbox(request.Payload, &call); err != nil || call.Method != "tools/list" || request.Epoch != epoch {
		t.Fatalf("request %+v call %+v err %v", request, call, err)
	}
	reply := conversationMailboxResponse{Version: 1, ID: request.ID, Epoch: epoch, ConversationID: "conv", SessionID: "exec", Dispatched: true, Payload: json.RawMessage(`{"jsonrpc":"2.0","result":{"tools":[]}}`)}
	if err := mailboxWrite(root, mailboxResponseName(request.ID), reply, conversationMailboxResponseBytes); err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if response := <-done; !response.Dispatched || string(response.Payload) != string(reply.Payload) {
		t.Fatalf("response %+v", response)
	}
	if _, err := root.Lstat(mailboxResponseName(request.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("client left its claimed response: %v", err)
	}
}

func TestHostMailboxResponseLossIsUnconfirmedAndNeverRepublished(t *testing.T) {
	dir, root := openTestMailbox(t)
	epoch, _ := newMailboxID()
	publishTestReady(t, root, "conv", "exec", epoch, true)
	client := conversationMailboxClient{dir: dir, conversation: "conv", session: "exec"}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	_, err := client.exchange(ctx, conversationMailboxCall{Method: "tools/call", Params: json.RawMessage(`{"name":"session_prompt","arguments":{}}`)})
	if !errors.Is(err, errMailboxUnconfirmed) || !strings.Contains(err.Error(), "do not resend") {
		t.Fatalf("lost response must be unconfirmed: %v", err)
	}
	if names := requestArtifacts(t, root); len(names) != 1 {
		t.Fatalf("one call must publish exactly one request, got %v", names)
	}
}

// Callers receive dispatch state as structured MCP error data, and a broker
// restart after a claimed, unjournaled call is reported as unconfirmed rather
// than waited on forever.
func TestHostMailboxErrorsCarryStructuredDispatchState(t *testing.T) {
	previous := conversationMailboxReadyWait
	conversationMailboxReadyWait = 200 * time.Millisecond
	defer func() { conversationMailboxReadyWait = previous }()
	dir, root := openTestMailbox(t)
	client := conversationMailboxClient{dir: dir, conversation: "conv", session: "exec"}
	data := func(response localMCPResponse) map[string]any {
		t.Helper()
		if response.Error == nil {
			t.Fatal("expected an error")
		}
		value, _ := response.Error.Data.(map[string]any)
		return value
	}
	if d := data(forwardMailboxCall(context.Background(), client, localMCPRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "ping"})); d["dispatched"] != "no" || d["outcome"] != brokerOutcomeNotDispatched || d["retry_safe"] != true {
		t.Fatalf("unavailable host data %v", d)
	}
	// Broker error envelopes reach the caller through the real response path;
	// only an explicit, consistent not_dispatched is safe to resend.
	for _, tc := range []struct {
		name           string
		dispatched     bool
		outcome        string
		wantDispatched string
		wantOutcome    string
		wantRetrySafe  bool
	}{
		{"missing outcome", false, "", "unknown", brokerOutcomeUnconfirmed, false},
		{"missing outcome dispatched", true, "", "unknown", brokerOutcomeUnconfirmed, false},
		{"undispatched completed", false, brokerOutcomeCompleted, "unknown", brokerOutcomeUnconfirmed, false},
		{"dispatched not_dispatched", true, brokerOutcomeNotDispatched, "unknown", brokerOutcomeUnconfirmed, false},
		{"dispatched unconfirmed", true, brokerOutcomeUnconfirmed, "unknown", brokerOutcomeUnconfirmed, false},
		{"undispatched unconfirmed", false, brokerOutcomeUnconfirmed, "unknown", brokerOutcomeUnconfirmed, false},
		{"unknown outcome", false, "bogus", "unknown", brokerOutcomeUnconfirmed, false},
		{"explicit not_dispatched", false, brokerOutcomeNotDispatched, "no", brokerOutcomeNotDispatched, true},
		{"dispatched completed", true, brokerOutcomeCompleted, "yes", brokerOutcomeCompleted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, root := openTestMailbox(t)
			epoch, _ := newMailboxID()
			publishTestReady(t, root, "conv", "exec", epoch, true)
			client := conversationMailboxClient{dir: dir, conversation: "conv", session: "exec"}
			go func() {
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					entries, _ := mailboxEntries(root, conversationMailboxEntryLimit)
					for _, entry := range entries {
						id, ok := mailboxRequestID(entry.Name())
						if !ok {
							continue
						}
						reply := conversationMailboxResponse{Version: 1, ID: id, Epoch: epoch, ConversationID: "conv", SessionID: "exec", Dispatched: tc.dispatched, Outcome: tc.outcome, Error: "x"}
						_ = mailboxWrite(root, mailboxResponseName(id), reply, conversationMailboxResponseBytes)
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			}()
			response := forwardMailboxCall(context.Background(), client, localMCPRequest{JSONRPC: "2.0", ID: json.RawMessage(`3`), Method: "tools/call", Params: json.RawMessage(`{"name":"session_prompt","arguments":{}}`)})
			if response.Error == nil {
				t.Fatalf("expected an error, got %+v", response)
			}
			d, _ := response.Error.Data.(map[string]any)
			if d["dispatched"] != tc.wantDispatched || d["outcome"] != tc.wantOutcome || d["retry_safe"] != tc.wantRetrySafe {
				t.Fatalf("envelope dispatched=%v outcome=%q mapped to %v", tc.dispatched, tc.outcome, d)
			}
		})
	}
	epoch, _ := newMailboxID()
	publishTestReady(t, root, "conv", "exec", epoch, true)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		current := epoch
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			// A broker claims the request, then restarts under a new epoch
			// without a journal entry for this read.
			entries, _ := mailboxEntries(root, conversationMailboxEntryLimit)
			for _, entry := range entries {
				if _, ok := mailboxRequestID(entry.Name()); ok {
					_ = root.Remove(entry.Name())
					current, _ = newMailboxID()
				}
			}
			_ = mailboxWrite(root, conversationMailboxReadyFile, conversationMailboxReady{Version: 1, Epoch: current, ConversationID: "conv", SessionID: "exec", HostReady: true, ObservedAt: time.Now().Unix()}, conversationMailboxReadyBytes)
		}
	}()
	d := data(forwardMailboxCall(context.Background(), client, localMCPRequest{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/list"}))
	if d["dispatched"] != "unknown" || d["outcome"] != brokerOutcomeUnconfirmed || d["retry_safe"] != false {
		t.Fatalf("restarted broker data %v", d)
	}
}

func TestHostMailboxClientPublishesNothingWithoutMatchingLiveHost(t *testing.T) {
	previous := conversationMailboxReadyWait
	conversationMailboxReadyWait = 200 * time.Millisecond
	defer func() { conversationMailboxReadyWait = previous }()
	for name, publish := range map[string]func(root *os.Root, epoch string){
		"absent":          func(*os.Root, string) {},
		"other execution": func(root *os.Root, epoch string) { publishTestReady(t, root, "conv", "other", epoch, true) },
		"not ready":       func(root *os.Root, epoch string) { publishTestReady(t, root, "conv", "exec", epoch, false) },
		"stale": func(root *os.Root, epoch string) {
			value := conversationMailboxReady{Version: 1, Epoch: epoch, ConversationID: "conv", SessionID: "exec", HostReady: true, ObservedAt: time.Now().Add(-time.Minute).Unix()}
			_ = mailboxWrite(root, conversationMailboxReadyFile, value, conversationMailboxReadyBytes)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir, root := openTestMailbox(t)
			epoch, _ := newMailboxID()
			publish(root, epoch)
			client := conversationMailboxClient{dir: dir, conversation: "conv", session: "exec"}
			_, err := client.exchange(context.Background(), conversationMailboxCall{Method: "ping"})
			if err == nil || !strings.Contains(err.Error(), "nothing was sent") {
				t.Fatalf("unavailable host accepted: %v", err)
			}
			if names := requestArtifacts(t, root); len(names) != 0 {
				t.Fatalf("request published without a live host: %v", names)
			}
		})
	}
}

func TestHostMailboxEnvelopesRejectAuthorityFieldsAndBounds(t *testing.T) {
	var request conversationMailboxRequest
	for _, forged := range []string{
		`{"version":1,"request_id":"x","epoch":"y","created_at":1,"payload":{},"principal":"other-owner"}`,
		`{"version":1,"request_id":"x","epoch":"y","created_at":1,"payload":{}} {"second":true}`,
	} {
		if decodeMailbox([]byte(forged), &request) == nil {
			t.Fatalf("outer envelope accepted %s", forged)
		}
	}
	var call conversationMailboxCall
	if decodeMailbox([]byte(`{"method":"tools/call","params":{},"actor":"browser","grants":["terminal.start"]}`), &call) == nil {
		t.Fatal("call envelope accepted caller-selected authority")
	}
	_, root := openTestMailbox(t)
	if err := mailboxWrite(root, "big.json", strings.Repeat("x", 64), 16); err == nil {
		t.Fatal("oversized artifact published")
	}
	target := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(target, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, root := openTestMailbox(t)
	if err := os.Symlink(target, filepath.Join(dir, mailboxRequestName(strings.Repeat("a", 32)))); err != nil {
		t.Fatal(err)
	}
	if _, err := mailboxRead(root, mailboxRequestName(strings.Repeat("a", 32)), 1024); err == nil {
		t.Fatal("mailbox followed a symlink artifact")
	}
	for _, name := range []string{"request.ABC.json", "request." + strings.Repeat("a", 31) + ".json", "response." + strings.Repeat("a", 32) + ".json", "request." + strings.Repeat("a", 32) + ".json.tmp"} {
		if _, ok := mailboxRequestID(name); ok {
			t.Fatalf("accepted request artifact name %q", name)
		}
	}
}

func TestHostMailboxStdioClientRestoresCallerIDsAndSkipsNotifications(t *testing.T) {
	dir, root := openTestMailbox(t)
	epoch, _ := newMailboxID()
	publishTestReady(t, root, "conv", "exec", epoch, true)
	stop := make(chan struct{})
	served := make(chan string, 4)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			_ = mailboxWrite(root, conversationMailboxReadyFile, conversationMailboxReady{Version: 1, Epoch: epoch, ConversationID: "conv", SessionID: "exec", HostReady: true, ObservedAt: time.Now().Unix()}, conversationMailboxReadyBytes)
			entries, _ := mailboxEntries(root, conversationMailboxEntryLimit)
			for _, entry := range entries {
				id, ok := mailboxRequestID(entry.Name())
				if !ok {
					continue
				}
				data, err := mailboxRead(root, entry.Name(), conversationMailboxRequestBytes)
				_ = root.Remove(entry.Name())
				var request conversationMailboxRequest
				var call conversationMailboxCall
				if err != nil || decodeMailbox(data, &request) != nil || decodeMailbox(request.Payload, &call) != nil {
					continue
				}
				served <- call.Method
				payload, _ := json.Marshal(localMCPResponse{JSONRPC: "2.0", Result: map[string]any{"method": call.Method}})
				_ = mailboxWrite(root, mailboxResponseName(id), conversationMailboxResponse{Version: 1, ID: id, Epoch: epoch, ConversationID: "conv", SessionID: "exec", Dispatched: true, Payload: payload}, conversationMailboxResponseBytes)
			}
		}
	}()
	defer close(stop)
	// Like the direct stdio server, stdin EOF cancels waits, so the provider
	// keeps stdin open until it has its reply.
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	finished := make(chan error, 1)
	go func() {
		finished <- serveConversationMailboxClient(context.Background(), inputReader, outputWriter, dir, "conv", "exec")
	}()
	if _, err := io.WriteString(inputWriter, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"+`{"jsonrpc":"2.0","id":"call-7","method":"tools/list"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(outputReader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	_ = inputWriter.Close()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID     string         `json:"id"`
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &response); err != nil || response.ID != "call-7" || response.Result["method"] != "tools/list" {
		t.Fatalf("stdio response %q: %v", line, err)
	}
	if method := <-served; method != "tools/list" || len(served) != 0 {
		t.Fatalf("forwarded %q plus %d more; notifications must stay local", method, len(served))
	}
	var output strings.Builder
	if err := serveConversationMailboxClient(context.Background(), strings.NewReader(""), &output, "relative/mailbox", "conv", "exec"); err == nil {
		t.Fatal("relative mailbox accepted")
	}
}
