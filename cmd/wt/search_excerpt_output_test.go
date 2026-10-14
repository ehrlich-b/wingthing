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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHumanSearchExcerptAndQuotedReadCommand(t *testing.T) {
	var out bytes.Buffer
	inv := fleet.Inventory{Query: "DNS helper", Sessions: []fleet.Session{{Identity: fleet.Identity{OriginWingID: "it's-wing", SessionID: "past-dns-run"}, Name: "DNS repair", MatchExcerpt: "fixed the dns helper"}}}
	if err := renderSessionInventory(&out, inv); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[dns] [helper]") || !strings.Contains(out.String(), `--wing 'it'\''s-wing'`) {
		t.Fatalf("snippet/command: %q", out.String())
	}
}
func TestStoryboardSessionSearchExcerptPTY(t *testing.T) {
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	dir := filepath.Join(f.local, "eggs", "past-dns-run")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(dir, "egg.meta"), "agent=codex\ncwd=/home/example/pairing\nprovider_home="+f.local+"\n", 0600)
	if err := eggclient.WriteSessionName(dir, "dns-helper-repair"); err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(dir, "lifecycle.jsonl"), "{\"sequence\":1,\"type\":\"message\",\"source\":\"codex_transcript\",\"text\":\"Fixed the DNS helper and checked the resolver.\"}\n", 0600)
	cfg := &config.Config{Dir: f.local}
	server, err := controlsocket.Listen(t.Context(), f.local, "sample-wing", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{Principal: "sample-owner"}, func(ctx context.Context, r control.DirectRequest) control.DirectResponse {
			if r.Tool != "session_search" {
				t.Error("unexpected mutation")
			}
			var args map[string]any
			json.Unmarshal(r.Arguments, &args)
			rows, complete, err := eggclient.SessionSearchCatalog(ctx, cfg, "sample-wing", "Studio Mac", args["query"].(string), func(s eggclient.LocalSession) bool { return s.ID == "past-dns-run" })
			if err != nil {
				return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Error: err.Error()}
			}
			at := time.Now().UTC().Format(time.RFC3339)
			for i := range rows {
				rows[i].Routes = []fleet.Route{{WingID: "sample-wing", Kind: "direct", Healthy: true, ObservedAt: at}}
			}
			inv := fleet.Inventory{Version: fleet.Version, Query: "DNS helper", Complete: complete, Sessions: rows}
			data, _ := json.Marshal(inv)
			var result map[string]any
			json.Unmarshal(data, &result)
			return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Result: result}
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	h := northstarStartPTY(t, f, binary, 120, 36, "search", "DNS helper", "--local")
	h.expect("bounded owned search excerpt", 0, "dns-helper-repair", "Studio Mac", "[dns] [helper]", "Open: wt session action")
	<-h.done
	storyboardCapture(t, h, "13-4-search-excerpt")
}
