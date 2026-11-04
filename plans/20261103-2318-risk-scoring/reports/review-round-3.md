# Devin Review — Round 3: Post-Remediation Re-Review (Phase 5, Risk Scoring)

**Date**: 2026-11-04
**Scope**: Same uncommitted change set as rounds 1–2: `migrations/000007_screenings.{up,down}.sql`,
`queries/screenings.sql`, `internal/db/screenings.sql.go`, `internal/scoring/`,
`internal/screening/`, `tests/screenings_{api,persistence}_test.go`, plus diffs to
`api/openapi.yaml`, `cmd/api/main.go`, `internal/httpserver/server.go`,
`internal/middleware/errors.go`, `internal/platform/metrics/metrics.go`,
`internal/db/models.go`. `git status` re-verified unchanged at review start and end.
**Risk level**: R3 (schema change + risk-decision surface + idempotent write API).
**Prior review**: `review-round-2.md` (CHANGES_REQUESTED, 5 minors). **Claimed fixes**:
`remediation-round-2.md` (R2-M1…R2-M5 all remediated).

## Verdict: CHANGES_REQUESTED

## Executive Summary

All five Round-2 findings are verifiably remediated — confirmed line-by-line,
by fresh test runs, and by direct inspection of persisted rows on the real
test database (not just the author's claims). The full gate is green on fresh
`-count=1` runs executed by this reviewer.

One new minor defect was found this round: `pollOrConflict` re-reads the claim
row each tick but never re-verifies `request_hash`, so a claim released by a
failed owner and re-claimed by the same client under a *different* payload can
be replayed back to the wrong request — returning a screening result for an
address the caller never asked about. Narrow window, same-client only, one-line
fix. Four nits ride along, including a correction to a round-2 disposition that
was inaccurate (the JSONB-null `findings` nit is only fixed in `persistFailed`,
not the main `persist` path).

## Verification Evidence (run by this reviewer, fresh `-count=1`)

| Command | Result |
|---|---|
| `go build ./...` | Exit 0 |
| `go vet ./...` | Exit 0 |
| `gofmt -l .` | Empty output |
| `python3 -c "import yaml; yaml.safe_load(open('api/openapi.yaml'))"` | Parses OK (v0.3.0) |
| `go test -count=1 -v -run 'TestEvaluate_DedupKeepsHighestProviderScore\|TestEvaluate_DedupKeepsStrongestEvidence' ./internal/scoring/` | Both PASS (0.010s) |
| `go test -count=1 -v -run 'TestService_EnrichFanoutError_ReportedOnFanoutField\|TestService_RawAddressPreserved\|TestService_PersistFailed_AuditRow' ./internal/screening/` | All 3 PASS on real `check_wallet_screening_test` (1.461s) |
| `go test -count=1 ./...` | All packages ok; `tests` 76.037s; `internal/screening` 14.961s; `internal/enrichment` 16.117s |
| `go test -race -count=1 ./internal/scoring/ ./internal/screening/` | ok 1.054s / 35.107s |
| `psql` on `check_wallet_test` after `TestScreenings_ResponseShape` | Stored `request` JSONB = `{"address":"0x098b…","network":"ethereum","options":{…}}` — **no `RawAddress` key**; `wallets.address` = `0x098B716B8Aaf21512996dC57EB0615e2383E2f96` (original casing preserved), `normalized_address` lowercase |

## R2 Claim-by-Claim Verification

| Claim | Verified | Evidence |
|---|---|---|
| R2-M1: dedupe tiebreak `si > sj`; test passes | **YES** | `engine.go:217-226` — score clause is last tiebreak, `return si > sj` (descending). `TestEvaluate_DedupKeepsHighestProviderScore` (`engine_test.go:84-101`) feeds same-key {30,75} in both orders, asserts `blocks.other.subtotal == 10` = band(75); under the old `si < sj` the survivor would be 30 → band 0 → test fails. Honest test, drives the exact axis. |
| R2-M2: `persistFailed` drops `cause`; static `"screening persist failed"` | **YES** | `service.go:278` signature `(walletID int64, req CanonicalRequest)` — no `cause`. `service.go:302-305` emits `Code:"internal", Message:"screening persist failed"`. `TestService_PersistFailed_AuditRow` asserts the exact string on the stored row — passes. No other writer persists raw error text into `result` (`run` wraps for logs only: `fmt.Errorf("persist: %w")` → `middleware.InternalError` generic envelope). |
| R2-M3: claim release bounded `WithTimeout(10s)` | **YES** | `handler.go:263-275` — release uses `context.WithTimeout(context.Background(), 10*time.Second)` with the guarded `DeleteScreeningRequest` (`AND screening_result_id IS NULL`), so an ambiguous-commit claim is never deleted. See audit §4 below for recoverability of an expired release. |
| R2-M4: `TestService_EnrichFanoutError_ReportedOnFanoutField` exists, passes | **YES** | `service_test.go:216-231` — `fakeEnrich{err: …}` drives `g.enrichErr` → asserts `Fanout.Status == "ERROR"`, empty `providers` map (no pseudo-provider keys), `PARTIAL` (sanctions leg usable). Real production branch exercised — G-006 satisfied. |
| R2-M5: `RawAddress` `json:"-"`, set in handler, passed as `address`; hash can't regress | **YES** | `model.go:105` `RawAddress string \`json:"-"\``; `handler.go:100-101` sets `RawAddress: req.Address`; `service.go:142-146` `raw := req.RawAddress; if raw == "" { raw = req.Address }` → `GetOrCreate(ctx, net.ID, raw, req.Address)`. **Empirical**: after an HTTP POST with a mixed-case address, `wallets.address` held the original casing verbatim and stored `request` JSONB had no `RawAddress` key — `requestHash` (sha256 over `json.Marshal(canon)`) therefore cannot regress idempotency: two spellings marshal identically. |

## New-change audit (prompt items)

1. **`RawAddress` in `persistFailed`/`Result` serialization — leak?**
   No. `Result` has no `RawAddress` field at all; `CanonicalRequest.RawAddress`
   carries `json:"-"`, so `json.Marshal(req)` (used for `request` JSONB in both
   `persist` and `persistFailed`, and for `requestHash`) omits it. Verified
   against the real DB (evidence table above). Design-wise this is correct:
   the `request` column is the idempotency identity, not an audit log — the
   original representation lives in `wallets.address`.

2. **`RawAddress` fallback semantics — normalized stored as `address` anywhere?**
   Only for programmatic callers that never set `RawAddress` (`service.go:143-145`
   falls back to `req.Address`) — documented, and for EVM the normalized form is
   still a valid representation. The HTTP handler always populates `RawAddress`,
   so no request-path regression. One adjacent latent inconsistency: the
   `Enrich` fan-out passes `req.Address` for *both* `rawAddress` and `normalized`
   (`service.go:185`), so `enrichment.Service`'s own internal
   `GetOrCreate(net.ID, normalized, normalized)` (`enrichment/service.go:117`)
   would store normalized had the row not already existed — it is harmless only
   because `run`'s `GetOrCreate` runs sequentially before the errgroup, and the
   wallet upsert's `DO UPDATE SET updated_at = wallets.updated_at`
   (`queries/wallets.sql:5`) is first-seen-wins. Verified safe today; flagged
   as Nit N-2 for fragility.

3. **`si > sj` — determinism when scores equal?**
   Preserved. `sort.SliceStable` returns false on `si == sj` → stable order →
   and for `KindProviderScore` same-key findings every other field is identical
   (mapper sets only `{Kind, Source, Ref:"src:score", ProviderScore,
   VerificationStatus:"reported"}`) — the survivor is deterministic in every
   reachable scenario. Hypothetical same-key findings differing only in
   non-comparator fields (`CategoryType`, `Basis`) would keep input order —
   pre-existing, unchanged by this fix, and unreachable via the current mapper
   (`Ref` embeds label ID / source+identity).

4. **10s release timeout — can an expired release orphan a claim forever?**
   No. An expired/failed `DeleteScreeningRequest` leaves the claim open
   (`screening_result_id IS NULL`), which becomes `claim_stale` after 5 minutes
   (DB-time predicate, byte-identical to the guarded delete). A same-key retry
   then takes over via `DeleteStaleScreeningRequest` — the state is
   recoverable by construction. Worst case is one extra retry cycle: a poller
   observing the claim mid-stale-transition simply exhausts its 30s window and
   gets 409 IN_FLIGHT.

## Findings

### [Blocker]
None.

### [Major]
None.

### [Minor]

**R3-M1 — `pollOrConflict` never re-verifies `request_hash` inside the poll
loop; a replaced claim can replay the wrong payload's result**
(`internal/screening/handler.go:222-235`).

The claim-first contract states "a key is bound to its payload for its whole
lifecycle," enforced by `existing.RequestHash != hash → 409` at `handler.go:171`
— but only on the *first* read. Inside `pollOrConflict`, each 250ms tick
re-fetches by `(client_id, idempotency_key)` and replays whenever
`ScreeningResultID.Valid` — with no hash re-check on the fresh row. Reachable
sequence, same client throughout:

1. POST key K payload P1 → claim C1 (hash H1) inserted; pipeline runs.
2. Client retries POST K P1 → insert conflicts → hash matches → enters
   `pollOrConflict`.
3. C1's pipeline fails → `runAndRespond` releases (deletes) C1 — the only
   hash-unconditional delete path.
4. Same client POSTs key K payload P2 → claim C2 (hash H2) inserts cleanly
   and completes within the poller's remaining window.
5. Next tick reads C2 → `ScreeningResultID.Valid` → `replay` serves R2 —
   the result for **P2**, marked `Idempotent-Replay`, to a caller that asked
   for P1.

The stale-takeover path cannot trigger this (it is hash-gated before the
delete, and a re-inserted row has a different ID). The `ErrNoRows → break`
path is safe. Only the release→reclaim→complete-under-the-poll sequence is
wrong. Fix: pass `hash` into `pollOrConflict` and re-check
`claim.RequestHash != hash` per iteration — respond `IDEMPOTENCY_CONFLICT`
(the key was rebound) rather than replaying foreign data.

### [Nit]

- **R3-N1** — `Enrich` fan-out passes `req.Address` for `rawAddress`
  (`service.go:185`); today the ordering and first-seen upsert make it
  harmless, but any reorder or future `rawAddress` use in enrichment silently
  degrades the original representation. Pass `raw` for consistency with R2-M5.

- **R3-N2** — Stale-claim boundary (literal `5 minutes` in
  `queries/screenings.sql:30,55`) is decoupled from `Deps.Deadline`
  (default 30s). Safe today — no config knob raises the deadline past 5min —
  but if `Deadline` ever exceeds the stale interval, a running owner becomes
  takeover-eligible mid-persist: duplicate screenings and an orphaned result
  row (`AttachScreeningResultToRequest` is `:exec` — no rows check). One
  comment or a derived constant prevents the drift.

- **R3-N3** — `persist` lacks the `Pool == nil || Queries == nil` guard that
  `persistFailed` has (`service.go:279-281` vs `service.go:420`). Unreachable
  under real wiring; flagged for consistency only.

- **R3-N4** — R2-N1 stands, and the round-2 disposition was inaccurate:
  `remediation-round-2.md` claims JSONB-null findings is "already
  `[]byte("[]")`" — that is only `persistFailed` (`service.go:341`). The main
  path still does `json.Marshal(m.findings)` (`service.go:392`), which stores
  `null` when every requested dimension is usable-but-empty (all dims usable,
  zero evidence → `m.findings == nil`). Cosmetic; consumers unmarshal
  tolerantly.

## Rejected candidates (checked, not findings)

- **Claim release vs ambiguous commit** — guarded delete (`screening_result_id
  IS NULL`) makes the post-commit release a no-op; retry replays the real
  result. Verified at `queries/screenings.sql:45-46` + `handler.go:269`.
- **`requestHash` ignores marshal error** — `CanonicalRequest` is
  {string,bool,int} fields; `json.Marshal` cannot fail.
- **`AttachScreeningResultToRequest` no row-count check** — the claim cannot
  be removed mid-run (not stale within a ≤30s deadline; only owner-release
  deletes it, which runs after `Run` returns). Attach always lands.
- **Reader role may POST /screenings** — intended partner-screening surface;
  spec assigns admin role only to admin endpoints.
- **`GetByID` unscoped read** — documented capability-URL design in OpenAPI
  ("any reader may fetch any result UUID").
- **`Enrich` fake signature** — `screeningEnricher`/`fakeEnrich` match the
  real `enrich(ctx, net, raw, normalized, opts)` contract verbatim (P-002).
- **Shared `check_wallet_test` across `tests/` and `internal/testutil`
  packages** — P-061-adjacent truncation race is pre-existing convention
  (all of `tests/` shares it; `internal/screening` correctly uses a
  package-private `check_wallet_screening_test`). The new files follow the
  dominant pattern; no new mechanism introduced.
- **OpenAPI `address` pattern `(0x)?`** — matches `NormalizeEVM` exactly:
  `0X`-prefixed input fails the `^0x…` regex → 400. No spec-vs-impl drift.

## Required Actions to Approve

1. **R3-M1**: re-check `claim.RequestHash` inside the `pollOrConflict` loop
   (pass `hash` in); a mismatched completed claim must not be replayed to the
   wrong request — `IDEMPOTENCY_CONFLICT` is the truthful answer.
2. Nits R3-N1…R3-N4 are advisory; R3-N1 is a one-token fix worth folding in
   while the file is open.

## Positive observations (verified, not assumed)

- R2-M5 was verified end-to-end on real infra this round — not just by the
  unit test but by reading the persisted `wallets` and `screening_results`
  rows after an HTTP-level run.
- The `Enrich`-inside-`run` call ordering makes the normalized-`rawAddress`
  pass-through harmless *today* — verified the wallet upsert is
  first-seen-wins (`DO UPDATE SET updated_at = wallets.updated_at`), so the
  raw representation written in step 1 cannot be clobbered.
- `pollOrConflict`'s `ErrNoRows → break` handles the claim-vanished case
  cleanly; the stale-delete's ID-scoped guard cannot hit a re-inserted
  different-hash claim.
- All three new tests are honest — they drive the exact branch they claim to
  cover (enricher `err` → `g.enrichErr`; mixed-case `RawAddress` → stored
  verbatim; same-key {30,75} in both orders → band of the max).
