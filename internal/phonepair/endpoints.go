package phonepair

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DiscoveryName identifies one wing, not a display name or another machine.
func DiscoveryName(wingID string) string {
	hash := sha256.Sum256([]byte(wingID))
	return "wt-" + hex.EncodeToString(hash[:8])
}
func directEndpoints(port int) []string {
	addrs, _ := net.InterfaceAddrs()
	var hosts []string
	for _, a := range addrs {
		ip, _, e := net.ParseCIDR(a.String())
		if e == nil && ip.To4() != nil && (ip.IsPrivate() || ip.IsLinkLocalUnicast()) && !ip.IsLoopback() {
			hosts = append(hosts, ip.String())
		}
	}
	if len(hosts) > 3 {
		hosts = hosts[:3]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	binary, e := exec.LookPath("tailscale")
	if e != nil {
		binary = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
	}
	data, e := exec.CommandContext(ctx, binary, "status", "--json").Output()
	if e != nil || len(data) > 1<<20 {
		data = nil
	}
	return directEndpointURLs(port, hosts, data)
}

func directEndpointURLs(port int, lan []string, tailnetStatus []byte) []string {
	hosts := append([]string(nil), lan...)
	if len(hosts) == 0 {
		hosts = append(hosts, "127.0.0.1")
	}
	hosts = append(hosts, tailnetHosts(tailnetStatus)...)
	var endpoints []string
	seen := map[string]bool{}
	for _, host := range hosts {
		if !seen[host] {
			seen[host] = true
			endpoints = append(endpoints, "http://"+net.JoinHostPort(host, strconv.Itoa(port)))
		}
	}
	return endpoints
}
func tailnetHosts(data []byte) []string {
	var status struct {
		BackendState string
		Self         struct {
			TailscaleIPs []string
			DNSName      string
		}
	}
	if json.Unmarshal(data, &status) != nil || status.BackendState != "Running" {
		return nil
	}
	var hosts []string
	for _, addr := range status.Self.TailscaleIPs {
		ip := net.ParseIP(addr)
		if ip != nil && ip.To4() != nil && tailnetIP(ip) {
			hosts = append(hosts, ip.String())
			break
		}
	}
	name := strings.TrimSuffix(strings.ToLower(status.Self.DNSName), ".")
	if len(hosts) > 0 && len(name) <= 253 && strings.HasSuffix(name, ".ts.net") && !strings.ContainsAny(name, " /:@?#\\") {
		hosts = append(hosts, name)
	}
	return hosts
}
func tailnetIP(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}
