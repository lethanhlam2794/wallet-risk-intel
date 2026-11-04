# Phase-01 — Frontier generalization + exposure scoring

## Files / changes

- `internal/tracing/service.go` — generalize the single-depth
  positions/results/prefix pipeline into a per-depth loop
  (`d = 2..max_hops`); `position` struct (addr + parents map);
  frontier build from emitted-prefix nodes' scanned edges (Reading A);
  multi-parent ordering via `firstEdge` over union; unified dedup
  set; per-depth contiguous prefix; truncation guards per §2.7.
- `internal/tracing/model.go` — `Item.ExposureScore *int`;
  `Result.ExposurePolicyVersion`; coverage fields `frontier_by_hop`,
  `nodes_examined_by_hop`, `frontier_emitted_by_hop`,
  `nodes_remaining_by_hop`, `max_depth_reached`.
- `internal/tracing/score.go` (new) — `exposure-v1` evaluator:
  `base(kind, code)`, `decay(hops)`, `vf×cf`, integer per-term rounding
  (sixteenths decay, half-up); null when all signals zero-weight.
- `internal/tracing/service.go::ValidateRequest` — max_hops 1..5.
- `internal/transaction/service.go` — `MaxPagesFor(frontierIdx int)`
  replaces `(hopN bool)`; callers updated.
- `internal/config/config.go`, `cmd/api/main.go` —
  `TRACING_MAX_NODES_{2,3,4,5}` (legacy `TRACING_MAX_NODES` = depth-2
  fallback), `TXINDEX_MAX_PAGES_HOP3` (default 1).

## Tests (plan §6)

1–5, 8, 9, 12–19, 21–24, 26–31, 33–35 in `internal/tracing/service_test.go`;
`internal/transaction` tier test (16); `Request.MaxHops` doc comment
lives in `internal/tracing/model.go:46`.
