Orchestrator response to Devin Critic round-1 (VERDICT: CHANGES_REQUIRED, RA-1..RA-10).

Verdict accepted in full — every amendment is valid and has been applied to the plan files. You were correct on the load-bearing points: the TRC-20 gap is a tri-problem (input windowing, stored evidence, coverage channel), `StreamStatus` carries no per-stream coverage channel, `txRowJSON.BlockNumber` non-pointer means block-less rows fabricate `0`+`'pending'`, undeclared streams are silent data loss, and verbatim hex emission would hit `NormalizeTron`'s base58check-only contract 100% of the time.

## Concessions applied (all 10)

- **RA-1** → `phase-03` restructured: the central problem is now split into (b1) window bridge for BOTH streams (resolved ms window carried inside the page token — your point about repaying 2 bridge calls/step and fingerprint+params consistency), (b2) TRC-20 per-row block resolution chosen explicitly from {unique-txID `walletsolidity/gettransactioninfobyid` fan-out, anchor-map+fan-out, defer-stream-unfinished} with named non-options (block-less emission — `block_number` nullable vs `txRowJSON` non-pointer noted; dropped rows; ts arithmetic; block scanning; `StreamStatus.CoveredThrough` contract extension — recorded as a possible FUTURE decision, not this scope, and noted it fixes coverage but not stored evidence), (b3) binding invariants INV-1/INV-2 (+INV-3 the streamMaxBlock-before-buildRows ordering recorded as an enablement blocker).
- **RA-2** → INV-1 (cursor never advances past unresolved rows) and INV-2 (every emitted stream ∈ page.Streams every step) now binding invariants in phase-03 §(b3) AND test-asserted in phase-05.
- **RA-3** → phase-03 (d) is DECIDED: base58-canonical emission, adapter-side hex→base58check conversion with `0x41`-version assertion, deviation-from-verbatim justification recorded. No longer "open".
- **RA-4** → `walletsolidity/` for all reads (consistency with `only_confirmed`); txinfo-lag → bounded in-step retry → `ErrDegraded`, never block-less rows; monotonicity stated as assumption + dedup-safe low-edge widening; `min/max_timestamp` inclusivity added to the mandatory probe list.
- **RA-5** → calls-per-ensure worst-case arithmetic is now a Phase-2 requirement (head + steps×(pages + bridge + fan-out-clamped)) judged against free-tier quota; trc20 page clamp stated; Enabled() layering recorded (resolver gates inactivity — `Network` has no `IsActive`; the prompt's "zero calls when inactive" criterion is proven by `tests/non_evm_networks_test.go`, the adapter proves the config gate); key-required recommendation with chainscan/nodereal precedent.
- **RA-6** → phase-03 (e) now: iterate `raw_data.contract[]` (multi-contract txs), excluded trc20 types with probe evidence, INTERNAL/`internal_transactions` exclusion documented, zero-amount/zero-address policy made explicit, `contractRet` IsError rule, empty-Kinds=both.
- **RA-7** → `internal/transaction/list.go:91` added to phase-01 control list and phase-03 (f) blocker list; `txRowJSON.BlockNumber→*int64` recorded under the honest-NULL alternative (non-option in this scope).
- **RA-8** → phase-04: `ErrUnknownAddressFormat` dedicated typed error; `package wallet_test` + `check_wallet_wallet_test` DSN convention; SQL comments only in leading `--` blocks above `-- name:` lines.
- **RA-9** → phase-05: `var _ provider.TxProvider = (*Adapter)(nil)`; fan-out per-step bound + exact `UpstreamCalls` fixture; dormant package doc with contract revision + Enabled() layering; txinfo/getblockbynum fixtures incl. failures, `num:0` degenerate, `FromBlock≤ToBlock` guard.
- **RA-10** → phase-06: discovery doc status-header update + `writeDisabled` dormant-runtime shape recorded in the audit update; deps now `[2,3,4,5]`.

## Additional blind spots incorporated (your numbered list)

- #11 fingerprint+params consistency → resolved window in token (RA-4).
- #12 `getblockbynum(0)`/`fetchFrom=0` → explicit fixture.
- #13 `FromBlock≤ToBlock` guard pinned despite the `head<fetchFrom` path.
- #14 duplicate same-content events `.k` suffix → replay-safety fixture named.
- #15 zero-amount/zero-address policy → explicit decision required.
- #16 keyless probe success ≠ production fitness → stated in phase-02.
- #17 out-of-table candidates (Tronscan etc.) → exclusion reasons now required in the decision doc.

## One clarification, not a disagreement

On your "dormant adapter" note: agreed — unused exported package violates nothing and `init(){RegisterAllowedHosts}` never fires unimported. Phase-5's package doc records exactly that framing plus the contract revision, which addresses the drift-armor concern alongside the `var _ provider.TxProvider` assertion.

Please re-review the amended files at `plans/20261106-1946-tron-enablement-prep/` (plan.md + phase-01..06). If satisfied, emit `VERDICT: PLAN_CONSENSUS_REACHED`; if anything remains, name the specific amendment still unmet.
