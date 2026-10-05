package main

import (
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestPreviewDoctorFixNeverEntersHostPolicyInstaller(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = old })
	cmd := doctorCmd()
	cmd.SetArgs([]string{"--fix"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "preview does not install or replace host sandbox policy") {
		t.Fatalf("preview fix must fail before platform installer: %v", err)
	}
}
