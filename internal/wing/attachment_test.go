package wing

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
)

func TestWingAttachmentReadOnlyDetachAndReattach(t *testing.T) {
	state, home, work := localOnlyState(t)
	runtime := startLocalOnlyRuntime(t, home, work, "", EntryOptions{LocalOnly: true}, nil)
	client := localOnlyClient(t, state, controlsocket.Hello{})
	result := localOnlyCall(t, client, "agent_start", map[string]any{"agent": "claude", "cwd": work, "label": "weekend"})
	id := result["session"].(string)
	if err := eggclient.WriteSessionName(runtime.service.Config.Dir+"/eggs/"+id, "weekend"); err != nil {
		t.Fatal(err)
	}
	runtime.fixture.mu.Lock()
	e := runtime.fixture.eggs[id]
	runtime.fixture.mu.Unlock()
	// Drain the wing's observer; it is independent of human attachments.
	for len(e.attached) > 0 {
		<-e.attached
	}
	stream, _, err := controlsocket.DialAttachment(t.Context(), state, controlsocket.Hello{Attach: &controlsocket.Attachment{Session: "weekend", ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	options := <-e.attached
	if !options.ReadOnly || options.Claim || options.Takeover || options.Rows != 0 || options.Cols != 0 {
		t.Fatalf("observer claimed input: %+v", options)
	}
	if err := stream.Send(&pb.SessionMsg{SessionId: id, Payload: &pb.SessionMsg_Input{Input: []byte("forbidden")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("read-only write accepted: %v", err)
	}
	select {
	case input := <-e.input:
		t.Fatalf("read-only forwarded %q", input)
	default:
	}
	var output bytes.Buffer
	detached, err := localmcp.AttachWingIO(t.Context(), state, "", "weekend", bytes.NewReader([]byte{2, 'q'}), &output, egg.AttachOptions{ReadOnly: true})
	if err != nil || !detached {
		t.Fatalf("detach: %t %v", detached, err)
	}
	<-e.attached
	if len(e.input) > 0 {
		t.Fatal("observer changed input")
	}
	select {
	case <-e.killed:
		t.Fatal("detach killed parent")
	default:
	}
	reattached, _, err := controlsocket.DialAttachment(t.Context(), state, controlsocket.Hello{Attach: &controlsocket.Attachment{Session: id, ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer reattached.Close()
	msg, err := reattached.Recv()
	if err != nil || msg.SessionId != id || msg.AttachmentInfo != nil {
		t.Fatalf("reattachment changed session or exposed a capability: %v %v", msg, err)
	}
	if _, _, err := controlsocket.DialAttachment(t.Context(), state, controlsocket.Hello{Client: "foreign", Attach: &controlsocket.Attachment{Session: id, ReadOnly: true}}); err == nil {
		t.Fatal("foreign client attached")
	}
}
