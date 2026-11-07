# Stage 5 — Surface Classification (solana-enablement-prep)

**Classification: `internal-only` → Stage 5 SKIPPED (with reason).**

## Why internal-only

The increment is a **dormant** `provider.TxProvider` adapter plus
decision/semantics docs. It touches no live surface:

- **No UI route exists in this repo** (backend-only monorepo).
- **No new or modified endpoint**: zero diffs under `internal/*/handler*`,
  `api/openapi.yaml`, routes, or middleware (`git diff 456c5f9..HEAD --
  'api/' 'internal/*/handler*'` → empty).
- **No live call site**: `internal/provider/solanarpc` is imported by no
  production binary — `go list -deps ./cmd/api ./cmd/worker` contains it
  only in its own package tests. `Enabled()` is doubly gated and
  `getSignaturesForAddress` can never fire: the network row is
  `is_active=FALSE` (resolver filters `WHERE is_active = TRUE`), no
  `data_sources` row, no providerconfig descriptor, no selector wiring,
  no `TX_PROVIDER_*` env surface.
- Contrast with the TRON run (classified `API` because `GetOrCreate`'s
  signature changed the live `/wallets` response): this increment changes
  **no** live code path — `git diff` is 4 new files + docs only.

## Evidence the dormancy holds end-to-end

- `SELECT code, is_active FROM blockchain_networks ORDER BY code`
  → solana `f` (dev DB :5433, verified during Phase 5).
- `SELECT count(*) FROM data_sources WHERE code='solana-rpc'` → 0.
- `/networks` serves only `is_active=TRUE` rows — solana cannot surface
  (`queries/networks.sql` + resolver, unchanged code path).

Recorded per pipeline rule: "Skip Stage 5 only for internal-only … record
the skip reason in reports/ and pipeline-log.md."
