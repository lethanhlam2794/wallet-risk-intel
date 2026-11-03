# Remediation — Review Round 2

Date: 2026-11-03 (post `review-round-2.md` CHANGES_REQUESTED)

## Round-2 findings and fixes

### MEDIUM (required) — raw `lerr.Response` into JSONB rolled back audit rows

`internal/enrichment/service.go` persist(): the `lr.Raw → lerr.Response →
{"attempt":true}` precedence fed arbitrary upstream error bytes into
`payload JSONB NOT NULL`. A non-JSON body (HTML 502 page) or a mid-token
truncated JSON made Postgres reject the INSERT (22P02), dropping the audit
row exactly on degraded paths.

Fix: `json.Valid` gate — valid JSON stored verbatim; anything else rides in
`{"upstream_error_body": "<bytes>"}` envelope. The audit row now always
persists.

### Advisory — residual `requested_chain` fallback on map-error paths

Do-path errors were stamped, but adapter-side `mapResponse` errors
(DEGRADED/MALFORMED — the literal token WAS sent) still fell back to
`net.Code`. Now stamped on the error branch in all three adapters:
goplus `net.ChainReference`, publicaml `chain`, chainabuse `net.Code`
(`provider.StampRequestedChain` used on the `error`-typed `lerr`).

### Advisory — zero coverage for remediation behaviors

Added to `internal/enrichment/service_test.go`:
- `TestEnrich_PersistsNonJSONErrorBody` — HTML body → envelope payload,
  `persisted:true`, `requested_chain:"1"` from stamped error (the exact
  round-2 defect scenario).
- `TestEnrich_PersistsJSONErrorBodyVerbatim` — valid-JSON `Response`
  stored verbatim.
- `TestEnrich_DuplicateProviderFilterDedupes` — one block, one call.

### Advisory — `strings.Replace` env-override edge

`tests/provider_observations_test.go` — `require.NotEqual(TestDSN, dsn)`
guard: if `TEST_DATABASE_URL` names a different DB (replace is a no-op),
the schema-mutating test refuses to run on the shared DSN.

## Post-remediation gates

- `gofmt -l .` → empty; `go vet`/`go build` → clean
- `go test -count=1 ./...` → all pass (tests/ 32.1 s)
- `go test -count=1 -race ./...` → all pass (tests/ 55.3 s)
- Tree still dirty; no commits.
