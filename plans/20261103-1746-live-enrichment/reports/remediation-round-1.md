# Remediation — Review Round 1

Date: 2026-11-03 (post `review-round-1.md` CHANGES_REQUESTED)
Scope: all 7 actionable findings (1 MEDIUM required, 6 LOW advisory) plus
the actionable misc items under finding 8.

## Fixes applied

| # | Severity | Finding | Fix |
|---|----------|---------|-----|
| 1 | MEDIUM (required) | `TestProviderObservations_MigrationRollback` mutated shared `check_wallet_test` schema | Test now derives `check_wallet_rollback_test` from `TestDSN()` via `strings.Replace` and uses `testutil.RequireTestPoolDSN`; `_test` suffix guard preserved. File: `tests/provider_observations_test.go`. |
| 2 | LOW | `requested_chain` fell back to `net.Code` on failure rows | `provider.LookupError.RequestedChain` added; `provider.StampRequestedChain` helper; stamped on the `cli.Do` error path in all three adapters (goplus `net.ChainReference`, publicaml `chain`, chainabuse `net.Code`); service `requestedChain(lr, lerr, net)` prefers result → error → `net.Code`. |
| 3 | LOW | `refresh=1` + Redis outage lost `bypassed` marker | `refreshAllowed` now returns `(granted, cacheDown)`; a down/unreachable guard marks `cacheDown` so `cache:"bypassed"` surfaces on the refresh path. `internal/enrichment/cache.go`, `service.go`. |
| 4 | LOW | sibling-cancel test never failed a sibling | `fast` fake now returns `ErrUpstream5xx` instantly; test asserts `PARTIAL` aggregate, `UPSTREAM_5XX` on the failed source and `OK` on the slow sibling. `internal/enrichment/service_test.go`. |
| 5 | LOW | `?providers=goplus,goplus` duplicated blocks | `resolveProviders` dedupes names with a `seen` set; unknown names still 400. `internal/enrichment/service.go`. |
| 6 | LOW | OpenAPI example/doc nits | Example `evidence` now matches GoPlus shape `{"field","value","data_source"}`; `persisted` doc states it reflects the producing fetch on cached hits. `api/openapi.yaml`. |
| 7 | LOW | caller cancellation counted as breaker failure | `breaker.Report(false)` now skipped when `ctx.Err() != nil`; comment explains rationale. `internal/provider/resilience/client.go`. |
| 8 | INFORMATIONAL | misc items | Same-host `https→http` redirect downgrade now refused in `CheckRedirect`. Failure rows persist bounded `LookupError.Response` when present (payload precedence: raw body → error snippet → `{"attempt":true}` marker). PublicAML unlisted breakdown fields: real captured fixtures contain no `aml_score_breakdown` entries — nothing to surface; raw payload is preserved verbatim in persistence regardless. `kylelemons/godebug` indirect: `go mod tidy -diff` empty — canonical. |

## Post-remediation gates

- `gofmt -l .` → empty
- `go vet ./...` → clean
- `go build ./...` → clean
- `go test -count=1 ./...` → all pass (tests/ 40.7 s)
- `go test -count=1 -race ./...` → all pass (tests/ 95.8 s)
- `go mod tidy -diff` → empty

Working tree intentionally dirty (31 changed/untracked paths) per pipeline
rule — no commits.
