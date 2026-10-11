//go:build runinspectfixture

package localmcp

import "encoding/json"

// FixturePersistCheckpoint exercises the broker serializer without dispatching.
// This adapter exists only in explicitly tagged fixture/test builds.
func FixturePersistCheckpoint(dir, id, phase, outcome string, payload json.RawMessage, digest string) error {
	b := &conversationBroker{dir: dir}
	return b.writeJournal(conversationBrokerJournal{Version: 1, ID: id, Method: "tools/call", Tool: "conversation_checkpoint", Payload: payload, Digest: digest, AcceptedAt: 1791633600, Phase: phase, Outcome: outcome})
}
