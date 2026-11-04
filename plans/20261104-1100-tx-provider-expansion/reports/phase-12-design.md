# Phase 12 design — ordered failover chain (sign-off artifact)

D1 is pre-decided (schema re-key). This doc nails D2–D5 mechanics before
code. Status: **for implementation**.

## D1 — Watermark semantics: schema re-key (pre-decided, option a)

`wallet_index_state` re-keys to `PRIMARY KEY (network_id, wallet_id,
source_id)`; `source_id` becomes NOT NULL. Each adapter owns an
independent coverage row (watermark, counters, status, freshness clock).
Provider switch no longer destroys coverage — a new adapter simply has
no row yet and indexes from its own cold start; a returning adapter
resumes its own watermark.

Migration `000019_wallet_index_state_source_key`:

- **up**: `DELETE` rows with `source_id IS NULL` (already distrusted by
  the old reset guard — no provenance to lose), drop the 2-column PK,
  `ALTER COLUMN source_id SET NOT NULL`, add the 3-column PK.
- **down**: keep the freshest row per `(network_id, wallet_id)`
  (`DELETE a USING b WHERE a.updated_at < b.updated_at OR (equal AND
  a.source_id < b.source_id)`), `DROP NOT NULL`, restore the 2-column
  PK. Downgrade loses per-source history — documented, acceptable
  (rollback implies pre-failover single-source semantics).

Ripple: the cross-source `ResetWalletIndexSource` logic in
`service.go:191-208` is **deleted** — per-source rows make "trust no
other adapter's watermark" structural instead of imperative. The
`!SourceID.Valid` arm dies (NULL impossible post-rekey).

## D2 — Budget semantics

- **Pool**: one request-global pool per `EnsureIndexed` call (unchanged
  mechanics: `tryTake(1)` prepay per seam call, `charge` settles
  `UpstreamCalls-1`). Pool size = `max(cfg.Budget[name])` over the
  resolved chain members — `BudgetFor(net)` returns that max. A shared
  pool sized by the primary would cap the secondary at the primary's
  quota; per-adapter pools would allow Σ(budgets). Max-of-chain keeps
  the request bounded by the most generous member while still letting a
  secondary use its own headroom.
- **Per-provider cap**: each attempt may spend at most
  `cfg.Budget[adapter.Name()]` upstream calls within the request.
  Hitting the per-adapter cap is a *boundary stop*, not a provider
  failure: the attempt closes as `partial` (honest bound) and does NOT
  trigger failover.
- **Accounting**: `out.ProviderCalls` accumulates real
  `UpstreamCalls` across all attempts; budget charged by actual calls.
  Per-source `rows_*` counters persist per attempt's own row.

## D3 — Failover mechanics (chain walk inside `Service.ensure`)

Rejected alternative: a `provider.Chain` wrapper implementing
`TxProvider` — it cannot own per-source watermark/state attribution
(service-layer concern) without awkward winner-exposure hooks. The
walk lives in `ensure()`; `adapters` becomes
`map[string][]provider.TxProvider`.

Per attempt `i` over the ordered chain:

1. Resolve `srcID_i = sourceID(adapter.Name())` **per attempt**.
2. Load `st_i = state(net, wallet, srcID_i)` (per-source row).
3. Per-source gates in this exact order (preserves current order):
   a. `fresh|partial` and within `FreshTTL` and `!Refresh` → serve `st_i`, return.
   b. `!adapter.Enabled(net)` → disabled-skip; continue.
   c. `failed` and within `NegativeTTL` cooldown → cooldown-skip; continue.
   d. `indexing` with live `updated_at` claim → serve `st_i`, return.
4. Fetch attempt: `HeadBlock` → per-source watermark → `fetchFrom`/
   `fetchTo` → `MarkWalletIndexing(srcID_i)` → paginate
   (`PageToken=""`, adapter-scoped) → single accumulated batch.
