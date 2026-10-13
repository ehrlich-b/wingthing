package controlsocket

import (
	"context"
	"errors"
)

type expectedWingKey struct{}

// WithExpectedWingID binds each operation's handshake, including reconnects,
// to the receiving wing selected by the caller's verified route.
func WithExpectedWingID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, expectedWingKey{}, id)
}

func ExpectedWingID(ctx context.Context) string {
	id, _ := ctx.Value(expectedWingKey{}).(string)
	return id
}

func expectedHello(ctx context.Context, hello Hello) (Hello, error) {
	if expected := ExpectedWingID(ctx); expected != "" {
		if hello.WingID != "" && hello.WingID != expected {
			return Hello{}, errors.New("pinned wing identity conflicts with requested handshake")
		}
		hello.WingID = expected
	}
	return hello, nil
}
