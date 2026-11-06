---
phase: 1
title: "AddressFormat abstraction + Solana/TRON validators + Normalize dispatch"
status: completed
priority: P1
dependencies: []
---

# Phase 1: AddressFormat + non-EVM validators + `wallet.Normalize` dispatch

## Overview

Introduce the address-format abstraction on the network model, implement
pure-function Solana and TRON validators (stdlib only — no new dependency),
and route the shared `ResolveAndNormalize` input validator through a
format-dispatching `wallet.Normalize`. Zero behavior change for ethereum/bsc.

## Requirements

- Functional:
  - `AddressFormat` type (`evm | solana | tron`); derivation from CAIP-2
    `namespace` (`eip155→evm`, `solana→solana`, `tron→tron`, else unknown).
  - `NormalizeSolana`: base58 string, decoded length **exactly 32 bytes**
    (ed25519 pubkey), input length 32–44 chars; normalized = input verbatim.
  - `NormalizeTron`: base58check, `T` prefix, decoded 21-byte payload
    (`0x41` + 20 bytes) + 4-byte checksum = `SHA256(SHA256(payload))[0:4]`
    **verified**; normalized = input verbatim.
  - `wallet.Normalize(net, raw)` dispatches on `net.AddressFormat()`;
    unknown/empty format → `ErrInvalidAddress` (fail-closed).
  - `ResolveAndNormalize` calls `Normalize` instead of `NormalizeEVM`.
    `ErrInvalidAddress`/`ErrInvalidNetwork` error shape unchanged.
- Non-functional:
  - No new module dependencies — base58 decode/encode via `math/big`;
    checksum via `crypto/sha256` (stdlib).
  - Validators are pure, allocation-bounded, and never mutate input.
  - TDD: tests written first (Red → Green → Refactor).

## Architecture

```
network.AddressFormat (new file internal/network/address_format.go)
  type AddressFormat string     // "evm" | "solana" | "tron" | ""
  func AddressFormatForNamespace(ns) AddressFormat
  func (n Network) AddressFormat() AddressFormat   // derived — never a stored column

wallet (internal/wallet/)
  base58.go        — decode(s) ([]byte, error); encode used only by tests
  address_solana.go — NormalizeSolana(input) (string, error)
  address_tron.go   — NormalizeTron(input) (string, error)
  address.go        — Normalize(net network.Network, raw string) (string, error)
                      ResolveAndNormalize → Normalize(net, raw)
```

Type/method naming: `AddressFormat` everywhere (the source prompt's mandated
vocabulary) — NOT `Format`; post-debate reconciliation.

Design notes:
- `AddressFormat` lives on the **network model as a derived accessor**, not
  a DB column: `namespace` is already the CAIP-2 discriminator; a column
  would create a second source of truth that can disagree (see plan D1).
  Recorded limitation: namespace↔format is not universally 1:1 (`bip122`
  covers multiple encodings) — unknown namespaces fail closed to `""` →
  `ErrInvalidAddress`, safe at the §15-gated enablement moment.
- Base58 alphabet: `123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz`.
  Decode must reject characters outside the alphabet, leading/trailing
  whitespace, and empty input. Preserve leading-zero bytes (`'1'` chars)
  correctly.
- TRON: decoded length is **25 bytes** = 1 (`0x41`) + 20 payload + 4 checksum;
  verify `0x41` version byte AND checksum before accepting.

## Related Code Files

- Create: `apps/api/internal/network/address_format.go`
- Create: `apps/api/internal/network/address_format_test.go`
- Create: `apps/api/internal/wallet/base58.go`
- Create: `apps/api/internal/wallet/address_solana.go`
- Create: `apps/api/internal/wallet/address_tron.go`
- Create: `apps/api/internal/wallet/address_non_evm_test.go` (table tests)
- Create: `apps/api/internal/wallet/address_non_evm_property_test.go` (5000-iter
  seeded property tests matching `address_property_test.go` style)
