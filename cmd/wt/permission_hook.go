package main

import (
	"context"
	"flag"
	"github.com/ehrlich-b/wingthing/internal/permissionbridge"
	"github.com/ehrlich-b/wingthing/internal/permissions"
	"io"
	"os"
)

func permissionHook(args []string) {
	f := flag.NewFlagSet("_permission_hook", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	socket := f.String("socket", "", "")
	session := f.String("session", "", "")
	generation := f.String("generation", "", "")
	if f.Parse(args) != nil || f.NArg() != 0 {
		return
	}
	in, err := permissionbridge.ParseInput(os.Stdin)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), permissions.Lifetime)
	defer cancel()
	_ = permissions.Hook(ctx, *socket, *session, *generation, os.Getenv("WT_PERMISSION_TOKEN"), in, os.Stdout)
}
