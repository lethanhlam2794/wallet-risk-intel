---
plan: solana-enablement-prep
title: "Solana Enablement Prep — dormant TxProvider adapter + verified semantics"
status: pending
priority: P2
risk: R3
blockedBy: []
blocks: [solana-enablement (future §15 flip)]
source-prompt: docs/prompts/20261107-0033-claude-code-solana-enablement-prep.md
mirrors: plans/20261106-1946-tron-enablement-prep
---

# Solana Enablement Prep

## Objective

Make Solana **code-complete but still INACTIVE** — "flip `is_active` and it
works": pick a verified free provider, implement a dormant `TxProvider`
adapter that can never emit traffic until configured AND the network is
active, resolve every open problem (a)–(h) into
`docs/solana-indexer-semantics.md`, and produce the decision docs a §15
reviewer needs to flip it later.

Risk class: **R3** — external provider integration + schema-adjacent
semantics. Enablement itself (`is_active` flip, selector wiring, env
surface, DDL) stays behind the AGENTS.md §15 gate and is NOT this scope.

## Context carried forward (verified by orchestrator scouting, 2026-11-07)

Groundwork already shipped — **do not redo**:

- `NormalizeSolana` — `internal/wallet/address_solana.go:10-22` (base58,
  32–44 chars → exactly 32 bytes, verbatim — no case folding).
- `AddressFormatSolana` via `namespace="solana"` —
  `internal/network/address_format.go:12,22-24`; `wallet.Normalize`
  dispatch — `internal/wallet/address.go:54-65`.
- `solana` row `is_active=FALSE` — `migrations/000026_non_evm_networks.up.sql`.
- `Service.GetOrCreate` stamps `net.AddressFormat()` and fails closed on
  unknown namespace — `internal/wallet/service.go:29-35`.
- OFAC mapper `SOL` + solana-format → `CHAIN_SPECIFIC` (network `""`,
  unmapped) — `internal/provider/ofac/mapper_test.go:29`.
- Resolver only loads active networks — `internal/network/resolver.go:63-65`
  (`ListActiveNetworks`), so `wattrFiles` never requests `solana.csv`.
- TRON pattern to mirror: dormant unwired adapter
  `internal/provider/trongrid/` + `docs/tron-indexer-semantics.md` +
  `docs/tron-provider-decision.md` — reuse the *skeleton*, not the
  open-problem set.

Unlike TRON, Solana does **not** map cleanly onto the EVM-shaped contract.
The real open problems (prompt + scouting):

- **(a) tx_hash** = ~88-char base58 signature — fits `VARCHAR(255)` but is
  not hex. `identity.go` `contentHash` is a content-derived SHA-256
  (`identity.go:41-45`) → likely reusable verbatim; must be *asserted* on
  base58 input.
- **(b) slot ≠ block ≠ wall-clock** — `block_number BIGINT` must store
  SLOT; intra-slot ordering is not defined by slot alone (deterministic
  tiebreak needed).
- **(c) pagination is signature-desc, not block-asc** —
  `getSignaturesForAddress` returns newest-first pages (`before`/`until`
  cursors, ≤1000/page); the indexer's ascending-block watermark contract
  needs a desc→asc bridge (the core adapter design problem).
- **(d) finality = `finalized` commitment only** — `confirmed`/`processed`
  can fork.
- **(e) SPL extraction** — token movement lives in `innerInstructions` /
  pre-post `tokenBalances`; failed txs (`meta.err`) roll back inner
  instructions but still charge fees — decide fee-only semantics; never
  fabricate. Vote/system-program noise policy must be stated.
- **(f) free public RPC truncates history** → needs an honest
  `history_truncated`/coverage signal path. The signal is the **node's
  retained-ledger floor** (`getFirstAvailableBlock`/`minimumLedgerSlot` —
  Phase-2 probe) vs `FromBlock`, NOT "oldest observed sig > FromBlock"
  (every newer address trips that — Critic R1).
