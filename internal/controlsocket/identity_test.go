package controlsocket

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestExpectedWingIdentityBindsEveryHandshake(t *testing.T) {
	dir := t.TempDir()
	var bound atomic.Int32
	s, err := Listen(t.Context(), dir, "wing-b", func(Hello) (Welcome, Handler, error) {
		bound.Add(1)
		return Welcome{}, nil, nil
	}, func(context.Context, Hello) (Welcome, AttachHandler, error) {
		bound.Add(1)
		return Welcome{}, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := WithExpectedWingID(t.Context(), "wing-a")
	if c, err := Dial(ctx, dir, Hello{}); err == nil {
		c.Close()
		t.Fatal("replacement wing admitted control")
	}
	for _, native := range []bool{false, true} {
		if stream, _, err := DialAttachment(ctx, dir, Hello{Attach: &Attachment{Session: "same-id", NativeTerminal: native}}); err == nil {
			stream.Close()
			t.Fatal("replacement wing admitted attachment")
		}
	}
	if _, err := Dial(ctx, dir, Hello{WingID: "wing-b"}); err == nil {
		t.Fatal("explicit hello bypassed caller pin")
	}
	if bound.Load() != 0 {
		t.Fatal("identity refusal reached operation binding")
	}
}
