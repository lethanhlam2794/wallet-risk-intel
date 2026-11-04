# Prompt: claude-code — bsc-tx-providers

> Created: 20261104-1050 | Target: claude-code | Template: M (agentic multi-step)

## Prompt

````text
## Context (carry forward)

You are working in /home/lethanhlam/Desktop/Profile/check-wallet — a Go +
PostgreSQL modular monolith for blockchain wallet risk intelligence. Governing
rules: AGENTS.md at repo root (read it first); spec docs/tailieu.md.

Established facts — do not re-derive:

- The transaction-history contract is `provider.TxProvider` in
  internal/provider/provider.go: `Name()`, `Enabled(net)`,
  `HeadBlock(ctx, net) (int64, error)`,
  `LookupTransfers(ctx, net, normalized, opts) (*TransferPage, error)`.
- `TransferPage` ordering contract: transfers within one stream MUST be in
  ascending block order; `NextPageToken == ""` means all streams finished;
  `Streams` reports per-stream `Finished`; `UpstreamCalls` MUST count real
  outbound requests (charged against a request-global budget).
- `provider.Transfer`: TxHash lowercase; AmountRaw raw integer decimal string
  (never float); Stream identifies the adapter stream for content-derived
  transfer_index identity; Asset "" for NATIVE.
- Typed failures use `provider.Errorf(provider.Err<Kind>, ...)`: rate_limited,
  timeout, upstream_5xx, client_4xx, malformed, circuit_open, disabled,
  degraded, transport, internal. Provider failure MUST NEVER become a clean
  result — disabled ≠ failed ≠ ok.
- Adapters are constructed as `New(baseURL, apiKey, opts...)` with a hardcoded
  SSRF `allowedHosts` (https only; `WithInsecureHTTP` + `WithAllowedHosts`
  exist for tests) and a resilience.NewClient (Rate, Burst, Concurrency,
  MaxRetries, Backoff, BreakerFailures, BreakerOpen, MaxBodyBytes,
  MaxLimiterWait).
- Reference implementations: internal/provider/nodereal/ (JSON-RPC) and
  internal/provider/chainscan/ (REST GET). Mimic their structure.
- transaction.Service holds `adapters map[string]provider.TxProvider` — ONE
  adapter per net.Code. wallet_index_state has source_id provenance and a
  provider-switch reset already exists: switching adapter resets coverage
  honestly. Keep single-adapter-per-network; add config env selection
  (e.g. TX_PROVIDER_BSC, TX_PROVIDER_ETH) defaulting to the CURRENT adapter
  so nothing changes when unset. Do NOT build automatic failover — out of
  scope.
- New sources need a `data_sources` row: follow
  migrations/000008_transactions.up.sql (INSERT in up, DELETE in down).
- cmd/api/main.go wires adapters + `Budget` map keyed by adapter Name().
- REAL-WORLD LESSON, just hit yesterday: nodereal sent `params` as a JSON
  object; the live upstream requires a positional array and returned
  -32700 parse error. Mock tests passed while production always failed.
  Therefore: mocks MUST parse the exact wire shape the real provider
  expects, and a provider is NOT done until verified against the live
  upstream with a real key.
- Never commit .env, API keys, or secrets. Env vars go in .env.example as
  commented placeholders. API keys arrive via env vars — never hardcode.

## Task

Implement eleven new TxProvider adapters. LIST ORDER = failover priority
order for PHASE 2 — later entries are lower-priority fallbacks. ONE
provider per commit with a stop-and-report checkpoint after each:

Tier 1 — proven contract shape:

1. alchemy  — alchemy_getAssetTransfers (JSON-RPC; near-identical to
   nr_getAssetTransfers). Chains: bsc + ethereum.
2. blockscout — Blockscout v2 REST for ETHEREUM ONLY
   (eth.blockscout.com, keyless). No block-range params upstream:
   paginate transactions/token-transfers and filter to the requested
   window client-side, stopping at the watermark. Enabled() = true for
   ethereum without a key — model gating after chainscan's
   enabled-chains list, not after key presence.
3. ankr     — Ankr Advanced API address-transfer method (JSON-RPC multichain).
   Verify the exact method name/params against current Ankr docs before
   coding; do not guess.
4. covalent — GoldRush REST `transactions_v3` for BSC.
5. moralis  — REST wallet token/native transfer endpoints for BSC.
6. tatum    — REST address transaction endpoints for BSC.

Tier 2 — VERIFY-FIRST candidates (unproven for our contract; last in
priority). For each, FIRST confirm against official docs + one live
probe call that the free endpoint can deliver BOTH native and token
transfers for a BSC address with pagination. If it cannot, do NOT build
the adapter — write `NOT-VIABLE: <reason>` in the report and move on:

7. bitquery — Bitquery v2 GraphQL address transactions for BSC.
8. oklink   — OKLink REST address transaction history for BSC.
9. blockchair — Blockchair address endpoints for BNB Chain (confirm BSC
   coverage exists before coding).
10. tokenview — Tokenview address transaction endpoints, ETH + BSC;
    free tier is small (~300 req/day) so it is a last-resort fallback.
11. chainbase — Chainbase data API address transactions, ETH + BSC;
    verify the exact free-tier endpoint shape before coding.

