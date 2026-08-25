//go:build linux

package sandbox

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sort"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// networkBridge is the host half of the only network path inherited by a
// Linux sandbox. The child accepts loopback connections in its otherwise empty
// network namespace and passes each connected socket back with SCM_RIGHTS. The
// host validates the requested listener and connects it either to DomainProxy
// or to one explicitly allowed host-loopback port.
type networkBridge struct {
	parent    *os.File
	child     *os.File
	targets   map[uint16]string
	closeOnce sync.Once
	childOnce sync.Once
}

func newNetworkBridge(proxyPort int, localPorts []int) (*networkBridge, *os.File, error) {
	targets := make(map[uint16]string)
	addTarget := func(port int) error {
		if port < 1 || port > 65535 {
			return fmt.Errorf("invalid relay port %d", port)
		}
		key := uint16(port)
		if _, exists := targets[key]; exists {
			return fmt.Errorf("duplicate relay port %d", port)
		}
		targets[key] = net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port))
		return nil
	}
	if proxyPort > 0 {
		if err := addTarget(proxyPort); err != nil {
			return nil, nil, err
		}
	}
	for _, port := range localPorts {
		if err := addTarget(port); err != nil {
			return nil, nil, err
		}
	}
	if len(targets) == 0 {
		return nil, nil, errors.New("network relay has no targets")
	}

	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("socketpair: %w", err)
	}
	bridge := &networkBridge{
		parent:  os.NewFile(uintptr(pair[0]), "wt-net-relay-parent"),
		child:   os.NewFile(uintptr(pair[1]), "wt-net-relay-child"),
		targets: targets,
	}
	go bridge.serve()
	return bridge, bridge.child, nil
}

func (b *networkBridge) closeChild() {
	b.childOnce.Do(func() {
		if b.child != nil {
			_ = b.child.Close()
		}
	})
}

func (b *networkBridge) Close() {
	b.closeOnce.Do(func() {
		b.closeChild()
		if b.parent != nil {
			_ = b.parent.Close()
		}
	})
}

func (b *networkBridge) serve() {
	data := make([]byte, 2)
	oob := make([]byte, unix.CmsgSpace(4))
	for {
		n, oobn, _, _, err := unix.Recvmsg(int(b.parent.Fd()), data, oob, 0)
		if err != nil {
			if !errors.Is(err, os.ErrClosed) && !errors.Is(err, unix.EBADF) && !errors.Is(err, unix.ECONNRESET) {
				log.Printf("linux network relay: receive: %v", err)
			}
			return
		}
		if n != len(data) {
			continue
		}
		port := binary.BigEndian.Uint16(data)
		target, allowed := b.targets[port]
		messages, parseErr := unix.ParseSocketControlMessage(oob[:oobn])
		if parseErr != nil {
			continue
		}
		var received []int
		for _, message := range messages {
			rights, rightsErr := unix.ParseUnixRights(&message)
			if rightsErr == nil {
				received = append(received, rights...)
			}
		}
		if len(received) == 0 {
			continue
		}
		for _, extra := range received[1:] {
			_ = unix.Close(extra)
		}
		if !allowed {
			_ = unix.Close(received[0])
			log.Printf("linux network relay: rejected undeclared target port %d", port)
			continue
		}
		go bridgeRelayConnection(received[0], target)
	}
}

func bridgeRelayConnection(receivedFD int, target string) {
	file := os.NewFile(uintptr(receivedFD), "wt-net-relay-connection")
	client, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		return
	}
	upstream, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		_ = client.Close()
		return
	}
	closeBoth := func() {
		_ = client.Close()
		_ = upstream.Close()
	}
	go func() {
		_, _ = io.Copy(upstream, client)
		closeBoth()
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		closeBoth()
	}()
}

// startNamespaceRelays runs in _deny_init after CLONE_NEWNET. It brings up the
// isolated loopback device and binds only the proxy and explicit local ports.
// The inherited bridge FD is marked close-on-exec before the agent is spawned,
// so untrusted code can use the listeners but cannot forge relay targets.
func startNamespaceRelays(bridgeFD, proxyPort int, localPorts []int) error {
	if bridgeFD < 3 {
		return fmt.Errorf("invalid inherited relay fd %d", bridgeFD)
	}
	if err := bringLoopbackUp(); err != nil {
		return err
	}
	ports := append([]int(nil), localPorts...)
	if proxyPort > 0 {
		ports = append(ports, proxyPort)
	}
	sort.Ints(ports)
	for index, port := range ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("invalid namespace relay port %d", port)
		}
		if index > 0 && ports[index-1] == port {
			return fmt.Errorf("duplicate namespace relay port %d", port)
		}
		listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port)))
		if err != nil {
			return fmt.Errorf("listen on isolated loopback port %d: %w", port, err)
		}
		go serveNamespaceRelay(listener, bridgeFD, uint16(port))
	}
	unix.CloseOnExec(bridgeFD)
	return nil
}

func serveNamespaceRelay(listener net.Listener, bridgeFD int, port uint16) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		file, err := connection.(*net.TCPConn).File()
		_ = connection.Close()
		if err != nil {
			continue
		}
		data := make([]byte, 2)
		binary.BigEndian.PutUint16(data, port)
		err = unix.Sendmsg(bridgeFD, data, unix.UnixRights(int(file.Fd())), nil, 0)
		_ = file.Close()
		if err != nil {
			_ = listener.Close()
			return
		}
	}
}

func bringLoopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open loopback control socket: %w", err)
	}
	defer unix.Close(fd)
	request, err := unix.NewIfreq("lo")
	if err != nil {
		return fmt.Errorf("prepare loopback interface request: %w", err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, request); err != nil {
		return fmt.Errorf("read loopback interface flags: %w", err)
	}
	request.SetUint16(request.Uint16() | unix.IFF_UP)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, request); err != nil {
		return fmt.Errorf("bring isolated loopback up: %w", err)
	}
	return nil
}
