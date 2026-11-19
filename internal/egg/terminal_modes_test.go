package egg

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"

	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"testing"
	"time"
)

// Real PTY reads must remain raw regardless of presentation or read boundaries.
func TestNativeTerminalReadPTYKeepsRawOffsets(t *testing.T) {
	master, slave := blockedInputPTY(t)
	sess := &Session{ID: "mouse", nonblockInput: true, ptmx: master, replay: newReplayBuffer(""), vtermCh: make(chan vtermMsg, 16)}
	done := make(chan struct{})
	go func() { (&Server{}).readPTY(sess); close(done) }()
	defer func() {
		_ = slave.Close()
		_ = master.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("PTY reader did not join")
		}
	}()
	chunks := [][]byte{[]byte("before\x1b[?1000h"), []byte("\x1b[?10"), []byte("06hafter\x1b[?1000l")}
	var want []byte
	for _, chunk := range chunks {
		before := sess.replay.WritePosition()
		if _, err := slave.Write(chunk); err != nil {
			t.Fatal(err)
		}
		cursor := sess.replay.Register(before)
		_, wait := sess.replay.ReadAfter(cursor)
		if wait != nil {
			select {
			case <-wait:
			case <-time.After(time.Second):
				t.Fatal("PTY output timeout")
			}
		}
		sess.replay.Unregister(cursor)
		want = append(want, chunk...)
		if got := sess.replay.Bytes(); !bytes.Equal(got, want) {
			t.Fatalf("raw PTY bytes lost: got %q want %q", got, want)
		}
		if got := sess.replay.WritePosition(); got != int64(len(want)) {
			t.Fatalf("raw offset = %d want %d", got, len(want))
		}
	}
}

func TestNativeTerminalBrowserStreaming(t *testing.T) {
	cases := []struct{ raw, browser string }{
		{"\x9b?1000hTAIL", "\x1b]7771;1\aTAIL"},
		{"\x1b]0;✳ \x1b[?1000h\aTEXT", "\x1b]0;✳ \x1b[?1000h\aTEXT"},
		{"before\x1b[?1000hafter", "before\x1b]7771;1\aafter"},
		{"\x1b[?1000;1006;1049hALT\x1b[?1000;1006l", "\x1b[?1049h\x1b]7771;1\aALT\x1b[?1000;1006l"},
		{"\x1b[?01002h\x1b[?1003l", "\x1b]7771;1\a\x1b[?1003l"},
		{"\x1b[?1000\nh", "\n\x1b]7771;1\a"},
		{"\x1b]0;literal \x1b[?1000h\aTEXT", "\x1b]0;literal \x1b[?1000h\aTEXT"},
		{"\x1bPpayload \x1b[?1006h\x1b\\TEXT", "\x1bPpayload \x1b[?1006h\x1b\\TEXT"},
		{"é🙂\x1b[31mtext\x1b[m", "é🙂\x1b[31mtext\x1b[m"},
	}
	for _, tc := range cases {
		for split := 0; split <= len(tc.raw); split++ {
			var p browserPresentation
			got := append(p.write([]byte(tc.raw[:split])), p.write([]byte(tc.raw[split:]))...)
			got = append(got, p.finish()...)
			if string(got) != tc.browser {
				t.Fatalf("split %d raw %q: got %q want %q", split, tc.raw, got, tc.browser)
			}
		}
		var p browserPresentation
		var got []byte
		for _, b := range []byte(tc.raw) {
			got = append(got, p.write([]byte{b})...)
		}
		if string(got) != tc.browser {
			t.Fatalf("one-byte reads: got %q want %q", got, tc.browser)
		}
	}
	for _, mode := range presentationModes {
		if !mouseMode(mode) {
			continue
		}
		var p browserPresentation
		if got := p.write(fmt.Appendf(nil, "\x1b[?%dh", mode)); !bytes.Equal(got, mouseTrackingMarker) {
			t.Fatalf("browser captured mouse mode %d: %q", mode, got)
		}
	}
	var p browserPresentation
	for range 10000 {
		p.write([]byte("\x1b[?" + strings.Repeat("0;", 300)))
	}
	if len(p.decoder.pending) > maxTerminalEscape || cap(p.decoder.pending) > maxTerminalEscape {
		t.Fatal("escape buffering is unbounded")
	}
	if got := string(p.write([]byte("1000hTAIL"))); got != "TAIL" {
		t.Fatalf("oversized enable leaked: %q", got)
	}
}

