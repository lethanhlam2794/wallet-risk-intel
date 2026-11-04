# Prompt: claude-code — non-evm-expansion

> Created: 20261104-1055 | Target: claude-code | Template: M (agentic multi-step)

## Prompt

````text
## Context (carry forward)

You are working in /home/lethanhlam/Desktop/Profile/check-wallet — a Go +
PostgreSQL modular monolith for blockchain wallet risk intelligence. Governing
rules: AGENTS.md at repo root (read it FIRST). Section 15: adding a chain not
yet supported requires review sign-off — this task is GROUNDWORK ONLY, not
enablement.

Established facts — do not re-derive:

- The system is currently EVM-only. Networks: ethereum, bsc in
  blockchain_networks. Address pipeline: `wallet.NormalizeEVM` (validated
  20-byte hex, lowercase for indexing, original representation preserved).
- `provider.TxProvider` assumes block-number semantics: HeadBlock,
  FromBlock/ToBlock windows, per-stream pagination, NATIVE/TOKEN transfer
  kinds, tx_hash hex strings.
- `wallet_index_state` coverage watermarks are block numbers per
  (network, wallet, source). Sanctions/labels/tracing/scoring all key off
  network_id + normalized_address.
- The end-goal is adding Solana and TRON. No suitable free provider for
  either chain has been confirmed yet — the provider integration slot is
  explicitly PENDING DISCOVERY. Do not write adapter code.
- OFAC SDN does publish non-EVM addresses (TRX, and SOL where listed) —
  note this as a finding for future ingestion mapping; do not change
  ingestion code in this task.
- Never commit .env, keys, or secrets. Conventional Commits.

## Task — expansion groundwork only

1. Introduce an address-format abstraction:
   - `AddressFormat` type on the network model: `evm | solana | tron`.
   - Validators as pure functions + property-based tests:
     * solana: base58 string, decoded length exactly 32 bytes (ed25519
       pubkey), 32–44 chars.
     * tron: base58check, 'T' prefix, decoded 21 bytes (0x41 + 20-byte
       payload), checksum verified.
   - `wallet.Normalize` dispatches on the network's AddressFormat:
     EVM keeps current lowercase-normalized behavior; solana/tron keep
     the base58 representation AS-IS (case-sensitive alphabet — never
     lowercase), original preserved.
   - Invalid addresses must be rejected with the existing error shape,
     not silently normalized.
2. Migration: add solana + tron rows to `blockchain_networks` marked
   INACTIVE (use whatever active/enabled flag the schema already has —
   check the column set first; if none exists, do not invent one — report
   it). Resolver/network registry must continue to REJECT solana/tron
   until explicitly enabled — zero behavior change for existing
   ethereum/bsc paths.
3. Provider slot — PENDING DISCOVERY. Write
   `docs/non-evm-provider-discovery.md` containing:
   - The contract requirements a provider MUST meet to become a
     TxProvider for these chains: per-address transfer history covering
     native + token (SPL / TRC-20) with pagination, deterministic
     ordering usable for coverage watermarks (block or slot), a typed
     head/slot endpoint, and a free tier without a credit card.
   - Candidate list to evaluate LATER (all UNCONFIRMED — do not code):
     Helius Enhanced Transactions (Solana), TronGrid (TRON), Tatum
     multi-chain, Ankr multi-chain, Covalent multi-chain.
   - Per-candidate evaluation table: free quota, endpoint shape, native
     coverage, token coverage, pagination semantics, block-range limits,
     known quirks.
4. Compatibility audit — write `docs/non-evm-audit.md` listing concrete
   blockers per module, with file references:
   - transactions schema: tx_hash formats (solana signature base58 vs
     tron hex), block_number vs Solana slots, transfer_index identity.
   - tracing: BFS assumes EVM counterparties and shared address space —
     cross-chain correlation rules for disjoint alphabets.
   - sanctions ingestion: OFAC currency fields for TRX/SOL — mapping
     gaps.
   - scoring/screening: any hardcoded EVM assumptions.
   - OpenAPI: networks enum — add solana/tron marked unavailable with an
     example, matching existing spec style.
   This is a findings document — do NOT change tracing/scoring/
   screening/ingestion code.
5. Gates: `gofmt -l .` empty, `go vet ./...` clean, `go build ./...`
   clean, `go test ./...` green.
6. Commit: `feat(network): groundwork for solana and tron chain support`
   plus a separate `docs(spec): non-evm expansion audit and provider
   discovery` for the two markdown files.

## Stop conditions (mandatory)

- STOP and report before: enabling solana/tron for real traffic, writing
  any TxProvider adapter code, changing the transactions schema or
  indexes, modifying tracing/scoring/screening/ingestion behavior,
  altering EVM paths, adding paid providers, or inventing a schema flag
  not already present. All of those require the AGENTS.md §15 review —
  your job ends at groundwork + audit + checkpoint report.
- STOP and ask before any destructive operation.

## Forbidden

- No adapter code, no live traffic paths for new chains, no secrets/keys,
  no paid sources, no behavior changes to existing EVM flows, no
  speculative schema redesigns beyond the network rows + AddressFormat
  abstraction. Only make changes directly requested — do not refactor
  beyond what groundwork requires.

## Definition of done

AddressFormat + validators + tests pass; migration applies cleanly up and
down; solana/tron are rejected by the resolver (proven by a test or a
booted-API call showing the honest rejection); both markdown docs exist
with concrete file references; gates green; two commits exist; final
report lists what remains blocked pending §15 review and provider
discovery.
````

## Strategy

Groundwork-only expansion prompt: user explicitly wants Solana/TRON prep
with the provider slot marked pending-discovery, so the prompt scopes to
address-format abstraction + inactive network rows + audit docs, with
hard stops before any enablement — matching AGENTS.md §15 review
requirement instead of smuggling chain support into a code drop.

## Notes

No API keys needed — this task touches no upstream provider. The two
output docs (`non-evm-provider-discovery.md`, `non-evm-audit.md`) become
the input for the follow-up review before real chain enablement.
