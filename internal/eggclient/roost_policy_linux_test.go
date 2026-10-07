//go:build linux

package eggclient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// The Linux backend re-execs this test binary to install its namespace policy.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_deny_init" {
		sandbox.DenyInit(os.Args[2:])
		return
	}
	os.Exit(m.Run())
}

func TestRoostPolicyBlocksLinuxRootReplacement(t *testing.T) {
	if ok, help := sandbox.CheckCapability(); !ok {
		t.Skip(help)
	}
	for _, ancestor := range []bool{false, true} {
		t.Run(fmt.Sprint("ancestor=", ancestor), func(t *testing.T) {
			home, parent, _, wc := roostPolicyFixture(t)
			root := filepath.Join(parent, "role")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "egg.yaml"), []byte("base: none\nfs: [rw:"+home+"]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			replacement := filepath.Join(home, "replacement")
			if err := os.Mkdir(replacement, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(replacement, "egg.yaml"), []byte("base: none\nfs: [rw:/]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			wc.Paths[0].Path = root
			start := ws.PTYStart{UserID: "admin", OrgRole: "admin", CWD: root}
			cfg, _, err := PrepareBrowserLaunch(wc, &start, home, false, egg.DefaultEggConfig())
			if err != nil {
				t.Fatal(err)
			}
			sb, err := sandbox.New(cfg.ToSandboxConfig(home))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := sb.Destroy(); err != nil {
					t.Error(err)
				}
			})
			target := root
			if ancestor {
				target = parent
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd, err := sb.Exec(ctx, "/bin/sh", []string{"-c", `
set -e
printf ordinary > "$1/ordinary"
if mv "$2" "$2-old"; then
  mv "$3" "$2"
  exit 42
fi
`, "root-replacement", root, target, replacement})
			if err != nil {
				t.Fatal(err)
			}
			if output, err := cmd.CombinedOutput(); err != nil {
				if os.IsPermission(err) {
					t.Skipf("namespace creation unavailable: %v", err)
				}
				t.Fatalf("policy directory replacement or ordinary write failed: %v, %s", err, output)
			}
			if data, err := os.ReadFile(filepath.Join(root, "ordinary")); err != nil || string(data) != "ordinary" {
				t.Fatalf("ordinary role-root write failed: %q, %v", data, err)
			}
		})
	}
}