- **(g) Kinds mapping** — `NATIVE`↔lamports SOL, `TOKEN`↔SPL
  (mint = asset id, `amount_raw` = u64 decimal string).
- **(h) SPL DISCOVERY coverage** (Critic R1 — the decision-determining
  problem): `getSignaturesForAddress(owner)` indexes only txs where
  `owner` is in `accountKeys`. An SPL transfer's keys are
  `[sourceATA, destATA, (mint,) authority]` — the *receiving* wallet's
  owner pubkey is absent ⇒ **incoming SPL transfers to a wallet's ATA
  are invisible on the owner feed**. Honest owner-level SPL history via
  raw RPC needs `getTokenAccountsByOwner` + per-ATA feeds — unbounded,
  and closed ATAs are unenumerable forever. Consequence: the keyless
  baseline may fail requirement 1 for SPL-incoming → routes the decision
  to the keyed-approval path OR a documented coverage-limited design
  (`spl` stream perpetual-unfinished named non-option, mirroring TRON's
  defer-trc20 fallback).

## Architecture sketch (for debate, locked by Phase 3)

- **Streams**: `sol` (native) + `spl` (token) over `TransferPage.Streams`
  — per-stream `Finished`. NOTE: whether both ride the same upstream feed
  is a Phase-2/3 question, not a given — owner-feed native SOL is
  owner-indexed, but SPL incoming needs per-ATA feeds (open problem h).
- **Window bridge (corrected after Critic R1)**: `FromBlock`/`ToBlock` =
  slot range → `getSignaturesForAddress` desc collection. **Binding
  invariant: the emitted set per stream MUST be a contiguous ascending
  prefix of the requested window** — the watermark formula
  (`service.go:507-523`, `cov = streamMaxBlock − 1` for unfinished
  streams) is only sound when emission is bottom-anchored contiguous.
  Emitting a top-anchored slice `(frontier, T]` while `[F, frontier]`
  is uncollected lets `streamMaxBlock ≈ T` claim coverage the adapter
  never fetched — a permanent silent gap (Critic R1 proof). Therefore:
  **no row is emitted until desc collection has passed below `FromBlock`
  (or the ledger floor)** — i.e. the full window's signature list is
  known first; emission is then ascending slices of it. Phase 3 decides
  sig-list persistence across steps: token-embedded bounded list
  (~64-90B/sig → sig-cap the window), bounded per-step re-collection
  (token = emission cursor; O(sigpages)/step), or the named non-option.
- **Per-tx detail**: raw RPC path needs `getTransaction` per emitted
  signature (fan-out; JSON-RPC batching is the main lever — probe limits);
  Helius-style parsed APIs fold transfers into the listing response at
  ~1 call/100 txs but require an API key.
- **Truncation honesty (corrected after Critic R1)**: retained-ledger
  floor above `FromBlock` ⇒ `[F, floor)` unobservable ⇒ emit everything
  honestly collected, keep stream `Finished=false`, end the token ⇒
  indexer writes `partial` + `history_truncated` and the watermark stays
  the top-side contiguous bound `min(fetchTo, streamMaxBlock−1)`
  (the only bound `service.go` produces — "oldestObserved−1" was wrong;
  a ledger-floor probe distinguishes node truncation from a genuinely
  new address). No contract change needed.
- **Failed txs**: `meta.err != null` ⇒ excluded (rolled back); fee is an
  implicit balance change, NOT a transfer instruction — never emitted.
- **Finality**: `HeadBlock` = `getSlot` @ `finalized`; all reads pinned to
  `finalized` commitment.
- **Gating**: `Enabled(net) = apiKey != "" && chains[lowercase(code)]` for
  a keyed provider, or `chains[code]`-only for the keyless public RPC —
  plus resolver inactivity (dormant: not imported by any binary).
