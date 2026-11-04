# Cook Report — Phase 6: Transaction Tracing

Plan: `plans/20261104-0056-transaction-tracing/` (9 Critic rounds →
PLAN_CONSENSUS_REACHED). Implemented per plan; deviations and residuals
listed at the end.

## What was built

### Phase 1 — Schema, provider seam, adapters, config

- `migrations/000008_transactions.up.sql` / `.down.sql`: `transactions`
  table (lowercase `confirmation_status` CHECK `pending|confirmed`,
  `is_error BOOLEAN NOT NULL DEFAULT false`), `wallet_index_state`
  (watermark, coverage counters, `history_truncated`, provider source,
  status/cooldown fields), and `data_sources` seeds for `chainscan` +
  `nodereal`.
- `queries/transactions.sql` + regenerated `internal/db/transactions.sql.go`:
  batch upsert via `jsonb_populate_recordset` (sqlc cannot type variadic
  `unnest` args — see deviations), index-state claim/advance/fail queries,
  keyset listing, and the wallet-edge scan used by tracing.
- `internal/provider/provider.go`: new `TxProvider` seam
  (`LookupTransfers`, `HeadBlock`, `SourceCode`, `Enabled`) alongside the
  existing `Provider` interface — no breakage.
- `internal/provider/chainscan/`: Etherscan V2-style adapter —
  `txlist` (native, `value>0` filter), `tokentx` (ERC-20), `eth_blockNumber`
  for head; per-chain `chainid`, `Enabled()` driven by
  `CHAINSCAN_ENABLED_CHAINS`.
- `internal/provider/nodereal/`: JSON-RPC `nr_getAssetTransfers` adapter for
  BSC — from+to streams, `category` filter, raw hex→decimal amount
  normalization, confirmations = `head - blockNum + 1`; `eth_blockNumber`
  for head.
- Both adapters wrap `internal/provider/resilience` (timeout, rate limit,
  concurrency limit, retry, circuit breaker, SSRF host allowlist, bounded
  bodies) with injected HTTP clients; `WithDurationFunc` hook times each
  upstream `Do()`.
- `internal/config/config.go`: provider keys/base URLs, enabled-chain
  lists, page size / max pages / reorg tail, `IndexingFreshTTL`,
  `IndexingStaleAfter`, per-adapter pool budgets, tracing bounds
  (max hops/nodes/edges, deadline, response cap, concurrency).

### Phase 2 — Indexing service + listing

- `internal/transaction/model.go`: statuses (`not_indexed|indexing|
  fresh|partial|failed|disabled`), outcomes, coverage.
- `internal/transaction/service.go` — `EnsureIndexed`:
  - singleflight per `(network,address)` (concurrent cold callers share
    one fetch sequence — test 14);
  - gates: disabled provider → persisted `disabled`; `fresh` /
    `partial` served without refetch unless `refresh=1`; `failed`
    obeys cooldown even under `refresh=1`; stale `indexing` claims become
    retryable after the stale horizon (tests 7, 17, 18c);
  - provider-switch: stored source ≠ current source resets watermark and
    reindexes (18b);
  - span-capped windows with reorg-tail overlap; contiguous-watermark
    advancement; persisted coverage counters; `history_truncated`
    persisted once max pages hit and reported on later serve/cooldown
    responses (18);
  - request-global provider `Budget` shared between root indexing and
    tracing hop-2 indexing; head calls consume budget;
    `BudgetExhausted` is a first-class outcome (never an error).
- `internal/transaction/identity.go`: content-hash `transfer_index` —
  `n:`/`t:` prefix + first 16 hex of SHA-256 over transfer type, asset id,
  normalized from/to, canonical amount; byte-identical duplicates get
  `.1`, `.2` suffixes counted max-per-stream across the assembled scan
  (context-free identity ⇒ cross-wallet ingestion dedups, test 13).
- `internal/transaction/list.go`: keyset pagination over
  `(block_timestamp DESC, id DESC)`; wallet-side from∪to scan; direction
  `self|out|in` with self rows emitted once and matching both `in`/`out`
  filters; `asset=native|<contract>` filter; malformed cursor →
  `INVALID_CURSOR`.

### Phase 3 — Bounded BFS tracing

