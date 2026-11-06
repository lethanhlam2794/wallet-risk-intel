---
phase: 4
title: "address_type Generalization — thread network.AddressFormat through GetOrCreate"
status: completed
priority: P1
dependencies: [1]
---

# Phase 4: address_type Generalization

## Overview

Replace the hardcoded `AddressType: "evm"` in `wallet.Service.GetOrCreate`
with the resolved network's `AddressFormat`, update every caller, and
document why the remaining `'evm'` SQL literals stay. EVM rows must remain
byte-identical (`"evm"`) after the change.

## Requirements

- Functional: `GetOrCreate` stamps `string(net.AddressFormat())`; errors
  on `""` (fail closed — an unknown-namespace network can never write a
  wallet row with an empty address_type).
- Non-functional: zero behavior change for EVM paths; no SQL regeneration
  needed (`wallets.sql` is param-driven — verified).

## Architecture

### Design decision (orchestrator-grounded)

New signature:

```go
func (s *Service) GetOrCreate(ctx context.Context, net network.Network,
    address, normalized string) (db.Wallet, error)
```

- Pass the whole `network.Network` — every production caller already has
  `net` in scope (verified). The type derives from the namespace, so a
  caller cannot pass a format that disagrees with the network row.
- `net.AddressFormat() == ""` → return a **dedicated typed error**
  `ErrUnknownAddressFormat` (critic RA-8): an unknown-namespace network is
  a registry-invariant violation, not malformed client input — do not
  conflate with `ErrInvalidAddress`. Fail closed, consistent with
  `wallet.Normalize` (`address.go:62-64`).
- `wallet` package already imports `checkwallet/internal/network` — no
  new import direction (no cycle: `network` does not import `wallet`).

### Complete call-site inventory (from Phase 1 report; orchestrator list)

Production:
1. `internal/screening/service.go:146` — via `WalletStore` interface
   (`:54-57` — update signature to take `net network.Network`; the file
   already imports `network`).
2. `internal/enrichment/service.go:117` — `s.wallets` is concrete
   `*wallet.Service`; `net` in scope.
3. `internal/transaction/service.go:182` — `net` in scope.
4. `internal/httpserver/server.go:270` — `net` in scope; **was missed by
   the audit** — this change makes the wallet API return the real
   `address_type` for future non-EVM rows.

Tests (all must compile — they call `GetOrCreate` directly):
`internal/transaction/failover_test.go:283,353,394,421`,
`internal/transaction/service_test.go:254,356,469`,
`internal/transaction/killswitch_test.go:185`,
`internal/screening/service_test.go:505`.
(Direct `q.GetOrCreateWallet` callers pass `AddressType` explicitly —
unchanged.)

### 'evm' SQL literals — stay, with comments (prompt rule)

These are fed by EVM-only upstreams; add an explanatory comment, no
semantic change:

- `queries/transactions.sql:27` (`BulkResolveWallets`) — fed by
  `buildRows` which `NormalizeEVM`s endpoints; tron bulk-resolve is an
  enablement task per the semantics doc.
- `queries/ofac_publish.sql:54` — OFAC listings normalize via
  `NormalizeEVM` (TRX → UNRESOLVED by design).
- `queries/labels_publish.sql:9` — wallet-attribution CSVs are EVM-only.
- `queries/stablecoin_freeze.sql:58` — freeze feeds are EVM-scanned.

Comment style: place the explanation in the **leading `--` comment block
above the `-- name:` line** (`transactions.sql:1-3,23-24` convention —
never inline mid-statement, which keeps `sqlc generate` a guaranteed
no-op). E.g. `-- 'evm' literal: upstream feeds are EVM-only
(NormalizeEVM); non-EVM stamping is an enablement task
(docs/tron-indexer-semantics.md).` Verify generated output unchanged.

Test conventions (critic RA-8): the new service test goes in
`package wallet_test` (external test package — `handler_test.go`
precedent) with the per-package dedicated DSN
`check_wallet_wallet_test` (`transaction/service_test.go:22` precedent);
`testutil` auto-creates + migrates any `*_test` DB (`testdb.go`).

## Related Code Files

- Modify: `internal/wallet/service.go`, `internal/screening/service.go`,
  `internal/enrichment/service.go`, `internal/transaction/service.go`,
  `internal/httpserver/server.go`, the 9 test call sites, and the 4
  `queries/*.sql` comment lines (sqlc regenerate NOT needed — comments
  only inside SQL files do not change generated code; verify generated
  output unchanged or regenerate if the toolchain embeds comments).
- Read: `internal/network/address_format.go`, `internal/wallet/address.go`

## Implementation Steps (TDD)

1. **Red**: new `internal/wallet/service_test.go` in `package wallet_test`
   asserting: tron-format `network.Network` (`Namespace: "tron"`) →
   `AddressType == "tron"`; eip155 → `"evm"`; unknown namespace →
   `ErrUnknownAddressFormat`. Also assert an existing-EVM lookup still
   returns `"evm"` (byte-identical). Dedicated DSN
   `check_wallet_wallet_test` via `testutil` — real insert, not a mock.
2. **Green**: change signature + stamp derived format; update all
   production + test call sites; compile (`go build ./...`).
3. Add the 4 SQL comment lines.
4. **Refactor**: confirm gofmt clean; confirm no other `AddressType`
   literals (`grep -rn 'AddressType' --include='*.go'`).

## Success Criteria

- [x] `GetOrCreate` stamps `string(net.AddressFormat())`; `""` →
      `ErrUnknownAddressFormat`
- [x] All 4 production + 9 test call sites updated and compiling
- [x] `WalletStore` interface signature updated; `screening` compiles
- [x] New service test (`package wallet_test`, dedicated
      `check_wallet_wallet_test` DSN) proves tron→"tron", evm→"evm",
      unknown→typed error against a real DB
- [x] EVM stamped values byte-identical (no EVM test regressed)
- [x] 4 `'evm'` SQL literals carry leading-`--`-block comments; no SQL
      semantic change; generated code untouched (or regenerated
      identically)

## Risk Assessment

- Signature churn is mechanical but touches 13 call sites — Phase 1's
  inventory is the control list; `go build` is the backstop.
- The `""` fail-closed path must not break any current caller: every
  resolvable network today is eip155 (inactive non-EVM rows never resolve)
  — verified via `migrations/000026` + resolver.
- sqlc comment-only changes: confirm `sqlc generate` produces identical
  output or skip regeneration entirely.
