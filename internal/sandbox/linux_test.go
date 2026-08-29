//go:build linux

package sandbox

import (
	"net"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNetworkBridgeConnectionLimit(t *testing.T) {
	bridge := &networkBridge{
		connections: make(chan struct{}, 1),
		active:      make(map[net.Conn]struct{}),
	}
	if !bridge.acquireConnection() {
		t.Fatal("first bridge connection was rejected")
	}
	if bridge.acquireConnection() {
		t.Fatal("bridge accepted a connection beyond its limit")
	}
	<-bridge.connections
	if !bridge.acquireConnection() {
		t.Fatal("bridge did not release connection capacity")
	}
	<-bridge.connections
	bridge.Close()
	if bridge.acquireConnection() {
		t.Fatal("closed bridge accepted a connection")
	}
}

func TestNetworkBridgeCloseClosesTrackedConnections(t *testing.T) {
	bridge := &networkBridge{
		connections: make(chan struct{}, 1),
		active:      make(map[net.Conn]struct{}),
	}
	if !bridge.acquireConnection() {
		t.Fatal("bridge connection was rejected")
	}
	client, clientPeer := net.Pipe()
	upstream, upstreamPeer := net.Pipe()
	defer clientPeer.Close()
	defer upstreamPeer.Close()
	cleanup, ok := bridge.trackConnections(client, upstream)
	if !ok {
		t.Fatal("bridge did not track active connections")
	}

	bridge.Close()
	for name, peer := range map[string]net.Conn{"client": clientPeer, "upstream": upstreamPeer} {
		_ = peer.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := peer.Read(make([]byte, 1)); err == nil {
			t.Errorf("%s connection remained open after bridge close", name)
		}
	}
	cleanup()
	cleanup()
	if got := len(bridge.connections); got != 0 {
		t.Fatalf("bridge connection slots after cleanup = %d, want 0", got)
	}
}

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
	want := uintptr(syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNET)
	if flags != want {
		t.Errorf("NetworkLocal cloneFlags = 0x%x, want 0x%x", flags, want)
	}
	if flags&syscall.CLONE_NEWNET == 0 {
		t.Error("NetworkLocal should set CLONE_NEWNET")
	}
}

func TestCloneFlagsFull(t *testing.T) {
	s := &linuxSandbox{cfg: Config{NetworkNeed: NetworkFull}}
	flags := s.cloneFlags()
	want := uintptr(syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNET)
	if flags != want {
		t.Errorf("NetworkFull cloneFlags = 0x%x, want 0x%x", flags, want)
	}
	if flags&syscall.CLONE_NEWNET == 0 {
		t.Error("NetworkFull should set CLONE_NEWNET")
	}
}

