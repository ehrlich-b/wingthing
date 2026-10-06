package main

import (
	"testing"

	"github.com/ehrlich-b/wingthing/internal/auth"
)

func TestFormatUserIdentity(t *testing.T) {
	tests := []struct {
		name string
		info auth.UserInfo
		want string
	}{
		{
			"full info",
			auth.UserInfo{DisplayName: "Phil Heckel", Email: "phil@test.com", Provider: "github"},
			"Phil Heckel (phil@test.com) via github",
		},
		{
			"email only",
			auth.UserInfo{Email: "phil@test.com", Provider: "google"},
			"phil@test.com via google",
		},
		{
			"name only",
			auth.UserInfo{DisplayName: "Phil Heckel"},
			"Phil Heckel",
		},
		{
			"name and provider",
			auth.UserInfo{DisplayName: "Phil Heckel", Provider: "github"},
			"Phil Heckel via github",
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
