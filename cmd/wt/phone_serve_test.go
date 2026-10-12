package main

import (
	"io"
	"testing"
)

func TestPhoneServeRejectsUnsafeConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"--addr", "0.0.0.0:8081"},
		{"--addr", "localhost:8081"},
		{"--origin", "http://phone.example"},
		{"--origin", "https://phone.example", "--upstream", "http://192.168.1.2:8080"},
		{"--origin", "https://phone.example", "--key-file", "/does-not-exist"},
	} {
		cmd := phoneServeCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Errorf("accepted unsafe configuration %v", args)
		}
	}
}