- **Enablement blockers (documented §f-equivalent, NOT fixed)**:
  `identity.go` `NormalizeEVM`→`wallet.Normalize(net,…)` switch (INV-3
  analogue — solana rows would skip as `InvalidAddress` while coverage
  advances), **`identity.go:116` `strings.ToLower(tr.TxHash)` — corrupts
  case-sensitive base58 signatures** (Critic R1 landmine; stored tx_hash
  would decode to different bytes — worse than a skip), `list.go`/`transactions.sql:64-67`
  NULL-`block_timestamp` keyset (null-ts rows unreachable past page 1 —
  read-path consequence of the no-fabrication rule), `Transfer.Confirmations`
  slot semantics (slot distance ≠ confirmation depth; skipped slots
  inflate it), `queries/transactions.sql:30` `'evm'` literal,
  `list.go:91`, `data_sources` row, providerconfig descriptor, selector
  wiring, env surface, `solana.csv`, OpenAPI `Address` pattern, OFAC
  `SOL`→solana network mapping, warmindex EVM-only gates
  (`queue.go:47`, `seed.go:195-203`), `is_active` flip.

## Phase roadmap

| # | Phase | Deliverable | Gate |
|---|-------|-------------|------|
| 1 | Audit drift check | `reports/audit-drift-check.md` — VERIFIED/DRIFTED per claim | every claim adjudicated |
| 2 | Provider eval | `docs/solana-provider-decision.md` + discovery-table rows flipped to VERIFIED/REJECTED | zero UNCONFIRMED; stop on card-required / keyed-free-only |
| 3 | Indexer semantics | `docs/solana-indexer-semantics.md` — resolves (a)–(h) or named-non-option defers | no silent gaps |
| 4 | Dormant adapter | `internal/provider/<chosen>/` satisfying `TxProvider` + tests | acceptance criteria below |
| 5 | Gates + docs | clean `gofmt`/`vet`/`build`/`test`; `non-evm-audit.md` updated; `reports/final-report.md` | all gates exit 0 |

## Acceptance criteria (from source prompt)

- [ ] `solana` row still `is_active=FALSE`; `/networks` still returns
      `bsc`+`ethereum` only.
- [ ] Adapter emits zero upstream calls when `Enabled()=false` or network
      inactive (httptest counter test proves it).
- [ ] Refetching the same slot window yields identical `transfer_index`
      sets (replay-safe, no dup inserts — test).
- [ ] Ascending-order + per-stream `Finished` contracts hold in tests,
      incl. a multi-page signature window straddling a slot boundary.
- [ ] No fabricated slot/block_time — rows lacking finalized evidence are
      never emitted (named non-option).
- [ ] Every discovery-doc Solana row is VERIFIED-with-evidence or
      REJECTED-with-reason; zero UNCONFIRMED left.
- [ ] Semantics doc resolves (a)–(h) or explicitly defers with named
      non-options — no silent gaps.
- [ ] No new external dependency without asking; no secrets or keys in
      code, tests, or commits.

## Stop conditions (task-level — override --auto)

Pause and surface to the user BEFORE:

- flipping `is_active` or modifying `blockchain_networks` in any migration;
- changing `transactions`/`wallet_index_state` DDL;
- wiring the adapter into any provider selector, providerconfig
  descriptor, `data_sources` seed, or `cmd/*` default;
- creating `solana.csv` or touching `wattrFiles`;
- generalizing the OpenAPI `Address` pattern;
- **choosing a provider that requires a credit card — hard STOP**;
- **choosing a keyed-free provider (e.g. Helius free tier) — reportable
  outcome but needs user approval before the adapter phase proceeds**;
- editing warmindex (queue stays EVM-only);
- deleting existing files or changing EVM/TRON adapter behavior;
- paying for or embedding any API key.

## Risk matrix

