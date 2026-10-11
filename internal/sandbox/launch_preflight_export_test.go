//go:build darwin

package sandbox

import (
	"net/http"
	"net/http/httptest"
)

// Exercise the production proxy's actual refusal before any DNS or dial path.
func ReviewProxyStatus(domains []string, host string) int {
	p := &DomainProxy{domains: map[string]bool{}}
	p.setDomains(domains)
	r := httptest.NewRecorder()
	p.ServeHTTP(r, httptest.NewRequest(http.MethodConnect, "http://"+host, nil))
	return r.Code
}

// Return the exact production profile, without creating a sandbox or process.
func ReviewProfile(cfg Config) string { return buildProfile(cfg) }
