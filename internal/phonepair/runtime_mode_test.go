package phonepair

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestRunningModeRejectsUnusableLiveReplies(t *testing.T) {
	for _, reply := range []string{
		`{"wing_id":"fixture-wing","mode":"local-only"}`,
		`{"wing_id":"fixture-wing","mode":"relay"}`,
		`{"error":"denied"}`, // An older daemon does not implement the mode op.
		`{"wing_id":"fixture-wing","mode":""}`,
		`{"wing_id":"fixture-wing","mode":true}`,
		`{"wing_id":"fixture-wing","mode":"connecting"}`,
		`{"wing_id":"another-wing","mode":"local-only"}`,
		`{`,
	} {
		t.Run(reply, func(t *testing.T) {
			dir := testsocket.Dir(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/admin" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("invalid mode request")
					w.WriteHeader(403)
					return
				}
				var request map[string]string
				if json.NewDecoder(r.Body).Decode(&request) != nil || request["Op"] != "mode" {
					t.Error("mode query required")
				}
				_, _ = w.Write([]byte(reply))
			}))
			defer server.Close()
			control := Control{Port: server.Listener.Addr().(*net.TCPAddr).Port, Token: "fixture-token", WingID: "fixture-wing"}
			data, _ := json.Marshal(control)
			if err := os.WriteFile(filepath.Join(dir, "phone-control.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			want := ""
			if reply == `{"wing_id":"fixture-wing","mode":"local-only"}` {
				want = ModeLocalOnly
			}
			if reply == `{"wing_id":"fixture-wing","mode":"relay"}` {
				want = ModeRelay
			}
			if got := RunningMode(dir); got != want {
				t.Fatalf("mode = %q; want %q", got, want)
			}
			server.Close()
			if got := RunningMode(dir); got != "" {
				t.Fatalf("dead server reported %q", got)
			}
		})
	}
}
