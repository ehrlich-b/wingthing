package main

import (
	"os"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func TestPreviewChannelGuardPrecedesStateAndPreservesAgentArguments(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = old })
	if _, err := channelInvocationArgs([]string{"--expected-channel", "stable", "stop"}); err == nil {
		t.Fatal("cross-channel control accepted")
	}
	args, err := channelInvocationArgs([]string{"--expected-channel=preview", "egg", "codex", "--", "--expected-channel", "stable"})
	if err != nil || strings.Join(args, " ") != "egg codex -- --expected-channel stable" {
		t.Fatalf("provider argv changed: %v %v", args, err)
	}
	args, err = channelInvocationArgs([]string{"tool-call", "native", "--expected-channel", "stable"})
	if err != nil || strings.Join(args, " ") != "tool-call native --expected-channel stable" {
		t.Fatal("native tool argv changed")
	}
	if eggclient.EggPidMatchesSession(os.Getpid(), "fixture") {
		t.Fatal("preview accepted unrelated live process as orphan egg")
	}
}
