//go:build integration

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type reclaimFixtureEgg struct{ pb.UnimplementedEggServer }

func (reclaimFixtureEgg) Session(stream grpc.BidiStreamingServer[pb.SessionMsg, pb.SessionMsg]) error {
	md, _ := metadata.FromIncomingContext(stream.Context())
	if values := md.Get("authorization"); len(values) != 1 || values[0] != "fixture-egg-token" {
		return fmt.Errorf("wrong egg credential")
	}
	message, err := stream.Recv()
	if err != nil {
		return err
	}
	if message.SessionId != "late-session" || !message.GetAttach() {
		return fmt.Errorf("unexpected egg attachment")
	}
	if err := stream.Send(&pb.SessionMsg{SessionId: message.SessionId, Payload: &pb.SessionMsg_Output{Output: []byte("login fixture ready\r\n")}}); err != nil {
		return err
	}
	<-stream.Context().Done()
	return stream.Context().Err()
}

func TestOnDemandEggReclaimPreservesOwnerAndEncryption(t *testing.T) {
	for _, test := range []struct {
		name      string
		user      string
		locked    bool
		omitKey   bool
		wantError string
	}{
		{name: "owner receives replay", user: "alice"},
		{name: "other member denied", user: "bob", wantError: "session not found or not owned by caller"},
		{name: "encryption required", user: "alice", omitKey: true, wantError: "client encryption key required"},
		{name: "locked wing denies unenrolled owner", user: "alice", locked: true, wantError: "not allowed by wing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "wt-reclaim-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			cfg := &config.Config{Dir: root}
			if _, err := auth.EnsureKeyPair(root); err != nil {
				t.Fatal(err)
			}
			eggDir := filepath.Join(root, "eggs", "late-session")
			if err := os.MkdirAll(eggDir, 0700); err != nil {
				t.Fatal(err)
			}
			for name, value := range map[string]string{"egg.pid": strconv.Itoa(os.Getpid()), "egg.token": "fixture-egg-token"} {
				if err := os.WriteFile(filepath.Join(eggDir, name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeEggOwner(eggDir, "alice", ""); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", filepath.Join(eggDir, "egg.sock"))
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer()
			pb.RegisterEggServer(server, reclaimFixtureEgg{})
			go func() { _ = server.Serve(listener) }()
			defer server.Stop()
			browserKey, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result := make(chan error, 1)
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					result <- err
					return
				}
				defer conn.CloseNow()
				result <- exerciseReclaimedEgg(ctx, conn, browserKey, test.user, test.omitKey, test.wantError)
				<-ctx.Done()
			}))
			defer relay.Close()
			client := &ws.Client{RoostURL: strings.Replace(relay.URL, "http://", "ws://", 1), WingID: "wing", Token: "fixture-device-token"}
			client.OnPTYReclaim = func(ctx context.Context, sessionID string) {
				reclaimEggSession(ctx, cfg, client, sessionID, &config.WingConfig{Locked: test.locked}, nil, auth.NewAuthCache(), auth.PasskeyPolicy{}, 0, nil)
			}
			done := make(chan struct{})
			go func() { _ = client.Run(ctx); close(done) }()
			defer func() { cancel(); <-done }()
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("on-demand egg attachment hung")
			}
		})
	}
}

func exerciseReclaimedEgg(ctx context.Context, conn *websocket.Conn, browserKey *ecdh.PrivateKey, user string, omitKey bool, wantError string) error {
	if _, _, err := conn.Read(ctx); err != nil {
		return err
	}
	if err := wsjson.Write(ctx, conn, ws.RegisteredMsg{Type: ws.TypeRegistered, WingID: "wing"}); err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		attach := ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: "late-session", UserID: user, OrgRole: "member", PublicKey: base64.StdEncoding.EncodeToString(browserKey.PublicKey().Bytes()), ViewerID: fmt.Sprintf("viewer-%d", attempt)}
		if omitKey {
			attach.PublicKey = ""
		}
		if err := wsjson.Write(ctx, conn, attach); err != nil {
			return err
		}
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		if wantError != "" {
			var denial ws.ErrorMsg
			if err := json.Unmarshal(data, &denial); err != nil {
				return err
			}
			if denial.Type != ws.TypeError || denial.Message != wantError || denial.ViewerID != attach.ViewerID {
				return fmt.Errorf("wrong denial or leaked terminal output: %s", data)
			}
			continue
		}
		var started ws.PTYStarted
		if err := json.Unmarshal(data, &started); err != nil {
			return err
		}
		if started.Type != ws.TypePTYStarted || started.SessionID != attach.SessionID || started.PublicKey == "" {
			return fmt.Errorf("missing authenticated attach response: %s", data)
		}
		gcm, err := auth.DeriveSharedKey(browserKey, started.PublicKey, "wt-pty")
		if err != nil {
			return err
		}
		var output ws.PTYOutput
		if err := wsjson.Read(ctx, conn, &output); err != nil {
			return err
		}
		if output.Type != ws.TypePTYOutput || output.SessionID != attach.SessionID {
			return fmt.Errorf("wrong replay envelope: %#v", output)
		}
		plaintext, err := auth.Decrypt(gcm, output.Data)
		if err != nil {
			return err
		}
		if output.Compressed {
			reader, err := gzip.NewReader(bytes.NewReader(plaintext))
			if err != nil {
				return err
			}
			plaintext, err = io.ReadAll(reader)
			_ = reader.Close()
			if err != nil {
				return err
			}
		}
		if string(plaintext) != "login fixture ready\r\n" {
			return fmt.Errorf("wrong decrypted replay: %q", plaintext)
		}
	}
	return nil
}
