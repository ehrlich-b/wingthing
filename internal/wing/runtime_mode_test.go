package wing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/pake"
	"github.com/ehrlich-b/wingthing/internal/phonepair"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestRunningWingReportsResolvedMode(t *testing.T) {
	if !pake.Available {
		t.Skip("PAKE unavailable")
	}
	for _, localOnly := range []bool{true, false} {
		mode := phonepair.ModeRelay
		if localOnly {
			mode = phonepair.ModeLocalOnly
		}
		t.Run(mode, func(t *testing.T) {
			state, home, work := localOnlyState(t)
			if err := auth.NewTokenStore(state).Save(&auth.DeviceToken{Token: "fixture", UserID: "fixture-owner"}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				var reg ws.WingRegister
				if wsjson.Read(ctx, conn, &reg) != nil {
					return
				}
				if wsjson.Write(ctx, conn, ws.RegisteredMsg{Type: ws.TypeRegistered, WingID: reg.WingID}) != nil {
					return
				}
				for {
					if _, _, err := conn.Read(ctx); err != nil {
						return
					}
				}
			}))
			defer relay.Close()
			connected := make(chan struct{})
			var once sync.Once
			runtime := startLocalOnlyRuntime(t, home, work, relay.URL, EntryOptions{LocalOnly: localOnly, ListenAddress: "127.0.0.1", RelayState: func(state string, _ error) {
				if state == "connected" {
					once.Do(func() { close(connected) })
				}
			}}, nil)
			defer runtime.stop()
			if !localOnly {
				select {
				case <-connected:
				case <-ctx.Done():
					t.Fatal("fixture wing never connected to relay")
				}
			}
			// Runtime readiness can precede the phone listener's publication.
			for phonepair.RunningMode(state) != mode {
				select {
				case <-ctx.Done():
					t.Fatal("live mode query did not report", mode)
				case <-time.After(10 * time.Millisecond):
				}
			}
			got := phonepair.RestartGuidance(phonepair.RunningMode(state), true)
			want := "wt wing start --listen 0.0.0.0"
			if localOnly {
				want = "wt wing start --local-only --listen 0.0.0.0"
			}
			if got != want+"\nOther flags the wing was started with must be added by hand." {
				t.Fatalf("live restart guidance: %s", got)
			}
			helper, err := filepath.Abs("../../mobile/ios/scripts/ios_install_support.py")
			if err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(ctx, "python3", "-B", helper, "lan-restart", state)
			if output, err := command.CombinedOutput(); err != nil || string(output) != mode+"\n" {
				t.Fatalf("iOS helper live mode: %s %v", output, err)
			}
		})
	}
}
