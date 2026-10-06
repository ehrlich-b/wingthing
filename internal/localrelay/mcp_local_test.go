package localrelay

import (
	"reflect"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/relay"
)

func TestLocalAndHTTPMCPShareControlRegistry(t *testing.T) {
	local := make(map[string]localmcp.LocalMCPTool)
	for _, tool := range localmcp.LocalMCPTools() {
		local[tool.Name] = tool
	}
	cfg := &config.Config{WingID: "embedded-wing"}
	native := roostMCPControlTools("dev", relay.NewServer(nil, relay.ServerConfig{}), cfg, false)
	httpDefinitions := control.Tools(control.SurfaceHTTPMCP)
	if len(native) != len(httpDefinitions) {
		t.Fatalf("HTTP native tools = %d, registry = %d", len(native), len(httpDefinitions))
	}
	for index, want := range httpDefinitions {
		got := native[index]
		if got.Name != want.Name || got.Title != want.Title || got.Description != want.Description {
			t.Errorf("HTTP tool %d metadata = %q/%q/%q, want %q/%q/%q",
				index, got.Name, got.Title, got.Description, want.Name, want.Title, want.Description)
		}
		if !reflect.DeepEqual(got.InputSchema, want.InputSchema) {
			t.Errorf("%s HTTP schema differs from registry", want.Name)
		}
		if !reflect.DeepEqual(got.Annotations, want.Annotations) {
			t.Errorf("%s HTTP annotations differ from registry", want.Name)
		}
		if want.Authority == control.AuthorityWing && !reflect.DeepEqual(local[want.Name].InputSchema, want.InputSchema) {
			t.Errorf("%s local schema differs from registry", want.Name)
		}
	}
}
