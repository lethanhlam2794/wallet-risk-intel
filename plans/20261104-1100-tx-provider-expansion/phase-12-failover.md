---
phase: 12
title: "Ordered failover chain (design sign-off required)"
status: pending
priority: P2
dependencies: [phase-01, phase-02, phase-03, phase-04, phase-05, phase-06, phase-07, phase-08, phase-09, phase-10, phase-11]
---

# Phase 12: Ordered failover chain — DESIGN SIGN-OFF FIRST

## Overview

`main.go:242` currently wires `map[string]provider.TxProvider` — one
adapter per network. Replace with an ordered chain
`map[string][]provider.TxProvider` selected by `TX_PROVIDER_ETH` /
`TX_PROVIDER_BSC`, with failover restricted to the spec's failure kinds.

## STOP — mandatory design sign-off gate (spec requirement)

The spec requires: failover runs "ONLY after all adapter commits are
reviewed, and STOP for design sign-off before starting." Critic round 1
found the blocking design question — resolve ALL of the following in a
design doc (`reports/phase-12-design.md`). D1 is pre-decided (a);
the doc must still nail D2–D4 mechanics before code, but no additional
D1 halt remains:

### D1. Per-source watermark impossibility (BLOCKER)

`wallet_index_state` PK is `(network_id, wallet_id)` — one row, ONE
`source_id` column (migrations/000008). `ResetWalletIndexSource` wipes
`indexed_until_block`/counters/status on ANY source change, and
`service.go:191-199` fires it whenever stored `source_id ≠` resolved.
Under failover: secondary writes state → next request (primary healthy)
sees mismatch → wipes secondary coverage → re-index from 0 → primary
fails again → secondary resets → repeat. Every alternation destroys
coverage and burns BOTH providers' quota — the opposite of the goal.

