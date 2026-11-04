# Devin Critic — Round 1: Transaction Tracing plan

scope: `plans/20261104-0056-transaction-tracing/` (plan.md + 4 phase files)
verified against: `docs/tailieu.md` (§4.7, §7.13, §9.9–9.10, §11, §13, §15–17,
§19), `internal/provider/provider.go`, `internal/provider/resilience/`,
`internal/screening/{mapper,model}.go`, `internal/scoring/{engine,model}.go`,
`internal/enrichment/service.go`, `internal/ingestion/locks.go`,
`internal/wallet/address.go`, `internal/httpserver/server.go`,
`internal/middleware/errors.go`, `queries/{wallets,sanctions,labels_read}.sql`,
`migrations/000001/000007`, `api/openapi.yaml`, `internal/testutil/testdb.go`,
plus live verification of upstream provider status (Etherscan/BscScan/
Routescan/Blockscout docs, checked 2026-11-04).

---

## [Blocker] B1 — Provider premise is dead: BSC has no free Etherscan-family source, and both listed URLs are defunct

**Plan claim** (plan.md:61–64):

| network | upstream |
|---|---|
| ethereum | `api.etherscan.io/v2/api` (v2 unified) **or** `api.etherscan.io/api` |
| bsc | `api.bscscan.com/api` + `BSCSCAN_API_KEY` |

and plan.md:71 "Etherscan-family covers BOTH chains with one normalized
surface; free tier permits dev non-commercial use".

**Verified facts (2026-11-04):**

- `api.etherscan.io/api` (V1) was **fully deprecated 2025-08-15** — all V1
  requests stopped working (Etherscan Information Center). The "or V1"
  fallback in the table points at a dead endpoint.
- `api.bscscan.com/api` was **deprecated in Dec 2025 and ceased operations
  ~July 2026** (BNB Chain migration guide → Etherscan V2 / BSCTrace).
  `BSCSCAN_API_KEY` no longer exists as a concept: V2 uses **one**
  Etherscan key for all chains.
- Etherscan V2 (`https://api.etherscan.io/v2/api`) selects the chain via a
  **`chainid` query parameter**, not per-network base URLs. The plan's
  per-network URL+key model is the V1 model.
- **Decisive:** Etherscan's supported-chains table marks BSC mainnet
  (`chainid=56`) **Paid Tier Only**. A free key calling `chainid=56`
  returns `"Free API access is not supported for this chain. Please
  upgrade your api plan"`. The free tier (~3 req/s, 100k/day) covers
  `chainid=1` and selected chains — not BSC.
- Routescan (spec §4.7-listed, Etherscan-compatible, keyless free tier
  2 req/s / 10k/day) **does not index chainid 56** — its supported-chains
  list has ~35 EVM chains, BSC absent. No hosted Blockscout instance
  serves BSC either (`chains.blockscout.com` has no 56;
  `bsc.blockscout.com` → 404).

