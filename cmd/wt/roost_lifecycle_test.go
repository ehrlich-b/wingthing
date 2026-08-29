package main

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAwaitRoostReadyRequiresExactToken(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		wantErr bool
	}{
		{name: "ready", payload: roostReadyToken},
		{name: "empty", wantErr: true},
		{name: "partial", payload: "read", wantErr: true},
		{name: "extra", payload: roostReadyToken + "surprise", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader, writer := io.Pipe()
			go func() {
				_, _ = io.WriteString(writer, test.payload)
				_ = writer.Close()
			}()
			err := awaitRoostReady(reader, time.Second)
			if (err != nil) != test.wantErr {
				t.Fatalf("awaitRoostReady error = %v, wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestAwaitRoostReadyTimesOutAndClosesReader(t *testing.T) {
	reader, writer := io.Pipe()
	defer closeForTest(t, "readiness writer", writer)
	if err := awaitRoostReady(reader, 10*time.Millisecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
	if _, err := writer.Write([]byte("late")); err == nil {
		t.Fatal("readiness reader remained open after timeout")
	}
}

func TestSignalRoostReadyWritesOnlyToInheritedDescriptor(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, "readiness reader", reader)
	// signalRoostReady takes ownership of the inherited descriptor. Duplicate
	// the pipe end to model exec.ExtraFiles, then close the original os.File so
	// its finalizer cannot later close an unrelated descriptor that reused the
	// same integer (for example SQLite's WAL file in a subsequent test).
	inheritedFD, err := syscall.Dup(int(writer.Fd()))
	if err != nil {
		closeForTest(t, "readiness writer", writer)
		t.Fatal(err)
	}
	owned := true
	defer func() {
		if owned {
			_ = syscall.Close(inheritedFD)
		}
	}()
	closeForTest(t, "readiness writer", writer)
	t.Setenv(roostReadyFDEnv, strconv.Itoa(inheritedFD))
	if err := signalRoostReady(); err != nil {
		t.Fatal(err)
	}
	owned = false
	payload, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != roostReadyToken {
		t.Fatalf("readiness payload = %q", payload)
	}

	// Exercise finalizers before later tests open security or database files.
	// There must be no stale os.File left that can close a reused descriptor.
	runtime.GC()
	sentinel, err := os.CreateTemp(t.TempDir(), "fd-reuse-sentinel-")
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, "descriptor reuse sentinel", sentinel)
	if _, err := sentinel.WriteString("still open"); err != nil {
		t.Fatalf("write after readiness descriptor handoff: %v", err)
	}
}

func TestReplaceEnvironmentValueRemovesInheritedSpoof(t *testing.T) {
	got := replaceEnvironmentValue([]string{"A=1", roostReadyFDEnv + "=99", "B=2", roostReadyFDEnv + "=100"}, roostReadyFDEnv, "3")
	want := []string{"A=1", "B=2", roostReadyFDEnv + "=3"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("environment = %#v, want %#v", got, want)
	}
}

func TestSignalRoostReadyRejectsInvalidDescriptor(t *testing.T) {
	t.Setenv(roostReadyFDEnv, "stdout")
	if err := signalRoostReady(); err == nil {
		t.Fatal("invalid readiness descriptor accepted")
	}
}

func TestAwaitEmbeddedWingReadyRequiresConnectedStatus(t *testing.T) {
	reads := 0
	err := awaitEmbeddedWingReady(context.Background(), make(chan error), make(chan namedServerError), func() (*wingStatus, error) {
		reads++
		if reads == 1 {
			return &wingStatus{State: "connecting"}, nil
		}
		return &wingStatus{State: "connected"}, nil
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if reads < 2 {
		t.Fatalf("status reads = %d, want connecting followed by connected", reads)
	}
}

func TestAwaitEmbeddedWingReadyReportsEarlyFailures(t *testing.T) {
	t.Run("wing", func(t *testing.T) {
		wingErrors := make(chan error, 1)
		wingErrors <- errors.New("authentication transport failed")
		err := awaitEmbeddedWingReady(context.Background(), wingErrors, make(chan namedServerError), func() (*wingStatus, error) {
			return nil, os.ErrNotExist
		}, time.Second)
		if err == nil || !strings.Contains(err.Error(), "authentication transport failed") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("relay", func(t *testing.T) {
		relayErrors := make(chan namedServerError, 1)
		relayErrors <- namedServerError{listener: "browser HTTPS", err: os.ErrPermission}
		err := awaitEmbeddedWingReady(context.Background(), make(chan error), relayErrors, func() (*wingStatus, error) {
			return nil, os.ErrNotExist
		}, time.Second)
		if err == nil || !strings.Contains(err.Error(), "browser HTTPS") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("auth status", func(t *testing.T) {
		err := awaitEmbeddedWingReady(context.Background(), make(chan error), make(chan namedServerError), func() (*wingStatus, error) {
			return &wingStatus{State: "auth_failed", Error: "token rejected"}, nil
		}, time.Second)
		if err == nil || !strings.Contains(err.Error(), "token rejected") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestAwaitEmbeddedWingReadyTimesOut(t *testing.T) {
	err := awaitEmbeddedWingReady(context.Background(), make(chan error), make(chan namedServerError), func() (*wingStatus, error) {
		return &wingStatus{State: "connecting"}, nil
	}, 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v", err)
	}
}

func TestRoostWingExitResultHandlesCleanCancellationAndUnexpectedExit(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := roostWingExitResult(canceled, nil, nil); err != nil {
		t.Fatalf("clean canceled exit = %v", err)
	}
	if err := roostWingExitResult(context.Background(), nil, nil); err == nil || !strings.Contains(err.Error(), "wing exited unexpectedly") {
		t.Fatalf("unexpected nil exit = %v", err)
	}
	wingErr := errors.New("wing transport failed")
	shutdownErr := errors.New("relay shutdown failed")
	err := roostWingExitResult(context.Background(), wingErr, shutdownErr)
	if !errors.Is(err, wingErr) || !errors.Is(err, shutdownErr) {
		t.Fatalf("joined exit error = %v", err)
	}
}
