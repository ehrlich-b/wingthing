package egg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Client wraps the generated gRPC client for a single egg process.
type Client struct {
	conn    *grpc.ClientConn
	client  pb.EggClient
	token   string
	leaseMu sync.Mutex
	leases  map[string]*pb.AttachmentInfo
}

// Dial connects to an egg's Unix socket and reads its auth token.
func Dial(socketPath, tokenPath string) (*Client, error) {
	dir := filepath.Dir(tokenPath)
	if controlDir, err := readControlDirectory(dir); err == nil {
		if !hasControlIsolationAt(controlDir) {
			return nil, fmt.Errorf("egg controller isolation marker is unavailable")
		}
		tokenPath = filepath.Join(controlDir, "egg.token")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	tokenData, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("read egg token: %w", err)
	}
	token := string(tokenData)

	conn, err := grpc.NewClient(
		"unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("dial egg: %w", err)
	}

	return &Client{
		conn:   conn,
		client: pb.NewEggClient(conn),
		token:  token,
	}, nil
}

func (c *Client) authCtx(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", c.token)
}

// Kill terminates the session.
func (c *Client) Kill(ctx context.Context, sessionID string) error {
	_, err := c.client.Kill(c.authCtx(ctx), &pb.KillRequest{SessionId: sessionID})
	return err
}

// ReclaimToolCapability recovers a surviving egg's original tool authority.
// The egg token is host-only; neither ordinary status nor the sandbox exposes it.
func (c *Client) ReclaimToolCapability(ctx context.Context) (string, error) {
	response, err := c.client.Status(c.authCtx(ctx), &pb.StatusRequest{ReclaimTools: true})
	if err != nil {
		return "", err
	}
	if response.ToolCapability == "" {
		return "", fmt.Errorf("egg has no recoverable tool capability; start a new egg session")
	}
	return response.ToolCapability, nil
}

// Resize changes terminal dimensions.
func (c *Client) Resize(ctx context.Context, sessionID string, rows, cols uint32) error {
	c.leaseMu.Lock()
	lease := c.leases[sessionID]
	c.leaseMu.Unlock()
	return c.ResizeForAttachment(ctx, sessionID, rows, cols, lease)
}

// ResizeForAttachment binds a resize to a specific stream's confirmed lease.
// Adapters sharing a client must retain this value rather than adopting a
// newer stream's token when the old connection sends a late resize.
func (c *Client) ResizeForAttachment(ctx context.Context, sessionID string, rows, cols uint32, lease *pb.AttachmentInfo) error {
	request := &pb.ResizeRequest{SessionId: sessionID, Rows: rows, Cols: cols}
	if lease != nil {
		request.AttachmentId, request.InputEpoch, request.AttachmentToken = lease.AttachmentId, lease.InputEpoch, lease.AttachmentToken
	}
	_, err := c.client.Resize(c.authCtx(ctx), request)
	return err
}

// AttachSession opens a bidirectional stream for PTY I/O.
func (c *Client) AttachSession(ctx context.Context, sessionID string) (pb.Egg_SessionClient, error) {
	return c.AttachSessionWithOptions(ctx, sessionID, AttachOptions{})
}

type AttachOptions struct {
	ReadOnly bool
	Takeover bool
	Claim    bool
	Owner    string
	Rows     uint32
	Cols     uint32
}

// AttachSessionWithOptions confirms the initial snapshot before returning.
// An eager writer claim therefore fails before any prompt/input is submitted.
func (c *Client) AttachSessionWithOptions(ctx context.Context, sessionID string, options AttachOptions) (pb.Egg_SessionClient, error) {
	if options.ReadOnly && (options.Claim || options.Takeover) {
		return nil, fmt.Errorf("read-only and writer claim/takeover are mutually exclusive")
	}
	if len(options.Owner) > 128 {
		return nil, fmt.Errorf("attachment owner label must be at most 128 bytes")
	}
	if options.Rows != 0 || options.Cols != 0 {
		if options.ReadOnly {
			return nil, fmt.Errorf("read-only attachment cannot resize")
		}
		if err := validatePTYSize(options.Cols, options.Rows); err != nil {
			return nil, err
		}
	}
	stream, err := c.client.Session(c.authCtx(ctx))
	if err != nil {
		return nil, err
	}

	if err := stream.Send(&pb.SessionMsg{
		SessionId:     sessionID,
		Payload:       &pb.SessionMsg_Attach{Attach: true},
		AttachOptions: &pb.AttachOptions{ReadOnly: options.ReadOnly, Takeover: options.Takeover, Claim: options.Claim, Owner: options.Owner, Rows: options.Rows, Cols: options.Cols},
	}); err != nil {
		return nil, fmt.Errorf("send attach: %w", err)
	}

	first, err := stream.Recv()
	if err != nil {
		return nil, fmt.Errorf("confirm attachment: %w", err)
	}
	if lease := first.AttachmentInfo; lease != nil && lease.AttachmentId == lease.WriterId && lease.AttachmentToken != "" {
		c.leaseMu.Lock()
		if c.leases == nil {
			c.leases = make(map[string]*pb.AttachmentInfo)
		}
		c.leases[sessionID] = lease
		c.leaseMu.Unlock()
	}
	return &attachmentStream{Egg_SessionClient: stream, first: first, lease: first.AttachmentInfo, readOnly: options.ReadOnly}, nil
}

// AttachmentInfo returns a copy of the immutable snapshot acknowledgment.
// AttachmentToken is private to this authenticated writer stream and must
// never be included in public status, relay messages, or logs.
func AttachmentInfo(stream pb.Egg_SessionClient) *pb.AttachmentInfo {
	if attached, ok := stream.(*attachmentStream); ok && attached.lease != nil {
		lease := attached.lease
		return &pb.AttachmentInfo{AttachmentId: lease.AttachmentId, WriterId: lease.WriterId, WriterOwner: lease.WriterOwner, InputEpoch: lease.InputEpoch, AttachmentToken: lease.AttachmentToken}
	}
	return nil
}

type attachmentStream struct {
	pb.Egg_SessionClient
	first    *pb.SessionMsg
	lease    *pb.AttachmentInfo
	readOnly bool
	mu       sync.Mutex // gRPC permits one sender and one receiver concurrently.
}

func (s *attachmentStream) Recv() (*pb.SessionMsg, error) {
	if s.first != nil {
		first := s.first
		s.first = nil
		return first, nil
	}
	return s.Egg_SessionClient.Recv()
}

func (s *attachmentStream) Send(message *pb.SessionMsg) error {
	if s.readOnly {
		switch message.Payload.(type) {
		case *pb.SessionMsg_Input, *pb.SessionMsg_Resize:
			return status.Error(codes.PermissionDenied, "read-only attachment cannot write or resize")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Egg_SessionClient.Send(message)
}

// Status returns debug stats from the egg session.
func (c *Client) Status(ctx context.Context) (*pb.StatusResponse, error) {
	return c.client.Status(c.authCtx(ctx), &pb.StatusRequest{})
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}
