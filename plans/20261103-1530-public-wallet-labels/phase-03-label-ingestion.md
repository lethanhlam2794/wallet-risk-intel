# Phase 03 — Label Ingestion (dispatch + staging → validate → publish)

## F1/F2 — Multi-source dispatch design (BLOCKING amendments, decided)

`internal/ingestion/service.go` is OFAC-hardcoded at six seams. The
abstraction, named here so Cook doesn't improvise:

```go
// internal/ingestion/pipeline.go
type Meta any // per-pipeline validation payload (ofac.DocMeta for OFAC, labelMeta for labels)

type Pipeline interface {
    // Import parses artifact files into stg_* rows scoped to runID.
    Import(ctx context.Context, qc *db.Queries, artifact provider.Snapshot, runID pgtype.UUID) (provider.ImportResult, Meta, error)
    // Validate checks staged data before publish; prev = previous published version row (may be nil).
    Validate(ctx context.Context, qc *db.Queries, runID pgtype.UUID, meta Meta) error
    // Publish applies staged rows atomically inside tx; returns (version, stats).
    Publish(ctx context.Context, qtx *db.Queries, runID pgtype.UUID, sourceID int64) (version string, err error)
    // LatestVersion returns the last published version for checksum no-op ("" if none).
    LatestVersion(ctx context.Context, qc *db.Queries, sourceID int64) (string, error)
    // StagingQueries returns the purge/delete query set used by purgeStagingExcept/deleteStaging.
    StagingNames() []string
}
```

- `Service` gains `pipelines map[string]Pipeline` + `fetchers map[string]provider.SnapshotSource`, keyed by `data_sources.code` (`ofac_sdn`, `wallet_attribution`).
- `ofacPipeline` wraps today's `ofac.NewImporter`/`validateStaged`/`publish` verbatim — OFAC behavior unchanged; its `StagingNames` returns the 5 existing tables.
- `labelsPipeline` implements the semantics below.
- `execute()` resolves `p := s.pipelines[req.SourceCode]`; unknown source → run FAILED `no pipeline registered for source`.
- `Service.LatestVersion` per pipeline: OFAC → `sanctions_snapshots` (unchanged), labels → `data_source_snapshots` (new generic table, `UNIQUE(source_id, version)`; OFAC table NOT migrated — surgical scope).
- `POST /admin/ingestion-runs` validates `source` against the registered set; empty body keeps `ofac_sdn` default (documented).
- Files touched: `internal/ingestion/{service,pipeline,publish_labels,validate_labels}.go`, `internal/provider/provider.go` (Snapshot shape + ImportResult.Labels), `cmd/api/main.go`, `cmd/worker/main.go`, `internal/ingestion/handler.go`, `queries/` (new label queries), `internal/config/config.go` (labels stale-after).

## F2 — Multi-file artifact contract

```go
// provider/provider.go
type File struct { Path, Name, Checksum, FinalURL string; SizeBytes int64 }
type Snapshot struct {
    Files    []File  // ≥1; OFAC uses exactly one
    Checksum string  // combined sha256 of per-file checksums in canonical order
    SizeBytes int64  // summed
}
```

- OFAC downloader returns `Files: [single]`; its combined checksum equals the file's own sha256 (unchanged semantics).
- Labels fetcher returns `Files: [eth.csv, bsc.csv]`; combined `sha256(ethSumHex||bscSumHex)` sorted by filename — deterministic regardless of fetch order.
- `execute()` cleanup: `for _, f := range snap.Files { defer os.Remove(f.Path) }`.
- `-file` mode: accepts comma-separated paths (`-file a.csv,b.csv`); single-CSV file mode is legal and produces a network-scoped run (F3) with checksum composed over provided files only.

## F5 — ImportResult

Add `Labels int64` (and keep OFAC fields); `finish()` sums into
`records_fetched/inserted`; `IncIngestionRecords` uses `.Labels` for labels
runs. Per-file stats go to run logs, not `records_*` scalars.

## Publish semantics (one tx, labelsPipeline.Publish)

