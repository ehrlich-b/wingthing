package main

import (
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/relay"
)

func TestSaveLocalServeTokenPreservesOrdinaryPortalLogin(t *testing.T) {
	dir := t.TempDir()
	ordinary := auth.NewTokenStore(dir)
	if err := ordinary.Save(&auth.DeviceToken{Token: "hosted-login", DeviceID: "hosted"}); err != nil {
		t.Fatal(err)
	}
	if err := saveLocalServeToken(dir, "localhost-login"); err != nil {
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

	policy, cutoff, err := relayPolicyFromEnv()
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

	policy, cutoff, err := relayPolicyFromEnv()
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
	_, cutoff, err := relayPolicyFromEnv()
	if err != nil || cutoff.IsZero() {
		t.Fatalf("legacy cutoff: cutoff=%s err=%v", cutoff, err)
	}
}

func TestRelayPolicyFromEnvRejectsAmbiguousOrInvalidConfiguration(t *testing.T) {
	t.Run("unknown policy", func(t *testing.T) {
		t.Setenv("WT_RELAY_POLICY", "surprise")
		t.Setenv("WT_RELAY_MIGRATION_BEFORE", "")
		t.Setenv("WT_RELAY_GRANDFATHER_BEFORE", "")
		if _, _, err := relayPolicyFromEnv(); err == nil {
			t.Fatal("unknown policy accepted")
		}
	})
	t.Run("bad timestamp", func(t *testing.T) {
		t.Setenv("WT_RELAY_POLICY", relay.RelayPolicyDirectFree)
		t.Setenv("WT_RELAY_MIGRATION_BEFORE", "yesterday")
		t.Setenv("WT_RELAY_GRANDFATHER_BEFORE", "")
		if _, _, err := relayPolicyFromEnv(); err == nil {
			t.Fatal("invalid timestamp accepted")
		}
	})
	t.Run("conflicting aliases", func(t *testing.T) {
		t.Setenv("WT_RELAY_POLICY", relay.RelayPolicyDirectFree)
		t.Setenv("WT_RELAY_MIGRATION_BEFORE", "2026-08-26T00:00:00Z")
		t.Setenv("WT_RELAY_GRANDFATHER_BEFORE", "2026-08-25T00:00:00Z")
		if _, _, err := relayPolicyFromEnv(); err == nil {
			t.Fatal("conflicting migration timestamps accepted")
		}
	})
}
