package controlsocket

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/control"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"google.golang.org/protobuf/encoding/protojson"
)

// Attachment upgrades an authenticated wing connection to an ordered terminal
// stream. No egg socket, attachment token or host authority crosses this API.
type Attachment struct {
	Session  string `json:"session"`
	ReadOnly bool   `json:"read_only,omitempty"`
	Takeover bool   `json:"takeover,omitempty"`
	Rows     uint32 `json:"rows,omitempty"`
	Cols     uint32 `json:"cols,omitempty"`
}

type AttachHandler func(context.Context, *SessionStream) error
type AttachBind func(context.Context, Hello) (Welcome, AttachHandler, error)

type streamFrame struct {
	Message json.RawMessage `json:"message,omitempty"`
	End     bool            `json:"end,omitempty"`
	Error   string          `json:"error,omitempty"`
}

type SessionStream struct {
	conn    net.Conn
	scanner *bufio.Scanner
	encoder *json.Encoder
	mu      sync.Mutex
	stop    func() bool
}

func (s *SessionStream) frame(f streamFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return s.encoder.Encode(f)
}
func (s *SessionStream) Send(msg *pb.SessionMsg) error {
	data, err := protojson.Marshal(msg)
	if err != nil {
		return err
	}
	if len(data) > maxEnvelope-128 {
		return errors.New("terminal frame exceeds limit")
	}
	return s.frame(streamFrame{Message: data})
}
func (s *SessionStream) Recv() (*pb.SessionMsg, error) {
	if !s.scanner.Scan() {
		if err := s.scanner.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	var frame streamFrame
	if err := strictJSON(s.scanner.Bytes(), &frame); err != nil {
		return nil, err
	}
	if frame.Error != "" {
		return nil, errors.New(frame.Error)
	}
	if frame.End {
		return nil, io.EOF
	}
	msg := new(pb.SessionMsg)
	if err := protojson.Unmarshal(frame.Message, msg); err != nil {
		return nil, err
	}
	return msg, nil
}
func (s *SessionStream) Close() error {
	if s.stop != nil {
		s.stop()
	}
	return s.conn.Close()
}

func serveAttachment(ctx context.Context, conn net.Conn, scanner *bufio.Scanner, encoder *json.Encoder, wingID string, hello Hello, binders []AttachBind) {
	welcome := Welcome{Version: control.ContractVersion, WingID: wingID}
	var handler AttachHandler
	var err error
	if len(binders) != 1 {
		err = errors.New("this wing does not support terminal streaming")
	} else {
		welcome, handler, err = binders[0](ctx, hello)
		welcome.Version, welcome.WingID = control.ContractVersion, wingID
	}
	if err != nil {
		welcome.Error = err.Error()
	}
	if encoder.Encode(welcome) != nil || err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	stream := &SessionStream{conn: conn, scanner: scanner, encoder: encoder}
	err = handler(ctx, stream)
	frame := streamFrame{End: true}
	if err != nil && !errors.Is(err, context.Canceled) {
		frame.Error = err.Error()
	}
	_ = stream.frame(frame)
}

func DialAttachment(ctx context.Context, dir string, hello Hello) (*SessionStream, Welcome, error) {
	path, err := Path(dir)
	if err == nil {
		var stream *SessionStream
		var welcome Welcome
		stream, welcome, err = DialAttachmentPath(ctx, path, hello)
		if err == nil {
			return stream, welcome, nil
		}
	}
	return nil, Welcome{}, fmt.Errorf("no local wing for WINGTHING_DIR=%s: %w; start one with wt wing start --local-only or wt roost start", dir, err)
}

func DialAttachmentPath(ctx context.Context, path string, hello Hello) (*SessionStream, Welcome, error) {
	if hello.Attach == nil {
		return nil, Welcome{}, errors.New("attachment is required")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, Welcome{}, err
	}
	stream := &SessionStream{conn: conn, scanner: scan(conn), encoder: json.NewEncoder(conn)}
	stream.stop = context.AfterFunc(ctx, func() { _ = conn.Close() })
	ok := false
	defer func() {
		if !ok {
			_ = stream.Close()
		}
	}()
	if err := checkPeer(conn.(*net.UnixConn)); err != nil {
		return nil, Welcome{}, err
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	hello.Version = control.ContractVersion
	if err := stream.encoder.Encode(hello); err != nil {
		return nil, Welcome{}, err
	}
	if !stream.scanner.Scan() {
		return nil, Welcome{}, errors.New("wing attachment handshake closed")
	}
	var welcome Welcome
	if err := strictJSON(stream.scanner.Bytes(), &welcome); err != nil {
		return nil, welcome, err
	}
	if welcome.Error != "" {
		return nil, welcome, errors.New(welcome.Error)
	}
	if welcome.Version != control.ContractVersion || welcome.WingID == "" || hello.WingID != "" && hello.WingID != welcome.WingID {
		return nil, welcome, errors.New("incompatible wing attachment handshake")
	}
	_ = conn.SetDeadline(time.Time{})
	ok = true
	return stream, welcome, nil
}