| Risk | Likelihood | Mitigation |
|------|-----------|------------|
| **Bridge emission leaks coverage (top-anchored emission ⇒ `streamMaxBlock−1` over-claims; Critic R1)** | High — was the plan's headline defect | Binding invariant pinned in Phase 3: emit only contiguous ascending prefix; no emission before collection completes below `FromBlock`; cross-step ascending test mandatory |
| SPL incoming transfers undiscoverable on owner feed (open problem h) | High (structural) | Phase-2 owner-vs-ATA probe quantifies the gap; outcome routes to keyed-approval or documented coverage limit — never a silent `Finished` |
| getTransaction fan-out exceeds calls-per-ensure budget | Medium | JSON-RPC batching probe + accounting convention; dual per-step bounds (sig-pages AND fan-out); Helius as keyed upgrade path |
| `fetchTo < head` (WindowCap/lagged head) leaves desc collection unbounded above | Medium | `getBlock` signatures-detail `before`-seed probe; documented WindowCap restriction fallback |
| Public RPC truncates history silently | High (expected) | Ledger-floor probe (`getFirstAvailableBlock`/`minimumLedgerSlot`); unfinished-stream + ended token ⇒ partial + history_truncated |
| SPL extraction picks a non-authoritative source | Medium | innerInstructions-vs-tokenBalances decision locked by probe evidence; raw-chain reproducibility required |
| Keyed-only viable candidate hits approval gate | Medium | Keyless baseline evaluated first; keyed-free reported not chosen without approval |
| `meta.err` / fee semantics fabricated | Low | finalized-only + meta.err exclusion + fee-not-a-transfer documented; no row without finalized evidence |
| >1000 sigs in one slot blocks frontier passage | Low | Named edge case — bounded zero-emission steps documented |

## Stage 1 debate summary

Devin Critic round 1 (`cyclic-buffer`): **CHALLENGED** — structurally
complete plan, all ~30 citations verified, but the bridge sketch was
provably unsound and the open-problem set missed the decision-determining
problem. Seven required amendments (RA-1..RA-7), all conceded and applied:

- **RA-1**: struck the top-anchored "emit above the desc frontier" rule —
  it over-claims `cov = streamMaxBlock − 1` while `[F, frontier]` is
  unfetched (permanent silent gap). Replaced with the binding invariant:
  emitted set per stream = contiguous ascending prefix of the window; no
  emission until collection passes below `FromBlock`/ledger floor.
- **RA-2**: rewrote truncation honesty — signal = retained-ledger floor
  (`getFirstAvailableBlock`/`minimumLedgerSlot` probe) not "oldest sig >
  FromBlock"; watermark stays top-side `min(fetchTo, streamMaxBlock−1)`.
- **RA-3**: added 3 probe classes (ledger floor, SPL discovery gap,
  `getBlock` signatures `before`-seed).
- **RA-4**: added open problem **(h)** — SPL incoming transfers are
  invisible on the owner feed (receiver owner not in `accountKeys`);
  constrains `spl.Finished` honesty and may route the decision to the
  keyed-approval gate.
- **RA-5**: added `identity.go:116` `ToLower` (corrupts base58 sigs),
  NULL-`block_timestamp` keyset unreachability, `Confirmations` slot
  semantics to claims + blockers.
- **RA-6**: Phase-4 tests — cross-step global ascending, zero-emission
  mid-collection step, dual per-step bounds, batch `UpstreamCalls`
  convention.
- **RA-7**: risk matrix elevated (bridge leak High w/ mechanism; SPL
  discovery High/structural).

Round 2: **PLAN_CONSENSUS_REACHED** — all amendments verified applied
against the files. Cosmetic `(a)–(g)`→`(a)–(h)` label fix applied after.
Reports: `reports/critique-round-1.md`, `reports/rebuttal-round-1.md`,
`reports/critique-round-2.md`.

## Session audit trail

- Devin Critic (rounds 1+2): session `cyclic-buffer` — consensus
  2026-11-07.
