---
phase: 3
title: Runtime kill-switch — TTL-cached status in chain walk
status: completed
priority: high
dependencies: [2]
---

## Overview
`provider_configs.status` consulted per EnsureIndexed with ~30s
in-process cache — inactive ⇒ adapter skipped exactly like disabled
(zero traffic, never failure, never failover trigger).

## Requirements
- StatusCache{TTL}: `Active(ctx, code) bool`; row missing ⇒ active=true
  (env-only parity); `is_active=false` on data_sources also ⇒ inactive.
- Wire into transaction.Service chain walk: skip inactive sources
  BEFORE any upstream spend — identical code path as disabled skip
  (status='disabled' state write reuses existing writeDisabled path
  semantics: per-source row, chain[0] attribution on all-inactive).
- Cache failure modes: DB read error ⇒ fail-closed to LAST-KNOWN value
  (never silently re-enable); no last-known ⇒ active (env parity).
- singleflight per wallet already prevents concurrent ensure — cache is
  process-local, documented.

## Related Code Files
- Create: internal/providerconfig/status_source.go (TTL cache; +_test.go)
- Modify: internal/transaction/service.go (chain walk skip)

## Implementation Steps
1. Red: stub status flip ⇒ second EnsureIndexed skips provider,
   provider_status reports disabled-ish state; zero upstream calls
   counted.
2. Implement cache + skip; reuse disabled-skip path.
3. Green: failover suite still passes; new kill-switch tests.

## Success Criteria
- [ ] Flip to inactive ⇒ next refresh skips, zero upstream traffic
- [ ] Flip back ⇒ serves again without restart
- [ ] Missing row ⇒ active (env parity); DB error ⇒ last-known
- [ ] All-inactive ⇒ honest disabled state (chain[0] attribution)

## Risk Assessment
Medium — touches EnsureIndexed hot path; keep the check outside the
per-attempt budget prepay so no quota burns on skipped sources.

## Amendments (critic R1)
- **Wire state:** inactive ⇒ exactly `disabled` via existing
  `writeDisabled` — not "disabled-ish"; all-inactive ⇒ disabled under
  chain[0]'s source_id (current attribution semantics preserved).
- **Stale `indexing` rows:** a mid-flight `indexing` row under a
  flipped-inactive source stays `indexing` until StaleAfter/reactivation
  — chosen behavior, stated explicitly (never served, self-heals).
- **StatusCache must be goroutine-safe** (mutex/RWMutex) — different
  wallets' ensures run concurrently; singleflight is per-wallet only.
- Last-known on DB error documented: cache keeps serving last value;
  cold-start DB error ⇒ `active=true` (env parity) + error log.

## Outcome (cook)
- Commit `11bc1a7`. `providerconfig.StatusCache` (TTL, mutex-serialized
  refresh, last-known on error, cold ⇒ active+error log) wired via
  `Service.SetStatusChecker`; the skip sits in the same case as
  `!adapter.Enabled(net)` — byte-identical disabled path, before any
  budget prepay. `StatusChecker` interface lives in `transaction` (ISP).
- Tests: effective-active truth table incl. ErrNoRows⇒active, TTL window,
  flip/flip-back, all-inactive disabled under chain[0], mid-flight
  `indexing` row untouched (self-heals), nil-checker env parity, real
  DB-backed flip via `ttl=0` cache. `go test -race` clean.
