package relay

import (
	"encoding/json"
	"fmt"
	"io"
)

const maxInternalJSONResponseBytes = 4 << 20

// decodeInternalJSONResponse bounds responses from other cluster nodes before
// decoding them. Internal authentication establishes who sent a response; it
// does not make an upgraded, compromised, or misconfigured peer safe to let
// allocate without limit.
func decodeInternalJSONResponse(reader io.Reader, dst any) error {
	body, err := io.ReadAll(io.LimitReader(reader, maxInternalJSONResponseBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxInternalJSONResponseBytes {
		return fmt.Errorf("internal response exceeds %d bytes", maxInternalJSONResponseBytes)
	}
	return json.Unmarshal(body, dst)
}
