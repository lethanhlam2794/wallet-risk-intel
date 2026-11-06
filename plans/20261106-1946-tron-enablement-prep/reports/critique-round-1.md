# Devin Critic — Round 1 Critique: TRON Enablement Prep Plan

Session: `swanky-icon` · Mode: `auto` (write tool rejected in non-interactive mode — report delivered inline, persisted here by orchestrator) · Date: 2026-11-06

Verdict: **CHANGES_REQUIRED**

## Executive Assessment (abridged — full text in session transcript)

~35 cited file:line claims independently re-verified — every material claim accurate, including the genuinely new `internal/httpserver/server.go:270` fourth `GetOrCreate` caller (audit miss confirmed). Critic also pre-verified the plan's three `⚠` items: `warmindex/queue.go:46-47`, `ofac/mapper.go:32-44`, migration `000019` re-key shape.

**Load-bearing weakness**: Phase 3 framed the TRC-20 problem as one "cursor→watermark bridge" — it is actually THREE orthogonal problems:
1. **Input side**: `FromBlock`/`ToBlock` → `min/max_timestamp` bridge (both streams).
2. **Stored evidence**: `txRowJSON.BlockNumber` non-pointer `int64` (`identity.go:32`) → absent block marshals `0` → `block_number=0` + `confirmation_status='pending'` — fabricated evidence. (`transactions.block_number` IS nullable, `000008:22`.)
3. **Coverage channel**: `streamMaxBlock` derives only from `Transfer.BlockNumber` (`service.go:454-456,516-519`) → unfinished block-less stream ⇒ `fetchFrom−1` coverage → watermark clamped → perpetual `PARTIAL`. `StreamStatus` (`provider.go:258-261`) has only `{Name, Finished}` — no channel exists.

Plus: blockscout keyless precedent exists (`:182-184`); `wallets.address_type VARCHAR(30)` no CHECK (`000001:20`) — `'tron'` schema-legal.

## Blind Spots (17, full detail in transcript)

TRC-20 unfinished-stream stall · fabricated block-0 evidence · budget blow-out on fan-out (post-hoc `settle`) · streamMaxBlock-before-buildRows silent skip + advancing watermark at enablement · cursor advance past unresolved rows · undeclared streams · hex-verbatim = 100% InvalidAddress skip · non-monotonic/boundary-exclusive timestamps · txinfo indexing lag · multi-contract txs missed by `[0]`-only · fingerprint+params consistency (token must carry resolved window) · `getblockbynum(0)`/`fetchFrom=0` degenerate boundary · `FromBlock≤ToBlock` guard · `.k`-suffix replay-safety fixture · zero-amount/zero-address policy · keyless ≠ production fitness · out-of-table candidates unnamed.

## Required Plan Amendments (RA-1..RA-10)

- RA-1: Phase 3 tri-problem restructure (b1 window / b2 per-row resolution {fan-out, anchor+fan-out, defer-unfinished} / b3 invariants) + calls-per-ensure arithmetic + named non-options.
- RA-2: INV-1 cursor-advance + INV-2 stream-declaration, binding + test-asserted.
- RA-3: base58-canonical DECIDED (adapter-side hex→base58check conversion).
- RA-4: `walletsolidity/` consistency; txinfo-lag → bounded retry → `ErrDegraded`; monotonicity assumption + dedup-safe low-edge widening; inclusivity probe.
- RA-5: calls-per-ensure model; trc20 page clamp; Enabled() layering; key-required recommendation.
- RA-6: contract[] iteration; excluded trc20 types; INTERNAL exclusion; zero-amount/zero-address policy; contractRet rule; empty-Kinds.
- RA-7: `list.go:91` blocker; `txRowJSON.BlockNumber→*int64` under honest-NULL.
- RA-8: dedicated `ErrUnknownAddressFormat`; `check_wallet_wallet_test` + `package wallet_test`; SQL comments in leading `--` blocks.
- RA-9: `var _ provider.TxProvider`; fan-out bound + exact `UpstreamCalls` test; dormant doc + contract revision; txinfo/bridge/`num:0` fixtures.
- RA-10: discovery header update; `writeDisabled` dormant shape in audit update.

Per-phase verdicts: P1 ACCEPTED (control-list additions) · P2 ACCEPTED (quota-model + probe strengthenings) · P3 CHALLENGED (restructure required) · P4 ACCEPTED (nits) · P5 CHALLENGED (missing binding invariants) · P6 ACCEPTED (doc-consistency additions).