5. Error handling:
   - failover-kind (`rate_limited`, `upstream_5xx`, `degraded`,
     `transport`, `circuit_open`) → write FAILED on `st_i`'s row
     (kind + indexed_at), **discard the attempt's accumulated batch**,
     continue to next adapter — fresh head, fresh window, fresh token.
   - non-failover kind (`client_4xx`, `malformed`, `timeout`,
     `internal`) → write FAILED on `st_i`, return that outcome.
   - Any provider-level failure writes that adapter's own FAILED row —
     every attempted source ends the request in a terminal state; no
     stale `indexing` claims linger.
6. Success (empty page or data, `token==""`) or bound-stop → write
   terminal on `st_i`, serve it — the chain is never walked on success.

Terminals:

- **No adapter attempted** (all skipped): if every member was
  disabled-skipped → `writeDisabled` under chain[0]'s `srcID` (the
  configured selector's verdict). Otherwise serve chain[0]'s persisted
  row (primary cooling down; secondary also unusable). Single-adapter
  chains collapse to today's exact behavior.
- **All attempted, all failed**: serve the LAST attempted adapter's
  FAILED row — freshest truth; per-attempt failure is already persisted
  per source. The wire `provider_status` carries the last kind; the
  ordered attempts are debug-visible via each source's row.
- **`MarkWalletIndexing`**: fires per attempt on that source's own row —
  required, because the claim guards that source's concurrent indexing.
  It always resolves to a terminal write before the request ends.

## D4 — `timeout` excluded (spec-faithful, justified)

Keep the spec's exclusion. At the error boundary a `timeout` kind is
ambiguous between "upstream slow" (failover-worthy) and "our request
deadline expired" (failover wasteful — secondary calls would run inside
an already-dead context and burn quota for nothing). Failing over on an
indistinguishable deadline-expiry would multiply cost per user-facing
timeout. Adapters that can *prove* upstream slowness (HTTP 504,
gateway timeouts returned by the provider, provider-side degraded
payloads) SHOULD map those to `upstream_5xx`/`degraded` — which ARE
failover kinds — rather than the generic `timeout`.

## D5 — Re-key ripple enumeration

- **`source_id` NOT NULL write paths**: every state write carries a
  resolved `srcID` (chain[0] for the all-disabled terminal). If
  `sourceID()` fails (boot check already guarantees the row exists),
  the attempt records an internal failure and state writes for that
  source are skipped — never a NULL insert.
- **All-disabled row**: chain[0]'s `source_id` (designated configured
  source) — concrete, satisfies NOT NULL, matches "first configured"
  option.
- **`GetWalletIndexState` fan-out**: gains a `source_id` param;
  `:one` per `(net, wallet, source)`. The served response row is the
  winning/serving adapter's row — per-attempt `loadState` selects it.
- **`ON CONFLICT` targets**: `MarkWalletIndexing`,
  `UpsertWalletIndexState` re-target to `(network_id, wallet_id,
  source_id)`. `ResetWalletIndexSource` is removed (dead query;
  per-source reset would be a new explicit admin op if ever needed —
  not in scope). Call sites: `service.go` only — no other reader or
  writer exists (verified by grep; tracing reads state only through
  `EnsureIndexed`).
- **`transfer_index` divergence**: chosen semantics = **union of
  evidenced slots, idempotent writes**. `UNIQUE(network_id, tx_hash,
  transfer_index)` already collapses the same slot across sources —
  source B's `.1` upserts source A's `.1`, never duplicates it.
  Divergent slots (A claimed n=2 for content B claims once) persist as
  bounded residue: each slot was genuinely reported by some source, so
  the served union is evidence-faithful. Responses can never show the
  same slot twice. Accepted residual: stale extra `.k` slots can
  over-count vs. the newest source's stricter view — documented; adding
  per-row `source_id` + source-weighted reads is a future additive
  option, not required for correctness since slots are content-hashed.
  No cleanup job in this phase.

## Wire/safety guarantees kept

- Unset `TX_PROVIDER_<NET>` → chain of length 1 at the historical
  default → byte-identical behavior.
- Primary healthy → secondary sees zero upstream traffic AND zero
  state reads beyond its own gate order (secondary rows aren't even
  loaded until a failover-worthy failure occurs).
- Empty successful page / bound partial = terminal success → no walk.
- Unknown selector entries still fail boot.
- `UpstreamCalls` counts real outbound requests across all attempts.
