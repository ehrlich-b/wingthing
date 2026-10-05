package eggclient

import (
	"bytes"
	"context"

	"io"

	"testing"
)

func TestRemoteSessionBufferBoundsLargeWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buffer := remoteSessionBuffer{limit: 64 << 10, stream: "stderr", cancel: cancel}
	input := bytes.Repeat([]byte("x"), buffer.limit+1)
	// io.Copy must not find a promoted bytes.Buffer.ReadFrom that bypasses Write.
	n, err := io.Copy(&buffer, bytes.NewReader(input))
	if n != int64(buffer.limit) || err == nil || buffer.buffer.Len() != buffer.limit || ctx.Err() == nil {
		t.Fatalf("copy = %d, %v; buffer length = %d; context = %v", n, err, buffer.buffer.Len(), ctx.Err())
	}
	if n, err := buffer.Write(input); n != 0 || err == nil || buffer.buffer.Len() != buffer.limit {
		t.Fatalf("write after overflow = %d, %v; length = %d", n, err, buffer.buffer.Len())
	}
}
