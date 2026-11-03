---
phase: 3
title: "OFAC SnapshotSource — Download, Stream-Parse, Map"
status: pending
priority: P1
dependencies: [1, 2]
---

# Phase 3: OFAC SnapshotSource (Download / Parse / Map)

## Overview

Implement `internal/provider/ofac` behind the spec §13 `SnapshotSource`
contract: bounded download of `SDN_ADVANCED.XML` to a temp file with
validation and checksum, streaming `xml.Decoder` parsing into a domain model,
and mapping to staging rows (entities, programs, digital-currency wallet
identifiers, source references).

## Requirements

- Functional: `ofac.Downloader` (fixed allowlisted URL, configurable for
  tests; size cap, timeout, HTTP-status check, sha256 checksum, resume-safe
  temp file, cleanup; **bounded retry per spec §15**: max 3 attempts,
  exponential backoff + jitter, honor `Retry-After`, 4xx fail-fast vs
  5xx/network retry; download failure metrics. Circuit breaker explicitly
  deferred — single-source batch job, recorded decision); `ofac.Parser` —
  `encoding/xml.Decoder` token loop, never `Unmarshal` whole doc
  (spec §4.1); `ofac.Mapper` — XML →
  `StagedEntity{ExternalID, PrimaryName, EntityType, Programs[],
  Wallets[], References[], Details}`.
- Non-functional: SSRF allowlist (`sanctionslistservice.ofac.treas.gov`);
  parser hardening — `Decoder.Strict=true`, explicit charset, entity-expansion
  limits / reject DOCTYPE with external entities; download cap
  (e.g. 512MB) + timeout; all ctx-respecting.

## Architecture

OFAC Advanced XML — **structure verified against the live file
(2026-11-03, ranged HTTP fetches), documented in
`reports/ofac-xml-shape.md`**. Essentials: root `<Sanctions>` (ADVANCED_XML
namespace, Version=3) → `<ReferenceValueSets>` code tables →
`<DistinctParties>/<DistinctParty>/<Profile>` (Identity→Alias→DocumentedName
→DocumentedNamePart/NamePartValue; `<Feature FeatureTypeID>` →
`<FeatureVersion>/<VersionDetail>` = identifier text) → trailing
`<SanctionsEntries>/<SanctionsEntry ProfileID>` (EntryEvent@LegalBasisID +
SanctionsMeasure/Comment = program codes). Digital-currency membership is
resolved **dynamically** from FeatureTypeValues (name prefix
`Digital Currency Address - `), never hardcoded IDs. Fixtures must mirror
this verified shape (P-021, P-047).

**Document-order handling**: `<SanctionsEntries>` (program links) appear
AFTER all `<DistinctParty>` blocks — the importer buffers
`profile_id → program_codes` (small map) and flushes
`stg_sanctions_entity_programs` at end-of-document, plus extracts
`Sanctions/DateOfIssue` for snapshot-freshness guards (phase 04).

**Dedup expectations**: multiple `FeatureVersion`s per `Feature@ID` and
repeat listings of the same address+currency per entity are expected;
staging dedups on the live unique-key tuple
`(entity, normalized_address, identifier_type, currency, source_identifier_id)`
— different `Feature@ID`s stay distinct rows, same tuple collapses once.

Currency suffix parsing trims/case-normalizes the
`Digital Currency Address - ` suffix; include a UTF-8 BOM fixture.

Parser-hardening correction: Go's `encoding/xml` never fetches external
entities — the real threat is internal entity expansion, covered by
`Decoder.Strict` (rejects DTD) + size cap. Keep the DOCTYPE-reject test.

Mapping rules:

- `external_entity_id` ← stable OFAC party/profile identifier.
- Digital currency feature → `sanctions_wallets` row: `listed_address`
  (verbatim), `currency_code` (e.g. `ETH`, `BNB`, `BTC`, `USDT`...),
  `identifier_type` (feature type verbatim), `source_identifier_id` (feature
  id), `evidence` jsonb (feature/version detail ids, remarks).
