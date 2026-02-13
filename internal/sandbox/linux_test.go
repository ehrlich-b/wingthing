//go:build linux

package sandbox

import (
	"os"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCloneFlagsNoNetwork(t *testing.T) {
	s := &linuxSandbox{cfg: Config{NetworkNeed: NetworkNone}}
	flags := s.cloneFlags()
	want := uintptr(syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNET)
	if flags != want {
		t.Errorf("NetworkNone cloneFlags = 0x%x, want 0x%x", flags, want)
	}
}

func TestCloneFlagsLocal(t *testing.T) {
	s := &linuxSandbox{cfg: Config{NetworkNeed: NetworkLocal}}
	flags := s.cloneFlags()
	want := uintptr(syscall.CLONE_NEWNS | syscall.CLONE_NEWPID)
	if flags != want {
		t.Errorf("NetworkLocal cloneFlags = 0x%x, want 0x%x", flags, want)
	}
	if flags&syscall.CLONE_NEWNET != 0 {
		t.Error("NetworkLocal should not set CLONE_NEWNET")
	}
}

func TestCloneFlagsFull(t *testing.T) {
	s := &linuxSandbox{cfg: Config{NetworkNeed: NetworkFull}}
	flags := s.cloneFlags()
	want := uintptr(syscall.CLONE_NEWNS | syscall.CLONE_NEWPID)
	if flags != want {
		t.Errorf("NetworkFull cloneFlags = 0x%x, want 0x%x", flags, want)
	}
	if flags&syscall.CLONE_NEWNET != 0 {
		t.Error("NetworkFull should not set CLONE_NEWNET")
	}
}

func TestSeccompFilterStructure(t *testing.T) {
	filter := buildSeccompFilter()
	nDenied := len(deniedSyscalls)

	// Expected: 1 (load) + nDenied (jeq checks) + 1 (allow) + 1 (deny)
	wantLen := nDenied + 3
	if len(filter) != wantLen {
		t.Fatalf("filter length = %d, want %d", len(filter), wantLen)
	}

	// First instruction: load syscall number
	load := filter[0]
	if load.Code != unix.BPF_LD|unix.BPF_W|unix.BPF_ABS {
		t.Errorf("load instruction code = 0x%x, want BPF_LD|BPF_W|BPF_ABS", load.Code)
	}
	if load.K != 0 {
		t.Errorf("load offset = %d, want 0 (seccomp_data.nr)", load.K)
	}

	// Check each deny-check instruction
	for i := 0; i < nDenied; i++ {
		inst := filter[1+i]
		if inst.Code != unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K {
			t.Errorf("filter[%d] code = 0x%x, want BPF_JMP|BPF_JEQ|BPF_K", 1+i, inst.Code)
		}
		if inst.K != deniedSyscalls[i] {
			t.Errorf("filter[%d] K = %d, want syscall %d", 1+i, inst.K, deniedSyscalls[i])
		}
		// Jt should jump to the deny instruction
		wantJt := uint8(nDenied - i)
		if inst.Jt != wantJt {
			t.Errorf("filter[%d] Jt = %d, want %d", 1+i, inst.Jt, wantJt)
		}
		if inst.Jf != 0 {
			t.Errorf("filter[%d] Jf = %d, want 0 (fall through)", 1+i, inst.Jf)
		}
	}

	// Allow instruction (second to last)
	allow := filter[len(filter)-2]
	if allow.Code != unix.BPF_RET|unix.BPF_K {
		t.Errorf("allow code = 0x%x, want BPF_RET|BPF_K", allow.Code)
	}
	if allow.K != seccompRetAllow {
		t.Errorf("allow K = 0x%x, want 0x%x", allow.K, seccompRetAllow)
	}

	// Deny instruction (last)
	deny := filter[len(filter)-1]
	if deny.Code != unix.BPF_RET|unix.BPF_K {
		t.Errorf("deny code = 0x%x, want BPF_RET|BPF_K", deny.Code)
	}
	wantDenyK := seccompRetErrno | uint32(unix.EPERM)
	if deny.K != wantDenyK {
		t.Errorf("deny K = 0x%x, want 0x%x", deny.K, wantDenyK)
	}
}

func TestSeccompDeniedSyscallsIncluded(t *testing.T) {
	filter := buildSeccompFilter()
	// Collect all syscall numbers checked in the filter
	checked := make(map[uint32]bool)
	for _, inst := range filter {
		if inst.Code == unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K {
			checked[inst.K] = true
		}
	}
	for _, nr := range deniedSyscalls {
		if !checked[nr] {
			t.Errorf("syscall %d not in seccomp filter", nr)
		}
	}
}

func TestRlimitNoDefaults(t *testing.T) {
	s := &linuxSandbox{cfg: Config{NetworkNeed: NetworkNone}}
	limits := s.rlimits()

	if len(limits) != 0 {
		t.Fatalf("rlimits count = %d, want 0 (no defaults)", len(limits))
	}
}

func TestRlimitConfigOverrides(t *testing.T) {
	s := &linuxSandbox{cfg: Config{
		NetworkNeed: NetworkNone,
		CPULimit:    300 * time.Second,
		MemLimit:    2 * 1024 * 1024 * 1024, // 2GB
		MaxFDs:      1024,
	}}
	limits := s.rlimits()

	expected := map[int]uint64{
		unix.RLIMIT_CPU:    300,
		unix.RLIMIT_AS:     4 * 1024 * 1024 * 1024, // 4GB floor (JIT needs virtual address space)
		unix.RLIMIT_NOFILE: 1024,
	}

	for _, rl := range limits {
		want, ok := expected[rl.resource]
		if !ok {
			continue
		}
		if rl.value != want {
			t.Errorf("rlimit %d value = %d, want %d", rl.resource, rl.value, want)
		}
	}
}

func TestRlimitOnlyExplicit(t *testing.T) {
	// Only CPU set — should only get CPU limit
	s := &linuxSandbox{cfg: Config{NetworkNeed: NetworkNone, CPULimit: 60 * time.Second}}
	limits := s.rlimits()
	if len(limits) != 1 {
		t.Fatalf("rlimits count = %d, want 1", len(limits))
	}
	if limits[0].resource != unix.RLIMIT_CPU || limits[0].value != 60 {
		t.Errorf("got resource=%d value=%d, want RLIMIT_CPU=60", limits[0].resource, limits[0].value)
	}
}

func TestSysProcAttrCloneflags(t *testing.T) {
	tests := []struct {
		need NetworkNeed
		want uintptr
	}{
		{NetworkNone, syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNET},
		{NetworkLocal, syscall.CLONE_NEWNS | syscall.CLONE_NEWPID},
		{NetworkHTTPS, syscall.CLONE_NEWNS | syscall.CLONE_NEWPID},
		{NetworkFull, syscall.CLONE_NEWNS | syscall.CLONE_NEWPID},
	}
	// sysProcAttr adds CLONE_NEWUSER for non-root
	var extra uintptr
	if os.Geteuid() != 0 {
		extra = syscall.CLONE_NEWUSER
	}
	for _, tt := range tests {
		s := &linuxSandbox{cfg: Config{NetworkNeed: tt.need}}
		attr := s.sysProcAttr()
		want := tt.want | extra
		if attr.Cloneflags != want {
			t.Errorf("NetworkNeed %v: Cloneflags = 0x%x, want 0x%x", tt.need, attr.Cloneflags, want)
		}
	}
}

func TestNetworkNeedClearsNewnet(t *testing.T) {
	tests := []struct {
		need    NetworkNeed
		wantNet bool // true = CLONE_NEWNET should be absent
	}{
		{NetworkNone, false},
		{NetworkLocal, true},
		{NetworkHTTPS, true},
		{NetworkFull, true},
	}
	for _, tt := range tests {
		s := &linuxSandbox{cfg: Config{NetworkNeed: tt.need}}
		flags := s.cloneFlags()
		hasNewnet := flags&syscall.CLONE_NEWNET != 0
		if tt.wantNet && hasNewnet {
			t.Errorf("NetworkNeed %v should clear CLONE_NEWNET", tt.need)
		}
		if !tt.wantNet && !hasNewnet {
			t.Errorf("NetworkNeed %v should keep CLONE_NEWNET", tt.need)
		}
	}
}
