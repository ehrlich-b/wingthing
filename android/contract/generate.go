//go:build ignore

// Generate synthetic wire evidence using Wingthing's actual crypto contract.
package main

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/ehrlich-b/wingthing/internal/auth"
)

func main() {
	privateBytes := make([]byte, 32)
	peerBytes := make([]byte, 32)
	for i := range privateBytes {
		privateBytes[i] = byte(i + 1)
		peerBytes[i] = byte(101 + i)
	}
	private, err := ecdh.X25519().NewPrivateKey(privateBytes)
	if err != nil {
		panic(err)
	}
	peer, err := ecdh.X25519().NewPrivateKey(peerBytes)
	if err != nil {
		panic(err)
	}
	peerPub := base64.StdEncoding.EncodeToString(peer.PublicKey().Bytes())
	gcm, err := auth.DeriveSharedKey(private, peerPub, "wt-tunnel")
	if err != nil {
		panic(err)
	}
	nonce := make([]byte, 12)
	for i := range nonce {
		nonce[i] = byte(i + 201)
	}
	plain := `{"type":"session.control","operation":"session_status","arguments":{"session":"synthetic-session"}}`
	encrypted := gcm.Seal(append([]byte(nil), nonce...), nonce, []byte(plain), nil)
	fixture := map[string]string{"private_hex": hex.EncodeToString(privateBytes), "public_b64": base64.StdEncoding.EncodeToString(private.PublicKey().Bytes()), "peer_public_b64": peerPub, "nonce_hex": hex.EncodeToString(nonce), "plaintext": plain, "encrypted_b64": base64.StdEncoding.EncodeToString(encrypted)}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		panic(err)
	}
	if err = os.WriteFile("android/contract/tunnel-vector.json", append(data, '\n'), 0644); err != nil {
		panic(err)
	}
	fmt.Println("Generated public synthetic Android contract vector")
}
