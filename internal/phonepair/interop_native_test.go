//go:build cgo

package phonepair

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/pake"
)

// Executed only by the CGO-off test's CGO-built test binary, never a mock peer.
func TestNativePairPeer(t *testing.T) {
	if os.Getenv("WT_NATIVE_PAIR_PEER") != "1" {
		t.Skip("subprocess fixture")
	}
	var x *pake.Exchange
	defer func() {
		if x != nil {
			x.Close()
		}
	}()
	var f *phoneFixture
	var q Invitation
	var message []byte
	var alice bool
	input, output := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var r interopRequest
		if err := input.Decode(&r); err == io.EOF {
			return
		} else if err != nil {
			t.Fatal(err)
		}
		var reply interopReply
		switch r.Op {
		case "start":
			q, alice = r.Q, r.Alice
			var err error
			x, message, err = pake.New(alice, q.Secret, Identity(q))
			if err != nil {
				t.Fatal(err)
			}
			reply.Message = message
		case "finish":
			shared, err := x.Finish(r.Message)
			if err != nil {
				t.Fatal(err)
			}
			a, b := base64.StdEncoding.EncodeToString(r.Message), base64.StdEncoding.EncodeToString(message)
			if alice {
				a, b = b, a
			}
			reply.Shared = shared
			reply.Transcript, reply.Key = interopMaterial(q, shared, a, b)
			gcm, err := PairKey(q, shared, a, b)
			if err != nil {
				t.Fatal(err)
			}
			reply.Proof, err = auth.Encrypt(gcm, []byte("native AES-256-GCM proof"))
			if err != nil {
				t.Fatal(err)
			}
		case "server":
			f = fixture(t)
			defer f.m.Close()
			reply.Q = f.q
		case "handle":
			reply.Response = f.m.Handle(t.Context(), r.Request)
		case "allow":
			if err := f.m.Allow(f.q.ID); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unknown peer operation %q", r.Op)
		}
		if err := output.Encode(reply); err != nil {
			t.Fatal(err)
		}
	}
}