func TestNativeTerminalReplayModesAndRawTrimOffsets(t *testing.T) {
	r := newReplayBuffer("claude")
	raw := []byte("\x1b[?1000h\x1b[?1006h\x1b[?1049hALT\x1b[?1049l\x1b[?1000l\x1b[?1006lNORMAL")
	for _, b := range raw {
		r.Write([]byte{b})
	}
	snapshot, offset := r.AttachmentSnapshot()
	if offset != int64(len(raw)) || !bytes.Contains(snapshot, raw) {
		t.Fatal("native replay lost bytes or used projected offsets")
	}
	var modes terminalModes
	modes.write(snapshot)
	for i, mode := range presentationModes {
		if (mouseMode(mode) || alternateMode(mode)) && modes.states[i].enabled {
			t.Fatalf("old enable overrode disable for %d", mode)
		}
	}
	r.Write(bytes.Repeat([]byte("line\r\n"), maxReplaySize/6+1))
	snapshot, offset = r.AttachmentSnapshot()
	stats := r.Stats()
	if offset != stats.Written || stats.Trimmed+int64(stats.BufSize) != stats.Written || stats.Trimmed == 0 {
		t.Fatalf("raw trim offsets: %+v offset=%d", stats, offset)
	}
	var restored terminalModes
	restored.write(snapshot)
	for i, mode := range presentationModes {
		if mouseMode(mode) && restored.states[i].enabled {
			t.Fatal("trim restored stale mouse enable")
		}
	}
	cursor := r.Register(offset)
	defer r.Unregister(cursor)
	tail := []byte("\x1b[?1000hTAIL")
	r.Write(tail)
	data, _ := r.ReadAfter(cursor)
	if !bytes.Equal(data, tail) || cursor.offset != offset+int64(len(tail)) {
		t.Fatalf("raw cursor after trim: %q at %d", data, cursor.offset)
	}
}

// A terminal drops its scroll region when resized; the raw replay must too,
// or a fresh terminal scrolls the taller redraw inside the old region.
func TestNativeTerminalReplayResetsScrollRegionAtResize(t *testing.T) {
	r := newReplayBuffer("")
	r.Resized()
	r.Write([]byte("\x1b[1;24rOLD\x1b[?10"))
	r.Resized()
	r.Resized()
	r.Write([]byte("49hNEW"))
	snapshot, offset := r.AttachmentSnapshot()
	if want := "\x1b[1;24rOLD\x1b7\x1b[r\x1b8\x1b[?1049hNEW"; string(snapshot) != want || offset != 21 {
		t.Fatalf("snapshot %q at %d, want %q", snapshot, offset, want)
	}
	r.Write([]byte("\x1b[2;5r\x1b[r"))
	r.Resized()
	if next, _ := r.AttachmentSnapshot(); bytes.Count(next, marginReset) != 1 {
		t.Fatalf("a region the provider already cleared was reset again: %q", next)
	}
	r.Write(bytes.Repeat([]byte("line\r\n"), maxReplaySize/6+1))
	if trimmed, _ := r.AttachmentSnapshot(); bytes.Contains(trimmed, marginReset) {
		t.Fatal("a reset outlived the bytes it followed")
	}
}

func TestNativeTerminalSnapshotContinuesSplitCSI(t *testing.T) {
	r := newReplayBuffer("")
	r.Write([]byte("HISTORY\x1b[?1000h\x1b[?100"))
	snapshot, offset := r.AttachmentSnapshot()
	if !bytes.HasSuffix(snapshot, []byte("\x1b[?100")) {
		t.Fatal("restoration corrupted partial CSI")
	}
	var browser browserPresentation
	projected := browser.write(snapshot)
	cursor := r.Register(offset)
	defer r.Unregister(cursor)
	r.Write([]byte("0lAFTER"))
	live, _ := r.ReadAfter(cursor)
	projected = append(projected, browser.write(live)...)
	if !bytes.HasSuffix(projected, []byte("\x1b[?1000lAFTER")) {
		t.Fatalf("browser snapshot/live continuation: %q", projected)
	}
	var modes terminalModes
	modes.write(snapshot)
	modes.write(live)
	for i, mode := range presentationModes {
		if mode == 1000 && modes.states[i].enabled {
			t.Fatal("split disable did not restore latest state")
		}
	}
}

// A legacy Status has no capability. Record the actual wire option rather than
// trusting that an old protobuf server would silently ignore an unknown field.
type presentationPeer struct {
	pb.UnimplementedEggServer
	server   *Server
	capable  bool
	attached chan bool
}

func (p *presentationPeer) Status(context.Context, *pb.StatusRequest) (*pb.StatusResponse, error) {
	return &pb.StatusResponse{NativeTerminal: p.capable}, nil
}
func (p *presentationPeer) Session(stream grpc.BidiStreamingServer[pb.SessionMsg, pb.SessionMsg]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	p.attached <- first.AttachOptions.GetNativeTerminal()
	return p.server.Session(&firstSessionStream{Egg_SessionServer: stream, first: first})
}

type firstSessionStream struct {
	pb.Egg_SessionServer
	first *pb.SessionMsg
}

func (s *firstSessionStream) Recv() (*pb.SessionMsg, error) {
	if s.first != nil {
		first := s.first
		s.first = nil
		return first, nil
	}
	return s.Egg_SessionServer.Recv()
}

