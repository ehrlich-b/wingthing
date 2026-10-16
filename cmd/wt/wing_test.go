package main

import (
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/auth"
)

func TestLocalOnlyWingRejectsRelayAndOrganizationFlags(t *testing.T) {
	for _, conflict := range [][]string{{"--local"}, {"--roost", "http://relay.example"}, {"--org", "example"}} {
		cmd := wingStartCmd()
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetArgs(append([]string{"--foreground", "--local-only"}, conflict...))
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--local-only cannot be combined") {
			t.Fatalf("conflicting local-only flags %v: %v", conflict, err)
		}
	}
}

func TestFormatUserIdentity(t *testing.T) {
	tests := []struct {
		name string
		info auth.UserInfo
		want string
	}{
		{
			"full info",
			auth.UserInfo{DisplayName: "Test User", Email: "user@example.com", Provider: "github"},
			"Test User (user@example.com) via github",
		},
		{
			"email only",
			auth.UserInfo{Email: "user@example.com", Provider: "google"},
			"user@example.com via google",
		},
		{
			"name only",
			auth.UserInfo{DisplayName: "Test User"},
			"Test User",
		},
		{
			"name and provider",
			auth.UserInfo{DisplayName: "Test User", Provider: "github"},
			"Test User via github",
		},
		{
			"user_id fallback",
			auth.UserInfo{UserID: "abc123"},
			"abc123",
		},
		{
			"empty",
			auth.UserInfo{},
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatUserIdentity(&tt.info)
			if got != tt.want {
				t.Errorf("formatUserIdentity(%+v) = %q, want %q", tt.info, got, tt.want)
			}
		})
	}
}
