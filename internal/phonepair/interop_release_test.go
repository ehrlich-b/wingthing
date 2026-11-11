//go:build !cgo

package phonepair

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/pake"
)

func TestWASMNativePairing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	binary := filepath.Join(t.TempDir(), "native-pair.test")
	build := exec.CommandContext(ctx, "go", "test", "-p", "2", "-c", "-o", binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=1")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real CGO peer: %v\n%s", err, out)
	}
	peer := exec.CommandContext(ctx, binary, "-test.run=^TestNativePairPeer$")
	peer.Env = append(os.Environ(), "WT_NATIVE_PAIR_PEER=1")
	var stderr bytes.Buffer
	peer.Stderr = &stderr
	in, err := peer.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := peer.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = in.Close()
		if err := peer.Wait(); err != nil {
			t.Errorf("CGO peer: %v\n%s", err, &stderr)
		}
	})
	encoder, decoder := json.NewEncoder(in), json.NewDecoder(out)
	call := func(r interopRequest) interopReply {
		t.Helper()
		if err := encoder.Encode(r); err != nil {
			t.Fatal(err)
		}
		var reply interopReply
		if err := decoder.Decode(&reply); err != nil {
			t.Fatalf("CGO peer response: %v\n%s", err, &stderr)
		}
		return reply
	}

	// Check the raw SPAKE2 result, exact transcript, 32-byte HKDF key and
	// production PairKey AES-GCM encryption with WASM in both SPAKE2 roles.
	for _, wasmAlice := range []bool{true, false} {
		name := "wasm-bob"
		if wasmAlice {
			name = "wasm-alice"
		}
		t.Run(name, func(t *testing.T) {
			f := fixture(t)
			defer f.m.Close()
			x, message, err := pake.New(wasmAlice, f.q.Secret, Identity(f.q))
			if err != nil {
				t.Fatal(err)
			}
			defer x.Close()
			native := call(interopRequest{Op: "start", Q: f.q, Alice: !wasmAlice})
			shared, err := x.Finish(native.Message)
			if err != nil {
				t.Fatal(err)
			}
			finished := call(interopRequest{Op: "finish", Message: message})
			a, b := base64.StdEncoding.EncodeToString(message), base64.StdEncoding.EncodeToString(native.Message)
			if !wasmAlice {
				a, b = b, a
			}
			transcript, key := interopMaterial(f.q, shared, a, b)
			if len(shared) != 64 || !bytes.Equal(shared, finished.Shared) || transcript != finished.Transcript || len(key) != 32 || !bytes.Equal(key, finished.Key) {
				t.Fatal("native/WASM shared secret, transcript or AES-256 key disagrees")
			}
			gcm, err := PairKey(f.q, shared, a, b)
			if err != nil {
				t.Fatal(err)
			}
			plain, err := auth.Decrypt(gcm, finished.Proof)
			if err != nil || string(plain) != "native AES-256-GCM proof" {
				t.Fatalf("native PairKey proof: %v", err)
			}
			t.Logf("64-byte SPAKE2 result, transcript sha256=%x, identical 32-byte AES-GCM key", sha256.Sum256([]byte(transcript)))
		})
	}

	// Complete the real manager's prove, terminal approval and phone confirmation
	// protocol with WASM on either the phone or wing side, including enrollment.
	for _, wasmPhone := range []bool{true, false} {
		name := "wasm-wing-native-phone"
		if wasmPhone {
			name = "wasm-phone-native-wing"
		}
		t.Run(name, func(t *testing.T) {
			var q Invitation
			var handle func(Request) Response
			var allow func()
			var manager *Manager
			if wasmPhone {
				q = call(interopRequest{Op: "server"}).Q
				handle = func(r Request) Response { return call(interopRequest{Op: "handle", Request: r}).Response }
				allow = func() { call(interopRequest{Op: "allow"}) }
			} else {
				f := fixture(t)
				manager, q = f.m, f.q
				defer manager.Close()
				handle = func(r Request) Response { return manager.Handle(t.Context(), r) }
				allow = func() {
					if err := manager.Allow(q.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			var x *pake.Exchange
			var am, shared []byte
			var err error
			if wasmPhone {
				x, am, err = pake.New(true, q.Secret, Identity(q))
				if err != nil {
					t.Fatal(err)
				}
				defer x.Close()
			} else {
				am = call(interopRequest{Op: "start", Q: q, Alice: true}).Message
			}
			as := base64.StdEncoding.EncodeToString(am)
			response := handle(Request{Op: "pair.start", ID: q.ID, Message: as})
			if response.Error != "" {
				t.Fatal(response.Error)
			}
			bm, err := base64.StdEncoding.DecodeString(response.Message)
			if err != nil {
				t.Fatal(err)
			}
			if wasmPhone {
				shared, err = x.Finish(bm)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				shared = call(interopRequest{Op: "finish", Message: bm}).Shared
			}
			gcm, err := PairKey(q, shared, as, response.Message)
			if err != nil {
				t.Fatal(err)
			}
			emoji := EmojiFromShared(q, shared, as, response.Message)
			completeInteropPairing(t, q, gcm, emoji, handle, allow)
			if manager != nil && len(manager.Devices()) != 1 {
				t.Fatal("WASM wing did not enroll native phone")
			}
			t.Log("prove, matching emoji, both approvals and persisted enrollment succeeded")
		})
	}
}

func completeInteropPairing(t *testing.T, q Invitation, gcm cipher.AEAD, emoji string, handle func(Request) Response, allow func()) {
	t.Helper()
	signing, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	send := func(op string, fields map[string]any) map[string]any {
		t.Helper()
		fields["op"], fields["id"] = op, q.ID
		payload, err := auth.Encrypt(gcm, encode(fields))
		if err != nil {
			t.Fatal(err)
		}
		r := handle(Request{Op: "pair." + op, ID: q.ID, Payload: payload})
		if r.Error != "" {
			t.Fatal(r.Error)
		}
		plain, err := auth.Decrypt(gcm, r.Payload)
		if err != nil {
			t.Fatal(err)
		}
		var reply map[string]any
		if err := json.Unmarshal(plain, &reply); err != nil {
			t.Fatal(err)
		}
		return reply
	}
	proof := send("prove", map[string]any{"name": "Interop phone", "public_key": base64.StdEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), signing.X, signing.Y))})
	if proof["emoji"] != emoji {
		t.Fatal("native/WASM manager transcript emojis differ")
	}
	allow()
	confirmed := send("confirm", map[string]any{"emoji": emoji})
	if confirmed["device_id"] == "" || confirmed["device_id"] == nil || confirmed["credential"] == "" || confirmed["credential"] == nil {
		t.Fatal("missing enrollment credential")
	}
}
