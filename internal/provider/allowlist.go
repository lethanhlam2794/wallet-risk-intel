package provider

import (
	"sort"
	"sync"
)

// Adapter packages publish their compiled-in SSRF allowlists here via
// RegisterAllowedHosts (each adapter's init registers its own defaults —
// the provider package cannot import adapters without a cycle).
// AllowedHostsFor lets non-adapter callers (config resolution, the admin
// write path) apply the exact same host check adapters enforce on their
// base URLs.
var (
	allowedHostsMu sync.RWMutex
	allowedHosts   = map[string][]string{}
)

// RegisterAllowedHosts publishes an adapter's default allowlist under its
// source code. Called from adapter init(); not for runtime mutation.
func RegisterAllowedHosts(sourceCode string, hosts []string) {
	allowedHostsMu.Lock()
	defer allowedHostsMu.Unlock()
	allowedHosts[sourceCode] = append([]string(nil), hosts...)
}

// AllowedHostsFor returns the adapter's registered SSRF allowlist
// (defensive copy), or nil when the code has no registered adapter.
func AllowedHostsFor(sourceCode string) []string {
	allowedHostsMu.RLock()
	defer allowedHostsMu.RUnlock()
	hosts, ok := allowedHosts[sourceCode]
	if !ok {
		return nil
	}
	return append([]string(nil), hosts...)
}

// AllowedHostsCodes lists the source codes with registered allowlists
// (sorted — diagnostics only).
func AllowedHostsCodes() []string {
	allowedHostsMu.RLock()
	defer allowedHostsMu.RUnlock()
	out := make([]string, 0, len(allowedHosts))
	for code := range allowedHosts {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}