func TestSeccompFilterStructure(t *testing.T) {
	filter := buildSeccompFilter()
	allDenied := append(deniedSyscallsCommon, deniedSyscallsArch...)
	nDenied := len(allDenied)
	denyIdx := len(filter) - 1

	// Optional ABI prologue (present when seccompNativeArch != 0): load arch,
	// allow the native arch through, deny everything else.
	idx := 0
	if seccompNativeArch != 0 {
		if filter[0].Code != unix.BPF_LD|unix.BPF_W|unix.BPF_ABS || filter[0].K != 4 {
			t.Errorf("filter[0] = code 0x%x K %d, want load of seccomp_data.arch (offset 4)", filter[0].Code, filter[0].K)
		}
		if filter[1].Code != unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K || filter[1].K != seccompNativeArch || filter[1].Jt != 1 {
			t.Errorf("filter[1] should JEQ native arch 0x%x with Jt=1", seccompNativeArch)
		}
		if filter[2].Code != unix.BPF_RET|unix.BPF_K {
			t.Errorf("filter[2] should deny on non-native arch")
		}
		idx = 3
	}

	// Load syscall number.
	if filter[idx].Code != unix.BPF_LD|unix.BPF_W|unix.BPF_ABS || filter[idx].K != 0 {
		t.Errorf("filter[%d] = code 0x%x K %d, want load of seccomp_data.nr (offset 0)", idx, filter[idx].Code, filter[idx].K)
	}
	idx++

	// Optional x32 range rejection (present when seccompX32Bit != 0).
	if seccompX32Bit != 0 {
		x := filter[idx]
		if x.Code != unix.BPF_JMP|unix.BPF_JGE|unix.BPF_K || x.K != seccompX32Bit {
			t.Errorf("filter[%d] should JGE x32 bit 0x%x", idx, seccompX32Bit)
		}
		if x.Jt != uint8(denyIdx-idx-1) {
			t.Errorf("filter[%d] x32 Jt = %d, want %d (jump to deny)", idx, x.Jt, denyIdx-idx-1)
		}
		idx++
	}

	// Each denied-syscall check jumps to the final deny on match.
	for i := 0; i < nDenied; i++ {
		p := idx + i
		inst := filter[p]
		if inst.Code != unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K {
			t.Errorf("filter[%d] code = 0x%x, want BPF_JMP|BPF_JEQ|BPF_K", p, inst.Code)
		}
		if inst.K != allDenied[i] {
			t.Errorf("filter[%d] K = %d, want syscall %d", p, inst.K, allDenied[i])
		}
		if inst.Jt != uint8(denyIdx-p-1) {
			t.Errorf("filter[%d] Jt = %d, want %d (jump to deny)", p, inst.Jt, denyIdx-p-1)
		}
		if inst.Jf != 0 {
			t.Errorf("filter[%d] Jf = %d, want 0 (fall through)", p, inst.Jf)
		}
	}

	// Total: prologue (idx) + nDenied checks + allow + deny.
	if wantLen := idx + nDenied + 2; len(filter) != wantLen {
		t.Fatalf("filter length = %d, want %d", len(filter), wantLen)
	}

	// Allow instruction (second to last).
	allow := filter[len(filter)-2]
	if allow.Code != unix.BPF_RET|unix.BPF_K || allow.K != seccompRetAllow {
		t.Errorf("allow = code 0x%x K 0x%x, want BPF_RET|BPF_K / 0x%x", allow.Code, allow.K, seccompRetAllow)
	}

	// Deny instruction (last).
	deny := filter[denyIdx]
	wantDenyK := seccompRetErrno | uint32(unix.EPERM)
	if deny.Code != unix.BPF_RET|unix.BPF_K || deny.K != wantDenyK {
		t.Errorf("deny = code 0x%x K 0x%x, want BPF_RET|BPF_K / 0x%x", deny.Code, deny.K, wantDenyK)
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
	allDenied := append(deniedSyscallsCommon, deniedSyscallsArch...)
	for _, nr := range allDenied {
		if !checked[nr] {
			t.Errorf("syscall %d not in seccomp filter", nr)
		}
	}
}

func TestSeccompMinimumDenyCount(t *testing.T) {
	allDenied := append(deniedSyscallsCommon, deniedSyscallsArch...)
	// Common has 26 syscalls, amd64 adds 3 more
	if len(allDenied) < 26 {
		t.Errorf("total denied syscalls = %d, want >= 26", len(allDenied))
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
		{NetworkLocal, syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNET},
		{NetworkHTTPS, syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNET},
		{NetworkFull, syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNET},
	}
	for _, tt := range tests {
		s := &linuxSandbox{cfg: Config{NetworkNeed: tt.need}, userNamespace: true}
		attr := s.sysProcAttr()
		want := tt.want | syscall.CLONE_NEWUSER
		if attr.Cloneflags != want {
			t.Errorf("NetworkNeed %v: Cloneflags = 0x%x, want 0x%x", tt.need, attr.Cloneflags, want)
		}
	}
}

func TestSysProcAttrUsesCapabilitiesNotEffectiveUID(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		userNamespace bool
		wantNewUser   bool
	}{
		{name: "capability available", userNamespace: false, wantNewUser: false},
		{name: "reduced capability root or ordinary user", userNamespace: true, wantNewUser: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s := &linuxSandbox{cfg: Config{NetworkNeed: NetworkNone}, userNamespace: testCase.userNamespace}
			got := s.sysProcAttr().Cloneflags&syscall.CLONE_NEWUSER != 0
			if got != testCase.wantNewUser {
				t.Fatalf("CLONE_NEWUSER = %v, want %v", got, testCase.wantNewUser)
			}
		})
	}
}

func TestEveryNetworkNeedKeepsNewnet(t *testing.T) {
	for _, need := range []NetworkNeed{NetworkNone, NetworkLocal, NetworkHTTPS, NetworkFull} {
		s := &linuxSandbox{cfg: Config{NetworkNeed: need}}
		if s.cloneFlags()&syscall.CLONE_NEWNET == 0 {
			t.Errorf("NetworkNeed %v should keep CLONE_NEWNET", need)
		}
	}
}

func TestEverySandboxPolicyUsesEnforcementWrapper(t *testing.T) {
	for name, cfg := range map[string]Config{
		"empty":      {},
		"deny":       {Deny: []string{"/secret"}},
		"deny write": {DenyWrite: []string{"/policy.yaml"}},
		"mount":      {Mounts: []Mount{{Source: "/tmp", Target: "/tmp"}}},
		"proxy":      {ProxyPort: 1234},
		"local port": {LocalPorts: []int{4321}},
	} {
		t.Run(name, func(t *testing.T) {
			if !(&linuxSandbox{cfg: cfg}).needsEnforcementWrapper() {
				t.Fatal("sandbox policy would bypass the seccomp/mount enforcement wrapper")
			}
		})
	}
}
