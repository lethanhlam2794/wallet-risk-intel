---
phase: 4
title: "cmd/worker warmdrain mode + config envs"
status: completed
priority: P1
dependencies: [2]
---

# Phase 4: `cmd/worker -mode warmdrain`

## Overview

Daemon-style drain mode in `cmd/worker` (poll loop, graceful SIGTERM) plus the
`config.Load` envs and `.env.example` docs. Default `ingest` mode is
byte-identical.

## Requirements

- Flag `-mode` ∈ {`ingest` (default, current one-shot), `warmdrain`}.
- Disabled flag (`WARM_INDEX_ENABLED` unset/false) → log + clean exit 0.
- Poll interval `WARM_INDEX_POLL_INTERVAL` (60s); **one item per cycle**.
- `warmdrain` builds its **own** `signal.NotifyContext` — never reuses
  `run()`'s `WithTimeout(OFACDownloadTimeout+30m)` ctx (a ~40m inherited
  deadline would kill the daemon). Mid-drain cancel releases the claim to
  `pending` (Drainer handles it) and exits.
- Minimal deps: config → migrate → pool → queries → resolver → keystore/
  `ResolveAll` → lanes → drainer. **No** Redis, no OFAC/wattr downloaders,
  no sanctions/label services.
- Providers: only `WARM_INDEX_PROVIDERS` codes (default
  `blockscout,blockscout_pro`); worker-local factory switch — unknown code →
  boot error naming it; same constructor args as `cmd/api/main.go:280-435`
  minus metrics `Hooks` (no scrape endpoint in worker).
  `ProviderLane.Code = Adapter.Name()`.
- Per-provider `transaction.Service` lane with that provider as the **only**
  chain member per network (D1); `SetStatusChecker(providerconfig.NewStatusCache
  (q,30s,WithLogger(log)))` on each lane — killswitch parity with the API.
- Boot check (cmd/api:466-473 parity): every warm provider resolves a
  `data_sources` row and has `Budget[code] > 0` — boot-fatal otherwise.
- `providerconfig.NewKeystore(cfg.ProviderConfigKEK)` + `ResolveAll` before
  building keyed adapters (required for blockscout_pro etc.).
- `transaction.Config` mirrors cmd/api — `ReorgTail`/`WindowCap` copied
  **verbatim** (incl. the BSC-only `WindowCap` asymmetry at main.go:485-489);
  a resolver-derived map would silently drop it. `Budget` restricted to warm
  codes; Hop* fields irrelevant (explicit `MaxPages` per drain).
- Migrations run at boot (`database.Migrate`) — same as ingest mode.
- **Producer wiring (blocking amendment A1)** in `cmd/api/main.go::run()`
  after `buildTransactions` (~main.go:172): construct
  `warmindex.NewQueue(queries, cfg.WarmIndexEnabled, log)` and
  `tracingSvc.SetEnqueuer(queue, log)`. Always wire — the Queue's `Enabled`
  flag makes the hook a no-op when off (one code path, contract-honest).

## Related Code Files

- Modify: `cmd/worker/main.go` (`-mode` dispatch, `runWarmDrain`),
  `internal/config/config.go` (6 fields + `envBool` helper),
  `cmd/api/main.go` (Queue construction + `SetEnqueuer` wiring),
  `.env.example` (documented envs)
- Create: `cmd/worker/warmproviders.go` (factory + lanes builder),
  `cmd/worker/warmdrain_test.go` (mode dispatch / flag-exit test if feasible
  without a running worker — otherwise covered by warmindex tests)

## Implementation Steps (TDD)

1. RED: `config_test.go` additions — new env defaults + parse errors
   (`WARM_INDEX_DAILY_CAP=abc` → error; `WARM_INDEX_ENABLED` accepts exactly
   `strconv.ParseBool` vocabulary — `1/true/false` yes, garbage → boot error).
2. GREEN: `config.go` fields + `envBool` helper.
3. `warmproviders.go`: `buildWarmLanes(ctx, cfg, pool, pcRes, q, resolver,
   wallets, codes, log) ([]warmindex.ProviderLane, error)` — switch
   constructing only listed codes
   (all 13 registered codes supported: chainscan, nodereal, alchemy,
   blockscout, blockscout_pro, ankr, covalent, moralis, tatum, bitquery,
   oklink, tokenview, chainbase); per-lane `transaction.New` single-member
   chains for every resolver network; boot checks per requirement.
4. `main.go`: `-mode` flag; `runWarmDrain` — enabled gate, resolver load,
   keystore/resolver, lanes, `warmindex.NewQueue`+`NewDrainer`, **own**
   `signal.NotifyContext` (not `run()`'s OFAC+30m timeout), `drainer.Run`.
5. `cmd/api/main.go`: `warmQ := warmindex.NewQueue(queries,
   cfg.WarmIndexEnabled, log)` + `tracingSvc.SetEnqueuer(warmQ, log)` after
   `buildTransactions` — always wired, no-op when disabled.
6. `.env.example`: document the 6 envs (defaults, no secrets).
7. Manual smoke: `WARM_INDEX_ENABLED=false go run ./cmd/worker -mode warmdrain`
   → exits 0 immediately with log line.

## Success Criteria

- [x] `go run ./cmd/worker` (no mode) still runs ingest unchanged.
- [x] Disabled-flag warmdrain exits 0 immediately.
- [x] `WARM_INDEX_PROVIDERS=bogus go run ./cmd/worker -mode warmdrain` fails
  naming `bogus`.
- [x] `cmd/api` calls `tracingSvc.SetEnqueuer` — grep-visible wiring.
- [x] `go build ./...` + `go vet ./...` + `gofmt -l .` clean.
- [x] `.env.example` lists all new envs.

## Risk Assessment

- Worker-local factory duplicates ~13 constructor cases (vs refactoring
  cmd/api) — accepted: smaller blast radius, documented deviation.
- `blockscout`/`blockscout_pro` default `EnabledChains=ethereum` only → BSC
  items stay pending when no allowlisted provider enables BSC — correct,
  not a bug (operators extend `*_ENABLED_CHAINS` or the allowlist).
- `ResolveAll` in worker requires `PROVIDER_CONFIG_KEK` parity with api —
  same env contract, no change.
