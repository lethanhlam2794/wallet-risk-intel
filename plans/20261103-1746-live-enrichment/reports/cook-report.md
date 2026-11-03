# Cook Report — spec §19 Phase 4: Live Enrichment

Executed phases 1→5 per the adversarially-hardened plan (all Devin Critic
amendments F1–F9 applied). Strict TDD per phase; no commits — working tree
left dirty for Stage-3 review.

## Files created

- `migrations/000006_provider_observations.{up,down}.sql` — append-only
  audit table (CHECK'd status vocabulary, `requested_chain`, JSONB payload,
  nullable score/error/http_status/latency, `fetched_at`, newest-25 index)
  + seeds `goplus|publicaml|chainabuse` as `source_type='api'`.
- `queries/provider_observations.sql` → `internal/db/provider_observations.sql.go`
  (insert / list-latest / prune; retention pruned inside the insert tx).
- `internal/provider/resilience/{client,limiter,breaker,retry}.go` +
  tests — shared toolkit: token bucket (1 token/attempt incl. retries,
  bounded wait), semaphore cap, closed/open/half-open breaker, bounded
  transport/5xx-only retry, Retry-After surfaced (never slept in-request),
  HTTPS + allowed-host + same-host-redirect + 4 MB body guards, metrics
  hooks.
- `internal/provider/goplus/{client,model,mapper,adapter}.go` + tests +
  `testdata/{address_security_sanctioned,address_security_clean}.json`
  (real captures: sanctioned OFAC address + clean burn address).
- `internal/provider/publicaml/{model,mapper,adapter}.go` + tests +
  `testdata/{enrich_sanctioned,enrich_unknown,error}.json`
  (real captures; sanctioned abridged to top-3 outbound items).
- `internal/provider/chainabuse/{model,mapper,adapter}.go` + tests +
  `testdata/reports_sample.json` (docs-shaped, provisional — no key).
- `internal/enrichment/{model,cache,service,handler}.go` + tests.
- `tests/provider_observations_test.go`, `tests/enrichment_api_test.go`,
  `tests/enrichment_failure_test.go`, `internal/platform/metrics/metrics_test.go`.

## Files modified

- `internal/provider/provider.go` — on-demand contracts (LookupResult/
  Observation/Exposure/LookupError/ErrKind) documented as deliberate §13
  extension; `HTTPStatus` added for the audit row.
- `internal/config/config.go` + `config_test.go` + `.env.example` —
  provider base URLs/keys/rates + `ENRICHMENT_*` knobs.
- `internal/platform/metrics/metrics.go` — provider collectors
  (`provider_requests_total`, `provider_errors_total`,
  `provider_rate_limited_total`, `provider_circuit_open`) + `ProviderHooks`.
- `internal/platform/cache/keys.go` — `ProviderRefresh` key.
- `internal/httpserver/server.go` — `Deps.Enrichment` + route.
- `cmd/api/main.go` — `buildEnrichment` wiring (fatal on misconfig;
  chainabuse DISABLED logged without key; keys never logged).
- `internal/testutil/testdb.go` — `RequireTestPoolDSN` extracted
  (guard preserved); `testdb_integration_test.go` — data_sources assertion
  now checks all 5 seed codes.
- `tests/api_wallets_test.go` — row count scoped to `normalized_address`
  (parallel packages share `check_wallet_test`; a global `count(*)` cannot
  be stable — the idempotency invariant is preserved, not weakened).
- `api/openapi.yaml` — stale `info.description` fixed; enrichment route +
  schemas with all mandated semantics documented.
- `README.md` — scope line, Live enrichment section + attribution note.
- `docs/pipeline-log.md` — stage log entries (earlier stages).

## Verification

- `go test -count=1 ./...` — ALL PASS (incl. real `check_wallet_test` DB +
  Redis, `httptest` providers only).
- `go test -count=1 -race ./...` — ALL PASS (fan-out race-clean).
- `gofmt -l .` — empty. `go vet ./...` — clean. `go build ./...` — exit 0.
- Migration 000006 up/down verified on `check_wallet_test` (permanent
  rollback test in `tests/provider_observations_test.go`).
- Live dev-stack curl (real adapters): `COMPLETED` with GoPlus OK (3 obs),
  PublicAML OK (`provider_score:100`, 2 obs, 7 exposures), Chainabuse
  `DISABLED` (zero calls); second call `cached:true` at 5 ms; `refresh=1`
  refetches; `provider_observations` rows carry `requested_chain` `1`/`ETH`
  verbatim, latency, score, http_status.

## Deviations (documented, none violating the contract)

- `sanctioned:true` AND `direct_attribution` each produce an observation
  for PublicAML (both plan-mandated mappings; dedup is a Phase-5-scoring
  concern — audit log preserves both claims verbatim).
- Chainabuse `requested_chain` records our internal network code — its API
  is address-keyed; no literal chain token is sent upstream.
- `refresh` parse failure → `400 BAD_REQUEST` (strict `ParseBool`).
- `PARTIAL` dev-verification via env-URL stub is **structurally
  impossible by design**: the adapter's hardcoded host allowlist rejects
  any `PUBLICAML_BASE_URL` override at boot (`api: publicaml: host "…" not
  in allowlist`). Covered instead by `TestEnrichment_PartialWhenProviderFails`
  + typed 5xx resilience tests — the allowlist working as intended.
- `internal/enrichment` tests use a package-private DB
  (`check_wallet_enrichment_test`, `_test`-suffixed, guard-compliant) +
  Redis DB 10 — `go test` runs packages in parallel and the shared
  `check_wallet_test` is truncated per-test by every pool caller, so
  row-level persistence assertions cannot survive cross-package truncation.
  (`tests/` keeps the shared DB — same pre-existing posture as before.)

## Known limits

- Chainabuse model is provisional (docs-shaped; no key to live-verify).
- PublicAML golden fixture abridged to top-3 outbound items (shape real,
  list shortened for testdata size).
- Redis-outage fail-open verified at service level + API level with a
  dead-port client.
- Retention bound may transiently exceed by one row under cross-process
  bursts — self-heals on next insert (documented in DDL).
