I'll start by reading the mandatory compliance inputs and the plan structure.Plan structure: 5 phases in roadmap, 5 phase files on disk — structural audit pending content verification. Now let me read all phase files and the binding docs.All 5 phase files exist on disk matching the 5-phase roadmap. Now deep codebase verification — reading the actual contract surfaces the plan cites.Now the rest of service.go (writeTerminal/history_truncated), trongrid adapter, warmindex gates, and migrations.Now the wallet/network/resolver layer, OFAC mapper, queries, list.go, and migrations.Verifying remaining citations: OFAC mapper test, queries, list.go, migrations, chainscan gating, labels publish predicates.Now the binding reference docs and the TRON mirror plan/doc structure.Strong picture forming. Let me verify a few remaining citations: `RegisterAllowedHosts`, `wattrFiles` locations, `Budget` semantics, and the TRON mirror plan structure.# Devin Critic — Adversarial Plan Review

## 1. Structural Audit Result

**PASS.** Roadmap declares 5 phases (`plan.md:113-119`); disk contains exactly `phase-01` through `phase-05`, all with frontmatter, requirements, and success criteria. Zero missing specification files. Consensus is therefore *evaluated*, not withheld on structure.

## 2. Executive Assessment

The plan is a faithful, well-scoped mirror of the proven TRON skeleton. I verified ~30 file:line citations against current code — **all accurate** (`identity.go:41-45` contentHash, `:88,93,104` NormalizeEVM, `:192-196` NULL-ts; `service.go:339-344` settle, `:398-408` lag, `:493-559` watermark+terminal; `provider.go:269-271` ordering contract; `warmindex/queue.go:47`, `seed.go:195-203`; `queries/transactions.sql:30`; `list.go:91`; `mapper_test.go:29`; `000008:12,22,33`, `000019`, `000026`; `resolver.go:63-65`; `chainscan/adapter.go:150-152`). Stop-condition fidelity is complete — every source-prompt stop is preserved, including the keyed-free→approval split.

**However**: the plan's central architectural sketch — the signature-desc→ascending bridge and the truncation-honesty rule — is **provably unsound under the actual indexer mechanics in `service.go`**. I can construct a concrete sequence where the sketched rule produces a permanent, silent coverage gap (the exact INV-2-class failure the TRON doc was built to prevent). Additionally, the open-problem set (a)–(g) misses the single most decision-relevant Solana problem — SPL *discovery* coverage — which likely predetermines the Phase-2 provider outcome.

Verdict: **CHALLENGED** — structurally sound plan with a fatally under-specified core sketch. Amendments below are required before consensus.

## 3. Phase-by-Phase Critique

### Phase 1 — Audit Drift Check: **CHALLENGED**

The 10-claim list is good but misses three verifiable hazards, including one real landmine:

