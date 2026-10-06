package localrelay

import (
	"github.com/ehrlich-b/wingthing/internal/config"
)

func DefaultHTTPSAddr() string {
	if config.Channel() == "preview" {
		return "127.0.0.1:8181"
	}
	return DefaultLocalHTTPSAddr
}
