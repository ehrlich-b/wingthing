package main

import (
	"context"
	"flag"
	"github.com/ehrlich-b/wingthing/internal/permissions"
	"io"
	"os"
)

func permissionHook(args []string) {
	f := flag.NewFlagSet("_permission_hook", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	socket := f.String("socket", "", "")
	session := f.String("session", "", "")
	provider := f.String("provider", "", "")
	brokerPID := f.Int("broker-pid", -1, "")
	generation := f.String("generation", "", "")
	if f.Parse(args) != nil || f.NArg() != 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), permissions.Lifetime)
	defer cancel()
	_ = permissions.HookNative(ctx, *socket, *session, *generation, *provider, *brokerPID, os.Stdin, os.Stdout)
}
