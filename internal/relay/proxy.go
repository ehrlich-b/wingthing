package relay

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// NewLoginProxy creates a reverse proxy to the login node for edge nodes.
// Edge nodes proxy API, auth, and page requests to the login node while
// serving WebSocket connections directly.
func NewLoginProxy(loginAddr string) *httputil.ReverseProxy {
	target, err := url.Parse(loginAddr)
	if err != nil {
		log.Fatalf("invalid login node address: %s", loginAddr)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy to login node failed: %v", err)
		http.Error(w, "login node unavailable", http.StatusBadGateway)
	}
	return proxy
}
