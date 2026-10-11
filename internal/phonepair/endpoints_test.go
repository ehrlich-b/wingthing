package phonepair

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestDirectEndpointPreferenceWithoutAccount(t *testing.T) {
	status := []byte(`{"BackendState":"Running","Self":{"TailscaleIPs":["100.91.2.3"],"DNSName":"EhrlichMac.fixture.ts.net."}}`)
	for _, tc := range []struct {
		name   string
		lan    []string
		status []byte
		want   []string
	}{
		{"lan then tailnet IP and MagicDNS", []string{"192.168.1.40", "10.0.0.2", "192.168.1.40"}, status, []string{"http://192.168.1.40:6078", "http://10.0.0.2:6078", "http://100.91.2.3:6078", "http://ehrlichmac.fixture.ts.net:6078"}},
		{"no tailscale installed", []string{"192.168.1.40"}, nil, []string{"http://192.168.1.40:6078"}},
		{"stopped tailnet", []string{"192.168.1.40"}, []byte(`{"BackendState":"Stopped","Self":{"TailscaleIPs":["100.91.2.3"]}}`), []string{"http://192.168.1.40:6078"}},
		{"tailnet without wifi", nil, status, []string{"http://127.0.0.1:6078", "http://100.91.2.3:6078", "http://ehrlichmac.fixture.ts.net:6078"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := directEndpointURLs(6078, tc.lan, tc.status); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("direct routes = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPhoneTailnetEndpointsAndCompactInvitation(t *testing.T) {
	hosts := tailnetHosts([]byte(`{"BackendState":"Running","Self":{"TailscaleIPs":["fd7a:115c:a1e0::1","100.91.2.3"],"DNSName":"ehrlichmac.fixture.ts.net."}}`))
	if len(hosts) != 2 || hosts[0] != "100.91.2.3" || hosts[1] != "ehrlichmac.fixture.ts.net" {
		t.Fatal(hosts)
	}
	for _, data := range []string{`{"BackendState":"Stopped","Self":{"TailscaleIPs":["100.91.2.3"]}}`, `{"BackendState":"Running","Self":{"TailscaleIPs":["8.8.8.8"],"DNSName":"attacker.example"}}`} {
		if len(tailnetHosts([]byte(data))) != 0 {
			t.Fatal("advertised non-tailnet or stopped route")
		}
	}
	q := Invitation{Version: 1, Local: "http://192.168.1.40:6078", Endpoints: []string{"http://100.91.2.3:6078", "http://ehrlichmac.fixture.ts.net:6078"}}
	compact, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(q.Link(), "wingthing://pair#v1z."))
	if err != nil {
		t.Fatal(err)
	}
	reader := flate.NewReader(bytes.NewReader(compact))
	defer reader.Close()
	compact, err = io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var fields []json.RawMessage
	if json.Unmarshal(compact, &fields) != nil {
		t.Fatal("invalid compact QR")
	}
	var direct []string
	if json.Unmarshal(fields[4], &direct) != nil || len(direct) != 3 || direct[0] != q.Local {
		t.Fatal("lost endpoint order")
	}
	if DiscoveryName("EhrlichMac") == DiscoveryName("forge") {
		t.Fatal("Bonjour can discover a different wing")
	}
}
