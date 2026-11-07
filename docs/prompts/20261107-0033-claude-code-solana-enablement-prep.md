# Prompt: claude-code — solana-enablement-prep

> Created: 20261107-0033 | Target: claude-code | Template: M (agentic task brief, mirrored from `20261106-1740-codex-tron-enablement-prep.md`)

## Prompt

```text
You are implementing SOLANA enablement groundwork in the check-wallet repo
at /home/lethanhlam/Desktop/Profile/check-wallet (Go 1.27 module at
apps/api/, Gin + pgx + sqlc + golang-migrate). Risk class: R3 — external
provider integration + schema-adjacent semantics. Read AGENTS.md,
docs/non-evm-audit.md, docs/non-evm-provider-discovery.md,
docs/tron-indexer-semantics.md, docs/tron-provider-decision.md, and
~/.codex/rules/engineering-standard.md FIRST; they are binding.

## Context (carry forward)
- TRON enablement-prep is COMPLETE and is the pattern to mirror:
  plans/20261106-1946-tron-enablement-prep/, dormant adapter at
  internal/provider/trongrid/, decision docs under docs/tron-*.md.
  Reuse the phase skeleton; do NOT copy its open-problem set — Solana's
  differ (below).
- Groundwork already shipped for Solana — verify in Phase 1, do NOT re-do:
  NormalizeSolana (internal/wallet/address_solana.go — base58, 32-byte
  decode, verbatim); AddressFormatSolana (internal/network/
  address_format.go); solana row in blockchain_networks with
  is_active=FALSE (migration 000026); Service.GetOrCreate already stamps
  net.AddressFormat() and fails closed on unknown namespace
  (internal/wallet/service.go:29-35); OFAC mapper already maps SOL +
  solana-format → CHAIN_SPECIFIC (internal/provider/ofac/mapper_test.go:29).
- Unlike TRON, Solana does NOT map cleanly onto the EVM-shaped contract.
  Real open problems (from docs/non-evm-audit.md + discovery doc):
  (a) tx_hash = ~88-char base58 signature — fits VARCHAR(255) but is NOT
      hex; identity.go hash16 is content-derived SHA-256 → likely reusable
      verbatim, assert it;
  (b) block_number BIGINT must store SLOT — slot ≠ block ≠ wall-clock;
      ordering within a slot is not defined by slot alone;
  (c) getSignaturesForAddress paginates DESC by slot (before/until
      cursors, ≤1000/page) — the ascending-block watermark contract needs
      a signature-desc→slot-range bridge;
  (d) finality = `finalized` commitment ONLY (confirmed/processed fork);
  (e) SPL token transfers live in inner instructions / pre-post token
      balances; failed txs (meta.err) still charge fees — decide whether
      fee-only counts as transfer evidence; never fabricate;
  (f) free public RPC truncates history → history_truncated honesty;
  (g) Kinds mapping NATIVE↔lamports, TOKEN↔SPL mint (u64 raw strings).
- Dev DB: postgres://wallet:devonly@127.0.0.1:5433/check_wallet
  Test DB: postgres://wallet:devonly@127.0.0.1:5433/check_wallet_test
  Integration tests MUST use dedicated *_test databases only.

### Objective
Make Solana code-complete but still INACTIVE — "flip is_active and it
works": pick a verified free provider, implement a TxProvider adapter
that never emits traffic until configured AND the network is active,
resolve every open problem (a)-(g) into docs/solana-indexer-semantics.md,
and produce the decision docs a §15 reviewer needs to flip it later.

### Files & Scope
- Primary (may modify):
  - apps/api/internal/provider/<chosen>/ (new adapter + tests)
  - docs/non-evm-provider-discovery.md, docs/non-evm-audit.md
  - new: docs/solana-indexer-semantics.md, docs/solana-provider-decision.md
- Reference (read, do not modify): internal/provider/provider.go:242-288
  (TxProvider contract), internal/provider/trongrid/ (sibling dormant
  adapter — same gating shape), internal/transaction/ indexer watermark
  code, internal/transaction/identity.go, migrations 000008 + 000019 +
  000026, docs/tron-indexer-semantics.md (structure to mirror, NOT content).
- Do NOT touch: blockchain_networks rows or is_active; transactions /
  wallet_index_state DDL (block_number, tx_hash, transfer_index,
  indexed_until_block — slot semantics are DOCUMENTED, not DDL-changed);
  EVM or TRON adapters; trongrid; wallet GetOrCreate; OFAC mapper SOL
  path (already CHAIN_SPECIFIC); warmindex/* (queue is EVM-only —
  solana signatures/addresses must never enter; seed.go already skips
  non-EVM); wattrFiles; OpenAPI Address pattern; providerconfig
  descriptors; data_sources seed; provider selector wiring in cmd/*.

### Phases — checkpoint after each, output "Phase N done: <evidence>"
1. SCOUT/DRIFT CHECK: verify every Solana claim in non-evm-audit.md +
   discovery doc against code post-TRON-prep (GetOrCreate signature,
   OFAC SOL path, identity.go chain-agnosticism, warmindex EVM boundary).
   Report VERIFIED/DRIFTED per claim; write reports/audit-drift-check.md.
2. PROVIDER EVAL: verify each Solana candidate in the discovery table
   against the 6 contract requirements using CURRENT vendor docs (cite
   URL + date). Live-probe endpoints that need no key (e.g. public
   mainnet-beta JSON-RPC: getSignaturesForAddress page shape, slot
   ordering, finalized blockTime nullability). Write
   docs/solana-provider-decision.md: chosen provider, verified
   quota/limit evidence, why others rejected; flip every UNCONFIRMED row
   to VERIFIED/REJECTED. STOP and ask if the only viable candidates
   require a credit card; a free-with-API-key tier is a reportable
   option but choosing it needs user approval.
3. SEMANTICS DOC (docs/solana-indexer-semantics.md) — decide, with
   evidence, each of:
   (a) slot→block_number mapping + intra-slot ordering rule (two sigs in
       one slot need a deterministic tiebreak);
   (b) signature-desc pagination → ascending FromBlock/ToBlock bridge —
       cursor transform + edge cases (page straddles slot boundary,
       until/before semantics, >1000 sigs per window);
   (c) finality — finalized commitment ONLY;
   (d) transfer_index scheme per stream — reuse content-derived hash16
       only if it satisfies determinism + refetch-stability + the
       UNIQUE(network_id, tx_hash, transfer_index) invariant on base58
       input; document why;
   (e) SPL extraction path (inner instructions vs pre/postTokenBalances
       vs enhanced-tx API) — pick ONE authoritative source; failed-tx
       (meta.err) fee semantics — include or exclude, state why; vote /
       system-program noise — filtered or counted, honestly;
   (f) archival truncation → history_truncated/coverage signal channel;
   (g) Kinds mapping NATIVE↔lamports SOL, TOKEN↔SPL (mint = asset id,
       amount_raw = u64 string).
   For each not-honestly-decidable item use the named-non-option pattern
   from docs/tron-indexer-semantics.md — defer-unfinished is the only
   honest no-resolution path.
4. ADAPTER: implement internal/provider/<chosen>/ satisfying TxProvider —
   Name/Enabled/LookupTransfers/HeadBlock. Enabled(net) MUST return false
   for solana unless explicitly configured (env key + chain list),
   matching trongrid/chainscan gating. Two streams (native SOL + SPL) map
   to TransferPage.Streams with per-stream Finished and merged
   NextPageToken. Honor timeout/concurrency/Retry-After/backoff/circuit
   breaker and UpstreamCalls budget per AGENTS.md §7. Parsed/enhanced API
   shapes must be proven equivalent to raw tx evidence before use —
   never emit evidence the raw chain cannot reproduce.
5. GATES + DOCS: gofmt -l . (empty), go vet ./..., go build ./...,
   go test ./... all clean; update docs/non-evm-audit.md marking what
   this change resolved vs what remains for §15; write final report.

### Acceptance Criteria
- [ ] solana row still is_active=FALSE; /networks still returns
      bsc+eth only
- [ ] adapter emits zero upstream calls when Enabled()=false or the
      network is inactive (httptest counter test proves it)
- [ ] refetching the same slot window yields identical transfer_index
      sets (replay-safe, no dup inserts — test)
- [ ] ascending-order + per-stream Finished contracts hold in tests,
      incl. a multi-page signature window straddling a slot boundary
- [ ] no fabricated slot/block_time — rows lacking finalized evidence
      are never emitted (named non-option)
- [ ] every discovery-doc Solana row is VERIFIED-with-evidence or
      REJECTED-with-reason; zero UNCONFIRMED left
- [ ] semantics doc resolves (a)-(g) or explicitly defers with named
      non-options — no silent gaps
- [ ] no new external dependency without asking; no secrets or keys in
      code, tests, or commits

### Stop Conditions — pause and ask BEFORE
- flipping is_active or modifying blockchain_networks in any migration
- changing transactions/wallet_index_state DDL (block_number, tx_hash,
  transfer_index, indexed_until_block)
- wiring the adapter into any default provider selector, providerconfig
  descriptor, or data_sources seed
- creating solana.csv or touching wattrFiles
- generalizing the OpenAPI Address pattern
- choosing a provider that requires a credit card or paid tier
- editing warmindex (queue stays EVM-only; solana must never enter)
- any deletion of existing files or behavior change to EVM/TRON adapters
- paying for or embedding any API key

### Session Strategy
This is a multi-file task — read all Phase-0 files before writing code.
Use a subagent for vendor-doc verification + live RPC probes so quotas
and payloads stay out of main context. Keep commits conventional +
focused (feat(provider): … / docs(spec): …), no AI references.
```

## Strategy

Mirrored the proven TRON-prep Template-M skeleton (audit-drift → provider
eval → semantics doc → dormant adapter → gates) but re-derived the
open-problem set for Solana per P-069: slot≠block watermark semantics,
signature-desc pagination bridge, finalized-only commitment, SPL inner-
instruction extraction, failed-tx fee honesty, archival truncation —
instead of TRON's fingerprint/ms-cursor + block-less TRC-20 problems.
Dropped TRON's address_type phase (already generalized via
`net.AddressFormat()`) and OFAC-path stop (SOL already CHAIN_SPECIFIC);
added warmindex EVM-only boundary since the warm seed now exists and
must never admit base58 input. Provider eval keeps the free-tier gate
but splits the no-key vs keyed-free decision: keyed-free (Helius) is
reportable, card-required is a STOP.

## Notes

Paste into a fresh Claude Code session at repo root. Agentic tool with
real system access — review scope locks + stop conditions before
pasting; confirm paths match. If a provider needs an API key (e.g.
Helius free tier), supply it via env var — never paste into the prompt.
