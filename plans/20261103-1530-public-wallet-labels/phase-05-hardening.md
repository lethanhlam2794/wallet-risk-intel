# Phase 05 — Hardening, OpenAPI, Failure Modes

## OpenAPI (`api/openapi.yaml`)

- `GET /wallets/{network}/{address}/labels` — params, response schema,
  examples (labeled, unlabeled, revoked, paginated, errors)
- New schemas: `WalletLabel`, `LabelListResponse`, `LabelEntity`,
  `LabelSource`

## Metrics

- `wallet_labels_active_total` gauge (spec §1746) refreshed post-publish
- Reuse ingestion metrics (ObserveIngestion) with `source=wallet_attribution`

## Failure-mode tests (spec §18 label items)

- Interrupted download mid-CSV → run FAILED, nothing published/revoked
- Truncated/malformed CSV → FAILED, labels intact
- Schema drift (header change) → FAILED
- Worker crash simulation → heartbeat sweep marks stale run FAILED
- Concurrent runs → second rejected (advisory lock / RUNNING row)
- Empty labels result → valid response, never "safe" semantics
- Conflicting labels same wallet → coexist, both returned
- Disappearing listings → soft-revoked only after validated publish

## Worker/API wiring

- `cmd/worker -source wallet_attribution` live path (+`-file` mode)
- `POST /api/v1/admin/ingestion-runs` accepts `wallet_attribution`
- Admin GET run shows per-file stats in records_* counters

## Docs

- `README.md` — labels section (source, license MIT, endpoint, semantics)
- `docs/guidebook-*` updated in Stage 6 (not Cook's scope)

## Definition of done

- `gofmt -l .` clean, `go vet`, `go build`, `go test -count=1 ./...`,
  `go test -race ./...` all green on `check_wallet_test`
- Real `ethereum.csv`+`bsc.csv` import verified on dev DB with counts
- sqlc regenerated, no hand-edited generated code
