//go:build linux

package auth

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestCredentialStagingNeverCreatesUnmaskedTemporaryFiles(t *testing.T) {
	for _, kind := range []string{"device token", "local token", "wing key"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "eggs"), 0700); err != nil {
				t.Fatal(err)
			}
			fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			if _, err := unix.InotifyAddWatch(fd, dir, unix.IN_CREATE); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "device token":
				err = NewTokenStore(dir).Save(&DeviceToken{Token: "secret"})
			case "local token":
				err = NewLocalTokenStore(dir).Save(&DeviceToken{Token: "secret"})
			case "wing key":
				_, err = EnsureKeyPair(dir)
			}
			if err != nil {
				t.Fatal(err)
			}
			buffer := make([]byte, 4096)
			count, err := unix.Read(fd, buffer)
			if err == unix.EAGAIN {
				return // Creation happened only within the permanently masked eggs tree.
			}
			if err != nil {
				t.Fatal(err)
			}
			for offset := 0; offset < count; {
				event := (*unix.InotifyEvent)(unsafe.Pointer(&buffer[offset]))
				start := offset + unix.SizeofInotifyEvent
				name := unix.ByteSliceToString(buffer[start : start+int(event.Len)])
				t.Errorf("credential staging created readable unmasked name %s", name)
				offset = start + int(event.Len)
			}
		})
	}
}
