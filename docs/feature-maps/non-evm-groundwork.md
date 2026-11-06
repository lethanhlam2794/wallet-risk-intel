# Feature Map — Non-EVM Expansion Groundwork (Solana + TRON)

Delivered via the 6-stage Codex Vibe pipeline (plan:
`plans/20261106-1555-non-evm-expansion/`; commits `64b75e9` + `d137e01`).
API + internal feature — no UI; evidence is live API JSON + DB queries
(`docs/feature-maps/assets/non-evm-*`).

## 1. What ships

Groundwork for future Solana/TRON support: a namespace-derived address
format abstraction, pure validators, inactive registry rows, leak-proof
label joins, and two findings documents. **No live traffic path is
enabled** — solana/tron remain rejected at network resolution pending
AGENTS.md §15 review.

| Item | Value |
|---|---|
| `AddressFormat` | `evm` \| `solana` \| `tron` — derived from CAIP-2 `namespace`, never stored |
| Solana CAIP-2 | `solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp` |
| TRON CAIP-2 | `tron:0x2b6653dc` |
| Registry state | both `is_active = FALSE` → `ListActiveNetworks` + resolver structurally exclude them |
| Normalization | EVM: unchanged (lowercase `0x`, 20 bytes). Solana/TRON: **verbatim** base58 (case-sensitive alphabet — never case-folded) |
| Errors | existing `wallet.ErrInvalidAddress` / `wallet.ErrInvalidNetwork` — no new error shapes |

## 2. Source map

| Concern | Path |
|---|---|
| Format derivation | `internal/network/address_format.go` (`AddressFormatForNamespace`; unknown → `""` fail-closed) |
| Format on model | `internal/network/resolver.go` (`Network.AddressFormat()`) |
| Dispatch | `internal/wallet/address.go` (`Normalize(net, raw)` — `ErrInvalidAddress` on empty/unknown format) |
| Validators | `internal/wallet/address_solana.go`, `internal/wallet/address_tron.go` |
| Base58 codec | `internal/wallet/base58.go` (stdlib `math/big`; leading-`1` → zero bytes) |
| Migration | `migrations/000026_non_evm_networks.{up,down}.sql` (`ON CONFLICT DO NOTHING`; down guarded by `is_active = FALSE`) |
| Leak predicates | `queries/labels_staging.sql` (orphan EXISTS, CountLive), `queries/labels_publish.sql` (Ensure, Upsert, Revoke) — all carry `AND is_active` / `bn.is_active` |
| Contract | `api/openapi.yaml` (`network` enums `+solana,+tron` marked not-yet-available; `Address` EVM `pattern` noted) |
| Tests | `internal/wallet/address_{solana,tron}_test.go`, `address_property_test.go` (5000-iter), `tests/non_evm_networks_test.go` (5 integration) |
| Findings docs | `docs/non-evm-audit.md`, `docs/non-evm-provider-discovery.md` |

## 3. Validator contract

| Chain | Charset | Char length | Decoded | Integrity |
|---|---|---|---|---|
| solana | base58 | 32–44 | exactly 32 bytes | — (ed25519 pubkey) |
| tron | base58 | — | exactly 25 bytes | `0x41` prefix + double-SHA256 checksum (last 4 bytes) |

Both return the input **verbatim** — solana/tron alphabets are
case-sensitive so no normalization is applied. Invalid →
`ErrInvalidAddress`, never silently repaired.

## 4. Live API evidence (Stage 5, fresh boot)

| Request | Result | Latency |
|---|---|---|
| `GET /api/v1/networks` | 200 — `bsc`+`ethereum` only (no inactive leak) | 4.8ms |
| `GET /wallets/solana/So111…112` | 400 `INVALID_NETWORK` | 3.2ms |
| `GET /wallets/tron/TR7NHqje…` | 400 `INVALID_NETWORK` | 4.1ms |
| `GET /wallets/ethereum/0x098B…` | 200 (control — identical EVM behavior) | 19.4ms |

## 5. Key design decisions

- **D1** `AddressFormat` derived from namespace — no DB column, no
  drift surface; unknown namespace → `""` → fail closed.
- **D2** Verbatim base58 return — case-folding a base58 string corrupts
  it; "normalized" = validated original.
- **D4b** (Critic amendment) `is_active` predicates on all five
  `bn.code` joins — prevents a staged `solana` label from publishing
  `'0x'+lower(base58)` garbage wallets under an inactive chain (which
  would also FK-block the down-migration). No-op for today's
  all-EVM-active rows.
- **Down-guard** `AND is_active = FALSE` — rollback can't silently
  delete a row a future review activated.
- **stdlib-only** base58 (`math/big` + `crypto/sha256`) — zero new deps.

## 6. Deferred nits (non-blocking, from Devin Review)

- `NormalizeTron` lacks the char-length pre-check `NormalizeSolana` has
  (cosmetic O(n²) `big.Int` cost on oversized input; bounded by request
  limits).
- `decodeBase58` allocates `big.NewInt` per char — fine at ≤44 chars.

## 7. Explicitly NOT done (stop conditions)

`is_active` flips · TxProvider adapters · transaction schema/index
changes · tracing/scoring/screening/ingestion changes ·
`GetOrCreate` `address_type` generalization · OpenAPI `Address` pattern
generalization · paid provider selection. Activation checklist:
`docs/non-evm-audit.md`; candidate evaluation (UNCONFIRMED):
`docs/non-evm-provider-discovery.md`.

## 8. Pipeline provenance

| Stage | Session | Outcome |
|---|---|---|
| 1 Critic | `defiant-summer` (aborted, `auto` rejected exec) → `chestnut-crop` (`smart`) | Round 1 CHALLENGED (6 amendments) → Round 2 `PLAN_CONSENSUS_REACHED` |
| 2 Cook | `skitter-night` | TDD; commits `64b75e9` + `d137e01` |
| 3 Review | `efficient-gargoyle` (interrupted once on `smart` tool rejection; resumed) | **APPROVED** — 0 blockers/majors/minors, 3 nits |
| 4 Saturation | orchestrator | 10/10 shuffled fresh runs (unit + integration) |
| 5 Surface | orchestrator | API verified live on :8099 (above) |
| 6 Docs | orchestrator | this file + `docs/guidebook-non-evm-groundwork.md` |
