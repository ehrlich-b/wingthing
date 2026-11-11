package phonepair

import (
	"crypto/sha256"
	"io"

	"golang.org/x/crypto/hkdf"
)

// The subprocess protocol carries only disposable test secrets over private pipes.
type interopRequest struct {
	Op      string
	Alice   bool
	Q       Invitation
	Message []byte
	Request Request
}

type interopReply struct {
	Q          Invitation
	Message    []byte
	Shared     []byte
	Transcript string
	Key        []byte
	Proof      string
	Response   Response
}

func interopMaterial(q Invitation, shared []byte, alice, bob string) (string, []byte) {
	transcript := "wt-pair-v1\n" + q.WingID + "\n" + q.Key + "\n" + q.ID + "\n" + alice + "\n" + bob
	salt := sha256.Sum256([]byte(transcript))
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, salt[:], []byte("wt-pair-v1")), key); err != nil {
		panic(err)
	}
	return transcript, key
}
