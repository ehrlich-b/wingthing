package localrelay

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/relay"
)

func TestLoadServeRuntimeDetectsFlyRoleBeforeConfigCreatesDataDirectory(t *testing.T) {
	t.Run("unmounted edge", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "data")
		t.Setenv("WINGTHING_DIR", dataDir)
		t.Setenv("FLY_MACHINE_ID", "edge-machine")
		t.Setenv("FLY_REGION", "lhr")
		t.Setenv("FLY_APP_NAME", "wingthing-test")
		t.Setenv("WT_NODE_ROLE", "")
		t.Setenv("WT_LOGIN_ADDR", "")

		runtime, err := LoadServeRuntime(dataDir)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.NodeRole != "edge" || !runtime.AutoRole {
			t.Fatalf("role = %q auto=%v, want auto-detected edge", runtime.NodeRole, runtime.AutoRole)
		}
		if want := "http://login.process.wingthing-test.internal:8080"; runtime.LoginAddr != want || !runtime.AutoLogin {
			t.Fatalf("login address = %q auto=%v, want %q", runtime.LoginAddr, runtime.AutoLogin, want)
		}
		if _, err := os.Stat(dataDir); err != nil {
			t.Fatalf("config load did not create its state directory after role detection: %v", err)
		}
	})

	t.Run("mounted login", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "data")
		if err := os.Mkdir(dataDir, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("WINGTHING_DIR", filepath.Join(dataDir, ".wingthing"))
		t.Setenv("FLY_MACHINE_ID", "login-machine")
		t.Setenv("FLY_APP_NAME", "wingthing-test")
		t.Setenv("WT_NODE_ROLE", "")
		t.Setenv("WT_LOGIN_ADDR", "")

		runtime, err := LoadServeRuntime(dataDir)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.NodeRole != "login" || !runtime.AutoRole {
			t.Fatalf("role = %q auto=%v, want auto-detected login", runtime.NodeRole, runtime.AutoRole)
		}
		if runtime.LoginAddr != "" || runtime.AutoLogin {
			t.Fatalf("login node derived an edge address: %q auto=%v", runtime.LoginAddr, runtime.AutoLogin)
		}
	})
}

func TestLoadServeRuntimePreservesExplicitNodeRoleAndLoginAddress(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WINGTHING_DIR", filepath.Join(dataDir, ".wingthing"))
	t.Setenv("FLY_MACHINE_ID", "edge-machine")
	t.Setenv("FLY_APP_NAME", "wingthing-test")
	t.Setenv("WT_NODE_ROLE", "edge")
	t.Setenv("WT_LOGIN_ADDR", "http://login.internal:9090")

	runtime, err := LoadServeRuntime(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.NodeRole != "edge" || runtime.AutoRole {
		t.Fatalf("role = %q auto=%v, want explicit edge", runtime.NodeRole, runtime.AutoRole)
	}
	if runtime.LoginAddr != "http://login.internal:9090" || runtime.AutoLogin {
		t.Fatalf("login address = %q auto=%v, want explicit address", runtime.LoginAddr, runtime.AutoLogin)
	}
}

func TestSaveLocalServeTokenPreservesOrdinaryPortalLogin(t *testing.T) {
	dir := t.TempDir()
	ordinary := auth.NewTokenStore(dir)
	if err := ordinary.Save(&auth.DeviceToken{Token: "hosted-login", DeviceID: "hosted"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveLocalServeToken(dir, "localhost-login"); err != nil {
		t.Fatal(err)
	}
	hosted, err := ordinary.Load()
	if err != nil {
		t.Fatal(err)
	}
	local, err := auth.NewLocalTokenStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if hosted.Token != "hosted-login" || local.Token != "localhost-login" || local.DeviceID != "local" {
		t.Fatalf("saved credentials: hosted=%#v local=%#v", hosted, local)
	}
}

func TestRelayPolicyFromEnvDefaultsPrivateGatewaysToLegacy(t *testing.T) {
	t.Setenv("WT_RELAY_POLICY", "")
	t.Setenv("WT_RELAY_MIGRATION_BEFORE", "")
	t.Setenv("WT_RELAY_GRANDFATHER_BEFORE", "")

	policy, cutoff, err := RelayPolicyFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if policy != relay.RelayPolicyLegacy || !cutoff.IsZero() {
		t.Fatalf("default policy = %q cutoff=%s", policy, cutoff)
	}
}

func TestRelayPolicyFromEnvRequiresExplicitHostedMigrationState(t *testing.T) {
	t.Setenv("WT_RELAY_POLICY", relay.RelayPolicyDirectFree)
	t.Setenv("WT_RELAY_MIGRATION_BEFORE", "2026-08-26T00:00:00Z")
	t.Setenv("WT_RELAY_GRANDFATHER_BEFORE", "")

	policy, cutoff, err := RelayPolicyFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	if policy != relay.RelayPolicyDirectFree || !cutoff.Equal(want) {
		t.Fatalf("hosted policy = %q cutoff=%s", policy, cutoff)
	}
}

func TestRelayPolicyFromEnvAcceptsCompatibleLegacyCutoffName(t *testing.T) {
	t.Setenv("WT_RELAY_POLICY", relay.RelayPolicyDirectFree)
	t.Setenv("WT_RELAY_MIGRATION_BEFORE", "")
	t.Setenv("WT_RELAY_GRANDFATHER_BEFORE", "2026-08-26T00:00:00Z")
	_, cutoff, err := RelayPolicyFromEnv()
	if err != nil || cutoff.IsZero() {
		t.Fatalf("legacy cutoff: cutoff=%s err=%v", cutoff, err)
	}
}

func TestRelayPolicyFromEnvRejectsAmbiguousOrInvalidConfiguration(t *testing.T) {
	t.Run("unknown policy", func(t *testing.T) {
		t.Setenv("WT_RELAY_POLICY", "surprise")
		t.Setenv("WT_RELAY_MIGRATION_BEFORE", "")
		t.Setenv("WT_RELAY_GRANDFATHER_BEFORE", "")
		if _, _, err := RelayPolicyFromEnv(); err == nil {
			t.Fatal("unknown policy accepted")
		}
	})
	t.Run("bad timestamp", func(t *testing.T) {
		t.Setenv("WT_RELAY_POLICY", relay.RelayPolicyDirectFree)
		t.Setenv("WT_RELAY_MIGRATION_BEFORE", "yesterday")
		t.Setenv("WT_RELAY_GRANDFATHER_BEFORE", "")
		if _, _, err := RelayPolicyFromEnv(); err == nil {
			t.Fatal("invalid timestamp accepted")
		}
	})
	t.Run("conflicting aliases", func(t *testing.T) {
		t.Setenv("WT_RELAY_POLICY", relay.RelayPolicyDirectFree)
		t.Setenv("WT_RELAY_MIGRATION_BEFORE", "2026-08-26T00:00:00Z")
		t.Setenv("WT_RELAY_GRANDFATHER_BEFORE", "2026-08-25T00:00:00Z")
		if _, _, err := RelayPolicyFromEnv(); err == nil {
			t.Fatal("conflicting migration timestamps accepted")
		}
	})
}
