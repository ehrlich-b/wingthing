// Loopback fixture for the public pages using the branch's real relay handlers.
package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/relay"
)

func main() {
	addr := os.Args[1]
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" {
		log.Fatal("public capture fixture requires a loopback address")
	}
	store, err := relay.OpenRelay(filepath.Join(os.Getenv("WINGTHING_DIR"), "public.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	server := relay.NewServer(store, relay.ServerConfig{BaseURL: "http://" + addr})
	log.Fatal(http.ListenAndServe(addr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "read only capture fixture", http.StatusMethodNotAllowed)
			return
		}
		server.ServeHTTP(w, r)
	})))
}
