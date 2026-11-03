# Devin Review — Round 1

Verdict: **APPROVED**

Scope audited: `968cee3..HEAD` (`c71db06`, 55 files) — Phase 3 public wallet
labels. Compliance inputs read: `AGENTS.md`, `learning.md` (v9, G-001..G-006
invariants applied), `engineering-standard.md`. All findings verified against
real code, running tests, and both live databases — not plan text.

## Per-amendment verification (F1–F17)

| # | Fixed? | Evidence |
|---|--------|----------|
| F1 — multi-source dispatch | YES | `internal/ingestion/pipeline.go:34-42` `Pipeline` iface; `service.go:109-112` `pipelines`/`fetchers` registries keyed by `data_sources.code`; `ofacPipeline` wraps old importer verbatim (`pipeline_ofac.go`); unknown source → `execute()` fails run `"no pipeline registered for source %q"` (`service.go:223-228`) + handler rejects `INVALID_SOURCE` via `HasPipeline` (`handler.go:48-52`). |
| F2 — multi-file artifact | YES | `provider/provider.go:13-29` `File`/`Snapshot{Files,Checksum,SizeBytes}`; `CombinedChecksum` (provider.go:35-55) — 1 file → own sum (OFAC unchanged), N files → sha256 of hex-sums sorted by `Name` (fetch-order independent; test `downloader_test.go:73-103` proves order-independence). `execute()` removes all `snap.Files` temps only when downloaded (`service.go:267-273`). `-file` mode accepts comma-separated paths (`checksumFiles` service.go:423-453), single CSV legal → network-scoped. |
| F3 — network-scoped soft-revoke | YES | `queries/labels_publish.sql:93-103` revoke bounded by `wallet_id ∈ wallets on staged networks`; per-network sanity floor 20% (`pipeline_labels.go:95-114` + `labelNetworkFloor`); each file must stage >0 rows or run FAILED (`pipeline_labels.go:71-74`). Test `TestLabels_NetworkScopedRevoke` (labels_ingestion_test.go:74-95): eth-only republish revoked 1 eth label, 2 bsc untouched. |
| F4 — fingerprint code-strings | YES | `mapper.go:20-24` — `sha256("wallet_attribution\|<net_code>\|<norm_addr>\|<CATEGORY>\|<label verbatim>")`, zero DB ids. Unit test `TestFingerprint_PinnedFormula` asserts exact preimage + stability (mapper_test.go:23-46). **Independently reproduced**: `printf 'wallet_attribution\|ethereum\|0x46ce2da0…34cb9\|EXCHANGE\|FTX 10' \| sha256sum` = `ccc4c3ef…d03f4` = value stored in `check_wallet.wallet_labels`. Upsert SET list has `entity_id, confidence, source_url, evidence, observed_at, last_seen_at, revoked_at`, never `first_seen_at` (labels_publish.sql:73-80); revive keeps `first_seen_at` (test `TestLabels_ReviveKeepsFirstSeen`). |
| F5 — ImportResult counters | YES | `provider.ImportResult.Labels` (provider.go:73); `finish()` sums into `records_fetched/inserted` (service.go:368); `IncIngestionRecords` includes `.Labels` (service.go:354-357). Live run row: `records_fetched=108792`. |
| F6 — entities hygiene | YES | `uq_entities_name_ci` = `LOWER(TRIM(regexp_replace(name,'\s+',' ','g')))` (000005 up:100-101; verified `\d` on both DBs). `PublishEnsureLabelEntities` GROUP BY canonical, `MIN(TRIM(...))` spelling, `ON CONFLICT … DO UPDATE SET entity_type = COALESCE(entities.entity_type, EXCLUDED.entity_type)` — first-write-wins. Empty/whitespace → `entity_id NULL` (join predicate `entity_name IS NOT NULL AND TRIM <> ''`); live DB: exactly 4,537 NULL entity links (matches Critic's live count); `'Null'` literal kept as entity row + `evidence.upstream_record.entity`. Test `TestLabels_EntityDedupAndDynamicCategory` proves 'Binance '≡'Binance'. |
| F7 — staging collision semantics | YES | `stg_wallet_labels PK(run_id,fingerprint)` + `ON CONFLICT DO NOTHING` → `execrows` exposes collisions; `Validate` hard-fails `Attempted-Staged != 0` (`pipeline_labels.go:75-78`) and `Parsed != Attempted+Rejected` (79-82) — explicit hard-fail posture as amended. |
| F8 — staging convention | YES | `stg_wallet_labels` has NO FK/cascade (000005 up:66-80); comment cites convention; in both `stagingPurge`/`stagingDelete` maps (pipeline.go:63-65, 79-81). |
| F9 — labels `data_status` | YES | `label/service.go` `freshness()` → `AVAILABLE|STALE|UNAVAILABLE` from `data_source_snapshots` via `LatestLabelSnapshotPublished` (labels_read.sql:21-24 — own query, NOT the `ofac_sdn`-hardcoded sanctions one); `LABELS_STALE_AFTER` config default 168h (config.go:50); freshness recomputed per response, `labelSet` cache payload carries no freshness fields (service.go:86-89). Tests: UNAVAILABLE pre-publish, STALE after forced 200h age. `INVALID_CURSOR` added (`middleware/errors.go:20`). |
| F10 — wallet endpoint labels | YES | `server.go:192-206` populates `labels` (≤5 via `ListWalletLabelsSummary` LIMIT 5) + `labels_count` (`CountActiveWalletLabels`); `data_status` stays sanctions-scoped — documented in OpenAPI description and code comment (server.go:192). Test `TestLabelsAPI_WalletSummaryShowsLabels`. |
| F11 — source vs source_field | YES | `source=` filters `data_sources.code` (service.go:262-263); `source_field=` substring-matches `evidence.source_field` case-insensitively (265-267); response exposes both `source{code,name}` and `source_field`. Tests cover both incl. negative filters. |
| F12 — category fail-closed | YES | `CHECK (category_type IN (...))` (000005 up:10); unseen upstream category → dynamic row `code=UPPER(raw)`, `category_type='OTHER'`, warn-log + `labels_dynamic_categories_total` metric (pipeline_labels.go:142-151); `code` capped at 60 via parser reject (`parser.go:18,87-89`). Test proves `brandnewcat` → OTHER row. |
| F13 — purge keep-set | YES | `begin()` keep = `ListRecentRunIDs(5)` ∪ `ListRunningRunIDs()` (service.go:181-187); purge iterates every registered pipeline's `StagingNames()` (service.go:395-408) — global housekeeping protects a labels run's staging from an OFAC run and vice versa. Residual race is theoretical-only: a run inserts its row before it can stage, and staging requires a download, so a run inserted after our list cannot have rows when our delete executes. |
| F14 — CSV hardening | YES | `parser.go`: exact header contract → fatal drift; ragged arity, per-field UTF-8, `category>60` → reject-counted; row cap 2M → fatal; `encoding/csv` streaming handles quoted commas (golden fixture asserts `'Binance, Inc.'`). `last_updated` unparseable → `ObservedAt=nil`, `evidence.date_unparsed:true` (mapper.go:60-68) — never invented; live DB `observed_at NULL count = 0` on the uniform stamp. Wrong-network row reject-counted per file (importer.go:69-72). |
| F15 — cache | YES | `cache.WalletLabels(net,addr,version)` signature changed + call sites updated (keys.go:14-18); singleflight `group.Do(cacheKey)` (service.go:179); negative (empty) results cached under same versioned key; freshness never cached. Test asserts key contains `:wattr-`. |
| F16 — set-based ensure-wallets | YES | `PublishEnsureLabelWallets` `INSERT … SELECT DISTINCT ON` (labels_publish.sql:4-19) — single set-based statement like OFAC precedent; `wallets.address` keeps CSV spelling (live DB has `0x68FCf6034573EE9a…` verbatim, normalized lowercase). |
| F17 — assorted | YES, with one nit | Empty-body default `ofac_sdn` retained + `HasPipeline` 400 (handler.go:45-52); `verification_status` enum validated (handler.go:49-53); `-file` checksum composes over provided files (service.go:419-422 comment); `snapshot` source_type + taxonomy rationale in migration comment (000005:103-105); `wallet_labels_active_total` gauge present (metrics.go:77-89 — implemented scrape-time rather than post-publish-refresh, strictly better). Nit: `labels_count` semantics — see N-2 below. |

## New issues introduced

**N-1 — LOW — `execute()` still calls `finish()` while the publish tx is open on
the tx-tuning error path** (`internal/ingestion/service.go:324-332`). The
finish-ordering fix covered `p.Publish` and `tx.Commit` errors (explicit
rollback before finish), but a `SET LOCAL` failure returns through `s.finish`
first — the `FinishIngestionRun` UPDATE then executes *inside* the open tx and
is discarded by the deferred `tx.Rollback` (or fails 25P02 if the tx aborted).
The run stays RUNNING until the heartbeat sweep reaps it (~1h), and the real
error never lands on `ingestion_runs`. Old code was safe here because
`publish()` deferred its rollback internally before returning. Reachability is
narrow (SET LOCAL on a healthy conn doesn't fail — realistically dead-conn or
ctx-cancel in a microseconds window; on dead conn finish fails anyway), so the
practical exposure is a delayed-visibility zombie, not wrong data. Why it
matters: same defect class the commit's finish-ordering fix was meant to
eliminate; the correct fix is the same two-line rollback-before-finish reorder.

**N-2 — LOW — `labels_count` is cursor-scoped, not "total matching" as
documented** (`internal/label/service.go:157,278-286`). `countMatches` counts
`matches(f) AND id > cursorID`, so page 1 reports total matches but page 2+
reports *remaining* matches for an identical filter set. OpenAPI documents
"the total number of rows matching the active filters, not the page size"
(`api/openapi.yaml` LabelPage) and the plan pinned "total matching the active
filter set". Either drop the `cursorID` term or document remaining-count
semantics. No data-integrity impact — contract ambiguity only. (Not in spec
§9.8 — field is implementation-added, so the documented text is the contract.)

**N-3 — LOW — wallet endpoint silently swallows label-summary errors**
(`internal/httpserver/server.go:201-206`). `if items, count, err :=
labels.Summary(...); err == nil` — a DB failure degrades to `labels:[]`,
`labels_count:0` indistinguishable from "no labels", unlogged. Safety impact
nil (empty labels is never a verdict per the endpoint's own contract), but it
masks a real failure from operators; at minimum log the error.

**N-4 — LOW — file network identity is content-derived, not filename-asserted**
(`internal/provider/walletattribution/importer.go:59-72`). `fileNetwork` is set
by the first emitted row's `network` value; the artifact's filename is never
checked against it. Phase-02's "per-file network assertion" reads as a
filename↔network contract. End-to-end correctness holds — fingerprint,
staging, and revoke scope all use the same row-level network code — but an
upstream file named `ethereum.csv` containing only `bsc` rows would silently
scope as a bsc file. Rows carrying non-lowercase or unregistered network codes
are rejected/fail loudly, so contamination is not possible; only provenance
narrative is looser. Low because upstream layout is verified stable.

**N-5 — LOW — `last_seen_at < NOW()` revoke is clock-dependent**
(`queries/labels_publish.sql:96`). Correct under `transaction_timestamp()`
(verified: upsert stamps the same tx value the revoke compares against —
`T < T` false for refreshed rows, strictly older for absent rows). A DB clock
regression between publishes could leave future-dated `last_seen_at` rows
evading one revocation cycle (self-heals next publish once the clock is sane).
The traded-away fingerprint anti-join is clock-immune; this is a documented
scale trade-off (real evidence: first live run timed out on `NOT IN` at
108k rows — `ingestion_runs.error_message` in dev DB). Informational — note
for operators.

**N-6 — TRIVIAL nits (no severity action required)**

- `label.Service.DataStatus` (service.go:250-256) is dead — nothing calls it;
  wallet freshness stays with `sanctions.FreshnessProvider` by design.
- `downloader_test.go:90-92,129-131`: `defer func(p string){_ = p}(f.Path)` /
  `t.Cleanup(func(){_ = f})` are no-ops — temp files leak in tests.
- `labels_api_test.go:288`: `_ = strconv.Itoa(0)` — dead import anchor.
- Generated `StgLabelNewCategories` returns `[]interface{}` (sqlc couldn't
  type `UPPER()`); works via pgx text scan but a `::text` cast in the query
  would yield `[]string`.
- `records_updated` semantics differ across pipelines: OFAC counts ensure-*
  steps into `upserted`, labels pipeline discards them — `records_updated`
  for a labels run = label upserts only.
- `-file` pathnames containing commas now split (was single-path before).
- `INVALID_SOURCE` string literal in `handler.go` instead of a
  `middleware.Code*` constant — trivially inconsistent with convention.
- `docs/pipeline-log.md` modified but uncommitted — orchestrator log outside
  the reviewed commit; housekeeping only.
- Failed-run staging residue: `stg_wallet_labels` holds 108,792 rows owned by
  the FAILED first run (`run_id …80bb`) — deliberate forensic retention per
  convention, purged only when the run falls out of the keep set.

## Regression check — OFAC paths

Byte-equivalent in behavior, confirmed by side-by-side read of
`git show 968cee3:internal/ingestion/publish.go` and the service diff:

- **Publish step order/queries identical**: entities → programs →
  entity_programs → entity_program_revocations → wallets_resolve →
  sanctions_wallets → wallet_revocations → entity_revocations → references →
  reference_revocations → `InsertSnapshot` with `ofac-%s-%d` version
  (pipeline_ofac.go:83-145 vs old publish.go).
- **`ofacPipeline.Import`** asserts exactly 1 file and calls the same
  `ofac.NewImporter(nets).Import(ctx, qc, artifact.Files[0].Path, runID)`.
- **`Validate`** calls the same `validateStaged`; one extra `GetLatestSnapshot`
  read (old code reused the checksum-check row) — behavior-neutral under the
  per-source advisory lock.
- **Checksum no-op, finish counters, purge lists, `deleteStaging`** —
  semantics preserved; `Labels` field is 0 for OFAC so
  `records_fetched/inserted` unchanged.
- **Tx lifecycle moved up to `execute()`** with the added
  rollback-before-finish fix (publish/commit paths) — the claimed fix is real;
  residual path is N-1.
- OFAC test suite passes unchanged (module-time mechanical updates only:
  `snap.Path` → `snap.Files[0].Path`); downloader now returns
  `Files:[{Name:"sdn_advanced.xml"}]` + `Checksum` = file's own sum.
- `TestMetrics_IngestionValues` and `tests/failure_modes_test.go` still green
  under `-race`.

## Fresh verification evidence (commands actually run)

- `gofmt -l .` → **empty** · `go vet ./...` → **0 findings** · `go build ./...`
  → **ok**
- `go test -count=1 ./...` → **all PASS** (`checkwallet/tests` 17.2s incl. all
  labels_* suites)
- `go test -count=1 -race ./...` → **all PASS** (tests 68.0s)
- `psql check_wallet_test`: `\d wallet_labels` matches migration 000005 exactly
  (UNIQUE fingerprint, both CHECKs, 4 FKs, 3 indexes); `uq_entities_name_ci`
  expression = `lower(TRIM(regexp_replace(name,'\s+',' ','g')))` — exact match
  to the `ON CONFLICT` arbiter; `schema_migrations` at version 5.
- `psql check_wallet` (live import): 108,792 labels (105,267 eth / 3,525 bsc),
  2,748 entities, 4,537 NULL entity links, 0 revocations, snapshot
  `wattr-304b40821630-1793699980` row_count=108,792; second run COMPLETED with
  0 records (checksum no-op); first FAILED run preserved with real error
  `publish statement timeout on PublishRevokeAbsentWalletLabels (120s)` —
  proving error fidelity end-to-end.
- **Independent fingerprint recompute** — sha256 of the pinned preimage equals
  the stored fingerprint for `FTX 10` (byte-for-byte match).
- `git status` — clean except `M docs/pipeline-log.md` (orchestrator log) and
  the untracked review prompt; secrets scan of full diff → clean.

Status: DONE
