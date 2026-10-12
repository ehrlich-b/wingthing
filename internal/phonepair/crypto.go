package phonepair

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"golang.org/x/crypto/hkdf"
	"io"
	"strings"
)

func Fingerprint(key string) string {
	b, _ := base64.StdEncoding.DecodeString(key)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func Identity(q Invitation) string {
	return "wt-wing-v1:" + q.WingID + ":" + Fingerprint(q.Key) + ":" + q.ID
}
func PairKey(q Invitation, shared []byte, alice, bob string) (cipher.AEAD, error) {
	transcript := "wt-pair-v1\n" + q.WingID + "\n" + q.Key + "\n" + q.ID + "\n" + alice + "\n" + bob
	salt := sha256.Sum256([]byte(transcript))
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, salt[:], []byte("wt-pair-v1")), key); err != nil {
		return nil, err
	}
	return newCipher(key)
}
func newCipher(key []byte) (cipher.AEAD, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(b)
}
func CheckEmoji(key []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("wt-emoji-v1"))
	h := m.Sum(nil)
	out := make([]string, 4)
	for i := range out {
		out[i] = emojis[int(h[i])]
	}
	return strings.Join(out, " ")
}

// EmojiFromShared repeats the domain-separated HKDF so callers need not expose cipher internals.
func EmojiFromShared(q Invitation, shared []byte, alice, bob string) string {
	salt := sha256.Sum256([]byte("wt-pair-v1\n" + q.WingID + "\n" + q.Key + "\n" + q.ID + "\n" + alice + "\n" + bob))
	key := make([]byte, 32)
	_, _ = io.ReadFull(hkdf.New(sha256.New, shared, salt[:], []byte("wt-pair-v1")), key)
	return CheckEmoji(key)
}
func randomID() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func randomWords() string {
	out := make([]string, 6)
	for i := range out {
		var b [2]byte
		if _, e := rand.Read(b[:]); e != nil {
			panic(e)
		}
		out[i] = words[(int(b[0])<<8|int(b[1]))&2047]
	}
	return strings.Join(out, " ")
}
func encode(v any) []byte       { b, _ := json.Marshal(v); return b }
func failure(code string) error { return fmt.Errorf("%s", code) }

// Box encrypts the entire phone protocol, including PAKE public messages and errors,
// to the QR-pinned wing key before it enters an untrusted carrier.
func (m *Manager) Box(ctx context.Context, r Request) Response {
	if r.Op != "phone.box" || len(r.Payload) > MaxPayload+4096 {
		return Response{Error: "denied"}
	}
	g, e := auth.DeriveSharedKey(m.private, r.SenderPub, "wt-phone-carrier-v1")
	if e != nil {
		return Response{Error: "bad_proof"}
	}
	b, e := auth.Decrypt(g, r.Payload)
	if e != nil {
		return Response{Error: "bad_proof"}
	}
	var inner Request
	if json.Unmarshal(b, &inner) != nil || inner.Op == "phone.box" {
		return Response{Error: "bad_proof"}
	}
	result := m.Handle(ctx, inner)
	payload, e := auth.Encrypt(g, encode(result))
	if e != nil {
		return Response{Error: "bad_proof"}
	}
	return Response{Payload: payload}
}
