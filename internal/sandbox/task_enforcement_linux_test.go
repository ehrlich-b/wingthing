//go:build linux

package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestLinuxSandboxRefusesMissingControls(t *testing.T) {
	for _, cfg := range []Config{{NetworkNeed: NetworkNone}, {MemLimit: 1024}, {PidLimit: 8}} {
		_, err := newLinuxSandbox(cfg, func(bool) error { return errors.New("namespace unavailable") }, func(string, uint64, uint32) (*cgroupManager, error) {
			t.Fatal("cgroup reached after namespace refusal")
			return nil, nil
		})
		if err == nil || !strings.Contains(err.Error(), "namespace unavailable") {
			t.Fatalf("absent enforcement admitted: %v", err)
		}
	}
	for _, cfg := range []Config{{MemLimit: 1024}, {PidLimit: 8}} {
		for _, cause := range []error{nil, errors.New("delegation denied")} {
			_, err := newLinuxSandbox(cfg, func(bool) error { return nil }, func(string, uint64, uint32) (*cgroupManager, error) { return nil, cause })
			if err == nil || !strings.Contains(err.Error(), "no agent started") {
				t.Fatalf("missing cgroup admitted: %v", err)
			}
		}
	}
}

func TestLinuxSandboxCgroupAttachedBeforeExec(t *testing.T) {
	dir := testsocket.Dir(t)
	s := &linuxSandbox{cfg: Config{NetworkNeed: NetworkNone}, tmpDir: dir, cgroup: &cgroupManager{path: dir}}
	t.Cleanup(func() {
		if s.cgroupFile != nil {
			s.cgroupFile.Close()
		}
	})
	cmd, err := s.Exec(context.Background(), "/bin/true", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cmd.SysProcAttr.UseCgroupFD || cmd.SysProcAttr.CgroupFD != int(s.cgroupFile.Fd()) || cmd.SysProcAttr.Cloneflags == 0 {
		t.Fatalf("required controls missing: %+v", cmd.SysProcAttr)
	}
	// Never start this command: this is a bounded construction test.
}
