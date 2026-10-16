//go:build linux

package agent

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestOutputCancellationReleasesBlockedReader(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	// Keep a writer alive that discovery cannot kill: this process owns it.
	// Closing it on failure also releases a reader stuck in a blocking syscall.
	defer writer.Close()

	reading := make(chan struct{})
	reentered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		var b [1]byte
		close(reading)
		if _, err := reader.Read(b[:]); err != nil {
			done <- err
			return
		}
		close(reentered)
		_, err := reader.Read(b[:])
		done <- err
	}()
	<-reading
	select {
	case err := <-done:
		t.Fatalf("initial Read returned before cancellation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(closed)
		stopOutputWriters(reader)
		// Deterministically model a parser waking and re-entering Read during
		// discovery, before cancellation closes the read end.
		if _, err := writer.Write([]byte{'x'}); err != nil {
			done <- err
			return
		}
		<-reentered
		time.Sleep(20 * time.Millisecond)
		_ = reader.Close()
	})
	defer stop()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("Read = %v, want closed pipe", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not release blocked Read within one second")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not finish Close within one second")
	}
}
