package tracing

// Warm index enqueue hook (warm-index-queue v1): hop-1 neighbors that
// already produced a direct sanctions signal or a risky label are queued
// for deeper background indexing. The hook is fail-open and flag-gated
// downstream (warmindex.Queue no-ops when disabled), so the screening
// path is byte-identical when the queue is absent or broken.

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"checkwallet/internal/warmindex"
)

// Enqueuer is the consumer-defined queue port — *warmindex.Queue
// satisfies it; tests substitute fakes.
type Enqueuer interface {
	Enqueue(ctx context.Context, netID int64, address string,
		priority int, reason string, screeningID *uuid.UUID) error
}

// SetEnqueuer wires the warm queue and the hook logger (mirrors
// SetMetrics). nil enqueuer disables the hook entirely.
func (s *Service) SetEnqueuer(e Enqueuer, log *slog.Logger) {
	s.enqueuer = e
	if log != nil {
		s.log = log
	}
}

// riskyWarmCategories is the contract-narrowed enqueue set (D5): only
// these label categories justify background quota. GAMBLING/EXCHANGE are
// classifications, not crimes; DRAINER/MONEY_LAUNDERING/TERRORISM/
// DARKNET/EXTREMISM/FROZEN stay out of v1 by decision, not oversight.
var riskyWarmCategories = map[string]bool{
	"SCAM":       true,
	"PHISHING":   true,
	"HACK":       true,
	"RANSOMWARE": true,
	"MIXER":      true,
}

// warmEnqueuePlan is the pure decision: a direct sanctions signal always
// wins (priority 100); the first risky-category label enqueues at 50 —
// foreign-network evidence only changes the reason suffix, never the
// claim semantics (it is prioritization input, not a direct listing).
// Everything else — listing signals, classifications, the excluded
// categories — never reaches the queue.
func warmEnqueuePlan(sigs []MatchedSignal) (priority int, reason string, ok bool) {
	for _, sig := range sigs {
		if sig.Kind == SigSanctionsDirect {
			return warmindex.PrioritySanctionsMatch,
				"sanctions_direct_match", true
		}
	}
	for _, sig := range sigs {
		if sig.Kind != SigLabel || !riskyWarmCategories[sig.Category] {
			continue
		}
		reason = "label:" + sig.Category
		if sig.Network != nil && *sig.Network != "" {
			reason += "@" + *sig.Network
		}
		return warmindex.PriorityRiskyLabel, reason, true
	}
	return 0, "", false
}

// warmEnqueue performs one bounded, fail-open enqueue for a hop-1
// counterparty. The enqueue runs under a detached 2s ctx: a near-deadline
// BFS or a client disconnect must not silently drop the signal, and the
// call still cannot hang the request path.
func (s *Service) warmEnqueue(ctx context.Context, netID int64, netCode, cp string,
	sigs []MatchedSignal) {

	if s.enqueuer == nil {
		return
	}
	pri, reason, ok := warmEnqueuePlan(sigs)
	if !ok {
		return
	}
	qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	err := s.enqueuer.Enqueue(qctx, netID, cp, pri, reason, nil)
	cancel()
	if err != nil {
		s.log.Warn("warm enqueue failed", "network", netCode,
			"addr", warmindex.MaskAddr(cp), "error", err) // fail-open
	}
}
