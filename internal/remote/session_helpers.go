package remote

import (
	"context"
	"fmt"

	"github.com/ehrlich-b/wingthing/internal/config"
)

type IOContextKey struct{}

func Streams(ctx context.Context) IO {
	if streams, ok := ctx.Value(IOContextKey{}).(IO); ok {
		return streams
	}
	return ProcessIO()
}

func ConfiguredRemote(dir, name string) (config.Remote, error) {
	remotes, err := config.LoadRemotes(dir)
	if err != nil {
		return config.Remote{}, err
	}
	remote, exists := remotes[name]
	if !exists {
		return config.Remote{}, fmt.Errorf("unknown remote %q; configure it with 'wt remote add'", name)
	}
	return remote, nil
}