- Modify: `apps/api/internal/wallet/address.go` (add `Normalize`; route
  `ResolveAndNormalize` through it)
- Modify: `apps/api/internal/network/resolver.go` (document `AddressFormat`
  on `Network`; no behavioral change needed — method derives from
  `Namespace`)
- Delete: —

## Implementation Steps (TDD)

1. **Red**: write `address_format_test.go` — `AddressFormatForNamespace`
   table test (eip155→evm, solana→solana, tron→tron, `""`/unknown→empty);
   `Network.AddressFormat()` on struct literals.
2. **Green**: implement `internal/network/address_format.go` (type +
   mapping + method).
3. **Red**: write `address_non_evm_test.go` — table tests:
   - Solana valid vectors (real mainnet-style pubkeys, e.g. `11111111111111111111111111111111`
     is the system program — decide inclusion deliberately), wrong decoded
     length (31/33 bytes), invalid alphabet chars (`0`, `O`, `I`, `l`),
     too short/long, empty, whitespace.
   - TRON valid vectors (known mainnet addresses, e.g. a `T…` address with a
     checksum computed independently in the test), corrupted checksum, wrong
     version byte (`0x00`/`0x42`), wrong decoded length, `t` lowercase prefix
     (must fail — case-sensitive).
4. **Green**: implement `base58.go` + `address_solana.go` + `address_tron.go`.
5. **Red**: `address_non_evm_property_test.go` — 5000 seeded iterations:
   random 32-byte → base58-encode → `NormalizeSolana` accepts + returns
   verbatim + idempotent; random 20-byte + `0x41` + checksum → encode →
   `NormalizeTron` accepts; corrupted strings rejected (never silently
   normalized); **case-flip of a valid base58 must NOT be accepted** unless
   it coincidentally decodes correctly — assert output preservation.
6. **Green/Refactor**: fix any decoder edge cases (leading `'1'` → zero byte).
7. **Red**: `Normalize` dispatch tests — net with `Namespace:"solana"` +
   valid solana addr → verbatim; `Namespace:"tron"` + valid tron addr →
   verbatim; `Namespace:"eip155"` + EVM addr → existing lowercase behavior;
   mixed-case solana input is NOT lowercased.
8. **Green**: add `Normalize` + rewire `ResolveAndNormalize`; keep
   `NormalizeEVM`/`IsEVMAddress` exported signatures untouched.
9. Run `go test ./internal/wallet/ ./internal/network/` — all green; run
   existing suite to prove zero EVM regression.

## Success Criteria

- [x] `AddressFormatForNamespace` + `Network.AddressFormat()` return correct
      formats incl. unknown→empty
- [x] `NormalizeSolana`/`NormalizeTron` accept valid vectors, return input
      verbatim (case preserved), reject all invalid classes
- [x] TRON checksum verification is real (corrupted last char rejected)
- [x] `Normalize` dispatches correctly per format; unknown format →
      `ErrInvalidAddress`
- [x] `ResolveAndNormalize` output identical for ethereum/bsc (existing tests
      still pass, incl. `handler_test.go` solana→`ErrInvalidNetwork`)
- [x] Property tests 5000 iters green; no test weakened
- [x] `gofmt -l`, `go vet`, `go build` clean

## Risk Assessment

- **Base58 leading-zero handling**: classic bug — decoded `[]byte` must
  re-add one `0x00` per leading `'1'`. Mitigation: property test round-trips
  random byte arrays through encode→decode.
- **TRON checksum mismatch**: double-SHA256 over the 21-byte payload only;
  comparing first 4 bytes. Mitigation: test builds the checksum
  independently, then flips one char and asserts rejection.
- **Over-accepting solana**: length must be exactly 32 bytes decoded, not
  just 32–44 chars input. Enforced in decode-then-measure order.
- **Scope creep**: do NOT touch direct `NormalizeEVM` callers — they handle
  EVM-only upstream data and stay correct.
