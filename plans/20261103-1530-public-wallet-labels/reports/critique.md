# Devin Critic — Round 1

## Executive summary

**Verdict: APPROVE_WITH_AMENDMENTS** — two amendments are blocking (F1, F2);
the rest must land in the phase files before Cook.

The destination semantics are right: snapshot-style full-file publish with
source-scoped soft-revoke, stable fingerprint, `reported` status, OTHER
fallback preserving `upstream_category`, versioned cache key, combined
checksum no-op, and an atomic single run covering both network files. The
upstream CSV was re-verified live (105,267 eth rows / 3,525 bsc rows, quoted
commas in 2,819 rows, 4,537 empty entities, 1,542+144 mixed-case addresses,
uniform `last_updated=2026-10-01`, `network` values `ethereum`/`bsc` matching
`blockchain_networks.code`).

What is wrong is that the plan's core claim — "reuse the ingestion
machinery, generalize where cheap" — is asserted but never specified. The
real `ingestion.Service` is OFAC-hardcoded at six distinct seams
(`ofac.NewImporter` call site, `*ofac.DocMeta` validator signature,
`sanctions_snapshots`-bound latest-snapshot lookup, single-path
`provider.Snapshot`, OFAC-shaped `ImportResult` counters, hardcoded staging
purge lists). Phase-03 describes publish semantics correctly but never names
the dispatch mechanism, so an implementer would either fork the service
(violating the plan's own constraint) or improvise an `if source == ...`
chain. That is a missing design section, not a wrong design — hence
amendments, not rework.

## Findings

### F1 — [BLOCKING][HIGH] Multi-source dispatch is the central design question and is unanswered

- **Where**: `phase-03-label-ingestion.md` Context; `plan.md` phase 3 bullet.
- **Evidence**: `internal/ingestion/service.go:251` — `imp := ofac.NewImporter(s.networks)` regardless of `req.SourceCode`; `NewService(pool, resolver, downloader, log)` takes **one** `provider.SnapshotSource` (cmd/api/main.go:68-73, cmd/worker/main.go:69-78 wire an OFAC downloader only); `validateStaged(ctx, qc, runID, meta *ofac.DocMeta, prev *db.SanctionsSnapshot)` (validate.go:25-26); `publish()` is a fixed OFAC step list ending in `InsertSnapshot` → `sanctions_snapshots` with `ofac-%s-%d` version (publish.go:52-113); the checksum no-op reads `GetLatestSnapshot` → `sanctions_snapshots` (service.go:241); `purgeStagingExcept`/`deleteStaging` enumerate the five OFAC staging queries literally (service.go:318-338). Today `POST /admin/ingestion-runs {"source":"wallet_attribution"}` would insert a RUNNING row and then feed the CSV to the OFAC XML importer.
- **Amendment**: add an explicit dispatch section to phase-03 naming the abstraction and enumerating the touched seams. Minimum viable shape: a `pipeline` interface per source (`Import(ctx, qc, artifact, runID) (Result, Meta, error)`, `Validate(ctx, qc, runID, meta, prev)`, `Publish(ctx, tx, runID, sourceID) (stats, version, error)`, `StagingTables()` for purge/delete lists) plus `map[string]pipeline` and `map[string]provider.SnapshotSource` registries on `Service`; `GetLatestSnapshot` replaced by a per-source `LatestVersion(sourceID)` lookup over `data_source_snapshots` for labels while OFAC keeps `sanctions_snapshots`. List every file: `service.go`, `validate.go`, `publish.go`, `provider.go`, `cmd/api/main.go`, `cmd/worker/main.go`, `internal/ingestion/handler.go`, `internal/config/config.go`.

### F2 — [BLOCKING][HIGH] `provider.Snapshot` is single-path; the two-file snapshot contract is unspecified

- **Where**: `phase-02-attribution-source.md` Downloader; `phase-03` "-file mode accepts one or two CSVs".
- **Evidence**: `provider.Snapshot{Path, Checksum, SizeBytes, FinalURL}` is exactly one file (provider.go:9-14); `execute()` does `defer os.Remove(snap.Path)` and passes `snap.Path` to the importer (service.go:228,252); `checksumFile` hashes one path (service.go:341-353); `RunRequest.FilePath` is a single string (service.go:50).
- **Amendment**: specify the artifact shape — e.g. `Snapshot.Files []File{Path, Checksum, FinalURL, Name}` plus `Snapshot.Checksum = sha256(eth||bsc)` and `SizeBytes` summed — or give the label pipeline its own artifact type. State how `os.Remove` cleans up N temp files, how `-file` mode supplies two paths (repeated flag, comma-separated, or directory convention), and what `FinalURLs` look like in logs. OFAC must keep working unchanged (single-element Files slice) — state that explicitly.

### F3 — [HIGH] Soft-revoke scope is network-blind: a partial snapshot can silently revoke a whole chain's labels

- **Where**: `phase-03-label-ingestion.md` Publish step 5 + Validation.
- **Evidence**: revoke is `wallet_labels WHERE source_id AND fingerprint NOT IN (staged set)` with a **total** sanity band ("not <20% of prior count"). Verified live sizes: eth 105,267 vs bsc 3,525 rows. If a `-file` run stages only `ethereum.csv` (plan explicitly allows "single csv for tests"), every BSC label is revoked — silently, and `labels` API then reports them gone. Same failure if upstream ever ships a header-only bsc.csv inside a valid download: a 3.4% total shrink passes the 20% floor. Recovery exists (next good publish revives) but the wrongness is real: live data destroyed by an incomplete-scoped publish — the exact anti-pattern spec §12 lists.
- **Amendment**: scope soft-revoke to the networks actually staged (`AND wallet_id IN (wallets WHERE network_id = staged networks)` or an equivalent per-network staged-set comparison); require each fetched file to produce >0 staged rows (header-only file = FAILED, matching OFAC's "staging empty" refusal); run the sanity band **per network**, not just on the total; make single-CSV `-file` mode an explicit opt-in that marks the run network-scoped.

### F4 — [HIGH] Fingerprint inputs must be code strings, not database IDs; upsert refresh list is incomplete

- **Where**: `plan.md` Key design proposals; `phase-02` Mapper; `phase-03` Publish step 4.
- **Evidence**: plan.md writes `sha256(source_id|network_id|normalized_address|category|label)`; phase-02 writes `wallet_attribution|<network>|<norm_addr>|<CATEGORY>|<label>`. `network_id` is environment-dependent (BIGSERIAL) — a fingerprint must be stable across environments and restores, or the same upstream row yields different fingerprints per database and "stability across ingestion runs" (spec §7.12) breaks on any reseed. Separately, the publish upsert refreshes `last_seen_at/revoked_at/source_url/evidence/observed_at` but omits `entity_id` and `confidence`: upstream re-attribution or confidence upgrades (both deliberately excluded from the fingerprint) would leave stale values live forever.
- **Amendment**: pin the canonical preimage `wallet_attribution|<network_code>|<normalized_address>|<CATEGORY_CODE>|<label verbatim>` (source code + network code strings); write the upsert SET list explicitly: `entity_id`, `confidence`, `source_url`, `evidence`, `observed_at`, `last_seen_at=NOW()`, `revoked_at=NULL` — and never `first_seen_at` (revive keeps original, per the plan's own test). Note that a label-case flip (`Binance` → `binance`) creates a new row + revoke — acceptable churn, document it. Add a fingerprint-stability unit test asserting identical output from identical inputs independent of DB ids (spec §18.1 "Label fingerprint generation").

### F5 — [MEDIUM] `provider.ImportResult` and run counters are OFAC-shaped; a successful labels run would report 0 records

- **Where**: `phase-03` "records_inserted/updated/rejected counters reused".
- **Evidence**: `ImportResult{Entities, Wallets, Programs, EntityPrograms, References, Rejected}` (provider.go:17-24); `finish()` sums those fields into `records_fetched/inserted` (service.go:294-299); `IncIngestionRecords` likewise (service.go:281-285). No `Labels` field exists — every completed label run would persist `records_fetched=0, records_inserted=0`.
- **Amendment**: extend `ImportResult` with `Labels int64` (or replace with a per-source counts map) and update `finish()` + metrics accordingly. Also fix phase-05's "per-file stats in records_* counters": `records_*` columns are scalars — per-file stats need `ingestion_runs` metadata or the run's log, say which.

### F6 — [MEDIUM] `entities` dedup via `LOWER(name)`: right registry, missing hygiene rules — verified noisy upstream vocabulary

- **Where**: `phase-01-label-schema.md` "Entities dedup".
- **Evidence**: `entities` exists since 000001 with no unique constraint and **no writer today** (no `INSERT INTO entities` anywhere — index creation is safe on an empty table). Live CSV shows `entity` is attribution vocabulary, not verified org identity: `'OFAC SDN'`, `'OFAC Blocked'`, `'ofac-sanctioned'`, `'Null'` (literal string, 2 rows), `'USDT_blacklisted'`, `'Binance Dep'`, `'defi fraud'` (5,705 scam rows), `'brand-infringement'`, `'sushiswap'` under `scam`, `'OxGame, Gambling'` (commas inside entity names). Consequences: (a) name-only dedup merges impersonation tags with the real org (scam-category `sushiswap` → same entity row as the project); (b) `LOWER()` without `TRIM` splits `'Binance '` vs `'Binance'`; (c) an unchecked write path can overwrite `entity_type` on a row another source owns.
- **Amendment**: keep `LOWER(name)` get-or-create (shared registry is spec §7.5 intent, and `sanctions_entities.entity_id` benefits from linking the same real-world org) but: normalize before dedup — `TRIM` + collapse inner whitespace, map `''`→NULL (4,537 rows today), consider an upstream-junk awareness note for `'Null'`-like literals; make entity_type writes first-write-wins (`entity_type = COALESCE(entities.entity_type, EXCLUDED.entity_type)` — never downgrade an existing non-NULL, and never let label import overwrite an OFAC-populated type); document that `wallet_labels.entity_id` conveys *reported* attribution vocabulary, not verified identity — the raw entity string must also live in `evidence.upstream_record` so per-row provenance survives registry-level conflation.

### F7 — [MEDIUM] Staging PK `(run_id, fingerprint)` — define collision semantics; silent drop loses evidence

- **Where**: `phase-01` `stg_wallet_labels`; `phase-03` validation "zero fingerprint collisions staged (PK guarantees)".
- **Evidence**: OFAC staging inserts use `ON CONFLICT DO NOTHING` (queries/ofac_staging.sql:4,12) — with that pattern a same-run fingerprint collision is **silently deduplicated**, not surfaced. Real collision vectors within one file: the same address appearing both checksummed and lowercase (1,542 mixed-case addresses exist today — normalization can collapse distinct raw rows), or category `Scam`/`scam` both normalizing to `SCAM` with identical label text. Cross-file collisions can't happen (network is in the fingerprint) — that part is fine.
- **Amendment**: reconcile parsed-row count vs staged-row count per run; a mismatch means upstream sent colliding fingerprints — decide explicitly: hard-fail (preferred, schema-drift posture) or count into `records_rejected` with the colliding raw rows logged. "PK guarantees" is not a validation — it's a silent sink or an explosion, say which.

### F8 — [MEDIUM] `stg_wallet_labels.run_id REFERENCES ingestion_runs(id) ON DELETE CASCADE` deviates from the established staging convention

- **Where**: `phase-01` DDL.
- **Evidence**: all five OFAC staging tables have **no** FK and no cascade (migration 000002 — deliberate: "failed-run staging is retained for diagnosis", lifecycle managed by the purge lists). The plan adds FK+CASCADE without noting the deviation: a mass delete of run rows would wipe forensic staging, and every staged row pays an FK check.
- **Amendment**: drop the `REFERENCES ... ON DELETE CASCADE` to match convention, or write down why labels staging is deliberately different.

### F9 — [MEDIUM] `data_status` for labels is under-specified — only `AVAILABLE` appears

- **Where**: `phase-04` response + "data_status from latest published snapshot".
- **Evidence**: sanctions already defines `AVAILABLE/STALE/UNAVAILABLE` (sanctions/model.go:15-19) with `staleAfter` threshold and the invariant that freshness is **recomputed per response, never cached** (service.go:46-56 comment — learned the hard way). The plan shows only `"AVAILABLE"`. Also `queries/sanctions.sql:95-99` `LatestSnapshotPublished` is hardcoded `code='ofac_sdn'` — the labels service needs its own latest-snapshot query over `data_source_snapshots`; `metrics.LatestSnapshotQuery`/`sanctions_snapshot_age_seconds` stay OFAC-bound.
- **Amendment**: reuse the same enum: `UNAVAILABLE` when no published `data_source_snapshots` row exists for `wallet_attribution`, `STALE` past a configured threshold (add `LABELS_STALE_AFTER` or consciously reuse `SanctionsStaleAfter` — decide), cache payload must exclude freshness fields exactly like sanctions. Add INVALID_CURSOR to `middleware/errors.go` (does not exist today).

### F10 — [MEDIUM] `GET /wallets/{network}/{address}` will still return `labels: []` forever

- **Where**: not mentioned in any phase.
- **Evidence**: `httpserver/server.go:192` emits `"labels": []any{}`; `api/openapi.yaml` documents labels as "Reserved; always empty in this phase". Spec §9.2: the wallet endpoint returns stored labels. Phase 3 is where the reservation ends — but the plan only builds §9.8. Additionally `wallet.data_status` currently reports **sanctions** freshness via `FreshnessProvider`; once labels exist that field becomes ambiguous between two snapshot domains.
- **Amendment**: either populate a bounded labels summary in the wallet response (e.g. active `labels_count` + first N) or record an explicit deferral and keep the OpenAPI note. Decide what wallet-level `data_status` means with two sources of freshness (cheapest correct: keep it sanctions-scoped and say so; labels carry their own freshness under §9.8).

### F11 — [MEDIUM] `source` filter is ambiguous: `data_sources.code` vs upstream `source` column

- **Where**: `phase-04` Filters.
- **Evidence**: upstream `source` is the contributor list (`eth-labels+graphsense+etherscan-labels` — 8+ distinct combos verified) stored in `source_field`; the plan's `source=` filter targets `data_sources.code`, which has exactly one value (`wallet_attribution`) → a spec-listed filter (§9.8) that is a silent no-op.
- **Amendment**: define `source` as `data_sources.code` (forward-compatible) **and** add a `source_field` filter for upstream contributors (exact or `ILIKE` token match), or explicitly accept `source` filtering both. Same class of ambiguity in the response: `source{code,name}` + `source_field` is good — keep both.

### F12 — [LOW] Category normalization: acceptable, needs fail-closed details

- **Where**: `phase-01` seeds; `phase-02` mapper.
- **Evidence**: all 14 categories present in the live files (`bridge,defi,entity,exchange,extremism,frozen,gambling,hack,mev,mining,mixer,sanctioned,scam,service`) are covered by the seed list; OTHER-fallback preserves `code=UPPER(raw)` + `upstream_category` verbatim — no silent loss, spec §4.3-compliant. Gaps: `category_type` has no CHECK constraint (a typo'd type is silently accepted); an unseen malicious category lands as `OTHER` type and Phase-5 scoring could underweight it; `code VARCHAR(60)` vs unbounded `ToUpper(category)`.
- **Amendment**: `CHECK (category_type IN ('LEGAL','MALICIOUS_ACTIVITY','SERVICE_TYPE','ENTITY_TYPE','OTHER'))`; count + warn-log every dynamically-created category (run stats + metric) so a human promotes it to a real type; cap/reject codes >60 chars.

### F13 — [LOW] Cross-source staging purge can delete an in-flight run's rows

- **Where**: `phase-03` reuse claim (unexamined interaction).
- **Evidence**: `begin()` purges staging for all tables except the 5 most recent run ids — `ListRecentRunIDs` is **global** (`ORDER BY started_at DESC LIMIT 5`, queries/ingestion_runs.sql:43), not per-source and not RUNNING-aware. With two sources now allowed concurrently (advisory lock + `uq_ingestion_runs_running` are per-`source_id`), an OFAC run's purge deletes `stg_wallet_labels` of a labels run that fell outside the recent-5 window. Requires the new table be added to both `purgeStagingExcept` and `deleteStaging` lists — itself missing from the plan (forget it and failed-run label staging leaks forever).
- **Amendment**: keep-set := `status='RUNNING' OR id IN (recent N)`; per-source staging lists in the pipeline abstraction (F1).

### F14 — [LOW] CSV edge cases — verified safe, plus two parser hardening gaps

- **Where**: `phase-02` Parser.
- **Evidence** (live file): 2,819 rows carry commas/quotes inside `label`/`entity` (`'OxGame, Gambling'`, `instadapp_MAKERDAO-CLAIM-A"`) — `encoding/csv` is the correct choice, confirmed parses cleanly; header exactly as contracted; `network` ∈ {ethereum,bsc} matching codes; no ragged rows today. Gaps: (a) invalid UTF-8 in one field would poison `TEXT` columns at publish time → whole-run failure mid-publish; validate UTF-8 per row at parse and reject-count the row (consistent with invalid-address skip), don't fail the snapshot for one corrupted field; (b) `last_updated` parse failure policy unstated — unparseable ⇒ `observed_at=NULL` + reject-count (never invent a date); `last_updated` is uniform `2026-10-01` today, i.e. it's a rebuild stamp, not per-record observation — say that in the plan since it weakens the max-regression check to a coarse signal (warn-only is right).

### F15 — [LOW] Cache deviation accepted, with three unstated mechanics

- **Where**: `phase-04` Cache.
- **Evidence**: `cache.WalletLabels(network, addr)` helper **already exists** with the spec-literal key (cache/keys.go:15-17) — signature change + all call sites required; spec §14 "prevent cache stampedes" — sanctions uses `singleflight.Group` (service.go:69), plan never mentions it; spec §14 "separate positive and negative" — plan caches negative results, good, but say so in the helper.
- **Amendment**: document the helper signature change (`{net}:{addr}:{snapshot_version}`), add singleflight to the labels service, keep freshness out of cached payloads (F9). The deviation itself is the right call — a publish must not be masked by a pre-publish entry, and it matches the sanctions lesson; keep it.

### F16 — [LOW] `wallets` get-or-create: acceptable scale, wrong mechanics note

- **Where**: `phase-03` Publish step 1.
- **Evidence**: ~109K wallet rows (measured 105,267+3,525 — plan says "hundreds of K", same order, fine). `PublishEnsureWallets` precedent is **set-based** `INSERT ... SELECT DISTINCT` (ofac_publish.sql:52-58) — per-row `GetOrCreate` ×109K inside the publish tx is slower and long-tx; `wallets.address` must store the CSV spelling (1,542+144 mixed-case rows verified — preserves original representation per spec §7.3).
- **Amendment**: use a set-based ensure-wallets step like the OFAC publish; keep per-row only in tests.

### F17 — [LOW] Assorted smaller gaps

- `POST /admin/ingestion-runs` empty-body default hardcodes `ofac_sdn` (handler.go:46) — with two sources an empty body silently runs OFAC; validate against the registered-source set and document the default.
- `verification_status` filter — enum has `unverified|reported|verified|disputed`; fine.
- `labels_count` semantics undefined — returned-count vs total-matching; name or document it.
- Metric: `wallet_labels_active_total` covered; consider `data_source_snapshot_age_seconds{source}` — spec §17's `sanctions_snapshot_age_seconds` is OFAC-pinned today, labels get no freshness gauge otherwise (optional).
- `-file` checksum: single-CSV file-mode produces a different checksum than the combined eth+bsc formula — state how file-mode checksum composes with the two-file case (F2/F3 tie-in).
- `source_type='snapshot'` seed vs AGENTS.md table's "BulkSource" label — acceptable: the artifact is a full-file snapshot with soft-revoke lifecycle (FetchBatch/cursor semantics don't fit a git-hosted file); record the taxonomy rationale in the plan.
- `entity` empty string vs NULL across CSV — 4,537 verified empties; also 2 literal `'Null'` strings — verbatim is fine, note the junk reality in F6.

## Proposals accepted (brief)

- **Snapshot semantics for this source** (full download → stage → validate → atomic publish → source-scoped soft-revoke; checksum-identical → COMPLETED no-op): correct reading of spec §12 bulk-snapshot rules and the only semantics that makes "disappearing listings" work; right call over BulkSource cursor fetching.
- **One run covers both files, atomic across networks**: prevents a bsc-only failure revoking eth labels and vice versa — correct, modulo the per-network scoping fix in F3.
- **Fingerprint tuple shape** `source|network|addr|CATEGORY|label`: right grain — lets multiple labels coexist per wallet (spec §7.1), keeps upstream re-categorization as revoke+insert (provenance preserved), and other sources can coexist via the source segment. Amendments limited to code-string inputs and refresh-list completeness (F4).
- **`verification_status='reported'` always**: defensible and correct — the enum's accurate member for upstream-asserted claims; `unverified` would under-describe, `verified` would violate spec §4.3 ("không tự coi dữ liệu tổng hợp là đã xác minh").
- **SANCTIONED stays a label, never inflates to sanctions**: verified live rows (163 eth + 1 bsc, including noise like `entity='Gitcoin Grants'`) prove why this separation matters.
- **OTHER fallback with `upstream_category` verbatim + dynamic `risk_categories` rows**: no silent data loss; better than failing the snapshot on vocabulary drift.
- **New `data_source_snapshots` table instead of reusing `sanctions_snapshots`**: right — OFAC columns (`entity_count/wallet_count/program_count/date_of_issue`) don't generalize; make it `UNIQUE(source_id, version)` and do **not** migrate the OFAC table in this phase (surgical scope).
- **Versioned labels cache key**: deviation from spec literal, correct — documented and consistent with the sanctions anti-stale invariant.
- **Streaming `encoding/csv`, exact header contract, per-file network assertion, row cap, raw.githubusercontent.com allowlist, temp-file + checksum + bounded retry**: matches observed data and existing downloader posture; do-not-import-OFAC-package isolation is right (duplicate the small helpers rather than entangle adapters).
- **`wallet_labels` DDL**: matches spec §7.12 plus `upstream_category` column (spec §4.3 compliant) and partial active indexes (improvement over spec's non-partial index).
- **`labels: []` + `data_status` as a valid response; no risk-verdict field anywhere**: directly implements the never-safe-by-absence and GAMBLING/EXCHANGE-are-classifications invariants.
- **Test matrix**: revive-preserves-`first_seen_at`, conflicting-labels coexistence, concurrent-run rejection, empty-file FAILED, wrong-network reject — the right failure tests for this phase (spec §18).

## Proposals rejected/modified (why)

- **"Reuse the ingestion machinery, generalize where cheap" as stated** — modified into a mandatory dispatch design (F1): the reuse surface is real but currently hardcoded at six seams; without the named abstraction the constraint "do not fork the service" is unimplementable as written.
- **`provider.Snapshot` reuse as-is** — rejected: single-path contract cannot carry two files; extend to a multi-file artifact or a per-source artifact type (F2).
- **Soft-revoke `NOT IN (staged set)` scoped to source only** — modified: must additionally scope to staged networks, per-network sanity band, per-file non-empty checks (F3); as written, a legitimate single-file run deletes another chain's live labels.
- **`stg_wallet_labels` FK + `ON DELETE CASCADE`** — rejected: breaks the deliberate no-FK forensic-staging convention without justification (F8).
- **`entities` `LOWER(name)` dedup as stated** — modified, not rejected: keep the shared registry, but normalize names, protect `entity_type` from downgrade/overwrite, and document reported-vocabulary semantics with raw entity kept in per-row evidence (F6).
- **Fingerprint `(source_id|network_id|...)`** — modified to code strings: DB ids are environment-local and break fingerprint stability across restores/reseeds (F4).
- **`source` filter = `data_sources.code` only** — modified: add upstream `source_field` filtering or the §9.8 filter is a no-op (F11).
- **`data_status` `AVAILABLE`-only shape** — modified: reuse `AVAILABLE/STALE/UNAVAILABLE` with per-source snapshot resolution and serve-time freshness (F9).
- **"PK guarantees zero fingerprint collisions" as validation** — rejected as stated: define collided-row semantics and reconcile parsed vs staged counts (F7).
- **Per-row wallet get-or-create for ~109K rows** — modified to set-based insert following `PublishEnsureWallets` (F16).

Status: DONE
