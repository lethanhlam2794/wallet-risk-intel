---
phase: 3
title: "Worker mode wiring + verification gates"
status: completed
priority: P2
dependencies: [2]
---

# Phase 3: Worker mode wiring + verification gates

## Overview

Register `-mode warmseed` in `cmd/worker/main.go` and run the full
project gates plus a real CLI smoke test against a dedicated `_test`
database.

## Requirements

- Functional: `go run ./cmd/worker -mode warmseed` executes the seeder
  once and exits 0; seeder/DB-level failure exits non-zero.
- Functional: `WARM_INDEX_ENABLED=0` → log "warm index disabled" +
  exit 0 with zero work (mirrors `runWarmDrain`).
- Non-functional: no changes to `ingest`/`warmdrain` behavior.

## Architecture

`runWarmSeed()` mirrors `runWarmDrain()` but skips everything the seeder
doesn't need (no Redis, no provider keystore/resolver, no lanes). The
resolver is deliberately NOT used — classification reads
`is_active`/`namespace` from the JOIN, and `ListActiveNetworks` would
hide the very rows the counters must observe.

```go
func runWarmSeed() error {
	_ = godotenv.Load()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logger.New(cfg.LogLevel)
	if !cfg.WarmIndexEnabled {
		log.Info("warm index disabled (WARM_INDEX_ENABLED); exiting")
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := database.Migrate(cfg.DatabaseURL); err != nil {
		return err
	}
	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	queries := db.New(pool)
	queue := warmindex.NewQueue(queries, cfg.WarmIndexEnabled, log)
	seeder := warmindex.NewSeeder(queries, queue,
		cfg.WarmSeedBatchSize, log)
	return seeder.Run(ctx)
}
```

Flag help text updates: `"worker mode: ingest|warmdrain|warmseed"`.

**Runtime bound (documented decision)**: total run time is intentionally
unbounded — a cron-scheduled one-shot bounded per-batch (30 s); ops may
wrap with systemd/cron timeouts. No new config knob for this.

## Related Code Files

- Modify: `apps/api/cmd/worker/main.go`

## Implementation Steps

1. Add `case "warmseed": err = runWarmSeed()` to the mode switch.
2. Add `runWarmSeed()` per the wiring above (signatures verified:
   `config.Load() (*Config, error)`, `database.Connect (pool, err)`).
3. Update the `-mode` flag help string.
4. Run gates: `go build ./...`, `go vet ./...`, `gofmt -l .` (empty),
   `go test ./internal/warmindex/` and the wider suite.
5. CLI smoke (manual, recorded in reports): seed fixtures into
   `check_wallet_warmindex_test`, run the real binary with BOTH
   `DATABASE_URL` **and** `WARM_INDEX_ENABLED=1` exported explicitly
   (`godotenv.Load()` can mask) — assert counters + `warm_queue` rows;
   re-run to prove idempotency; run with `WARM_INDEX_ENABLED=0` to prove
   the no-op exit.

## Success Criteria

- [x] `go build ./...`, `go vet ./...`, `gofmt -l .` all clean
- [x] `go test ./...` zero drift vs baseline
- [x] Real `warmseed` run enqueues expected rows; second run adds none
- [x] `WARM_INDEX_ENABLED=0` exits 0 with zero queue writes
- [x] Unknown `-mode` still errors (no dispatch regression)

## Risk Assessment

- `signal.NotifyContext` on a one-shot: harmless; lets SIGINT abort a
  long scan mid-batch instead of hard-killing.
- `.env` masking risk in smoke — mitigated by explicit env exports.