- `internal/tracing/model.go` + `service.go`:
  - `max_hops ∈ {1,2}` only — 0 and ≥3 rejected (Phase 7 out of scope);
  - hop-1 edges exclude `is_error`, self-edges, root-returning paths;
    sentinel `0x0`/`0x…dead` counterparties emit items but are never
    expanded;
  - hop-2 positions processed in declared order with bounded concurrency;
    emitted items are the maximal contiguous resolved prefix; unresolved /
    budget-blocked positions stay in `nodes_remaining`;
  - failed/disabled node indexes resolve empty + `nodes_failed`;
  - bounds: deadline, shared provider budget (`max_provider_requests`),
    `max_nodes`, `max_edges`, response cap, `limit`; truncation precedence
    `deadline > max_provider_requests > max_nodes > max_edges >
    response_cap > limit`;
  - deterministic item order (hop, expansion position, normalized
    counterparty); relationship vocabulary `direct_counterparty` /
    `indirect_hop` only; `path_verified` always `false`;
  - evidence dedup by `(tx_hash, transfer_index)`;
    `temporal_order_valid` from non-decreasing block timestamps;
  - matched-category precedence `SANCTIONED` > label tier >
    `cross_chain_listing`; labels, same-network sanctions, cross-chain /
    unresolved listings, sources, verification status, entity and program
    carried in `matched_signals`;
  - coverage: root state, provider status, provider calls, node/edge
    counts, watermarks, snapshots, head block, skipped-row counters,
    truncation reason.

### Phase 4 — API, wiring, OpenAPI, metrics, tests

- `internal/httpserver/transactions.go`: thin handlers for
  `GET /api/v1/wallets/:network/:address/transactions` and
  `…/exposures`; input validation, `ErrInvalidInput` →
  `INVALID_CURSOR`/`VALIDATION_FAILED` 400 mapping, `refresh=1` support.
- `internal/httpserver/server.go`: optional `Transactions` + `Tracing`
  deps; routes registered only when wired.
- `cmd/api/main.go`: builds adapters (missing `ETHERSCAN_API_KEY` /
  `NODEREAL_API_KEY` ⇒ `Enabled()=false` DISABLED path, never a boot
  crash), transaction service, tracing service; `tracing.SetMetrics`.
- `internal/platform/metrics/metrics.go`: new
  `provider_request_duration_seconds{source}` histogram and
  `tracing_nodes_examined_total` counter; existing `provider_*` metrics
  reused.
- `api/openapi.yaml`: both routes with query params (cursor/limit/
  direction/asset/refresh, max_hops/refresh), response envelopes,
  coverage + truncation enum, relationship values, evidence schema;
  screening status enum fixed (~:1360) to include `INSUFFICIENT_DATA`;
  stale implemented-scope description near :14 updated; the 5
  `docs/feature-maps/assets/screening-*.json` captures regenerated with
  `exposure_tracing: "provider_reported_only"` per the declared
  deviation.
- `internal/testutil/testdb_integration_test.go`: expected seed list
  updated for the two new `data_sources` rows.

## Verification — commands run

| Command | Result |
|---|---|
| `gofmt -l .` | empty ✓ |
| `go build ./...` | pass ✓ |
| `go vet ./...` | pass ✓ |
| `go test ./...` | pass ✓ (all packages, incl. `tests` 98.9s) |
| `go test -race ./...` | pass ✓ after fixing a real race (below) |
| `sqlc generate` | clean ✓ (`internal/db/transactions.sql.go`, `models.go`) |
| `python3 yaml.safe_load(api/openapi.yaml)` | parses ✓ |

Race fix: `edgesExamined` was a plain `int64` mutated by concurrent
hop-2 workers (`service.go`); converted to `atomic.Int64`
(store/add/load) matching the sibling counters. `-race
./internal/tracing/` now clean.

## Required tests (plan §5) — all present and passing

