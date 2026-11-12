package tunnel

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// References supplies the runtime's live configuration and wing callbacks.
// Mutexes and configuration pointers retain their original identities.
type References struct {
	ClearAttention            func(string)
	Version                   string
	Sessions                  *wingsession.Service
	WingCfg                   *config.WingConfig
	WingCfgMu                 *sync.Mutex
	AllowedKeys               *[]config.AllowKey
	WingEggMu                 *sync.Mutex
	WingEggCfg                **egg.EggConfig
	BrowserFleetAttachment    func(context.Context, ws.TunnelRequest, string, json.RawMessage) (map[string]any, error)
	BrowserTools              func() []*config.ToolConfig
	ListAliveEggSessions      func(*config.Config) []ws.SessionInfo
	ResizeBrowserInput        func(context.Context, string, string, string, string, uint32, uint32) error
	KillSessionsViolatingACLs func(*config.Config, config.PathList, string)
}