OUT OF SCOPE (do not implement): Helius (Solana-only), TronGrid
(TRON-only) — this system has no networks for them; they are not
"unstable" candidates, they serve chains we do not support. Thirdweb
Insight, Envio HyperSync, SubQuery — infrastructure/indexer platforms,
not drop-in APIs, and they share the native-transfer coverage gap.
Routescan — no ETH/BSC mainnet instances. BscTrace — same NodeReal
account already configured.

PHASE 2 — quota-sharing failover (run ONLY after all adapter commits
are reviewed, and STOP for design sign-off before starting):

Goal: spread provider quota across free sources — when the primary
adapter for a network fails with rate_limited / upstream_5xx / degraded /
quota-exhausted, the SAME request falls through to the next configured
adapter.

Design constraints (non-negotiable):
- Change `adapters map[string]provider.TxProvider` to an ORDERED chain
  `map[string][]provider.TxProvider`; order comes from config
  (`TX_PROVIDER_BSC=nodereal,alchemy` — default stays the single current
  adapter, so behavior is unchanged when unset).
- Failover triggers ONLY on provider-level failure kinds (rate_limited,
  upstream_5xx, degraded, transport, circuit_open) — NEVER on successful
  empty pages, client_4xx, or malformed (those are real answers or real
  bugs, not quota problems).
- Each adapter keeps its OWN source_id and therefore its OWN
  wallet_index_state watermark — the existing provider-switch reset
  already guarantees honesty: a secondary provider indexes from its own
  coverage boundary, never trusting the primary's watermark. Do NOT share
  watermarks across providers; different upstreams have different
  transfer identities and page semantics.
- Budget accounting stays per-adapter (map keyed by Name()); failover
  must charge the real upstream calls of whichever adapter served.
- Tests must cover: failover ordering, per-source watermark isolation,
  primary-healthy ⇒ no secondary traffic, all-failed ⇒ honest
  failed/degraded status (never clean), disabled adapter skipped in
  chain order.

For EVERY provider, first verify the CURRENT upstream contract from that
provider's official docs (endpoint URL, auth, method name, request shape,
pagination token, response fields, rate limits, which block range caps
apply). If docs and code assumptions conflict, docs win — surface the
conflict in your report.

## Per-provider recipe (repeat for each)

1. `internal/provider/<name>/adapter.go` — implements TxProvider; streams
   must be partitioned so Finished semantics map to per-direction coverage
   like nodereal's from|to.
2. `internal/provider/<name>/mapper.go` — upstream row → provider.Transfer
   normalization; keep provider quirks inside the adapter.
3. `internal/config/config.go` — `<NAME>_API_KEY`, `<NAME>_BASE_URL`,
   enabled-chains/default; Enabled() = false without key → zero traffic.
4. New migration `data_sources` row (up INSERT, down DELETE).
5. `cmd/api/main.go` — construct adapter, register in the per-network
   selection map behind the new env selector (default unchanged), add
   Budget entry.
6. `.env.example` — commented placeholder.
7. Unit tests beside source: fixtures where the fake server parses the
   REAL wire shape (positional params if the upstream requires them),
   malformed payload, 429→rate_limited, timeout, disabled-without-key,
   pagination token continuation.
8. Gates: `gofmt -l .` empty, `go vet ./...` clean, `go build ./...` clean,
   `go test ./...` green.
9. LIVE VERIFY (mandatory when the key exists in env): call HeadBlock +
   LookupTransfers against the real upstream for a real address, then call
   `GET /api/v1/wallets/<net>/<addr>/transactions?refresh=1` on a booted API
   and show `provider_status:"ok"` with real rows. If the key is not
   configured, state `live-verify: BLOCKED (no key)` — never claim live
   verification you did not run.
10. Commit: `feat(provider): add <name> transaction-history adapter` —
    one provider per commit.

## Stop conditions (mandatory)

- After each provider commit: output a status table (provider / files /
  tests / gates / live-verify evidence or BLOCKED) and STOP for review
  before starting the next.
- STOP and ask before: changing the TxProvider interface, watermark/
  stream semantics, Budget semantics, any migration other than a new
  data_sources row, touching tracing/scoring/screening code, committing
  anything under .env, or any destructive operation (rm -rf, force push,
  DB drops). The single-adapter→ordered-chain change is PHASE 2 only and
  requires the design sign-off above.

## Forbidden

- No paid-only endpoints or providers. No secrets/keys in code, logs,
  tests, or commits. No relaxing
  invariants: disabled ≠ failed, unknown ≠ safe, honest coverage
  (history_truncated stays truthful). No mock that distorts the real
  upstream contract. Only make changes directly requested — do not
  refactor existing adapters beyond what the new env selection requires.

## Definition of done (per provider)

All recipe steps pass, one conventional commit exists, the status report
was produced, and either live-verify evidence is shown or live-verify is
explicitly marked BLOCKED (no key). Work is not done until these hold.
````

## Strategy

Agentic sequential-gates prompt: eleven adapters are too much for one blind
run, so each provider is a self-contained recipe with a commit + hard stop.
The -32700 drift lesson is encoded as both a mock-contract rule and a
mandatory live-verify gate. Quota-sharing failover is scoped as PHASE 2
with explicit design constraints and a sign-off checkpoint — it changes
transaction.Service internals so it must not ride along silently inside
adapter commits.

## Notes

Setup: obtain API keys for each provider and export them (or put in .env —
godotenv loads it) BEFORE the live-verify step of each provider. Blockscout
needs no key. Without keys, expect `live-verify: BLOCKED` reports per
provider.