1. Set-based ensure-wallets: `INSERT INTO wallets(network_id, address,
   normalized_address) SELECT DISTINCT ... FROM stg_wallet_labels ON
   CONFLICT DO NOTHING` — mirrors `PublishEnsureWallets`; `wallets.address`
   stores the CSV spelling verbatim (mixed-case preserved, spec §7.3).
2. `risk_categories` get-or-create by code; unseen upstream category →
   dynamic row `code=UPPER(raw)` `category_type='OTHER'` + warn-log +
   metric counter (`labels_dynamic_categories_total`).
3. `entities` get-or-create by `uq_entities_name_ci` on
   `TRIM`-normalized name (collapse inner whitespace before dedup);
   `entity_type` first-write-wins via `COALESCE(entities.entity_type,
   EXCLUDED.entity_type)` — label import never downgrades an existing
   type. `entity_name` empty/whitespace → `entity_id=NULL` (4,537 real
   rows); raw string still recorded in `evidence.upstream_record.entity`
   (junk vocabulary like literal `'Null'` stays evidence-level).
4. `wallet_labels` upsert `ON CONFLICT (fingerprint)` SET
   `entity_id, confidence, source_url, evidence, observed_at,
   last_seen_at=NOW(), revoked_at=NULL` — **never** `first_seen_at`.
5. Soft-revoke — **network-scoped** (F3):
   `UPDATE wallet_labels SET revoked_at=NOW() WHERE source_id=$src AND
   revoked_at IS NULL AND wallet_id IN (SELECT id FROM wallets WHERE
   network_id IN (staged networks)) AND fingerprint NOT IN (staged
   fingerprints)` — a staged set covering only eth can never revoke bsc
   labels; single-CSV file runs revoke only their network's scope.
6. Insert `data_source_snapshots(source_id, version, published_at,
   row_count)` row inside the same tx.

## Validation (labelsPipeline.Validate)

- Each provided file produced >0 staged rows (header-only file = FAILED).
- Per-network sanity band: staged count per network ≥20% of previous
  published count for that network (skip check on first publish).
- parsed-vs-staged reconciliation: parsed row count must equal staged
  count + rejected count; a fingerprint collision (silent `ON CONFLICT`
  drop) makes them diverge → **hard-fail** (schema-drift posture; F7).
- `last_updated` max-regression → warn-only (uniform rebuild stamp, not
  per-record observation — verified live: all rows `2026-10-01`).

## Run behavior

- Checksum-identical vs `LatestVersion` → COMPLETED no-op.
- Failed/partial → FAILED run, nothing published/revoked (spec §1564).
- Staging lifecycle per existing convention: `stg_wallet_labels` has NO
  FK/cascade (F8 — forensic retention); added to `purgeStagingExcept` +
  `deleteStaging` via `StagingNames()`.
- Staging purge keep-set (F13): `status='RUNNING' OR id IN (recent N)`
  — protects another source's in-flight run from cross-source purge.
- `records_rejected` counts parse rejects (bad address, bad UTF-8, ragged
  row, code>60); `date_unparsed` rows are kept (`observed_at=NULL`,
  `evidence.date_unparsed:true`) — never invent dates.

## Tests (integration, _test DB)

- Two-file publish → wallets/entities/categories/labels; row counts match
- Same re-publish → checksum no-op
- Reduced fixture → scoped soft-revoke; original re-publish → revive keeps
  `first_seen_at`
- **F3 regression**: publish eth+bsc, then eth-only file run → bsc labels
  untouched; eth-only revokes only within eth
- Header-only second file → run FAILED, zero revocations
- Conflicting labels (same wallet, 2 categories) coexist
- Concurrent same-source run → rejected
- `'Binance '` vs `'Binance'` → one entity row; scam `sushiswap` and defi
  `sushiswap` share entity row, keep per-row `upstream_record` truth
- Empty entity string → `entity_id IS NULL`, evidence keeps `entity:""`
- fingerprint stability unit test: same inputs → same hex, no DB ids
