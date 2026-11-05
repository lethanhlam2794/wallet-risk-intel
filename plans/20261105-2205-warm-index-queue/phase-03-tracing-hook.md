---
phase: 3
title: "Tracing enqueue hook — risky 1-hop neighbors"
status: completed
priority: P1
dependencies: [2]
---

# Phase 3: Enqueue hook in `internal/tracing`

## Overview

When a hop-1 neighbor produces a direct sanctions match or a risky label
(SCAM/PHISHING/HACK/RANSOMWARE/MIXER), enqueue it for deeper background
indexing — fail-open, flag-gated by the `warmindex.Queue` no-op.

## Requirements

- Hop-1 only (the `rootSet.order` loop, `service.go:421`) — never deeper hops.
- **Sentinels never enqueued** (`sentinels[cp]` — `0x0`/`0xdead` never expand;
  indexing them is pure waste).
- `sanctions_direct` sig → priority 100, reason `"sanctions_direct_match"`.
- `label` sig with Category ∈ {SCAM, PHISHING, HACK, RANSOMWARE, MIXER}
  → priority 50, reason `"label:<CAT>"` (uppercase code verbatim).
- Foreign-network `label` sig (`sig.Network` set) with risky Category →
  enqueue too, reason `"label:<CAT>@<net>"` — the cp is a real counterparty
  on this network; foreign evidence is prioritization input, not a
  direct-listing claim (decision recorded — contract's "risky label" is
  ambiguous on this point).
- `listing` sigs and GAMBLING/EXCHANGE labels → **no enqueue**. Contract's
  risky set is deliberately narrower than the seeded vocabulary — DRAINER,
  MONEY_LAUNDERING, TERRORISM, DARKNET, EXTREMISM, FROZEN do NOT enqueue.
- Fail-open: enqueue errors logged (`s.log`), never returned — screening must
  be byte-identical when the queue is absent/broken.
- Enqueue ctx: `context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)`
  — a near-deadline BFS or client disconnect must not silently drop the
  enqueue; still bounded and fail-open.
- Disabled flag: `warmindex.Queue.Enqueue` already no-ops → tracing code is
  unconditional; when no enqueuer is wired (`nil`), skip entirely.
- `screeningID`: `nil` — no producer exists in v1 (tracing is invoked from
  `GET /wallets/…/exposures`, not from `screening.Service`).

## Architecture

```go
// internal/tracing/warm.go
type Enqueuer interface {
    Enqueue(ctx context.Context, netID int64, address string,
        priority int, reason string, screeningID *uuid.UUID) error
}

func (s *Service) SetEnqueuer(e Enqueuer, log *slog.Logger)

// pure decision fn — table-tested
func warmEnqueuePlan(sigs []MatchedSignal) (priority int, reason string, ok bool)
```

In the hop-1 loop (after `len(sigs)==0 → continue`):
```go
if pri, reason, ok := warmEnqueuePlan(sigs); ok &&
    s.enqueuer != nil && !c.sentinel {
    qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
    err := s.enqueuer.Enqueue(qctx, net.ID, cp, pri, reason, nil)
    cancel()
    if err != nil {
        s.log.Warn("warm enqueue failed", "network", net.Code,
            "addr", cp, "error", err) // fail-open
    }
}
```

## Related Code Files

- Create: `internal/tracing/warm.go`, `internal/tracing/warm_test.go`
- Modify: `internal/tracing/service.go` (fields `enqueuer`+`log`, setter,
  ~6-line hook), `internal/tracing/service_test.go` (hook tests)

## Implementation Steps (TDD)

1. RED: `warm_test.go` — `warmEnqueuePlan` table: sanctions_direct→(100);
   each risky label→(50); foreign `label` sig with risky Category→
   (50,"label:<CAT>@<net>"); GAMBLING/EXCHANGE/MINING/`listing`-only→ok=false;
   precedence when both sig kinds present (sanctions wins); multiple risky
   labels → deterministic (first sig).
2. GREEN: `warm.go` implementation + `SetEnqueuer` + service fields.
3. RED: service test — fake `Enqueuer` capturing calls: risky 1-hop neighbor
   enqueued once; clean/GAMBLING neighbor not enqueued; **sentinel neighbor
   not enqueued**; enqueuer returning error → `Exposures` still returns
   success (fail-open). Reuse existing tracing-service test scaffolding
   (`service_test.go`).
4. GREEN: hook wired in the hop-1 loop.
5. Verify existing tracing tests still pass (`go test ./internal/tracing/`).

## Success Criteria

- [x] Label filter test: every risky category enqueues; GAMBLING/EXCHANGE never.
- [x] Fail-open test: enqueue error → `Exposures` result unchanged, no error.
- [x] No enqueue for hop ≥2 items, foreign `listing` signals, or `nil` enqueuer.
- [x] No change to wire payloads (Result/Item/Coverage untouched).

## Risk Assessment

- Enqueue runs on the hot request path → bounded by hop-1 item count
  (≤ MaxNodesPerDepth[0], default 50); one INSERT each.
- Duplicate signals for one cp → single decision per cp (loop evaluates once).
