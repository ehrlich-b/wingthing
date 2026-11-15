package wing

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
	"github.com/ehrlich-b/wingthing/internal/ws"
	pion "github.com/pion/webrtc/v4"
)

type originTestChannel struct {
	browser, wing *pion.DataChannel
	received      chan []byte
	closed        chan struct{}
}

// Host-only SCTP/DTLS transport: assertions read the browser endpoint, rather
// than treating SendText's return value as proof that a refusal arrived.
func originChannel(t *testing.T, id string, binding *browserInputController, channels *sync.Map, push func(string, []byte) *ws.ErrorMsg) *originTestChannel {
	t.Helper()
	pair := &originTestChannel{received: make(chan []byte, 16), closed: make(chan struct{})}
	manager := webrtcpkg.NewPeerManager(nil)
	t.Cleanup(manager.Close)
	ready := make(chan struct{})
	manager.OnDC(func(_, _ string, _ webrtcpkg.PeerIdentity, dc *pion.DataChannel) {
		pair.wing = dc
		channels.Store(id, dc)
		dc.OnMessage(browserDataChannelInputHandler(channels, id, dc, binding, push))
		close(ready)
	})
	pc, err := pion.NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	pair.browser, err = pc.CreateDataChannel("pty:"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan struct{})
	pair.browser.OnOpen(func() { close(opened) })
	pair.browser.OnClose(func() { close(pair.closed) })
	pair.browser.OnMessage(func(msg pion.DataChannelMessage) { pair.received <- append([]byte(nil), msg.Data...) })
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := pion.GatheringCompletePromise(pc)
	if err = pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	originAwait(t, gathered)
	answer, err := manager.HandleOffer("origin-fixture", "alice", "", "owner", nil, pc.LocalDescription().SDP)
	if err != nil {
		t.Fatal(err)
	}
	if err = pc.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatal(err)
	}
	originAwait(t, opened)
	originAwait(t, ready)
	return pair
}

func originAwait(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("transport fence timed out")
	}
}

func originReceive[T ~[]byte](t *testing.T, incoming <-chan T) []byte {
	t.Helper()
	select {
	case data := <-incoming:
		return data
	case <-time.After(5 * time.Second):
		t.Fatal("input/recipient fence timed out")
		return nil
	}
}

