package ws

import (
	"crypto/cipher"
	"encoding/json"
	"log"

	"github.com/ehrlich-b/wingthing/internal/auth"
)

func WritePTYMessage(write PTYWriteFunc, message any) {
	if err := write(message); err != nil {
		log.Printf("send PTY message %T: %v", message, err)
	}
}

// TunnelRespond encrypts a JSON response and sends it as a tunnel.res message.
func TunnelRespond(gcm cipher.AEAD, requestID string, result any, write PTYWriteFunc) {
	data, _ := json.Marshal(result)
	encrypted, err := auth.Encrypt(gcm, data)
	if err != nil {
		return
	}
	WritePTYMessage(write, TunnelResponse{Type: TypeTunnelResponse, RequestID: requestID, Payload: encrypted})
}

// TunnelStreamChunk encrypts a streaming chunk and sends it as a tunnel.stream message.
func TunnelStreamChunk(gcm cipher.AEAD, requestID string, chunk []byte, done bool, write PTYWriteFunc) error {
	encrypted, err := auth.Encrypt(gcm, chunk)
	if err != nil {
		return err
	}
	return write(TunnelStream{Type: TypeTunnelStream, RequestID: requestID, Payload: encrypted, Done: done})
}