func TestNativeTerminalEggNegotiatesOldAndNewPeers(t *testing.T) {
	for _, capable := range []bool{false, true} {
		t.Run(fmt.Sprint(capable), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			replay := newReplayBuffer("")
			raw := []byte("\x1b[?1000h\x1b[?1006hREADY")
			replay.Write(raw)
			server := &Server{session: &Session{ID: "mouse", replay: replay, done: make(chan struct{})}}
			peer := &presentationPeer{server: server, capable: capable, attached: make(chan bool, 2)}
			listener := bufconn.Listen(1024 * 1024)
			rpc := grpc.NewServer()
			pb.RegisterEggServer(rpc, peer)
			done := make(chan struct{})
			go func() { defer close(done); _ = rpc.Serve(listener) }()
			defer func() { rpc.Stop(); _ = listener.Close(); <-done }()
			conn, err := grpc.NewClient("passthrough:///fixture", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			client := &Client{conn: conn, client: pb.NewEggClient(conn)}
			stream, err := client.AttachSessionWithOptions(ctx, "mouse", AttachOptions{ReadOnly: true, NativeTerminal: true})
			if err != nil {
				t.Fatal(err)
			}
			if got := <-peer.attached; got != capable {
				t.Fatalf("option sent without discovery: %t", got)
			}
			first, err := stream.Recv()
			if err != nil {
				t.Fatal(err)
			}
			var browser browserPresentation
			expected, _ := replay.AttachmentSnapshot()
			if !capable {
				expected = browser.write(expected)
			}
			if !bytes.Equal(first.GetOutput(), expected) {
				t.Fatalf("negotiated output: got %q want %q", first.GetOutput(), expected)
			}
			// An old client on a new server supplies no options and stays browser-compatible.
			legacy, err := client.client.Session(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := legacy.Send(&pb.SessionMsg{SessionId: "mouse", Payload: &pb.SessionMsg_Attach{Attach: true}}); err != nil {
				t.Fatal(err)
			}
			if <-peer.attached {
				t.Fatal("legacy client opted into native output")
			}
			message, err := legacy.Recv()
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(message.GetOutput(), []byte("\x1b[?1000h")) || !bytes.Contains(message.GetOutput(), mouseTrackingMarker) {
				t.Fatalf("legacy snapshot regression: %q", message.GetOutput())
			}
			// Both streams share raw positions, even though their output byte lengths differ.
			liveRaw := []byte("\x1b[?1002hLIVE")
			replay.Write(liveRaw)
			nativeLive, err := stream.Recv()
			if err != nil {
				t.Fatal(err)
			}
			legacyLive, err := legacy.Recv()
			if err != nil {
				t.Fatal(err)
			}
			if capable && !bytes.Equal(nativeLive.GetOutput(), liveRaw) {
				t.Fatalf("native live bytes lost: %q", nativeLive.GetOutput())
			}
			if !bytes.Equal(legacyLive.GetOutput(), []byte("\x1b]7771;1\aLIVE")) {
				t.Fatalf("legacy live output: %q", legacyLive.GetOutput())
			}
			if replay.WritePosition() != int64(len(raw)+len(liveRaw)) {
				t.Fatal("filter changed canonical replay offset")
			}
		})
	}
}

func TestNativeTerminalResetClearsTrackedModes(t *testing.T) {
	var modes terminalModes
	modes.write([]byte("\x1b[?1000h\x1b[?1049h\x1bc"))
	for i, mode := range presentationModes {
		if mode != 25 && modes.states[i].enabled {
			t.Fatalf("RIS retained mode %d", mode)
		}
	}
}

func TestNativeTerminalReplayPreservesAlternateGridAndTrimmedEnables(t *testing.T) {
	r := newReplayBuffer("")
	r.Write([]byte("NORMAL\x1b[?1000h\x1b[?1049h\x1b[2J\x1b[HALT-CONTENT"))
	snapshot, _ := r.AttachmentSnapshot()
	v := NewVTerm(30, 5)
	defer closeTerminalForTest(t, v)
	writeTerminalForTest(t, v, snapshot)
	if !strings.Contains(v.ScreenText(), "ALT-CONTENT") {
		t.Fatalf("mode restoration erased alternate grid: %q", v.ScreenText())
	}
	// Drop the original enable from history; latest state still restores native
	// mouse and alternate screen without adding synthetic bytes to raw positions.
	r.Write(bytes.Repeat([]byte("\x1b[HFRAME\x1b[?2026l"), maxReplaySize/16+1))
	snapshot, offset := r.AttachmentSnapshot()
	if r.Stats().Trimmed == 0 || offset != r.Stats().Written {
		t.Fatal("fixture did not trim canonical replay")
	}
	var restored terminalModes
	restored.write(snapshot)
	for i, mode := range presentationModes {
		if (mode == 1000 || mode == 1049) && !restored.states[i].enabled {
			t.Fatalf("trim lost enable for %d", mode)
		}
	}
	var browser browserPresentation
	projected := browser.write(snapshot)
	if bytes.Contains(projected, []byte("\x1b[?1000h")) || !bytes.Contains(projected, mouseTrackingMarker) {
		t.Fatal("trimmed browser snapshot lost selection contract")
	}
}
