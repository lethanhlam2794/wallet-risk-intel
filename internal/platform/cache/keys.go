package cache

import "fmt"

// Key patterns (spec §14). Every Redis key in the system is built here;
// inline key strings elsewhere are forbidden (P-023).
const (
	prefixWalletLabels    = "wallet:labels"
	prefixWalletSanctions = "wallet:sanctions"
	prefixProvider        = "provider"
	prefixRateLimit       = "rl"
	keyCurrentSnapshot    = "sanctions:current_snapshot"
)

// WalletLabels keys the wallet-label lookup cache.
func WalletLabels(network, normalizedAddress string) string {
	return fmt.Sprintf("%s:%s:%s", prefixWalletLabels, network, normalizedAddress)
}

// WalletSanctions keys the sanctions lookup cache; the snapshot version is
// embedded so a publish makes old entries unreachable.
func WalletSanctions(network, normalizedAddress, snapshotVersion string) string {
	return fmt.Sprintf("%s:%s:%s:%s", prefixWalletSanctions, network, normalizedAddress, snapshotVersion)
}

// Provider keys the generic provider-lookup cache.
func Provider(source, network, normalizedAddress string) string {
	return fmt.Sprintf("%s:%s:%s:%s", prefixProvider, source, network, normalizedAddress)
}

// RateLimitClient keys the authenticated fixed-window bucket.
func RateLimitClient(clientID string, window int64) string {
	return fmt.Sprintf("%s:%s:%d", prefixRateLimit, clientID, window)
}

// RateLimitAnon keys the anonymous (invalid/absent-key) bucket by IP so
// 401-probing is rate limited too (spec §16).
func RateLimitAnon(ip string, window int64) string {
	return fmt.Sprintf("%s:anon:%s:%d", prefixRateLimit, ip, window)
}

// CurrentSnapshot is the published OFAC snapshot version pointer.
func CurrentSnapshot() string { return keyCurrentSnapshot }