So the plan's claim "covers BOTH chains … free tier" is false for BSC —
and BSC is the explicit tracing target chain (spec §11.3 "Target chain:
BNB Smart Chain"). Under AGENTS §2 "No paid providers without approval"
and §15 "stop and request review before adding a paid provider", the
plan as written cannot deliver BSC indexing at all.

**Amendment demanded:**

1. Rewrite §3.1: single adapter against `api.etherscan.io/v2/api` with
   `chainid=<net.ChainReference>` (the resolver already carries `"1"`/`"56"`,
   `internal/network/resolver.go:14`), **one** `ETHERSCAN_API_KEY`, and a
   per-chain enabled matrix — `Enabled(net)` returns false for `bsc`
   unless a paid plan is configured, reported as DISABLED-with-reason,
   never silently "indexed".
2. Pick and verify a BSC source or escalate. Realistic free candidates:
   BSCTrace/MegaNode (`bsc-mainnet.nodereal.io`, free tier,
   `nr_getAssetTransfers` — spec §4.7 "other providers with verified free
   tiers"), or public-RPC `eth_getLogs` for BEP-20 Transfer events
   (spec §4.7 explicitly anticipates event-log indexing; AGENTS §5 warns
   only against assuming RPC token-history *queries*). If neither pans
   out, the plan must say so and request approval — not ship ETH-only
   while claiming both chains.
3. Per AGENTS §5 last rule, cite the actual ToS/quota per source chosen
   (Etherscan free = rate-limited dev/non-commercial tier; commercial use
   requires a paid plan — fine for this MVP, state it).
4. Test-list addition: adapter `Enabled()` is per-network — ETH key
   present ⇒ ETH enabled, BSC reports DISABLED with reason surfaced in
   coverage — assert this in a unit test.

## [Blocker] B2 — `transfer_index` has no honest derivation; the UNIQUE key will silently drop real transfers

**Plan claim** (plan.md:49–50, phase-01:6–8): upsert identity
`(network_id, tx_hash, transfer_index)`; `ON CONFLICT DO NOTHING`.

**Problem:** spec §7.13 (`docs/tailieu.md:895`) makes `transfer_index`
part of identity and §7.13 rules demand "Cần định danh riêng cho event
log" — but the proposed source cannot supply one:

- `txlist` → one row per tx (no transfer index at all).
- `tokentx` → **no per-transfer identifier**: the response carries
  `transactionIndex` (the *tx's* position in its block — identical for
  every transfer in that tx) and **no `logIndex`**. Multi-transfer txs
  are routine (swaps, multicall, disperse) — N rows share `tx_hash`.
- `txlistinternal` → has `traceId` (usable).

Whatever the adapter synthesizes collides: `transactionIndex` is shared
by all token transfers in a tx; positional ordinals break when a page
boundary splits a tx's transfers; and `(tx_hash, "0")` collides *across
types* because `transfer_type` is **not** in the unique key — a NATIVE
row and an INTERNAL row of the same tx both indexed `"0"` →
`ON CONFLICT DO NOTHING` silently discards one. That is exactly the
"silent drop" class of defect this repo's invariants exist to prevent:
the index claims completeness it does not have.

**Amendment demanded:**

- Specify a namespaced deterministic vocabulary, e.g. `n` (or
  `n:{transactionIndex}`) for NATIVE, `t:{ordinal}` where ordinal = the
  transfer's index *within its tx* computed from the same-tx group in the
  fetched result set (must specify how a page-boundary split of one tx's
  transfers is detected/handled — refetch with adjusted window or
  deferred upsert), `i:{traceId}` for INTERNAL.
- State the collision rule for byte-identical repeat transfers
  (same contract/from/to/value twice in one tx — ordinal handles it;
  assert it).
- Required tests (add to plan §5): a tx containing ≥2 token transfers
  produces ≥2 distinct rows; identical-amount duplicates both survive;
  refetch of the same pages yields 0 dupes **and identical
  transfer_index values** (stability, not just idempotency).

## [Blocker] B3 — `indexed_until_block` semantics are undefined; as named, coverage can lie about which history exists

**Plan claim** (plan.md:84–86,112–114): bounded fetch (paginated,
`max_pages`) → `indexed_until_block` recorded; index-state "drives
coverage honesty".

**Problem:** the plan never defines the indexing window, and the two
natural choices give opposite meanings to the same column:

- `sort=desc` + max_pages ⇒ coverage = "all transfers from head down to
  some block" — `indexed_until_block` would have to mean the *oldest*
  covered block, opposite its name, and re-fetch windows on staleness
  are unspecified (re-scan from head every 10 min?).
- `sort=asc` watermark ⇒ `indexed_until_block` = highest *contiguously
  covered* block; refresh continues from watermark+1 — matches the name,
  cheap incremental refetch, but first requests for a busy wallet cover
  *ancient* history, not recent activity.
- Both are capped: Etherscan-family `page×offset` returns at most 10,000
  records — full history for active wallets requires `startblock`/
  `endblock` windowed pagination, which the plan never mentions.

**Amendment demanded:** define the window contract in §3.2 explicitly —
recommended: ascending watermark semantics
(`indexed_until_block` = max block contiguously indexed; stale refresh
fetches `(indexed_until+1 .. head]` bounded by `max_pages` and records
the new watermark or `head_gap` honestly) — or pick descending-recency
but then rename the column and define the coverage field that reports
"history from block B onward only". Either way, state the 10k record cap
and the block-window pagination strategy.

## [Major] M4 — `/transactions` response carries no coverage/freshness fields at all

plan.md:119–122 lists item fields only; §3.2's promise that index-state
drives "coverage honesty" has no wire presence. A response serving 3
pages of a 10,000-tx wallet is indistinguishable from a complete index —
a silent completeness lie on the endpoint whose entire purpose is
honest evidence.

**Amendment:** the data envelope must include
`{index_status, provider_status (typed ErrKind vocab from
provider.go:155–168), indexed_until_block, indexed_at,
history_truncated, next_cursor}` — same honesty bar §9.10 imposes via
`coverage` on exposures. Add to OpenAPI + a test asserting
`history_truncated` when `max_pages` bites.

## [Major] M5 — No concurrency control on lazy indexing → duplicate upstream fetch storms

Nothing in the plan prevents N concurrent first-requests for one wallet
(or one hot counterparty across many /exposures calls) from issuing N
identical provider page loops — burning the ~3 req/s free quota and
racing identical upsert batches. The repo already owns the pattern:
`singleflight.Group` (`internal/enrichment/service.go:39,217`;
`internal/label/service.go:103`) and `pg_advisory_lock` helpers
(`internal/ingestion/locks.go:14–25`).

**Amendment:** `EnsureIndexed` must singleflight on `(network_id,
wallet_id)` (in-process; document whether an advisory lock is added for
api+worker cross-process). Required test: K concurrent EnsureIndexed on
one cold wallet ⇒ exactly 1 upstream fetch sequence.

## [Major] M6 — Bound arithmetic for hop-2 doesn't close at the stated defaults

plan.md:104–106: `max_nodes=25`, `max_provider_requests=25`, deadline
25 s. But one `EnsureIndexed` costs ≥2 upstream calls (txlist page +
tokentx page; more under paging), so the request budget exhausts at
~8–12 nodes — `max_nodes=25` is unreachable, and the plan never states
which bound fires first nor a `truncated` reason vocabulary. Serialized
at the free tier's ~3 req/s, 25 calls burn ≥8 s of limiter wait inside
the 25 s deadline *before* per-call upstream latency (adapter Timeout up
to ~8 s each — one slow page can eat the whole budget).

**Amendment:** publish the arithmetic (calls-per-EnsureIndexed =
endpoints × pages; effective node bound = ⌊max_provider_requests /
calls⌉); declare bound precedence and a closed `truncated` reason enum
(`max_nodes`, `max_provider_requests`, `max_edges`, `deadline`,
`response_cap`); state whether hop-2 EnsureIndexed runs under bounded
concurrency or serial; add the required test asserting the *named*
reason when the provider budget fires before max_nodes. Consider a
smaller `max_pages` for hop-2 nodes vs hop-0 — unstated.

## [Major] M7 — Counterparty→wallets resolution is unspecified (FK, join, and bloat all ride on it)

`transactions` stores only `from_wallet_id`/`to_wallet_id`
(`docs/tailieu.md:898–899`); every BFS join and every exposure item must
resolve an *address string* to a `wallets` row. The plan never mentions
creating wallet rows for counterparties. `GetOrCreateWallet`
(`queries/wallets.sql:1–6`) is single-row — a 10k-row history means up
to 20k single upserts per index unless a bulk resolve exists; and every
counterparty becomes a first-class `wallets` row (visible via
`GET /wallets/{addr}`, bloat at scale). Leaving NULLs is not an option —
it breaks the joins.

**Amendment:** specify bulk counterparty resolution (`INSERT … SELECT
unnest(…) ON CONFLICT DO NOTHING`, returning an address→id map for the
batch); state that counterparty rows are created with
`address_type='unknown'` and normalized form, and acknowledge
wallets-table growth + the fact that these rows are addressable by other
endpoints. Also state that provider-emitted addresses pass through
`wallet.NormalizeEVM` (`internal/wallet/address.go:28`) before resolve —
the plan is silent.

## [Major] M8 — FAILED index-state retry semantics + partial-batch coverage undefined

phase-02:7 "FAILED + error + no fake empty; caller still serves what DB
has". Unanswered: does `status=FAILED` bypass the 10-min freshness gate
and hammer a dead provider on *every* request? If a mid-loop failure
already upserted k pages, does coverage show partial coverage or does
`indexed_until_block` claim a watermark the data doesn't have?

**Amendment:** define FAILED-state retry = allowed but throttled (a
failure cooldown, e.g. the enrichment negative-TTL convention ~1 min,
`config.go:81`), and require coverage to report
`{rows_indexed, window_covered, status}` — partial rows surfaced as
partial, never as complete.

## [Major] M9 — Screening contract drift: `coverage.tracing` is hardcoded `"NOT_IMPLEMENTED"` and the plan never mentions POST /screenings

`internal/screening/model.go:171` + `mapper.go:322–327` hardcode
`Tracing: "NOT_IMPLEMENTED"`; the Phase-5 plan explicitly deferred
own-graph exposures to this phase
(`plans/20261103-2318-risk-scoring/plan.md:26`). The moment this phase
lands, that literal string becomes false on the §9.7 response — and if
screenings *should* now consume graph exposures, that's a contract +
scoring-surface change (scoring only scores `basis=provider_reported`,
`engine.go:179`; a `graph_trace` basis is already correctly unscored —
`engine_test.go:240`).

**Amendment:** declare the decision in §3.5/§6 — either (a) screenings
stay provider-only by design: redefine the field to an honest value
(e.g. `"provider_reported_only"` or a documented enum) with an OpenAPI
update and a reason; or (b) screenings ingest graph exposures under a
new `basis` vocabulary — flagged as a §9.7 contract addition with
OpenAPI examples. Silence is not an option; today this is an unowned
contract drift.

## [Major] M10 — Sanctions matching for counterparties must reuse the same three-bucket semantics, or it re-violates an invariant

plan.md:97–98 "Match counterparties against `wallet_labels` +
`sanctions_wallets` (published snapshots only)". Existing semantics
(`queries/sanctions.sql:1–19`): direct = same `normalized_address`
**and same `network_id`** and `revoked_at IS NULL`; cross-chain and
network-unresolved listings are *separate* buckets — matching them as
"sanctioned" on the queried network re-violates "never turn an ETH
listing into a BSC listing" (AGENTS §5). Phase-03:7 doesn't specify
which bucket produces the `SANCTIONED` exposure category.

**Amendment:** reuse `sanctions.Service.LookupWallet` (already
snapshot-scoped, three-bucket) or the identical SQL semantics; state
that only the same-network direct bucket yields category `SANCTIONED`
exposure items, and specify whether cross-chain/unresolved counterparty
listings are reported under a distinguished flag or excluded — either is
defensible, counting them as direct is not. Labels match via
`wallet_labels` `revoked_at IS NULL` on the resolved counterparty wallet
row (depends on M7).

## [Major] M11 — Self-edges, path dedup, and multi-edge collapse are unspecified

- A wallet's self-transfer (`from==to`) produces a hop-1 "exposure" to
  itself — nonsense evidence unless excluded.
- `max_paths_per_pair=3` (plan.md:105) exists but no dedup rule: is a
  path a unique node sequence? Which of A→B's k parallel edges is
  emitted — earliest? largest? all (then paths-per-pair means what)?
- `transaction_evidence` "deduped" (phase-03:8) — dedup key unstated.

**Amendment:** exclude self-counterparties (test it); define path
identity, the per-pair edge-selection rule, and the evidence dedup key
(`tx_hash+transfer_index`).

## [Minor] m12 — `temporal_order_valid` and `path_verified` remain undefined

Spec §11.5 lists both; plan.md:125 says "explicit
`path_verified:false`/`temporal_order_valid` semantics documented" —
then documents neither. **Amendment:** define
`temporal_order_valid` = hop edges non-decreasing in `block_timestamp`
(or block_number) along path order, evaluation rule for multi-edge
paths stated; `path_verified` = constant `false` in MVP (we can never
verify same-asset continuity — spec §11.5's own caveat), documented as
deliberate, never emitted `true`. Encode both in OpenAPI descriptions.

## [Minor] m13 — `transaction_evidence` vs `path[]` duplication has no contract

§9.10 lists both fields; §3.5 emits both per item; phase-03 dedups
evidence. Without definitions the two arrays encode the same edges
twice and can drift. **Amendment:** `path[]` = ordered edges per path
(the chain); `transaction_evidence` = flat deduped union of edges across
the item's paths — or drop one and declare the deviation in §6.

## [Minor] m14 — `refresh=1` is an undeclared deviation from §9.9/§9.10

plan.md:82 adds `refresh=1`; spec §9.9 filters
(`docs/tailieu.md:1288–1295`) and §9.10 params list no refresh; plan
§6:156–161 declares "None planned". **Amendment:** move to §6
deviations with justification, or drop it.

## [Minor] m15 — `confirmation_status` "verbatim passthrough" describes a field the provider doesn't emit; reorg posture absent

txlist returns `confirmations` (a count) + `isError`; there is no status
enum to pass through (plan.md:48, test #9). Separately: nothing handles
reorg — a previously indexed tx that leaves the canonical chain simply
disappears upstream while our row persists with a stale status forever.

**Amendment:** define the status vocabulary + mapping (e.g.
`confirmations > 0` → `CONFIRMED`; decide how `isError=1` — an execution
failure, *not* a confirmation state — is surfaced); define the reorg
posture (re-validate rows inside a head window on reindex, or document
the residual limitation in coverage — §7.13 "Phải xử lý blockchain
reorganization" is a listed rule).

## [Minor] m16 — Cursor format, response-size cap, `direction` filter unspecified

Repo convention is opaque base64 cursors + `next_cursor`
(`api/openapi.yaml:185–188,941–943`; `INVALID_CURSOR` exists,
`middleware/errors.go:20`) — plan test #11 says "cursor honest" with no
format (keyset on `(block_timestamp, id)`?). §11.4 requires "maximum
response size" — `limit≤100` bounds item count but each item carries
`path[]`+evidence arrays; state the byte cap or the arithmetic bound.
`direction` on exposures (§9.10) is undefined for multi-hop paths whose
edges disagree — first edge? any edge?

## [Minor] m17 — E2E provider injection unstated

`tests/transactions_api_test.go`/`exposures_api_test.go` must inject a
stub `TxProvider` (fixture-backed — never live upstream, same discipline
as `internal/provider/*/testdata`); `httpserver.Deps` wiring
(`server.go:38–60`) must expose the seam. Implied, not stated — make it
explicit or E2E will hit the network.

## [Nit] n18 — `max_pages`/page-size defaults unstated (phase-01:11); specify + justify against the 10k record cap.

## [Nit] n19 — `wallet_index_state` PK `(network_id, wallet_id)` — `wallet_id` alone is unique per wallets UNIQUE(network_id, normalized_address); harmless, simplify or justify.

## [Nit] n20 — Provenance: register the new source in `data_sources` (consistent with `ingestion_runs.source_id` usage) rather than a bare string in index-state — or justify the divergence.

## [Nit] n21 — §9.10 exposure items: state explicitly that only label/sanctions-matched counterparties become items (raw counterparty list is not exposed) — spec purpose is "liên kết ví rủi ro"; the plan implies it but doesn't say it.

---

Verified-as-true plan claims (credit where due): spec sections cited
(§4.7, §7.13, §9.9–9.10, §11.4–11.5, §17 `tracing_nodes_examined_total`)
exist as described; migration 000008 is the next free number;
`internal/transaction`/`internal/tracing` match the AGENTS §3 layout;
the `TxProvider` seam fits spec §13 "not every provider supports every
interface"; `internal/provider/resilience` already supplies
timeout/limiter/semaphore/breaker/retry/allowlist (`client.go:26–50`);
`LookupError`/`ErrKind` reuse is correct (`provider.go:155–185`);
`basis=provider_reported` separation is real (`scoring/model.go:41–43`);
test infra (`testutil` _test-DB guard, `tests/` package) supports the
required list. Could NOT verify: the claim that a free BSC source exists
via this adapter family — refuted, see B1.

CHALLENGED
