// Package warmindex owns the warm index queue (warm-index-queue v1):
// signal-prioritized background deep-indexing for wallets that already
// produced risk signals during tracing. The queue is Postgres SKIP
// LOCKED — no external broker. Each drain consumes
// transaction.Service.EnsureIndexed through a per-provider single-member
// lane (plan D1) so leftover free-tier quota is spent without ever
// touching the interactive lane's providers, shared budget, or
// singleflight budget internals.
package warmindex

import (
	"time"

	"checkwallet/internal/provider"
	"checkwallet/internal/transaction"
)

const (
	// LeaseDuration bounds a claim's validity — a crashed drainer's items
	// return to pending after this horizon (D10: the reset counts as a
	// failed attempt).
	LeaseDuration = 15 * time.Minute
	// DrainTimeout bounds one EnsureIndexed call. It stays strictly below
	// LeaseDuration so a slow-but-legit drain is never lease-swept
	// mid-flight by a peer's reset pass.
	DrainTimeout = 5 * time.Minute
	// MaxAttempts bounds a queue item's lifetime — the third failed
	// attempt (or lease reset) moves it to 'dead'.
	MaxAttempts = 3
	// PrioritySanctionsMatch queues a hop-1 neighbor carrying a direct
	// sanctions signal — the highest warm priority.
	PrioritySanctionsMatch = 100
	// PriorityRiskyLabel queues a hop-1 neighbor carrying a risky label
	// (SCAM/PHISHING/HACK/RANSOMWARE/MIXER).
	PriorityRiskyLabel = 50
)

// Config carries the WARM_INDEX_* knobs (plan D7).
type Config struct {
	Enabled      bool          // feature flag — Queue.Enqueue and Drainer.Run no-op when false
	Providers    []string      // allowlist codes in selection order (WARM_INDEX_PROVIDERS)
	DailyCap     int           // per-provider upstream-call ceiling per UTC day
	PollInterval time.Duration // worker poll cadence
	ItemBudget   int           // per-drain provider-call pool (clamped further by the lane's TXINDEX_BUDGET_*)
	MaxPages     int           // stream-pair step bound per drain
	// Workers is the in-process concurrency: N claim loops run
	// concurrently inside one drain (SKIP LOCKED keeps them disjoint),
	// and worker i prefers lane[i%len(lanes)] so multiple providers
	// drain in parallel when the queue is deep. <=1 = the historical
	// single serial loop.
	Workers int
}

// ProviderLane binds one allowlisted provider to its dedicated
// transaction.Service — a single-member adapter chain per network (plan
// D1) so drain-time calls are attributed to exactly this provider and
// failover can never spend a non-selected provider's quota.
type ProviderLane struct {
	Code    string // = Adapter.Name() — killswitch/ledger/data_sources/budget all key on it
	Adapter provider.TxProvider
	Tx      *transaction.Service
}

// MaskAddr renders a wallet address for logs: first 6 + last 4 chars
// (0x1234…cdef). Wallet addresses are never logged in full.
func MaskAddr(addr string) string {
	if len(addr) <= 10 {
		return addr
	}
	return addr[:6] + "…" + addr[len(addr)-4:]
}
