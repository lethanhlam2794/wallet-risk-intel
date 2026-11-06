# Cook Report — non-evm-expansion groundwork

Date: 2026-11-06 · Commits: `64b75e9` (feat), `d137e01` (docs)

## What shipped (groundwork only — no traffic path enabled)

- `AddressFormat` (`evm|solana|tron`) derived from CAIP-2 `namespace`;
  `Network.AddressFormat()` + `AddressFormatForNamespace`, unknown→`""`
  fail-closed (D1).
- Stdlib-only base58 (`math/big`), `NormalizeSolana` (base58, exactly
  32-byte decode, 32–44 chars, verbatim) and `NormalizeTron` (base58check
  `0x41`+20B+double-SHA256 checksum, verbatim).
- `wallet.Normalize(net, raw)` dispatch; `ResolveAndNormalize` routed
  through it — error contract (`ErrInvalidAddress`/`ErrInvalidNetwork` →
  400) unchanged; zero EVM behavior change.
- Migration `000026_non_evm_networks`: solana/tron rows `is_active=FALSE`
  (CAIP-2 `solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp`,
  `tron:0x2b6653dc`); `ON CONFLICT DO NOTHING`; down guarded
  `AND is_active=FALSE`.
- D4b: `AND bn.is_active` predicates on all five code-keyed `bn.code`
  joins (`StgLabelOrphanNetworks`, `CountLiveLabelsForStagedNetworks`,
  `PublishEnsureLabelWallets`, `PublishUpsertWalletLabels`,
  `PublishRevokeAbsentWalletLabels`); `sqlc generate` diff committed.
- `testdb_integration_test.go` seed assertion updated to the
  `{bsc, ethereum, solana, tron}` code set + inactive check (spec change).
- OpenAPI: both `network` enums `[ethereum, bsc, solana, tron]` with
  INACTIVE marking incl. explicit `Address` EVM-only `pattern` note;
  `/networks` active-only note; `BadRequest` `invalid_network` example.
- `docs/non-evm-audit.md` + `docs/non-evm-provider-discovery.md`
  (repo-root docs/, all candidates UNCONFIRMED).

## Verification evidence

- `gofmt -l .` empty; `go vet ./...` clean; `go build ./...` clean.
- `go test ./...`: 35 packages ok, 0 failures (tracing flake re-ran green
  twice — unrelated to diff).
- New tests: 5 `TestNonEVM_*` integration tests (rows inactive, resolver
  rejects, staged-solana orphan pin, MigrateTo(25) rollback + re-up on
  scratch DB, down-guard never deletes active).
- Property tests: 4 × 5000 seeded iterations green.
- Dev DB (:5433): `make migrate` → 4 rows; manual down in txn →
  `DELETE 2`; activated solana → `DELETE 1` (guard holds); up re-run →
  `INSERT 0 0` idempotent.
- Booted API (`go run ./cmd/api` on dev DB):
  - `GET /api/v1/networks` → bsc + ethereum only.
  - `GET /api/v1/wallets/solana/So111…` → `400 INVALID_NETWORK`.
  - `GET /api/v1/wallets/tron/TR7NHqj…` → `400 INVALID_NETWORK`.
  - `GET /api/v1/wallets/ethereum/0x098B…` → `200` (control unchanged).

## Environment note

9 private `check_wallet_*_test` DBs were stale at schema_migrations v26
(from a prior rolled-back attempt); their rows already matched the
planned seed contents, so creating the real `000026` files reconciled
them — no DB reset was needed.

## Still blocked pending §15 review / provider discovery

- `is_active` flip for solana/tron (traffic enablement).
- Any TxProvider/SnapshotSource adapter code (discovery doc is
  UNCONFIRMED-only).
- `Service.GetOrCreate` signature (`address_type` is `$4`-param stamped
  `'evm'` — audit lists literal census).
- Transactions schema semantics for slot-based chains (slot vs block,
  transfer_index identity, SPL/TRC-20 typing).
- OpenAPI `Address` `pattern` generalization.
- `wattrFiles` artifact mapping for non-EVM codes.