| # | Test | Where |
|---|---|---|
| 1 | chainscan/nodereal fixture normalization | `internal/provider/{chainscan,nodereal}/adapter_test.go` |
| 2 | upsert idempotency + mutable-column revalidation | `internal/transaction/service_test.go` |
| 3 | DISABLED honest coverage (both adapters) | adapter tests + service tests |
| 4 | 1-hop labeled → `direct_counterparty` + evidence | `internal/tracing/service_test.go` |
| 5 | 2-hop → `indirect_hop`, path len 2, temporal order | same |
| 6 | bound truncation reasons | `max_nodes` + `max_provider_requests` covered directly |
| 7 | freshness refetch / fresh serve / FAILED cooldown | `internal/transaction/service_test.go` |
| 8 | provider failure → typed `coverage.provider_status` | transaction + tracing tests |
| 9 | confirmation mapping + reorg-tail mutable updates | service tests |
| 10 | same-network direct sanctions → `SANCTIONED` | `TestExposures_SanctionsPrecedence` |
| 11 | cursor pagination honesty, limit, `INVALID_CURSOR` | `tests/transactions_api_test.go` |
| 12 | relationship vocabulary, no ownership language | API + tracing tests |
| 13 | multi-transfer identity, `.k` duplicates, cross-wallet dedup, W→W | `internal/transaction` tests |
| 14 | concurrent cold `EnsureIndexed` = one fetch | singleflight test |
| 15 | budget exhaustion before max-nodes → `max_provider_requests` | tracing tests |
| 16 | per-network enablement matrix | adapter + config tests |
| 17 | FAILED keeps partial rows + coverage, cooldown honored | service tests |
| 18 | `history_truncated` persists on later responses | service tests |
| 18b | provider-switch source mismatch resets watermark | service tests |
| 18c | `refresh=1` bypasses freshness, not cooldown; stale indexing retryable | service tests |
| 19 | cross-chain-only → `cross_chain_listing`, never `SANCTIONED`; precedence | `TestExposures_SanctionsPrecedence` |
| 20 | self-edges + root-returning excluded; `path_verified=false` | tracing tests |
| 21 | `is_error` rows: no edges, still listed | transaction + tracing tests |
| 21b | W→W self row once, direction filters, sentinels not expanded, `max_hops` validation, filters | API + tracing tests |
| 22 | contiguous-prefix + deterministic order | `TestExposures_Deterministic` |

## Bugs found & fixed during implementation

- sqlc could not infer variadic `unnest` types → upsert rewritten as a
  JSONB `populate_recordset` batch (verified against real Postgres).
- `confirmation_status` initially emitted uppercase → aligned to the
  plan's lowercase `pending|confirmed` vocabulary (migration CHECK too).
- Serve-path `EnsureIndexed` outcomes left `WalletID=0` → listing found
  nothing; fixed by assigning `out.WalletID` on every serve/budget path.
- Budget-blocked hop-2 positions were counted as resolved → reworked so
  never-attempted positions stay in `nodes_remaining` and raced
  acquisitions roll back `examined`.
- `edgesExamined` data race under concurrent workers (above).
- Test-harness expectation: `data_sources` seed list updated for
  `chainscan`/`nodereal`.

## Deviations / residuals

- **Duration hook placement.** The resilience hook chain has no duration
  field, so adapters time their own `Do()` calls via `WithDurationFunc`
  into `Metrics.ProviderDurationHook(source)` — same observability, one
  layer deeper than the plan's wording.
- **`screening` captures.** Regenerated `screening-*.json` assets report
  `exposure_tracing: "provider_reported_only"` — the plan-declared
  deviation for the synchronous screening path (tracing is wired on the
  dedicated `/exposures` route, not inside `POST /screenings`).
- **Test 6 partiality.** `deadline`, `max_edges`, and `response_cap`
  truncations are exercised implicitly (precedence order is unit-covered
  for `max_provider_requests`/`max_nodes`); the other three reasons are
  implemented and emitted but not each given a dedicated fixture — noted
  as residual coverage risk.
- **NodeReal live verification.** NodeReal adapter behavior is verified
  against injected fake-RPC fixtures only (no live key/network in tests);
  the mapper follows the documented `nr_getAssetTransfers` shape.
- Untracked Phase-5 files (`internal/screening`, `internal/scoring`,
  `queries/screenings.sql`, `migrations/000007_*`, `screening-*.json`
  assets, `internal/middleware/errors.go` idempotency codes) predate this
  phase and were left untouched.

## Files changed (Phase 6)

- `migrations/000008_transactions.up.sql`, `…down.sql` (new)
- `queries/transactions.sql` (new)
- `internal/db/transactions.sql.go`, `internal/db/models.go` (generated)
- `internal/provider/provider.go` (TxProvider seam)
- `internal/provider/chainscan/{adapter,mapper,adapter_test}.go` (new)
- `internal/provider/nodereal/{adapter,mapper,adapter_test}.go` (new)
- `internal/transaction/{model,service,list,identity,service_test}.go` (new)
- `internal/tracing/{model,service,service_test}.go` (new)
- `internal/httpserver/{server,transactions}.go`
- `internal/config/config.go`
- `internal/platform/metrics/metrics.go`
- `internal/testutil/testdb_integration_test.go`
- `cmd/api/main.go`
- `api/openapi.yaml`
- `tests/transactions_api_test.go` (new)
- `docs/feature-maps/assets/screening-*.json` (5 regenerated captures)
