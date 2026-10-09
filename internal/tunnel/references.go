package tunnel

import (
	"context"
	"sync"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// References supplies the runtime's live configuration and wing callbacks.
// Mutexes and configuration pointers retain their original identities.
type References struct {
	Version                   string
	Sessions                  *wingsession.Service
	WingCfg                   *config.WingConfig
	WingCfgMu                 *sync.Mutex
	AllowedKeys               *[]config.AllowKey
	WingEggMu                 *sync.Mutex
	WingEggCfg                **egg.EggConfig
	BrowserTools              func() []*config.ToolConfig
	ListAliveEggSessions      func(*config.Config) []ws.SessionInfo
	ResizeBrowserInput        func(context.Context, string, string, string, string, uint32, uint32) error
	KillSessionsViolatingACLs func(*config.Config, config.PathList, string)
}
