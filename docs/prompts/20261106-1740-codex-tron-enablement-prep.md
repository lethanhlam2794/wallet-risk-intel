# Prompt: codex — tron-enablement-prep

> Created: 20261106-1740 | Target: codex | Template: M (agentic task brief)

## Prompt

```text
You are implementing TRON enablement groundwork in the check-wallet repo
at /home/lethanhlam/Desktop/Profile/check-wallet (Go 1.27 module at
apps/api/, Gin + pgx + sqlc + golang-migrate). Risk class: R3 — external
provider integration + schema-adjacent semantics. Read AGENTS.md,
docs/non-evm-audit.md, docs/non-evm-provider-discovery.md, and
~/.codex/rules/engineering-standard.md FIRST; they are binding.

## Context (carry forward)
- AddressFormat groundwork already shipped: solana/tron rows exist in
  blockchain_networks with is_active=FALSE; the resolver rejects them.
  Validators NormalizeSolana/NormalizeTron are done. Do NOT re-do this.
- TRON data shape ≈ EVM: tx_hash is 64-hex (fits VARCHAR(255) and
  hex-ish assumptions), block_number is sequential (fits BIGINT and the
  ascending-order watermark contract). The real open problems are only:
  transfer_index identity, finality policy, and cursor→watermark mapping.
- Dev DB: postgres://wallet:devonly@127.0.0.1:5433/check_wallet
  Test DB: postgres://wallet:devonly@127.0.0.1:5433/check_wallet_test
  Integration tests MUST use dedicated *_test databases only.

### Objective
Make TRON code-complete but still INACTIVE: pick a verified free
provider, implement a TxProvider adapter that never emits traffic until
configured AND the network is active, generalize the hardcoded 'evm'
address_type on wallet-creation paths, and produce the semantics
decision docs a §15 reviewer needs.

### Files & Scope
- Primary (may modify):
  - apps/api/internal/provider/<chosen>/ (new adapter + tests)
  - apps/api/internal/wallet/service.go + GetOrCreate callers
  - apps/api/queries/*.sql ONLY if a wallet-insert literal is on a
    non-EVM-reachable path (then regenerate sqlc)
  - docs/non-evm-provider-discovery.md, docs/non-evm-audit.md
  - new: docs/tron-indexer-semantics.md, docs/tron-provider-decision.md
- Reference (read, do not modify): internal/provider/provider.go:242-288
  (TxProvider contract), an exemplar adapter (internal/provider/
  blockscout/ or chainscan/), internal/transaction/ indexer watermark
  code, migrations/000008 + 000019 + 000026, cmd/api/main.go:651-658.
- Do NOT touch: blockchain_networks rows or is_active, EVM adapters,
  tracing/scoring/screening/ingestion behavior, OFAC mapper TRX path
  (stays UNRESOLVED per §5.6), warmindex/queue.go, wattrFiles, OpenAPI
  Address pattern, transaction schema DDL.

### Phases — checkpoint after each, output "Phase N done: <evidence>"
1. SCOUT: trace EnsureIndexed→TxProvider→watermark flow end-to-end.
   Confirm the audit's file:line claims; report any that drifted.
2. PROVIDER EVAL: verify each TRON candidate against the 6 contract
   requirements in non-evm-provider-discovery.md using CURRENT vendor
   docs (cite URL + date). Live-probe free endpoints where no key is
   needed. Write docs/tron-provider-decision.md: chosen provider,
   verified quota/limit evidence, why others rejected. Update the
   candidate table UNCONFIRMED→VERIFIED/REJECTED per row. STOP and ask
   if no candidate meets requirements on a free no-card tier.
3. SEMANTICS DOC (docs/tron-indexer-semantics.md) defining, with
   evidence: (a) transfer_index scheme per stream — deterministic,
   stable across refetches, unique under UNIQUE(network_id, tx_hash,
   transfer_index); (b) cursor→watermark mapping — our contract is
   FromBlock/ToBlock + ascending order; TronGrid paginates by
   fingerprint + ms timestamps, so specify the block→timestamp bridge
   and its edge cases; (c) finality — solidified head ONLY (never
   unconfirmed latest); (d) Kinds mapping NATIVE↔TRX, TOKEN↔TRC-20.
4. address_type GENERALIZATION: thread the resolved network's
   AddressFormat into Service.GetOrCreate (currently stamps "evm" at
   service.go:25) and audit every 'evm' literal on wallet-insert paths;
   literals fed by EVM-only upstreams stay as-is, with a code comment
   explaining why. EVM stamped values MUST be byte-identical after.
5. ADAPTER: implement internal/provider/<chosen>/ satisfying TxProvider
   — Name/Enabled/LookupTransfers/HeadBlock. Enabled(net) MUST return
   false for tron unless explicitly configured (env key + chain list),
   matching chainscan/nodereal gating. Two streams (native TRX + TRC-20)
   map to TransferPage.Streams with per-stream Finished state and merged
   NextPageToken. Honor timeout/concurrency/Retry-After/backoff/circuit
   breaker and UpstreamCalls budget per AGENTS.md §7.
6. GATES + DOCS: gofmt -l . (empty), go vet ./..., go build ./...,
   go test ./... all clean; update docs/non-evm-audit.md marking what
   this change resolved vs what remains for §15; write final report.

### Acceptance Criteria
- [ ] tron rows still is_active=FALSE; /networks still returns bsc+eth only
- [ ] adapter emits zero upstream calls when Enabled()=false or network
      inactive (test proves it)
- [ ] refetching the same block window yields identical transfer_index
      sets (replay-safe, no dup inserts — test)
- [ ] ascending-order + per-stream Finished contracts hold in tests
- [ ] GetOrCreate stamps "tron" for tron-format networks and still
      stamps "evm" for EVM — both covered by tests
- [ ] every candidate row in discovery doc is VERIFIED-with-evidence or
      REJECTED-with-reason; no UNCONFIRMED left for TRON
- [ ] no new external dependency without asking; no secrets in code/commits

### Stop Conditions — pause and ask BEFORE
- flipping is_active or modifying blockchain_networks in any migration
- changing transactions/wallet_index_state DDL (block_number, tx_hash,
  transfer_index, indexed_until_block)
- wiring the adapter into any default provider selector
- creating tron.csv or touching wattrFiles
- generalizing the OpenAPI Address pattern
- any deletion of existing files or behavior change to EVM adapters
- paying for or embedding any API key

### Session Strategy
This is a multi-file task — read all Phase-0 files before writing code.
Use a subagent for vendor-doc verification so quotas stay out of main
context. Keep commits conventional + focused (feat(provider): … /
docs(spec): …), no AI references.
```

## Strategy

Scoped TRON-only after clarifying: TRON's 64-hex tx_hash + sequential
block_number map cleanly onto the existing contract, so the audit's
"schema semantics" blocker collapses to transfer_index identity +
solidified-finality + fingerprint→watermark bridge — the prompt forbids
DDL changes instead of inviting a speculative migration. Provider eval
is encoded as verify-then-decide (discovery doc rows all start
UNCONFIRMED), and stop conditions lock every §15/residual surface
(is_active, wattrFiles, OpenAPI pattern, OFAC TRX mapping) so
code-complete can never drift into enablement.

## Notes

Paste into a fresh Codex session at repo root. Agentic tool with real
system access — confirm paths and review stop conditions first. If Codex
asks for a TronGrid API key, provide via env (`TRONGRID_API_KEY`), never
paste into the prompt.
