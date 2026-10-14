package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

func searchInventoryArguments(raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Query          string `json:"query"`
		IncludeRemotes *bool  `json:"include_remotes"`
	}
	if err := decodeFleetArguments(raw, &args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Query) == "" || len(args.Query) > 256 || len(strings.Fields(args.Query)) > 8 {
		return nil, errors.New("query must contain 1 to 8 words and be at most 256 bytes")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if value, exists := fields["include_remotes"]; exists && strings.TrimSpace(string(value)) == "null" {
		return nil, errors.New("include_remotes must be a boolean")
	}
	remotes := true
	if args.IncludeRemotes != nil {
		remotes = *args.IncludeRemotes
	}
	return json.Marshal(inventoryArgs{Query: args.Query, IncludeRemotes: remotes})
}
func (s *Server) ToolSessionSearch(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	raw, err := searchInventoryArguments(arguments)
	if err != nil {
		return nil, err
	}
	return s.toolSessionInventory(ctx, raw, true)
}
