Orchestrator response to Devin Critic round-1 (CHALLENGED, RA-1..RA-7).

You are correct on every load-bearing point. Conceding all of them — the amended plan is at `plans/20261107-0042-solana-enablement-prep/` (same paths). Re-read the diffs and confirm or counter.

**RA-1 — CONCEDED (the fatal one).** Your counter-example is airtight: `cov = streamMaxBlock − 1` (service.go:507-523) is sound only under bottom-anchored contiguous emission; my top-anchored "emit above the frontier" rule would let `streamMaxBlock ≈ T` claim `[F, T−1]` while `[F, f]` was never fetched — and `fetchFrom = wm − tail + 1` never re-enters it. Permanent silent gap, exactly the INV-2-class failure mode. Amended: plan.md and phase-03 (b) now pin the binding invariant *emitted set per stream = contiguous ascending prefix of [FromBlock, ToBlock]; no emission until desc collection passes below FromBlock (or ledger floor)*, and force the sig-list persistence decision (token-embedded bounded list / bounded re-collection / named non-option) into Phase 3. The sketch as written is struck.

**RA-2 — CONCEDED.** Two errors in one rule: (i) "oldestObserved > FromBlock" fires for every address whose first activity postdates the window bottom — perpetual partial for all wallets; the honest signal is the retained-ledger floor (`getFirstAvailableBlock`/`minimumLedgerSlot`, now a mandatory Phase-2 probe). (ii) `oldestObserved−1` is not a value service.go can produce — corrected to emit-everything + `Finished=false` + ended token ⇒ `partial` + `history_truncated` + top-side watermark. Amended in plan.md (f)/(h) and phase-03 (f).

**RA-3 — CONCEDED.** Phase-2 probe list extended: `getFirstAvailableBlock`/`minimumLedgerSlot`; `getBlock` signatures-detail as slot-anchored `before`-seed for the `fetchTo < head` case; and the owner-vs-ATA SPL discovery empirical check + `getTokenAccountsByOwner` enumeration + closed-ATA unenumerability.

**RA-4 — CONCEDED (the biggest one).** Your SPL discovery analysis is right: an SPL transfer's accountKeys carry `[sourceATA, destATA, (mint,) authority]` — the receiver's owner pubkey is absent ⇒ incoming SPL transfers are invisible on `getSignaturesForAddress(owner)`. Added as open problem **(h)** in plan.md and a dedicated Phase-3 decision section constraining `spl`'s `Finished` semantics and the provider req-1 verdict. The "one feed, two streams" presumption is now flagged as a Phase-2/3 question, not a given. The keyed-approval route is explicitly anticipated as a possible outcome (stop-condition split preserved).

**RA-5 — CONCEDED.** `identity.go:116` `strings.ToLower(tr.TxHash)` is a genuine landmine — verified it runs unconditionally for every provider row and corrupts case-sensitive base58 sigs (worse than a skip — silent corruption). Added to Phase-1 claims (#11) and the enablement-blockers list. Also added NULL-`block_timestamp` keyset reachability (transactions.sql:64-67, #12) and `Confirmations` slot semantics (#13) to Phase-1 claims and the blockers list.

**RA-6 — CONCEDED.** Phase-4 test matrix extended: cross-step global ascending assertion; a mid-collection step emitting zero rows (not a top-anchored slice); dual per-step bounds as named constants (sig-pages AND fan-out); pinned JSON-RPC batch `UpstreamCalls` accounting convention.

**RA-7 — CONCEDED.** Bridge risk elevated to High with your failure mechanism named verbatim; added SPL-discovery row at High (structural), `fetchTo < head` unbounded-above row, and the >1000-sigs-single-slot degenerate case.

No rebuttals — your scouting was deeper than the sketch deserved. One clarification note for the record: blind spot #9's `meta.err` divergence from chainscan's `is_error=1` convention is now explicitly listed inside Phase-3 (e) as a decision item (exclude-and-document vs emit-with-IsError), not silently assumed.

Re-verify the amended plan.md + phase files and return `PLAN_CONSENSUS_REACHED` or your remaining objections.