- Network resolution (verified currency×format rules in
  `reports/ofac-xml-shape.md`): `ETH+0x→eip155:1`, `BSC+0x→eip155:56`,
  `BNB+0x→eip155:56`; **USDT/USDC/ARB and other ambiguous multi-chain
  codes → `network_id=NULL`, `match_scope='UNRESOLVED'`** (chain not
  evidenced by code alone); non-EVM codes on chains outside the registry
  (XBT/XMR/TRX/SOL/...) → `network_id=NULL`,
  `match_scope='CHAIN_SPECIFIC'` (the listing IS chain-specific, on a
  chain we do not index), no wallet_id — never invent a network mapping;
  never turn an ETH listing into a BSC listing.
- EVM addresses normalize via `wallet.NormalizeEVM`; unparseable addresses
  are still stored verbatim with a validation flag in evidence (never drop
  evidence silently; count in `records_rejected` only if unusable).
- Programs deduplicated per entity; references keep type/url/title verbatim.

## Related Code Files

- Create: `internal/provider/provider.go` (SnapshotSource + shared types —
  keep minimal, only what ofac needs)
- Create: `internal/provider/ofac/downloader.go`, `parser.go`, `mapper.go`,
  `importer.go` (staging writer), `model.go`
- Create: `migrations/000002_ofac_staging.up.sql` / `.down.sql` —
  `stg_sanctions_entities`, `stg_sanctions_programs`,
  `stg_sanctions_entity_programs`, `stg_sanctions_wallets`,
  `stg_sanctions_references` (each carrying `run_id uuid`)
- Create: `queries/ofac_staging.sql`; `testdata/ofac/` fixtures (small valid
  doc, truncated doc, malformed doc, wrong-root doc, entity without programs,
  multi-program entity, non-EVM currency, duplicate identifiers)

## Implementation Steps (TDD)

1. Red: `downloader_test.go` — non-200 status → error; body larger than cap
   → error; checksum recorded; temp file cleaned on failure; httptest server
   for all cases (no live OFAC in unit tests).
2. Red: `parser_test.go` — valid fixture yields N profiles with expected
   names/types/programs; truncated → error, zero staged rows published;
   malformed → error; wrong root → error; DOCTYPE-attack fixture → error.
3. Red: `mapper_test.go` — ETH feature → network eip155:1; BNB → eip155:56;
   BTC/XBT → network NULL; same address two currencies → two staged rows;
   mixed-case EVM normalized lowercase while `listed_address` preserved;
   duplicate identifier in one file → dedup to unique-key cardinality.
4. Green: implement downloader/parser/mapper/importer streaming into staging
   (batched COPY/INSERT per run_id).
5. Integration (`tests/ofac_import_test.go`, `_test` db): import fixture →
   staging row counts match; FKs enforced; re-run same fixture → identical
   staging cardinality (idempotent).
6. Manual verification: `go run ./cmd/worker -source ofac -snapshot-file
   <fixture-or-real>` prints import stats; record real-XML shape notes.

## Success Criteria

- [ ] Parser streams (no `ioutil.ReadAll`+Unmarshal of whole doc) — code
      review assertion + test on multi-MB generated fixture.
- [ ] All failure fixtures produce errors and **zero** staging writes.
- [ ] Checksum/size/HTTP-status validation proven by tests.
- [ ] Currency→network mapping unit-tested incl. NULL-network cases.
- [ ] `records_fetched/inserted/rejected` counters emitted per run.
- [ ] `go test ./internal/provider/...` green; build/vet/fmt clean.

## Risk Assessment

- Real schema drift vs fixtures → mitigation: download real file during dev;
  parser fails closed on unexpected root element; shape doc recorded.
- XXE / entity expansion → Strict decoder, DOCTYPE rejection, size caps.
- 100MB+ memory → streaming decoder + batched inserts (e.g. 500/batch);
  test asserts allocation-bounded parse.
- OFAC URL changes → URL is config-driven but allowlist-enforced.
