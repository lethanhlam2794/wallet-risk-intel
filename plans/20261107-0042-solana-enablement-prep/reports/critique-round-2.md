Verifying the claimed amendments against the actual files.Now checking Phase 1 and Phase 4 for the claimed amendments.# Devin Critic — Round 2: Amendment Verification

All files re-read at the amended paths. Verification below is against the actual text, not the response's claims.

## Per-Amendment Verification

| RA | Verdict | Evidence |
|----|---------|----------|
| **RA-1** (bridge invariant) | ✓ Applied | `plan.md:95-109` — binding invariant stated verbatim ("emitted set per stream MUST be a contiguous ascending prefix"), the top-anchored leak is named with the correct mechanism, "no row is emitted until desc collection has passed below `FromBlock` (or the ledger floor)" is now the rule, and sig-list persistence is explicitly deferred to Phase 3 with the three viable options. `phase-03:29-44` mirrors it and adds the cross-step global-ascending edge case. The leaky sketch is genuinely struck — the remaining "emit ascending slices for fully-collected slots" phrasing in `phase-04:40-41` is loose but consistent under the corrected model (emission exists only post-full-collection). |
| **RA-2** (truncation rule) | ✓ Applied | `plan.md:69-73` (f) now cites the ledger-floor signal and explicitly rejects "oldest observed > FromBlock" with the correct reason. `plan.md:114-121` restates the outcome honestly: emit-everything + `Finished=false` + ended token ⇒ `partial` + `history_truncated`, watermark = `min(fetchTo, streamMaxBlock−1)` — matching `service.go:507-523`. `phase-03:69-79` same, and correctly demands verification against `writeTerminal`. |
| **RA-3** (probes) | ✓ Applied | `phase-02:40-53` — all three added: `getFirstAvailableBlock`/`minimumLedgerSlot`, the owner-vs-ATA empirical check + `getTokenAccountsByOwner` cost + closed-ATA unenumerability, and `getBlock` signatures-detail as `before`-seed for `fetchTo < head`. Bonus: `blockTime` nullability now probed on **both** endpoints (`:36-37`) and intra-slot ordering framed as an explicit question (`:32`). |
| **RA-4** (SPL discovery) | ✓ Applied | `plan.md:76-87` — (h) added with correct account-model reasoning and consequences for req-1 + the keyed-approval route. `phase-03:81-89` is a real decision section, not a stub — it constrains `spl`'s `Finished` semantics and offers honest fallbacks (per-ATA feeds, keyed indexing, coverage-limited/perpetual-unfinished named non-option mirroring defer-trc20). `plan.md:91-94` correctly downgrades "one feed, two streams" from premise to open question. |
| **RA-5** (blockers/claims) | ✓ Applied | `phase-01:51-65` — claims 11–13 added: `identity.go:116` lowercase corruption (citation verified: `txHash := strings.ToLower(tr.TxHash)` is at exactly :116, unconditional), NULL-ts keyset unreachability (`transactions.sql:64-67` verified), `Confirmations` slot semantics (`identity.go:188-191` verified). `plan.md:129-142` blockers list carries all three. |
| **RA-6** (Phase-4 tests) | ✓ Applied | `phase-04:56-75` — cross-step global ascending (`:58-59`), the zero-emission mid-collection test (`:60-62`, which is the direct regression test for the leak), dual bounds as named constants (`:70-72`), pinned batch `UpstreamCalls` convention (`:73-75`), and `spl`-never-lies-`Finished` assertion (`:68-69`). |
| **RA-7** (risk matrix) | ✓ Applied | `plan.md:194-202` — bridge leak elevated to High with the mechanism named; SPL discovery at High/structural; `fetchTo < head` unbounded-above; >1000-single-slot degenerate case. |

## Residual Notes (non-blocking)

- **Cosmetic staleness**: the open-problem set is now (a)–(h), but `plan.md:20-21` (Objective), `:150` (roadmap gate), `:168` (acceptance criterion), and `phase-03` frontmatter title/overview/success criteria (`:3,14,128`) still say "(a)–(g)". The (h) content exists in both files — the labels just lag. Should read (a)–(h) for consistency; zero functional impact.
- **Ordering**: (h) sits between (f) and (g) in both `plan.md` and `phase-03`. Unusual but harmless.
- No new errors introduced by the amendments; citations in the new text re-verified (`service.go:507-523`, `identity.go:116`, `:188-191`, `transactions.sql:64-67`).

## Verdict

**PLAN_CONSENSUS_REACHED**

All seven amendments are genuinely applied — not paraphrased into compliance. The binding invariant (contiguous ascending prefix; no emission before collection passes below `FromBlock`/ledger floor) now correctly encodes the constraint `service.go`'s watermark formula actually imposes, the truncation signal is mechanically sound, the SPL discovery gap is a first-class decision input with honest fallbacks, and Phase 4 carries the regression tests that would have caught the original sketch's failure. The plan is ready for Phase 1 execution.

Status: DONE
Summary: Round-2 verification complete — all RA-1..RA-7 amendments verified as applied in the files; consensus granted.
Concerns/Blockers: Cosmetic only — "(a)–(g)" labels in plan.md objective/roadmap/acceptance and phase-03 title should read "(a)–(h)" now that problem (h) exists.
