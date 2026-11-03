# Plan — Public Wallet Labels (Spec Phase 3)

Status: APPROVED_WITH_AMENDMENTS → all F1–F17 applied to phase files
(Critic round 1, `reports/critique.md`, session via `devin -p` dangerous
read-only mode; tree-hash verified unchanged).
Scope: spec §19 Phase 3 — ingest `prettydeath/wallet-attribution`, normalize
categories, deduplicate, provenance, label lookup API (§9.8).
Out of scope: live enrichment (Phase 4), scoring (Phase 5), tracing (Phase 6).

## Source (verified 2026-11-03)

- Repo: `github.com/prettydeath/wallet-attribution`, branch `main`, **MIT license**
- Per-network files under `data/`: `{network}.csv` / `{network}.json` (+`.gz` variants, `_all` aggregates)
- We fetch **ethereum.csv + bsc.csv only** (our supported chains)
- Row format (verified live): `address,network,entity,label,category,source,source_url,confidence,last_updated`
- Upstream PK: `(address, network)` — records already merged across upstream datasets; `source` keeps all contributors joined by `+`
- Upstream categories: sanctioned, terrorism, ransomware, hack, scam, darknet, extremism, frozen, exchange, mixer, gambling, mining, service, bridge, mev, defi, entity (+ others possible — normalize dynamically)
- Confidence: `high` (curated) | `medium` (scraped)

## Phases

1. `phase-01-label-schema.md` — migration 000005: `risk_categories` (+spec seeds), `wallet_labels`, `stg_wallet_labels`, `wallet_attribution` data_source; entities dedup strategy
2. `phase-02-attribution-source.md` — provider adapter: bounded multi-file fetcher (raw.githubusercontent.com allowlist), streaming CSV parser, category normalization, mapper
3. `phase-03-label-ingestion.md` — reuse snapshot pipeline: one run covers both network files; stage→validate→publish atomic; wallet get-or-create; entity get-or-create; source-scoped soft-revoke
4. `phase-04-label-query-api.md` — `GET /api/v1/wallets/{network}/{address}/labels` per §9.8: filters category/source/verification_status/active_only/cursor/limit; freshness metadata; cache
5. `phase-05-hardening.md` — OpenAPI, `wallet_labels_active_total` metric, failure-mode tests, failure tests per spec §18

## Key design decisions (post-Critic)

- **Dispatch**: `Pipeline` interface + per-source registry on ingestion
  Service — OFAC path preserved verbatim, labels adds `labelsPipeline`
  (F1); `provider.Snapshot` becomes multi-file `Files[]` (F2)
- **Fingerprint**: `sha256(wallet_attribution|<network_code>|<norm_addr>|<CATEGORY>|<label>)`
  — code strings only, never DB ids (F4)
- **Soft-revoke is network-scoped**: staged-network set bounds revocation;
  single-CSV file run can never revoke the other chain (F3)
- `verification_status`: always `reported`; `confidence` passthrough
- Category normalization: upstream → UPPER code; unseen → dynamic row
  `OTHER` type + warn metric; `category_type` CHECK constraint (F12)
- Entities: `LOWER(TRIM(name))` dedup, `entity_type` first-write-wins,
  empty→NULL, raw kept in evidence (F6)
- SANCTIONED labels stay labels — never inflate into sanctions matches
- Missing labels ≠ safe; `data_status` AVAILABLE|STALE|UNAVAILABLE from
  `data_source_snapshots` (F9); cache versioned + singleflight (F15)
- Staging: no FK (F8), parsed-vs-staged reconciliation hard-fail (F7),
  purge keep-set includes RUNNING runs (F13)
- Wallet endpoint gains labels summary (first 5 + count); its
  `data_status` documented as sanctions-scoped (F10)
- `source_field` filter added so §9.8 source dimension is real (F11)

## Acceptance criteria

- `go test -count=1 ./...` + `-race` green on `check_wallet_test`
- Real ethereum.csv + bsc.csv import end-to-end on dev DB
- Multiple labels coexist per wallet (across sources/categories)
- Re-import same file → zero new rows (idempotent, checksum no-op)
- Missing/revoked labels behave per spec §1564
- Labels API returns freshness; GAMBLING/EXCHANGE never imply risk verdict
