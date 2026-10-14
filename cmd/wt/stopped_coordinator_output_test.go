package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/fleet"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoppedCoordinatorHasInspectionAndNoWakePromise(t *testing.T) {
	var out bytes.Buffer
	inv := fleet.Inventory{Sessions: []fleet.Session{{Identity: fleet.Identity{OriginWingID: "sample-wing", SessionID: "past-coordinator"}, Role: "coordinator", Pinned: true, Live: false, Presence: &fleet.Presence{State: "dead", NextWake: "2099-01-01T00:00:00Z"}}}}
	if err := renderSessionInventory(&out, inv); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "Coordinator stopped") || !strings.Contains(text, "--operation session_read") || strings.Contains(text, "next wake") || strings.Contains(text, "2099") {
		t.Fatal(text)
	}
	if inv.Sessions[0].Presence.NextWake != "2099-01-01T00:00:00Z" {
		t.Fatal("recorded evidence changed")
	}
}
func TestStoryboardStoppedCoordinatorPTY(t *testing.T) {
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	dir := filepath.Join(f.local, "eggs", "past-coordinator")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(dir, "egg.meta"), "agent=claude\ncwd=/home/example/release\nprovider_home="+f.local+"\n", 0600)
	if err := eggclient.WriteSessionName(dir, "wing"); err != nil {
		t.Fatal(err)
	}
	if err := sessionrole.Write(dir, sessionrole.Metadata{Pinned: true, Role: "coordinator"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := sessionrole.RecordPresence(dir, fleet.Presence{State: "asleep", LastWake: now.Add(-time.Minute).Format(time.RFC3339), NextWake: now.Add(time.Hour).Format(time.RFC3339)}, now); err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(dir, "lifecycle.jsonl"), "{\"sequence\":1,\"type\":\"session_exit\",\"source\":\"egg_process\",\"state\":\"stopped\"}\n", 0600)
	server, err := controlsocket.Listen(t.Context(), f.local, "sample-wing", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{Principal: "sample-owner"}, func(ctx context.Context, r control.DirectRequest) control.DirectResponse {
			if r.Tool != "session_inventory" {
				t.Error("unexpected mutation")
			}
			rows, complete, err := eggclient.SessionCatalog(ctx, &config.Config{Dir: f.local}, "sample-wing", "Studio Mac", "", func(s eggclient.LocalSession) bool { return s.ID == "past-coordinator" })
			if err != nil {
				return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Error: err.Error()}
			}
			for i := range rows {
				rows[i].Routes = []fleet.Route{{WingID: "sample-wing", Kind: "direct", Healthy: true, ObservedAt: time.Now().UTC().Format(time.RFC3339)}}
			}
			data, _ := json.Marshal(fleet.Inventory{Version: fleet.Version, Complete: complete, Sessions: rows})
			var result map[string]any
			json.Unmarshal(data, &result)
			return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Result: result}
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	h := northstarStartPTY(t, f, binary, 120, 36, "session", "inventory")
	h.expect("stopped coordinator recovery", 0, "Coordinator stopped", "No restart is confirmed", "--operation session_read")
	<-h.done
	storyboardCapture(t, h, "13-10-stopped-coordinator")
}
