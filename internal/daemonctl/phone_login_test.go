package daemonctl

import (
	"reflect"
	"testing"
)

func TestPhoneLoginEnablesRelayAndPreservesMainWingArguments(t *testing.T) {
	wanted := []string{"wing", "--daemon", "--roost", "https://wingthing.ai", "--egg-config", "/fixture/main-egg.yaml"}
	for _, flag := range []string{"--local-only", "--local-only=true", "--local-only=false"} {
		args := append(append([]string(nil), wanted...), flag)
		before := append([]string(nil), args...)
		if got := relayArgsAfterLogin(args); !reflect.DeepEqual(got, wanted) {
			t.Fatal("login changed the main wing or kept it local-only", got)
		}
		if !reflect.DeepEqual(args, before) {
			t.Fatal("login mutated saved state")
		}
	}
	if got := relayArgsAfterLogin(wanted); !reflect.DeepEqual(got, wanted) {
		t.Fatal("registered wing arguments changed")
	}
}