func originSend(t *testing.T, dc *pion.DataChannel, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = dc.SendText(string(data)); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedDataChannelInputRefusalOrigin(t *testing.T) {
	old := config.ReleaseChannel
	t.Cleanup(func() { config.ReleaseChannel = old })
	for _, channel := range []string{"stable", "preview"} {
		for _, mode := range []string{"web", "reclaimed"} {
			t.Run(channel+"/"+mode, func(t *testing.T) { queuedOriginCase(t, channel, mode, "") })
		}
	}
}

// Use the same browser-shaped message and production ingress/queue on the
// historical candidate. No routing IDs are prefilled by this regression.
func queuedOriginCase(t *testing.T, channel, mode, lifecycle string) {
	config.ReleaseChannel = channel
	f := newSessionTransportFixture(t)
	pub := func(key *ecdh.PrivateKey) string { return base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()) }
	id := "queued-dc-origin"
	if mode == "web" {
		f.send(ws.PTYStart{Type: ws.TypePTYStart, SessionID: id, ControllerID: "A", Agent: "claude", CWD: f.work, UserID: "alice", OrgRole: "owner", PublicKey: pub(f.browserKey)})
	} else {
		result := callSessionMCP(t, f.mcp("http", "alice"), "terminal_start", map[string]any{"command": []string{"fixture-command"}, "cwd": f.work})
		id = result["session"].(string)
		f.send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: id, ControllerID: "A", UserID: "alice", OrgRole: "owner", PublicKey: pub(f.browserKey), Takeover: true})
	}
	f.next(ws.TypePTYStarted)
	binding, ok := browserDataChannelBinding(id, pub(f.browserKey), "alice")
	if !ok {
		t.Fatal("A binding missing")
	}
	// A real ws queue provides a deterministic consumer fence: hold its admitted
	// frame while B's attach goes through the actual session input loop.
	ingress := &ws.Client{}
	_, queue, cleanup, ok := ingress.RegisterPTYSession(f.ctx, id)
	if !ok {
		t.Fatal("register ingress queue")
	}
	t.Cleanup(cleanup)
	var channels sync.Map
	a := originChannel(t, id, binding, &channels, ingress.PushPTYInput)
	encrypted, err := auth.Encrypt(f.ptyKey, []byte("A-input"))
	if err != nil {
		t.Fatal(err)
	}
	message := map[string]string{"type": ws.TypePTYInput, "session_id": id, "data": encrypted}
	if lifecycle == "spoof" {
		message["controller_id"] = "B"
		message["request_id"] = "B-pending"
		message["wing_input_origin"] = "forged"
	}
	originSend(t, a.browser, message)
	held := originReceive(t, queue)
	replacement, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle == "same-key" {
		replacement = f.browserKey
	}
	replacementKey, err := auth.DeriveSharedKey(replacement, pub(f.wingKey), "wt-pty")
	if err != nil {
		t.Fatal(err)
	}
	f.send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: id, ControllerID: "B", UserID: "alice", OrgRole: "owner", PublicKey: pub(replacement), Takeover: true})
	f.next(ws.TypePTYStarted)
	bBinding, ok := browserDataChannelBinding(id, pub(replacement), "alice")
	if !ok {
		t.Fatal("B binding missing")
	}
	b := originChannel(t, id, bBinding, &channels, f.client.PushPTYInput)
	if denied := f.client.PushPTYInput(id, held); denied != nil {
		t.Fatalf("release queue: %+v", denied)
	}
	valid, err := auth.Encrypt(replacementKey, []byte("B-input"))
	if err != nil {
		t.Fatal(err)
	}
	originSend(t, b.browser, map[string]string{"type": ws.TypePTYInput, "session_id": id, "data": valid})
	if got := originReceive(t, f.egg(id).input); string(got) != "B-input" {
		t.Fatalf("A input reached egg: %q", got)
	}
	// B reaching the egg fences the single session consumer after A's result.
	// SCTP ordered messages then fence both final error recipients.
	originSend(t, b.wing, ws.Envelope{Type: "fence"})
	var bReply ws.Envelope
	_ = json.Unmarshal(originReceive(t, b.received), &bReply)
	if bReply.Type != "fence" {
		t.Fatalf("B received A's refusal: %+v", bReply)
	}
	{
		originSend(t, a.wing, ws.Envelope{Type: "fence"})
		var refusal ws.ErrorMsg
		_ = json.Unmarshal(originReceive(t, a.received), &refusal)
		if refusal.Type != ws.TypeError || refusal.SessionID != id || refusal.Operation != ws.TypePTYInput || refusal.Code != "controller_replaced" || refusal.ControllerID != "A" {
			t.Fatalf("A did not receive its typed input refusal: %+v", refusal)
		}
		var fence ws.Envelope
		_ = json.Unmarshal(originReceive(t, a.received), &fence)
		if fence.Type != "fence" {
			t.Fatalf("unexpected A reply: %+v", fence)
		}
	}
	// Session relay writes now target B. No A error may escape onto that route.
	if err := f.client.PTYWriter(f.ctx)(ws.Envelope{Type: "recipient-fence"}); err != nil {
		t.Fatal(err)
	}
	for {
		raw := originReceive(t, f.replies)
		var reply ws.ErrorMsg
		_ = json.Unmarshal(raw, &reply)
		if reply.Type == "recipient-fence" {
			break
		}
		if reply.Operation == ws.TypePTYInput {
			t.Fatalf("A refusal escaped to B's socket: %+v", reply)
		}
	}
	t.Log("A refused on its originating transport; B alone delivered; recipient fences passed")
}
