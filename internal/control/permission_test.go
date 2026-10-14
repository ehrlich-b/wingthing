package control

import "testing"

func TestPermissionContractIsExplicitAndHumanOnly(t *testing.T) {
	for _, name := range []string{"permission_list", "permission_respond"} {
		tool, ok := Lookup(name)
		if !ok || !tool.Supports(SurfaceHuman) || tool.InputSchema["additionalProperties"] != false || tool.Authority != AuthorityWing {
			t.Fatalf("invalid contract: %+v", tool)
		}
		for _, surface := range []Surface{SurfaceLocalMCP, SurfaceHTTPMCP, SurfaceDirectMCP} {
			if tool.Supports(surface) {
				t.Fatal("permission tool exposed to MCP")
			}
		}
		if name == "permission_respond" {
			if tool.Grant != "permission.respond" || len(tool.InputSchema["required"].([]string)) != 6 {
				t.Fatal("response binding incomplete")
			}
		} else if tool.Grant != "permission.read" {
			t.Fatal("read grants response authority")
		}
	}
}
