---
phase: 2
title: Config resolution layer + boot wiring
status: completed
priority: high
dependencies: [1]
---

## Overview
`internal/providerconfig/`: merge provider_configs row over env
per tx-history adapter; optional AES-256-GCM for api_key; adapters built
from resolved config in cmd/api/main.go.

## Requirements
- Resolver: `Resolve(ctx, q, sourceCode)` → {APIKey, BaseURL,
  EnabledChains, Status, Origin(db|env|default)}; DB field wins when
  non-empty/non-null else env.
- Encryption: `PROVIDER_CONFIG_KEK` (32-byte base64/hex) set ⇒ encrypt on
  write & decrypt on read; unset ⇒ plaintext (documented). Wire shape
  `enc:v1:<nonce>:<ct>` so plaintext/encrypted coexist honestly.
- main.go: each tx adapter constructed from resolved config (key,
  base_url, enabled_chains) instead of cfg.Xxx*; boot logs effective
  origin per provider (no values).
- Boot invariants unchanged: data_sources row + budget>0 + unknown
  selector fails.
- Absent provider_configs row ⇒ byte-identical env behavior (regression
  test asserting identical adapter config vs env-only path).

## Related Code Files
- Create: internal/providerconfig/{resolver,keystore,descriptor,status_source}.go (+_test.go)
- Modify: cmd/api/main.go (adapter construction sites)
- Modify: internal/config/config.go (KEK env only)

## Implementation Steps
1. Red: tests for precedence, absent-row env parity, enc round-trip,
   tamper/corrupt ciphertext ⇒ error (never plaintext fallback).
2. Implement resolver + keystore; wire main.go.
3. Green: package tests + `go test ./internal/config/...`.

## Success Criteria
- [ ] Precedence truth table covered by tests (8 truth-table cases)
- [ ] Enc round-trip + tamper tests; no-key → plaintext path honest
- [ ] Env-only parity: absent row ⇒ identical resolved config
- [ ] Boot log shows origin, never values

## Risk Assessment
Medium — touches boot wiring; parity test is the guard.

## Amendments (critic R1)
- **Package:** all of resolver/keystore/status-source/admin-service live
  in `internal/providerconfig/` (not `internal/config/`) — keeps `config`
  env-pure; consumed by `cmd/api`, `transaction.Service`, `httpserver`.
- **Descriptor map:** explicit `map[sourceCode]{keyEnv, baseURLEnv,
  chainsEnv}` — `chainscan` maps to `ETHERSCAN_API_KEY`; covers all 12
  tx providers; non-tx providers become a data-add later, not rework.
- **Error routing:** ANY resolution error (DB failure, decrypt failure,
  allowlist rejection, malformed JSON) is boot-fatal naming
  provider+field — never env fallback on error; fallback only on absent.
- **NULL-vs-empty:** `enabled_chains=[]` and `base_url=""` are explicit
  overrides; only NULL/missing falls back to env. Write-validation may
  additionally warn on `[]` (second kill channel — documented).
- **Truth table** (per field): row-absent ×{env set,env unset} +
  db-null ×{env set,env unset} + db-empty ×{env set,env unset} +
  db-set ×{env set,env unset} = 8 cases, all tested.
- **Parity test wording:** assert "identical resolved config" (resolver
  output), not adapter internals.
- **Marker check:** `enc:v1:` prefix must match full
  `enc:v1:<b64 nonce>:<b64 ct>` shape before attempting decrypt; a
  plaintext value with the prefix + KEK set ⇒ boot-fatal
  misconfiguration (documented).
- **KEK rotation** = manual re-encrypt job, explicitly out of v1 scope.

- **Resolved-config snapshot:** the resolver retains the boot-time
  resolved config per provider so phase-04 `warnings[]` can compare
  stored vs in-effect (N-A).
- **`provider.AllowedHostsFor(code)`** export is implemented HERE
  (adapters publish allowlists); phase-04 consumes it (N-D).

## Outcome (cook)
- Commit `b32cba2`. `internal/providerconfig/` = resolver + keystore +
  descriptor map (12 providers, `chainscan`→`ETHERSCAN_API_KEY`) +
  snapshot. `provider.AllowedHostsFor` via `init()`-registered adapter
  allowlists. `cmd/api` builds all 12 adapters from resolved config and
  logs per-field origins. Tests: 8-case truth table per field, enc
  round-trip/tamper/malformed-marker, env parity, fatal named errors,
  stale-row ignore. Verified by real boot: origin log lines only.
- Note: keystore nil-receiver safe (`keys == nil` = plaintext), so
  `ResolveAll` tolerates a nil `*Keystore`.
