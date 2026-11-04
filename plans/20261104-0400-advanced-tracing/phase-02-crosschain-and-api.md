# Phase-02 — Cross-chain correlation + API/OpenAPI + E2E

## Files / changes

- `queries/transactions.sql` — new `BulkMatchLabelsCrossNet`
  (`w.network_id = ANY($2::bigint[])`, projects `n.code AS
  network_code`, `w.address AS listed_address`, `wl.confidence`);
  `BulkMatchLabels` gains `wl.confidence` in its select list;
  `sqlc generate`.
- `internal/tracing/service.go` — resolver-driven foreign-label
  lookup; `kind:"label"` signals with `network`/`listed_address`;
  never in `consider()`; solely-foreign item →
  `category="cross_chain_listing"`.
- `internal/tracing/service.go::New` — signature gains
  `resolver *network.Resolver`; `cmd/api/main.go` + test stacks
  updated; `Request.MaxHops` doc comment updated to {1..5};
  `signals()` distinguishes foreign rows to skip `consider()`.
- `internal/httpserver/transactions.go` — max_hops validation 1–5,
  error strings updated.
- `api/openapi.yaml` — summary/description, `max_hops` enum [1..5] +
  400 text, item `hops` enum [1..5], `exposure_score` (nullable int),
  envelope `exposure_policy_version`, per-hop coverage fields,
  example added.
- `tests/transactions_api_test.go` — 3-hop seeded graph + cross-net
  seeded label E2E (second network via resolver).
- `docs/guidebook-transaction-tracing.md` +
  `docs/feature-maps/transaction-tracing.md` — max_hops 1–5,
  exposure_score/exposure_policy_version, per-hop coverage,
  cross-chain label semantics, Σ-additive deviation note;
  feature-map assets regenerated for `/exposures` capture.

## Tests (plan §6)

6, 7, 10, 11, 20, 25, 32 + API contract regressions.
