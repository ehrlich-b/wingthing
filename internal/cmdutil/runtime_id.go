package cmdutil

import (
	"encoding/hex"

	"github.com/google/uuid"
)

// NewRuntimeID returns a compact 64-bit identifier for new local routing and
// filesystem records. Older eight-character IDs remain valid and attachable;
// only newly-created records use the larger collision space.
func NewRuntimeID() string {
	id := uuid.New()
	return hex.EncodeToString(id[:8])
}
