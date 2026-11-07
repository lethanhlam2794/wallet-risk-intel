# Devin Review — Round 1: Solana enablement-prep increment

> Independent review by a fresh Devin session (review worker, read-only env).
> Scope: `456c5f9..HEAD` (4 commits) — dormant `provider.TxProvider` adapter
> `internal/provider/solanarpc/` + decision/semantics docs. Reviewed
> adversarially against `provider.go:282-288`, `service.go:179-732`,
> INV-S1..S5.
>
> **Verification limitation (disclosed by reviewer):** environment rejected
> every `go` invocation (`build`/`vet`/`test`, 3 attempts) and file writes.
> All findings are static analysis + the increment's own `gate-evidence.md`
> claims (all green, `ok … 51.124s`) — not independently re-run.

## Headline verdicts on the binding contract

- **INV-S1: HOLDS.** Only emission site is `adapter.go:428-440`, gated on
  `tok.Bottomed` — set exactly two ways: feed row below
  `lowerBound = max(F, floor)` (`:376-378`) or short-page exhaustion
  (`:393-395`). No path emits a top-anchored slice; `maxCollectSigs` overflow
  returns empty transfers + unfinished streams + ended token (`:413-422`) →
  `partial`+`history_truncated`.
- **Ordering: HOLDS.** Sorted once at bottoming `(slot,txIndex,sig)`
  (`:400-409`); `tok.Emit` monotonic (`:439`) ⇒ cross-step global ascending.
- **Truncation composition: VERIFIED against `service.go`.** `floor>F` ⇒
  `Finished=false` even at `emitDone` ⇒ `allFinished=false` ⇒
  `service.go:554-558` default branch ⇒ `StatusPartial` ⇒
  `HistoryTruncated=true` (`:657,669`); watermark min-folds `streamMaxBlock−1`
  (`:511-523`).
- **Extraction: correct** — `meta.err` excluded (collection + `mapTx`), fee
  never emitted, mint via ix→`mintOf(dst)`→`mintOf(src)`→honest skip, owner
  post>pre>raw-ATA, `transferWithSeed`, `spl-token-2022` (vendor-confirmed
  name), NULL `blockTime`→zero→NULL, `IsError=false` documented.
- **Dormancy: VERIFIED** — zero refs outside the package (grep), `init()` =
  allowlist only, `Enabled()` two-level gate, keyless, no DDL/wiring/
  OpenAPI/wattrFiles diffs.
- **Bounded work: enforced** — `UpstreamCalls` = real HTTP (batch=1,
  test-verified).

## Findings

### Minor

1. **JSON-RPC `code:429` mistyped as `ErrDegraded` not `ErrRateLimited`** —
   `adapter.go:703-709`. Probe §8 documents the real throttle signal as a
   `code:429` error body on HTTP 200; `rpcErr` maps it to degraded →
   persisted status reads "degraded", `OnRateLimited` hook never fires.
   Failover unaffected (both kinds eligible); still an honest failure. Fix:
   `case e.Code == 429 → ErrRateLimited`.
2. **Doc binds `min(FromBlock, ledgerFloor)`; code correctly uses `max`** —
   `solana-indexer-semantics.md:68,:287-288` vs `adapter.go:326-329`. `max` =
   observable bottom, strictly correct (sigs below floor can't exist); doc
   formula would demand collecting below an unreachable floor. Fix doc —
   reviewers cite INV-S1 literally.
3. **`solana-provider-decision.md` arithmetic stale** — "≤2 seed" (`:48`) vs
   `maxSeedWalk=5`; "≤14/step" (`:55-58`) vs ≤17–18 first-step; "1+5×14=71"
   (`:62-63`) vs ≤67; "~4 RPS" (`:64`) vs code 2 RPS (`adapter.go:174`).
4. **No intra-page ordering guard** — `adapter.go:364-399`: first
   below-bound row `break`s; later page rows unexamined. A misordered page
   (in-window rows after a below-bound row) → silent sig drop + `Finished`
   claim — a quiet coverage-lie path. Probe-verified ordering (0/1000+
   violations) makes it unlikely; a non-increasing-slot check → `ErrDegraded`
   is ~5 lines of hardening.
5. **`seedBelow` swallows all typed errors; error return is dead** —
   `adapter.go:499-528`: `-32602`-class failures burn 5 calls and silently
   degrade to head-scan; `:338`'s `if err != nil` unreachable.
6. **Failed steps' upstream calls uncharged** — `calls` returned only on
   success; `service.go:441-442` settles only on non-nil page → budget
   undercounts real traffic on errors. Bounded; document the gap.

### Nit

7. `adapter.go:561` — `r.Slot <= 0` rejects genesis slot-0 sigs as malformed
   (hard-fails a feed reaching genesis).
8. Dead fields: `pageToken.Exhaust`, `ix.ProgramID`, `tokenBalance.ProgramID`,
   `getTxResult.Slot` (or use it as a `tx.Slot != s.Slot` cross-check).
9. `mapper.go:98-101` — duplicate `innerInstructions` index overwrite +
   out-of-range index silently dropped.
10. `adapter.go:629-639` — single-call `rpc` doesn't validate response `id`.
11. Semantics `:95` labels `emitCap` "transfers/step" — actually sigs
    fetched; emitted rows can exceed 100.
12. Blind suite never exercises a successful `before` seed (`baseUpstream`
    getBlock → nil always); positional anchoring rests on probe §4 + one
    named test.
13. `session-log.md:44` says "HTTP 429s" — probe shows JSON-RPC `code:429`
    on HTTP 200.
14. `adapter.go:305-312` — `FloorSet` doubles as "window pinned"; decoded
    `FloorSet=false` token re-pins and adopts foreign `Sigs`/`Frontier`, and
    emitted sigs aren't re-validated `slot ∈ [F,T]` at `:428-440`.
    In-process-only → unreachable; harden if the token ever crosses a
    boundary.
15. "23 named tests" in docs vs 24 `Test*` functions in `adapter_test.go`.
16. `opts.PageSize` accepted, never referenced.
17. `Kinds={NATIVE}` still fetches SPL sigs via `getTransaction`
    (unavoidable — sig kind unknowable pre-fetch) — worth a doc line.

## Verified non-issues (examined, rejected)

Seed-fallback head-scan still fully collects `[lowerBound,T]`; empty feed +
`floor>F` → conservative `Finished=false`; short page all `>T` → honest
finish; foreign-seed `Signatures[0]` covers all of slot `T` (probe §4);
failed-tx sig skip at collection is coverage-safe (transfer-domain prefix
intact); `init()` matches trongrid dormancy shape.

## Verdict

The coverage-honesty core survives adversarial review — zero-emission
collection is enforced by construction and the truncation composition
round-trips correctly through `service.go`. Findings are one error mistype,
one doc-formula defect, stale arithmetic, and defense-in-depth gaps — none
blocking for a dormant adapter. Fix items 1–4 before §15 enablement.

`Verdict: APPROVED`

---

Status: DONE_WITH_CONCERNS
Summary: Review complete — INV-S1..S5 hold under adversarial analysis; verdict APPROVED with 6 Minor + 11 Nit findings (top fixes: 429→ErrRateLimited typing, min→max doc formula, decision-doc arithmetic, missing page-monotonicity guard).
Concerns/Blockers: Read-only environment — could not write `review-round-1.md` (report is inline above for capture) or re-run `go test`/`go vet`; test-green status relies on the increment's own gate evidence.
