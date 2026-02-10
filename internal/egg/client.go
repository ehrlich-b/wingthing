package egg

import (
	"context"
	"fmt"
	"os"

	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// Client wraps the generated gRPC client for the egg.
type Client struct {
	conn   *grpc.ClientConn
	client pb.EggClient
	token  string
}

// Dial connects to the egg Unix socket and reads the auth token.
func Dial(socketPath, tokenPath string) (*Client, error) {
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

// Spawn creates a new PTY session in the egg.
func (c *Client) Spawn(ctx context.Context, req *pb.SpawnRequest) (*pb.SpawnResponse, error) {
	return c.client.Spawn(c.authCtx(ctx), req)
}

// List returns all active sessions.
func (c *Client) List(ctx context.Context) (*pb.ListResponse, error) {
	return c.client.List(c.authCtx(ctx), &pb.ListRequest{})
}

// Kill terminates a session.
func (c *Client) Kill(ctx context.Context, sessionID string) error {
	_, err := c.client.Kill(c.authCtx(ctx), &pb.KillRequest{SessionId: sessionID})
	return err
}

// Resize changes terminal dimensions of a session.
func (c *Client) Resize(ctx context.Context, sessionID string, rows, cols uint32) error {
	_, err := c.client.Resize(c.authCtx(ctx), &pb.ResizeRequest{
		SessionId: sessionID,
		Rows:      rows,
		Cols:      cols,
	})
	return err
}

// AttachSession opens a bidirectional stream for PTY I/O.
func (c *Client) AttachSession(ctx context.Context, sessionID string) (pb.Egg_SessionClient, error) {
	stream, err := c.client.Session(c.authCtx(ctx))
	if err != nil {
		return nil, err
	}

	// Send attach message
	if err := stream.Send(&pb.SessionMsg{
		SessionId: sessionID,
		Payload:   &pb.SessionMsg_Attach{Attach: true},
	}); err != nil {
		return nil, fmt.Errorf("send attach: %w", err)
	}

	return stream, nil
}

// Version returns the egg's binary version.
func (c *Client) Version(ctx context.Context) (string, error) {
	resp, err := c.client.Version(c.authCtx(ctx), &pb.VersionRequest{})
	if err != nil {
		return "", err
	}
	return resp.Version, nil
}

// GetConfig returns the egg's current active config as YAML.
func (c *Client) GetConfig(ctx context.Context) (string, error) {
	resp, err := c.client.GetConfig(c.authCtx(ctx), &pb.GetConfigRequest{})
	if err != nil {
		return "", err
	}
	return resp.Yaml, nil
}

// SetConfig updates the egg's active config from YAML.
func (c *Client) SetConfig(ctx context.Context, yamlStr string) error {
	_, err := c.client.SetConfig(c.authCtx(ctx), &pb.SetConfigRequest{Yaml: yamlStr})
	return err
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}
