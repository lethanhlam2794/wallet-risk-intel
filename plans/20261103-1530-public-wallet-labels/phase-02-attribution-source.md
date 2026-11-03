# Phase 02 — Wallet-Attribution Provider Adapter

## Context

`internal/provider/walletattribution/` mirrors `internal/provider/ofac/`
structure: downloader / parser / mapper / importer. Reuses the hardened
downloader pattern — HTTPS-only, host allowlist, redirect revalidation,
bounded size/retry, checksum, temp file, FinalURL.

## Files

- `internal/provider/walletattribution/downloader.go`
- `internal/provider/walletattribution/parser.go`
- `internal/provider/walletattribution/mapper.go`
- `internal/provider/walletattribution/importer.go`
- tests + `testdata/` fixtures

## Downloader (F2 — multi-file contract)

- Allowlist: `raw.githubusercontent.com` only
- Fetch `data/ethereum.csv` AND `data/bsc.csv` (two bounded GETs, each ≤ ~32MB cap)
- Returns `provider.Snapshot{Files: []File{eth, bsc}, Checksum:
  sha256(ethSumHex||bscSumHex) sorted by filename, SizeBytes: summed}`;
  version = `wattr-<sum12>-<epoch>` (per phase-03 F2 contract)
- `-file` mode: comma-separated local paths; single CSV allowed →
  network-scoped run
- Same security posture as OFAC downloader (scheme/host per hop, capped
  Retry-After); duplicate the small allowlist/redirect helpers — do NOT
  import the OFAC package (adapter isolation)

## Parser (F14 hardening)

- `encoding/csv` streaming reader — never load whole file (verified live:
  2,819 rows contain commas/quotes inside `label`/`entity` fields —
  encoding/csv handles them correctly)
- Header contract: exactly
  `address,network,entity,label,category,source,source_url,confidence,last_updated`
  — mismatch = hard error (schema drift is a failure mode, spec §18)
- Per-row validation, reject-counted (never fail snapshot on one bad row):
  wrong `network` value for the file's chain; invalid UTF-8 in any field
  (would poison TEXT at publish); ragged row; `category` >60 chars
- `last_updated` unparseable → row KEPT with `observed_at=NULL` +
  `evidence.date_unparsed:true` (never invent dates; note the field is a
  uniform rebuild stamp, not per-record observation — verified `2026-10-01`)
- Row count bound (e.g. ≤ 2M/file) as parser resource limit
- Live-verified data facts to encode as fixture expectations: 105,267 eth +
  3,525 bsc rows; 4,537 empty `entity`; literal `'Null'` entity strings;
  1,542+144 mixed-case addresses; `network` ∈ {ethereum,bsc}

## Mapper (provider quirks stay here)

Per row → `ParsedLabel`:
- `NormalizedAddress`: EVM lowercase via `wallet.NormalizeEVM`; invalid →
  skip-count into run stats (upstream drops bad ones too, but log count)
- `UpstreamCategory`: raw string preserved verbatim
- `CategoryCode`: `strings.ToUpper(category)` + explicit map sanity list —
  unseen category still lands (OTHER type) rather than dropped
- `Label`, `EntityName`, `SourceField` (keep `a+b+c` verbatim — provenance
  of contributing datasets), `SourceURL`, `Confidence` passthrough
  (`high|medium` → same; other values → `unknown`)
- `VerificationStatus`: **always `reported`**
- `ObservedAt`: parse `last_updated` (date → timestamptz)
- `Fingerprint` (F4 — code strings only, never DB ids): sha256 hex of
  `wallet_attribution|<network_code>|<normalized_address>|<CATEGORY>|<label verbatim>`
  — stable across environments/restores. Note: upstream label-case flip
  (`Binance`→`binance`) produces new row + revoke — accepted churn,
  documented. Unit test asserts identical output independent of DB state.
- `Evidence` jsonb: `{source_field, upstream_record: {label, entity},
   fetch_checksum_part}`

## Importer

- `Import(ctx, snapshot) → []StagedLabel` + counts; staging insert via sqlc
  `stg_wallet_labels` (run_id + fingerprint PK → idempotent retries)

## Tests

- Golden fixture: small real-shaped CSV (10 rows incl. multi-source `+`,
  high/medium, all major categories, one bad address, one unseen category)
- Failure fixtures: wrong header, truncated file, wrong-network row,
  >cap row count, bad encoding
- Downloader: redirect-to-nonallowlisted host rejected; http:// rejected;
  capped Retry-After (same test patterns as OFAC)
