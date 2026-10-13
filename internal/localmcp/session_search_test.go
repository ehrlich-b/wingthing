package localmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/fleet"
)

func TestSessionSearchSchemaStrictArgumentsAndActualDispatch(t *testing.T) {
	s := inventoryFixture(t, "laptop-wing", "Laptop")
	peer := inventoryFixture(t, "desktop-wing", "Desktop")
	if err := config.SaveRemotes(s.Cfg.Dir, map[string]config.Remote{"desktop": {SSHTarget: "fixture", WingID: "desktop-wing", WingthingDir: "/fixture/state", ControlSocket: "/fixture/state/control.sock", ControlVersion: control.ContractVersion}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.remoteToolCall = func(ctx context.Context, name, operation string, args []byte) (map[string]any, error) {
		calls++
		if name != "desktop" || operation != "session_search" || strings.Contains(string(args), `"include_remotes":true`) {
			t.Fatal("unbounded remote search")
		}
		return peer.ToolSessionSearch(ctx, args)
	}
	tool, ok := control.Lookup("session_search")
	if !ok || tool.InputSchema["additionalProperties"] != false || tool.Grant != "terminal.read" || tool.Annotations["readOnlyHint"] != true {
		t.Fatalf("search contract: %+v", tool)
	}
	for _, arguments := range []string{`{}`, `null`, `{"query":""}`, `{"query":"   "}`, `{"query":"DNS","extra":true}`, `{"query":1}`, `{"query":"DNS","include_remotes":"true"}`, `{"query":"DNS","include_remotes":null}`, `{"query":"a b c d e f g h i"}`} {
		_, denied, err := s.callTool(t.Context(), "session_search", json.RawMessage(arguments))
		if !denied && err == nil {
			t.Fatalf("accepted %s", arguments)
		}
	}
	result, denied, protocolErr := s.callTool(t.Context(), "session_search", json.RawMessage(`{"query":"fixed routing"}`))
	if denied || protocolErr != nil {
		t.Fatalf("search dispatch denied: %+v %+v", result, protocolErr)
	}
	inv := asInventory(t, result)
	if calls != 1 || !inv.Complete || len(inv.Sessions) != 2 || inv.Sessions[0].OriginWingID == inv.Sessions[1].OriginWingID {
		t.Fatalf("actual search=%+v calls=%d", inv, calls)
	}
	s.enforcePathBounds = true
	s.allowedPaths = []string{"/personal"}
	if _, err := s.ToolSessionSearch(t.Context(), json.RawMessage(`{"query":"routing"}`)); err == nil {
		t.Fatal("scoped connection fanned out")
	}
	result, err := s.ToolSessionSearch(t.Context(), json.RawMessage(`{"query":"routing","include_remotes":false}`))
	if err != nil || len(asInventory(t, result).Sessions) != 1 {
		t.Fatalf("scoped local search: %+v %v", result, err)
	}
}

func TestSessionSearchRemoteAndAggregateBoundsPreservePinnedCoordinator(t *testing.T) {
	s := inventoryFixture(t, "laptop-wing", "Laptop")
	remotes := map[string]config.Remote{}
	for n := 0; n < 33; n++ {
		name := fmt.Sprintf("remote-%02d", n)
		remotes[name] = config.Remote{SSHTarget: "fixture", WingID: name, WingthingDir: "/fixture/state", ControlSocket: "/fixture/state/control.sock", ControlVersion: control.ContractVersion}
	}
	if err := config.SaveRemotes(s.Cfg.Dir, remotes); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s.remoteToolCall = func(ctx context.Context, name, operation string, args []byte) (map[string]any, error) {
		calls.Add(1)
		rows := []fleet.Session{}
		for n := 0; n < 8; n++ {
			rows = append(rows, fleet.Session{Identity: fleet.Identity{OriginWingID: name, SessionID: fmt.Sprintf("session-%d", n)}, Routes: []fleet.Route{}})
		}
		if name == "remote-31" {
			rows[7].Pinned = true
			rows[7].Role = "coordinator"
		}
		return inventoryMap(fleet.Inventory{Version: fleet.Version, WingID: name, Complete: true, Sessions: rows, Sources: []fleet.Source{}}), nil
	}
	result, err := s.ToolSessionSearch(t.Context(), json.RawMessage(`{"query":"routing"}`))
	if err != nil {
		t.Fatal(err)
	}
	inv := asInventory(t, result)
	if calls.Load() != 32 || inv.Complete || len(inv.Sessions) != 200 || !inv.Sessions[0].Pinned || inv.Sessions[0].OriginWingID != "remote-31" {
		t.Fatalf("bounded aggregate: calls=%d inv=%+v", calls.Load(), inv)
	}
	if len(inv.Sources) != 2 || inv.Sources[1].Healthy {
		t.Fatal("omitted remotes were not reported")
	}
}
