# Remediation — Review round 1 (APPROVED with 2 minors + 4 nits)

All findings were documentation-level; applied:

- **minor: `coverage.truncated` wording** (openapi.yaml:524) →
  `truncated` (sibling of `coverage`) — matches the wire.
- **minor: example `transaction_evidence: []`** → populated with the
  3 path edges (deduped evidence set, matching `dedupEvidence`
  behavior at service.go:442,469).
- **Found during fix: duplicate `ErrorEnvelope` schema** (lines
  1194+1428) → removed the later `nullable request_id` variant; wire
  always emits non-null `request_id`. Also found **18 invalid
  `nullable: true` usages** (OpenAPI 3.1 has no `nullable` keyword)
  → all converted to `type: [X, "null"]` unions; redocly lint now 0
  errors (was 18 + a duplicate-mapping parse failure).
- **nit map-domain prose** → `{2..max(max_hops,2)}`.
- **nit missing `.env.example` vars** → TRACING_MAX_NODES_{2..5} +
  TXINDEX_MAX_PAGES_HOP3 added (commented, defaults documented).
- **nit stale metric help** → `tracing_nodes_examined_total` help now
  "at every depth".
- **nit stale plan §5 residual** → updated: post-bound lookups run
  under caller ctx (impl improvement over drafted deadline→500).

## Verification
- `gofmt -l .` empty; `go vet`, `go build` clean.
- `go test -count=1 ./internal/tracing ./internal/transaction
  ./tests` — all green.
- `redocly lint api/openapi.yaml` — valid, 0 errors.
