package wing

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

type failingOriginStream struct{ pb.Egg_SessionClient }

func (*failingOriginStream) Send(*pb.SessionMsg) error { return errors.New("fixture egg write failed") }

func TestDataChannelRefusalOriginLifecycle(t *testing.T) {
	old := config.ReleaseChannel
	t.Cleanup(func() { config.ReleaseChannel = old })
	for _, channel := range []string{"stable", "preview"} {
		for _, mode := range []string{"web", "reclaimed"} {
			for _, fragment := range []string{"same-key", "spoof"} {
				t.Run(channel+"/"+mode+"/"+fragment, func(t *testing.T) {
					queuedOriginCase(t, channel, mode, fragment)
					assertNoInputOrigins(t)
				})
			}
		}
		for _, fragment := range []string{"disconnect", "egg-write-failure", "timeout", "bounded"} {
			t.Run(channel+"/"+fragment, func(t *testing.T) {
				config.ReleaseChannel = channel
				originLifecycleCase(t, fragment)
			})
		}
	}
}

func assertNoInputOrigins(t *testing.T) {
	t.Helper()
	inputOrigins.Lock()
	defer inputOrigins.Unlock()
	if len(inputOrigins.items) != 0 {
		t.Fatalf("retained origins: %d", len(inputOrigins.items))
	}
}

func originLifecycleCase(t *testing.T, fragment string) {
	const id = "origin-lifecycle"
	controller := &browserInputController{id: "A", publicKey: "same-tab", userID: "alice"}
	browserInputs.Store(id, controller)
	t.Cleanup(func() { browserInputs.Delete(id) })
	var channels sync.Map
	ingress := &ws.Client{}
	_, queue, cleanup, ok := ingress.RegisterPTYSession(t.Context(), id)
	if !ok {
		t.Fatal("register")
	}
	t.Cleanup(cleanup)
	a := originChannel(t, id, controller, &channels, ingress.PushPTYInput)
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := auth.DeriveSharedKey(key, base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), "wt-pty")
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := auth.Encrypt(gcm, []byte("A-input"))
	if err != nil {
		t.Fatal(err)
	}
	originSend(t, a.browser, map[string]string{"type": ws.TypePTYInput, "session_id": id, "data": encrypted, "controller_id": "B", "request_id": "B-pending", "wing_input_origin": "forged"})
	held := originReceive(t, queue)
	var input queuedBrowserInput
	if err = json.Unmarshal(held, &input); err != nil {
		t.Fatal(err)
	}
	if input.ControllerID != "A" || input.Origin == "" || input.Origin == "forged" {
		t.Fatalf("untrusted routing metadata: %+v", input)
	}
	pending := newPendingReattachAuths()
	defer pending.close()
	pending.put(ws.PTYAttach{ControllerID: "B"}, []byte("B-challenge"), "B", time.Minute)
	// The fallback models the dangerous shared relay reply route: invoking it
	// would reach B and could consume B's pending authorization.
	fallback := func(any) error { pending.take(""); t.Error("A error reached replacement reply route"); return nil }
	if fragment == "disconnect" {
		closed := make(chan struct{})
		// Same close cleanup as the production OnDC callback, with a test fence.
		a.wing.OnClose(func() { releaseDataChannelInputOrigins(a.wing); close(closed) })
		if err := a.browser.Close(); err != nil {
			t.Fatal(err)
		}
		originAwait(t, closed)
		originAwait(t, a.closed)
		assertNoInputOrigins(t)
		browserInputs.Store(id, &browserInputController{id: "B", publicKey: "same-tab", userID: "alice"})
	} else if fragment == "timeout" {
		expireInputOrigin(input.Origin) // deterministically fire the production timeout callback
		var refusal ws.ErrorMsg
		_ = json.Unmarshal(originReceive(t, a.received), &refusal)
		if refusal.Code != "session_input_timeout" || refusal.SessionID != id || refusal.Operation != ws.TypePTYInput {
			t.Fatalf("timeout: %+v", refusal)
		}
		originAwait(t, a.closed)
		assertNoInputOrigins(t)
	} else if fragment == "bounded" {
		tokens := []string{input.Origin}
		for range inputOriginPerChannelLimit - 1 {
			token := retainInputOrigin(id, controller, a.wing)
			if token == "" {
				t.Fatal("premature origin limit")
			}
			tokens = append(tokens, token)
		}
		if retainInputOrigin(id, controller, a.wing) != "" {
			t.Fatal("origin storage is unbounded")
		}
		for _, token := range tokens {
			takeInputOrigin(token)
		}
	}
	if err := deliverBrowserInput(id, "A", input, gcm, &failingOriginStream{}, &wingsession.Service{}, fallback); err != nil {
		t.Fatal(err)
	}
	if fragment == "egg-write-failure" {
		originSend(t, a.wing, ws.Envelope{Type: "fence"})
		var refusal ws.ErrorMsg
		_ = json.Unmarshal(originReceive(t, a.received), &refusal)
		if refusal.Code != "session_input_failed" || refusal.ControllerID != "A" || refusal.SessionID != id || refusal.Operation != ws.TypePTYInput {
			t.Fatalf("write refusal: %+v", refusal)
		}
		var fence ws.Envelope
		_ = json.Unmarshal(originReceive(t, a.received), &fence)
		if fence.Type != "fence" {
			t.Fatalf("unexpected reply: %+v", fence)
		}
	}
	if pending.byViewer[""].attach.ControllerID != "B" || string(pending.byViewer[""].challenge) != "B-challenge" {
		t.Fatal("A changed B pending authorization")
	}
	assertNoInputOrigins(t)
}