- **`identity.go:116` `txHash := strings.ToLower(tr.TxHash)`** — not in the claim list, not in `non-evm-audit.md`'s NormalizeEVM-caller table, not in the plan's enablement-blockers list. Base58 signatures are case-sensitive; lowercasing a Solana signature **corrupts it** (decodes to different bytes — the stored `tx_hash` would be unresolvable upstream). This is worse than the INV-3 "skip as InvalidAddress" mode: even a perfectly normalized solana row gets a mangled hash. It must be cited in the drift check and the §(f)-equivalent blockers table.
- **`list.go` NULL-`block_timestamp` interaction** — `ListTransactions` orders `block_timestamp DESC, id DESC` and keysets on `(block_timestamp, id) < (cursor_ts, cursor_id)` (`transactions.sql:64-67`). If `blockTime` is null on emitted rows, they sort first (NULLS FIRST under DESC) but can never satisfy a non-NULL cursor predicate → unreachable past page 1. The plan asserts "no fabricated block_time" (correct) but never accounts for the read-path consequence.
- **`Transfer.Confirmations` slot semantics** — `Confirmations = head − blockNumber + 1` (`identity.go:188-191` drives `confirmation_status`). For Solana, slot distance ≠ confirmation depth (skipped slots inflate it; slots aren't blocks). Needs a claim/adjudication row.

### Phase 2 — Provider Eval + Live Probes: **CHALLENGED**

The probe list is strong (`before`/`until`, intra-slot ordering, batch support, `maxSupportedTransactionVersion`, `blockTime` nullability, truncation horizon). Three decision-relevant probes are missing:

1. **The SPL discovery gap (the big one)** — `getSignaturesForAddress(owner)` indexes only txs where `owner` appears in `accountKeys`. For an SPL transfer instruction the accounts are `[source-ATA, dest-ATA, authority]` — the owner of a *receiving* ATA is not a key. **Incoming SPL transfers to a wallet's ATA are invisible to the owner-address feed**; delegate spends (authority = delegate, owner absent) are equally invisible. Honest SPL history via raw RPC requires `getTokenAccountsByOwner` + per-token-account signature feeds — unbounded accounts, and *closed* ATAs are unenumerable forever. This is not a field-mapping problem; it's an indexing-architecture gap that likely makes the keyless baseline **fail requirement 1 outright** → the decision outcome is probably "keyed Helius or a documented SPL-coverage-limited keyless design" → the stop-gate fires. The plan's sketch ("one feed, two logical streams", plan.md:76-78) already presumes the keyless feed suffices — that's the assumption to attack, and it's absent from the probe list.
2. **`getFirstAvailableBlock` / `minimumLedgerSlot`** — required to fix the broken (f) rule (below). Absent from the probe list.
3. **`getBlock`/`getBlockAvailability` with `transactionDetails:"signatures"`** — the only slot-anchored `before` seed available to bound desc collection when `fetchTo < head` (see blind spot #4). Absent.

### Phase 3 — Indexer Semantics: **CHALLENGED (highest severity)**

The phase's *mandate* is correct (resolve (a)–(g) with evidence). The *sketched answers* it seeds are wrong in two load-bearing places.

**(i) The frontier-emission rule leaks coverage — proof.**

Plan sketch (plan.md:80-85, phase-03:30-34): "emit ascending only slots strictly above the desc frontier slot." Per-phase-04's token design ("window + desc collection cursor + emission position" — cursors, not the sig list), emission must occur *as* slots complete during descent. Emitted set per step = `[f+1, e]` where `f` = desc frontier (oldest collected), `e` = emission cap. This is a **top-anchored floating window**.

The watermark formula (`service.go:507-523`) computes, for an unfinished stream, `cov = streamMaxBlock − 1` — sound **only** when the emitted set is a contiguous ascending prefix of `[fetchFrom, …]` (EVM feeds emit bottom-anchored ascending; `provider.go:269-271`'s ordering requirement is the load-bearing premise). Counter-example: window `[F,T]`; step 1 collects desc to frontier `f` (F < f < T), emits `(f,T]` ascending → `streamMaxBlock ≈ T` → `cov = T−1` → watermark `T−1` claims `[F,T−1]` covered while `[F,f]` was never emitted. Next ensure: `fetchFrom = T−1−tail` (service.go:393-396) — the gap `[F,f]` sits below the new window and is **never re-entered**. Permanent silent data loss, worse than INV-3 because rows don't even skip — they were never fetched.

**Required**: the semantics doc must pin the invariant *"emitted set per stream = contiguous ascending prefix of the requested window"* — i.e., **no emission until the desc frontier passes below `FromBlock`**, which forces the sig-list persistence question the token sketch dodges: bounded-window token-embedded list (~64B/sig → token caps around ~1-2K sigs), bounded per-step re-collection (token = emission cursor; each step re-scans desc to the cursor — honest, O(N/1000)/step, budgetable), whole-window single step (breaks per-step `UpstreamCalls` bounds — must be reconciled or rejected), or the named non-option (defer-unfinished). Whatever Phase 3 chooses, the sketch's emit-above-frontier rule as written must be struck.

**(ii) The truncation rule is wrong on both ends.**

Plan (plan.md:90-94, phase-03:59-64): "signature list ends while oldest observed slot > `FromBlock` ⇒ upstream truncated … watermark = `oldestObserved−1`".

- **Signal**: an address's sig list *always* ends at its own first activity — indistinguishable from node ledger truncation via the list alone. As written, `oldestObserved > FromBlock` fires for essentially every wallet on every ensure (any address whose first tx is above `fetchFrom` → permanent `partial`+`history_truncated` for all Solana wallets). The correct signal is the **node's retained-ledger floor** vs the window bottom: `getFirstAvailableBlock() > FromBlock` ⇒ `[fetchFrom, floor−1]` unobservable ⇒ truncated. That's a probe + design fix.
- **Watermark claim**: `service.go` will never produce `oldestObserved−1` — the code emits `min(fetchTo, streamMaxBlock−1)` = top-side coverage, with `history_truncated` as the gap channel (`writeTerminal` `:657`). If Phase 4 literally implemented `wm = oldestObserved−1`, every ensure would re-collect `[oldestObserved−tail, head]` — the entire visible history — forever, and the observed range would be persistently under-claimed. Restate (f) as: emit everything honestly collected, stream `Finished=false`, empty token ⇒ `partial` + `history_truncated`, watermark = top-side contiguous bound.

**(iii) Missing open problems** — add to the (a)–(g) set: **(h) SPL discovery coverage** (above — its consequences for the `spl` stream's `Finished` semantics and provider choice), **`Confirmations`/slot semantics**, **`identity.go:116` in the blockers table**, and **NULL-`blockTimestamp` read-path policy**.

### Phase 4 — Dormant Adapter: **CHALLENGED**

Faithful trongrid mirror (gating, resilience, SSRF allowlist, dormant `init()`, `var _` assertion — all verified at `trongrid/adapter.go:110-172,612-614`). Required additions:

- Test for **global** ascending across steps, not just per-step (the acceptance criterion "ascending-order contract" must cover the cross-step sequence the bridge emits).
- Pin **two** per-step bounds as named constants: max desc sig-pages collected AND max `getTransaction` fan-out (only the latter is gestured at via "page clamp"). Tron-parity demands both.
- Pin `UpstreamCalls` counting for JSON-RPC batch (one HTTP request, N logical calls — which side of the ledger does Budget charge?).
- Note degenerate case: one feed → both streams share pagination; per-stream `Finished` is honest only if the SPL-coverage question (h) is resolved — a finished `spl` stream claiming "complete" is a lie if the feed can't see ATA-routed transfers.

### Phase 5 — Gates + Docs: **ACCEPTED**

All four gates, dormancy check via `go list -deps`, `/networks` assertion, stop-condition ledger — complete and proportionate.

## 4. Blind Spots & Edge Cases (consolidated)

| # | Issue | Consequence if unamended |
|---|---|---|
| 1 | Top-anchored emission vs bottom-anchored watermark formula | Permanent silent coverage gap — the plan's headline risk |
| 2 | Truncation rule conflates address-history-end with ledger purge | Every wallet = perpetual `partial`+`history_truncated`; or under-claimed coverage if implemented literally |
| 3 | SPL ATA-routed/delegate transfers invisible to owner feed | `spl` stream `Finished` lies; requirement-1 failure likely un-anticipated → decision doc outcome predetermined differently than plan assumes |
| 4 | `fetchTo < head` (WindowCap, lagged head) → desc scan from head to `to` has no slot bound above except `before`=<signature> | Unbounded upstream pages per step; need `getBlock(to+1)`-seeded `before` or documented WindowCap restriction |
| 5 | `identity.go:116` `strings.ToLower(txHash)` | Corrupted stored signatures — uncited blocker |
| 6 | NULL `block_timestamp` rows unreachable by list-path cursor | Read-path coverage lie, undocumented |
| 7 | Sig-list persistence across steps (token carries cursors only) | Forces the leaky design or O(N²) re-collection — must be decided, not discovered in Phase 4 |
| 8 | Single-slot-heavy address (>1000 sigs/slot) | Frontier can't pass the slot → zero-emission steps; bounded but worth a named edge case |
| 9 | wSOL (`So1111…`) classification, `transfer`-without-mint resolution, token-2022, `meta.err` diverges from chainscan's emit-with-`is_error` convention | Partially covered in (e)/(g) — needs explicit statement that Solana *excludes* failed txs where EVM emits `is_error=1` evidence rows |

## 5. Required Plan Amendments

- **RA-1 (Phase 3, core):** Replace the frontier-emission sketch with the binding invariant: *emitted set per stream must be a contiguous ascending prefix of `[FromBlock, ToBlock]` — no emission before the desc frontier passes below `FromBlock`*. The doc must resolve sig-list persistence (token-embedded bounded list / bounded re-collection / single-step full window with reconciled `UpstreamCalls`) or record the named non-option.
- **RA-2 (Phase 3 (f)):** Rewrite truncation honesty: signal = retained-ledger floor (`getFirstAvailableBlock`, or verified equivalent) above `FromBlock`; outcome = `partial` + `history_truncated` + top-side watermark. Strike "`watermark = oldestObserved−1`".
- **RA-3 (Phase 2):** Add probes: owner-vs-ATA SPL discovery gap, `getFirstAvailableBlock`/`minimumLedgerSlot`, `getBlock` signatures-detail for `before`-seeding, `blockTime` nullability on `getSignaturesForAddress` entries *and* `getTransaction` under `finalized`, batch rate accounting.
- **RA-4 (open-problem set):** Add **(h)** SPL discovery/coverage scoping to (a)–(g), with its consequence for `spl` `Finished` semantics and provider-selection requirements — this likely routes the decision to the keyed-approval path, which the plan should anticipate explicitly.
- **RA-5 (Phase 1 + blockers table):** Add `identity.go:116`, NULL-ts list-path policy, `Confirmations` slot semantics to the claims/blockers.
- **RA-6 (Phase 4 tests):** Global cross-step ascending assertion; dual per-step bounds as named constants; batch `UpstreamCalls` convention.
- **RA-7 (Risk matrix):** Elevate the bridge risk from Medium to High with the actual failure mechanism named (top-anchored emission ⇒ silent gap), not just "can't satisfy contract".

No amendment touches scope, stop conditions, or the TRON-mirror skeleton — those survive review intact.

Status: DONE_WITH_CONCERNS
Summary: Plan is structurally complete and meticulously verified against code, but its core bridge sketch (top-anchored emission vs the bottom-anchored watermark formula) provably leaks coverage, the truncation rule is wrong on both ends, and the SPL discovery gap — likely the provider-decision-determining problem — is absent from the open-problem set. Seven concrete amendments (RA-1..RA-7) required before consensus.
Concerns/Blockers: Coverage-leak in sketched desc→asc bridge; broken truncation signal; missing SPL ATA-discovery problem; three missing Phase-2 probes; uncited `identity.go:116` base58-lowercase landmine.
