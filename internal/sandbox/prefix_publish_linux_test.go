//go:build linux

package sandbox

import (
	"bytes"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestPrefixPublicationWithoutExchangeNeverReplacesDestination(t *testing.T) {
	if blocked := os.Getenv("WT_TEST_PREFIX_RENAME_ERRNO"); blocked != "" {
		errno, err := strconv.Atoi(blocked)
		if err != nil {
			t.Fatal(err)
		}
		// Only this disposable subprocess/thread loses renameat2. Exercise the
		// real fallback without a production hook or filesystem assumptions.
		runtime.LockOSThread()
		filter := []unix.SockFilter{
			{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
			{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.SYS_RENAMEAT2, Jf: 1},
			{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(errno)},
			{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
		}
		program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			t.Skipf("seccomp unavailable: %v", err)
		}
		if _, _, err := unix.Syscall6(unix.SYS_PRCTL, unix.PR_SET_SECCOMP, 2, uintptr(unsafe.Pointer(&program)), 0, 0, 0); err != 0 {
			t.Skipf("seccomp unavailable: %v", err)
		}
		var logs bytes.Buffer
		previous := log.Writer()
		log.SetOutput(&logs)
		defer log.SetOutput(previous)
		for _, exists := range []bool{false, true} {
			dir := t.TempDir()
			dst := filepath.Join(dir, "settings.json")
			if exists {
				if err := os.WriteFile(dst, []byte("host"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			src := filepath.Join(dir, "session")
			if err := os.WriteFile(src, []byte("session"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := copyFile(src, dst); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(dst)
			if exists && (err != nil || string(data) != "host") || !exists && !os.IsNotExist(err) {
				t.Fatalf("fallback replaced host destination (exists=%v): %q, %v", exists, data, err)
			}
			conflicts, err := filepath.Glob(filepath.Join(dir, ".wingthing-conflict-settings.json-*"))
			if err != nil || len(conflicts) != 1 {
				t.Fatalf("session conflict missing: %v, %v", conflicts, err)
			}
			data, err = os.ReadFile(conflicts[0])
			if err != nil || string(data) != "session" || !strings.Contains(logs.String(), filepath.Base(conflicts[0])) {
				t.Fatalf("session version or conflict log missing: %q, %v, %s", data, err, &logs)
			}
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, errno := range []unix.Errno{unix.ENOSYS, unix.EINVAL, unix.EOPNOTSUPP} {
		cmd := exec.Command(exe, "-test.run=^TestPrefixPublicationWithoutExchangeNeverReplacesDestination$", "-test.v")
		cmd.Env = append(os.Environ(), "WT_TEST_PREFIX_RENAME_ERRNO="+strconv.Itoa(int(errno)))
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fallback %v: %v\n%s", errno, err, output)
		}
	}
}

func TestPrefixPublicationPreservesHostChangesDuringStaging(t *testing.T) {
	for _, change := range []string{"replace", "in-place", "create", "delete"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			name := ".claude.json"
			destination := filepath.Join(dir, name)
			if change != "create" {
				if err := os.WriteFile(destination, []byte("snapshot"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			parent, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			done := make(chan error, 1)
			go func() { done <- publishPinnedFile(reader, 0600, parent, name) }()
			// Writing more than the pipe capacity requires a read, proving
			// comparison has completed. Keep EOF pending during the host edit.
			capacity, err := unix.FcntlInt(writer.Fd(), unix.F_GETPIPE_SZ, 0)
			if err != nil {
				t.Fatal(err)
			}
			session := strings.Repeat("session", capacity/len("session")+1)
			if _, err := writer.Write([]byte(session)); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "replace":
				if err := os.WriteFile(filepath.Join(dir, "host-new"), []byte("host"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(dir, "host-new"), destination); err != nil {
					t.Fatal(err)
				}
			case "in-place", "create":
				if err := os.WriteFile(destination, []byte("host"), 0600); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if err := os.Remove(destination); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("prefix publication did not finish")
			}
			conflicts, err := filepath.Glob(filepath.Join(dir, ".wingthing-conflict-*"))
			if err != nil || len(conflicts) != 1 {
				t.Fatalf("host edit was not preserved as a conflict: %v, %v", conflicts, err)
			}
			versions := make(map[string]bool)
			for _, path := range append(conflicts, destination) {
				data, err := os.ReadFile(path)
				if os.IsNotExist(err) && change == "delete" {
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				versions[string(data)] = true
			}
			if !versions[session] || (change != "delete" && !versions["host"]) {
				t.Fatalf("lost a concurrent version: session=%v host=%v", versions[session], versions["host"])
			}
			if change == "delete" {
				if _, err := os.Lstat(destination); !os.IsNotExist(err) {
					t.Fatalf("session undid host deletion: %v", err)
				}
			}
		})
	}
}