**RESOLVED by user pre-decision (validate round): option (a) — schema
re-key.** `wallet_index_state` migrates to PK `(network_id, wallet_id,
source_id)` giving real per-source watermarks; provider-switch reset
becomes per-source (only that source's row resets); failover then
resumes each adapter from ITS OWN coverage boundary. This exceeds the
"only a data_sources row" migration allowance — user sign-off for the
re-keyed schema is now recorded in the plan; the migration itself still
requires its own up+down file and the existing readers/writers of
wallet_index_state (EnsureIndexed, reset guards, watermark reads in
service.go + any tracing freshness reads) updated to be source-scoped.
Rejected alternatives kept for the record: (b) sticky-last-writer —
primary degraded forever never regains coverage; (c) bounded
reset-per-flip — honest but wasteful churn.

### D2. Budget semantics under failover

`ListTransactions` creates ONE request-global pool sized by
`BudgetFor(net)` which reads the single adapter's configured budget.
With a chain: shared pool sized by primary caps secondary at primary's
quota (defeats the feature); per-adapter pools burn Σ(budgets) — a
Budget-semantics change the spec says STOP for. Propose: shared pool
sized by max(chain budgets), charged by actual upstream calls;
secondary's own cap also enforced. Document chosen semantics.

### D3. Failover mechanics spec (underspecified in v1)

- **Partial batch discard**: if primary fails on page 3, its accumulated
  transfers are discarded before secondary fetch (never mix batches —
  `transfer_index` k-suffixes derive from per-stream counts;
  identity.go:78).
- **Per-try restart**: each adapter attempt re-runs `HeadBlock`,
  recomputes `fetchFrom`/`fetchTo`, starts `PageToken=""` — tokens and
  windows are adapter-scoped.
- **srcID resolution**: resolve per attempt, not once per request;
  reset check runs per attempt (D1 decides its effect).
- **All-disabled terminal**: define which source_id the disabled state
  carries (first configured, or a designated disabled sentinel — the
  null/unknown option is infeasible post-rekey, see D5).
- **`MarkWalletIndexing`**: write source_id once for the winning attempt,
  not per hop in the chain.
- **All-fail terminal kind**: persist the LAST non-disabled attempt's
  failure kind as the wire `provider_status`/error (freshest truth);
  per-attempt failure log kept in metadata.

### D4. `timeout` failover-kind justification

Spec excludes `timeout` from failover kinds. Keep spec-faithful but
justify in design doc (upstream slow vs request-deadline expiry are
indistinguishable at the error boundary; failing over on deadline
expiry wastes secondary quota inside an already-dead context — bounded
upstream timeout SHOULD however be treated as `degraded`/`transport`,
not `timeout`, where the adapter can prove it's upstream).

### D5. Re-key ripple enumeration (critic R4 — required answers)

Under D1(a) `(network_id, wallet_id, source_id)` the design doc MUST
explicitly answer all of the following — the sign-off gate cannot pass
with any open:

- `source_id` becomes a PK column → NOT NULL. Today's NULL-`source_id`
  write paths (`srcID` unresolved/adapter nil → `writeDisabled`,
  `UpsertWalletIndexState` inserting NULL, service.go:441-493) violate
  the new key. Decide: sentinel disabled-source row, NULL column
  dropped + separate disabled-state surface, or per-row fallback.
- All-disabled terminal: with `source_id` NOT NULL, "none →
  null/unknown" (D3 above) is infeasible — pick the concrete row.
- `GetWalletIndexState :one` (net,wallet) fans out to N rows post-rekey:
  which row serves the response (winning adapter's? freshest? union?)?
- Every `ON CONFLICT (network_id, wallet_id)` target
  (MarkWalletIndexing, UpsertWalletIndexState, any tracing freshness
  reads) must be re-targeted to the 3-column key — enumerate call sites.
- `transfer_index` divergence across adapters: differing stream
  partitions yield different occurrence counts `n` for identical logical
  transfers (identity.go:130,175-181) — under (a) no reset runs, so rows
  a previous source produced persist. State the chosen semantics
  (per-source provenance retained vs dedup-at-read vs cleanup) and how
  responses avoid double-counting the same logical transfer served by
  two sources.

## Requirements (post-sign-off)

- `map[string][]provider.TxProvider`; `TX_PROVIDER_<NET>` comma list;
  unknown codes → config error (never silently misroute).
- Comma list pre-resolution: every entry must name a registered adapter.
- Failover ONLY on: `rate_limited`, `upstream_5xx`, `degraded`,
  `transport`, `circuit_open`. Never on: empty page, `client_4xx`,
  `malformed`, `timeout` (per D4), `internal`, `disabled` (skipped, not
  failed) — the never-list is the explicit complement of the ONLY-list
  across all ErrKinds.
- Successful empty page = terminal success — do NOT walk the chain.
- Preserve single-adapter behavior when selectors unset.
- Per D1 outcome: implement and test the chosen watermark semantics.
- Budget charging per D2 outcome; per-provider accounting kept.

## Architecture

- `internal/transaction/service.go`: chain walk inside the fetch
  boundary (or a `provider.Chain` wrapper implementing `TxProvider`
  that hides iteration — decide in design doc; wrapper is cleaner:
  service sees ONE TxProvider, chain owns per-try semantics, but then
  source_id/watermark attribution needs the chain to expose the winning
  adapter — spec this).
- `internal/config/config.go`: `TxProviderOrder map[string][]string`.
- `cmd/api/main.go`: build all configured adapters, resolve chain by env.

## Related Code Files

- Modify: `internal/transaction/service.go`, `internal/config/config.go`,
  `cmd/api/main.go`, `internal/provider/provider.go` (Chain type if
  chosen), `.env.example`, maybe `internal/tracing/` BudgetFor callers.
- Create: `reports/phase-12-design.md` (design doc), failover tests.
- Possibly: new migration IF option (a) chosen — sign-off covers it.

## Implementation Steps

1. Write `reports/phase-12-design.md` answering D1–D5 with a chosen
   option + trade-off table; present to user; **HALT for sign-off**
   (D5's enumeration is mandatory — sign-off cannot pass with open
   re-key ripples).
2. Post-sign-off: implement per chosen design.
3. Tests: failover ordering; primary healthy → zero secondary traffic;
   per-source watermark behavior per D1; all-fail → honest
   failed/degraded (never clean); disabled skipped; empty page terminal;
   client_4xx/malformed never fail over; budget charged by actual
   upstream calls.
4. Gate: `go build ./... && go test ./...` exit 0.

## Success Criteria

- [ ] Design doc written covering D1–D5
- [x] D1 sign-off recorded: schema re-key (a) chosen at validate gate
- [ ] D2–D5 mechanics written into reports/phase-12-design.md before code
- [ ] Chain ordering respected; selector validation errors on unknown
- [ ] Failure-kind restriction enforced and tested both directions
- [ ] Watermark semantics per signed-off design, proven by test
- [ ] Budget semantics per signed-off design
- [ ] All-failed → honest status; empty page → no chain walk
- [ ] Default-unset → byte-identical current behavior

## Risk Assessment

- D1 unresolved → coverage destruction under failover (the blocker).
- Wrong partial-batch handling → corrupted transfer_index identity/dedup.
- Budget mis-semantics → quota accounting lies to operators.
- Mixing sources within one response → provenance invariant broken.
