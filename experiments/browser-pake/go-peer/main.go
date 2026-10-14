// A local JSON-lines peer that calls the real CGO PAKE and phonepair packages.
package main

import (
	"bufio"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/pake"
	"github.com/ehrlich-b/wingthing/internal/phonepair"
)

type request struct {
	Op      string `json:"op"`
	Alice   bool   `json:"alice"`
	Message string `json:"message"`
	Proof   string `json:"proof"`
}

type peer struct {
	exchange *pake.Exchange
	q        phonepair.Invitation
	message  string
	alice    bool
}

func (p *peer) close() {
	if p.exchange != nil {
		p.exchange.Close()
		p.exchange = nil
	}
	p.q.Secret = ""
}

func (p *peer) call(r request) (any, error) {
	switch r.Op {
	case "start":
		if p.exchange != nil {
			return nil, fmt.Errorf("previous exchange still open")
		}
		private, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return nil, err
		}
		// Generated fixtures only; this manager never enrolls or writes a device.
		m, err := phonepair.New(filepath.Join(os.Getenv("HOME"), "fixture"),
			fmt.Sprintf("%x", id), "interop", "", nil, private, nil)
		if err != nil {
			return nil, err
		}
		defer m.Close()
		p.q, err = m.Create()
		if err != nil {
			return nil, err
		}
		var message []byte
		p.alice = r.Alice
		p.exchange, message, err = pake.New(r.Alice, p.q.Secret, phonepair.Identity(p.q))
		if err != nil {
			return nil, err
		}
		p.message = base64.StdEncoding.EncodeToString(message)
		return map[string]any{"message": p.message, "invitation": p.q,
			"identity": phonepair.Identity(p.q)}, nil
	case "finish":
		if p.exchange == nil {
			return nil, fmt.Errorf("no exchange")
		}
		defer p.close()
		message, err := base64.StdEncoding.DecodeString(r.Message)
		if err != nil {
			return nil, err
		}
		shared, err := p.exchange.Finish(message)
		if err != nil {
			return nil, err
		}
		alice, bob := r.Message, p.message
		if p.alice {
			alice, bob = bob, alice
		}
		gcm, err := phonepair.PairKey(p.q, shared, alice, bob)
		if err != nil {
			return nil, err
		}
		proof, proofErr := auth.Decrypt(gcm, r.Proof)
		proofOK := proofErr == nil && string(proof) == "browser-pake proof"
		sealed, err := auth.Encrypt(gcm, []byte("go-pake proof"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"shared": base64.StdEncoding.EncodeToString(shared),
			"proofOK": proofOK, "sealed": sealed,
			"emoji": phonepair.EmojiFromShared(p.q, shared, alice, bob)}, nil
	case "close":
		p.close()
		return map[string]any{"closed": true}, nil
	default:
		return nil, fmt.Errorf("unknown operation")
	}
}

func main() {
	p := &peer{}
	defer p.close()
	input := bufio.NewScanner(os.Stdin)
	input.Buffer(make([]byte, 4096), 1<<20)
	output := json.NewEncoder(os.Stdout)
	for input.Scan() {
		var r request
		err := json.Unmarshal(input.Bytes(), &r)
		var result any
		if err == nil {
			result, err = p.call(r)
		}
		if err != nil {
			p.close()
			result = map[string]string{"error": err.Error()}
		}
		if err := output.Encode(result); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if err := input.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
