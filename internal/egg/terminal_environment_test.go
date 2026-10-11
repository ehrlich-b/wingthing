package egg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTerminalToolchainEnvironmentPreservesExplicitHome(t *testing.T) {
	for _, mode := range []string{"default", "explicit", "all"} {
		t.Run(mode, func(t *testing.T) {
			root := terminalDogfoodDir(t)
			home, toolchainHome := filepath.Join(root, "home"), filepath.Join(root, "pinned-elan")
			t.Setenv("HOME", home)
			t.Setenv("ELAN_HOME", toolchainHome)
			t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
			t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(root, "runtime", "bus"))
			cfg := DefaultEggConfig()
			switch mode {
			case "explicit":
				cfg.Env = append(cfg.Env, "ELAN_HOME")
			case "all":
				cfg.Env = EnvField{"*"}
			}
			// The home used to resolve filesystem policy must not retarget
			// toolchain environment values to a cache or an isolated user home.
			env := cfg.BuildEnvMap(filepath.Join(root, "isolated-home"))
			if mode == "default" {
				if _, ok := env["ELAN_HOME"]; ok {
					t.Fatal("default terminal leaked the host's ELAN_HOME")
				}
			} else if env["ELAN_HOME"] != toolchainHome {
				t.Fatalf("toolchain home = %q, want %q", env["ELAN_HOME"], toolchainHome)
			}
			if mode != "all" {
				for _, key := range []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
					if _, ok := env[key]; ok {
						t.Fatalf("terminal implicitly inherited host service endpoint %s", key)
					}
				}
			}
			if os.Getenv("HOME") != home || os.Getenv("ELAN_HOME") != toolchainHome {
				t.Fatal("building an egg environment changed the user's toolchain home")
			}
		})
	}
}
