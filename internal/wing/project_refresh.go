package wing

import (
	"crypto/ecdh"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

const projectRefreshInterval = 30 * time.Second

// projectInventory retains one snapshot, coalescing concurrent discovery
// requests into at most one scan per interval. Root changes and SIGHUP bypass
// the interval. Discovery keeps its existing configured-root and depth bounds.
type projectInventory struct {
	mu       sync.Mutex
	cwd      string
	roots    []string
	projects []ws.WingProject
	expires  time.Time
	now      func() time.Time
	discover func([]string, string) []ws.WingProject
}

func newProjectInventory(cwd string) *projectInventory {
	return &projectInventory{cwd: cwd, now: time.Now, discover: wingpolicy.DiscoverWingProjects}
}

func (p *projectInventory) snapshot(roots []string) []ws.WingProject {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.now().Before(p.expires) || !slices.Equal(roots, p.roots) {
		p.projects = p.discover(roots, p.cwd)
		p.roots = slices.Clone(roots)
		p.expires = p.now().Add(projectRefreshInterval)
	}
	// Neither a caller nor the next refresh can mutate an in-flight response.
	return slices.Clone(p.projects)
}

func (p *projectInventory) invalidate() {
	p.mu.Lock()
	p.expires = time.Time{}
	p.mu.Unlock()
}

// Discovery receives a request-local metadata client so refreshing projects
// never mutates the live relay client or copies its connection locks. The
// tunnel dispatcher still authenticates, decrypts and applies current ACLs.
func projectDiscoveryClient(client *ws.Client, projects []ws.WingProject) *ws.Client {
	return &ws.Client{Hostname: client.Hostname, Platform: client.Platform, Version: client.Version, Agents: client.Agents, Projects: projects}
}

func isProjectDiscovery(req ws.TunnelRequest, privKey *ecdh.PrivateKey) bool {
	if req.Purpose != "" {
		return req.Purpose == ws.TunnelPurposeDiscovery
	}
	// Older clients omit Purpose. Inspect only their encrypted operation name;
	// malformed requests still follow the ordinary dispatcher's rejection path.
	gcm, err := auth.DeriveSharedKey(privKey, req.SenderPub, "wt-tunnel")
	if err != nil {
		return false
	}
	plain, err := auth.Decrypt(gcm, req.Payload)
	if err != nil {
		return false
	}
	var inner struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(plain, &inner) == nil && inner.Type == "wing.info"
}
