package phonepair

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

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
