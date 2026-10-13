package main

import (
	"bytes"
	"io"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

func TestPWAExplicitPhoneFleetConfigPreservesOtherGates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WINGTHING_DIR", dir)
	if err := config.SaveWingConfig(dir, &config.WingConfig{Locked: true, HostedRelay: config.HostedRelayDeny}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	execute := func(value string) error {
		return executeCLI(t.Context(), []string{"wing", "config", "set", "phone_fleet=" + value}, remotepkg.IO{Out: &output, ErrOut: io.Discard})
	}
	if err := execute("true"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWingConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PhoneFleet || !cfg.Locked || cfg.HostedRelay != config.HostedRelayDeny {
		t.Fatal("phone delegation changed unrelated gates")
	}
	if err := execute("maybe"); err == nil {
		t.Fatal("accepted nonboolean delegation")
	}
	if err := execute("false"); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.LoadWingConfig(dir)
	if err != nil || cfg.PhoneFleet {
		t.Fatal("delegation did not disable")
	}
}
