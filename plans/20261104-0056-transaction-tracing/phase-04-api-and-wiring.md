# Phase 4 — API + wiring (plan §3.5)

status: pending

## Tasks
- [ ] Handlers + routes (TxProvider-injectable via httpserver.Deps; fixture stub in E2E)
- [ ] `cmd/api/main.go` wiring (adapter registry; missing keys ⇒ DISABLED not crash)
- [ ] OpenAPI: both paths + full coverage schema (counterparties_total/positions_emitted/node_indexed_until/head_block/root.*) + truncated enums + direction filter in|out|self (/transactions) & in|out (/exposures) + index_scope + rows_skipped + examples; screening `coverage.tracing` enum `[NOT_IMPLEMENTED]` → `[provider_reported_only, bounded_bfs]` at api/openapi.yaml:1316; touch-points: internal/screening/{service.go,mapper.go}, model.go comment, service_test.go assertion; regenerate 5 docs/feature-maps/assets/*.json captures; fix stale top-level description at api/openapi.yaml:14 ("tracing intentionally absent")
- [ ] Metrics: tracing_nodes_examined_total + provider_requests_total/provider_errors_total + NEW provider_request_duration_seconds (OnResult lacks latency — call-site observe or OnDuration)
- [ ] `tests/transactions_api_test.go` + `tests/exposures_api_test.go` — required list 1–22 (+18b/18c/21b)

## Success criteria
- E2E green; OpenAPI parses; no stale NOT_IMPLEMENTED consumers
