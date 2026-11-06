# TRON TxProvider Decision

Status: **decided — TronGrid** (dormant adapter target; still §15-gated
for enablement). Verified 2026-11-06 via live keyless probes + current
vendor docs. Every claim cites a URL with retrieval date or a verbatim
probe result.

## Decision

| # | Requirement | TronGrid verdict | Evidence |
|---|---|---|---|
| 1 | Per-address native + token history | PASS | `GET /v1/accounts/{a}/transactions` (native, `blockNumber` present) and `/v1/accounts/{a}/transactions/trc20` (TRC-20 events, **no block number** — tri-problem handled in `docs/tron-indexer-semantics.md`) — live keyless 200 on both, 2026-11-06 |
| 2 | Deterministic ordering for watermarks | PASS | `order_by=block_timestamp,asc` honored on BOTH streams (probe: native ts 1555400628000<1555401072000<1555457175000; trc20 ts 1559473476000<1563125823000<1563179460000). Block ordering within a stream follows block_timestamp ordering; per-row block number resolvable (see b2) |
| 3 | Typed finalized head | PASS | `GET /walletsolidity/getnowblock` → `block_header.raw_data.number` = 87765540, ts 1793970309000 (solidified) — live keyless, 2026-11-06 |
| 4 | PageToken-compatible pagination | PASS | `meta.fingerprint` + `meta.links.next` on both streams; absent when the stream is exhausted (empty page: `data:[], success:true, meta{at,page_size:0}` — no fingerprint, no next). `limit≤200`; `limit≥201` → HTTP 400 `{"success":false,"error":"A valid limit by parameter is required."}` — live probes, 2026-11-06 |
| 5 | Free tier, no card, quota fits calls-per-ensure | PASS (keyed) / dev-only (keyless) | Free Basic: **100,000 req/day, 3 keys, 15 RPS/key**; signup = email activation only, no card. Keyless: **3 RPS shared per-IP**, 429 + 5s suspension, no `Retry-After`. Sources below. Calls-per-ensure model below |
| 6 | Terms permit our use case | PASS (with caveat) | ToS (https://www.trongrid.io/terms-of-service, retrieved 2026-11-06): prohibits reselling/sublicensing API access and scraping website content; no commercial-use ban located. Our use — internal screening API, not resale — is consistent. §15 reviewer should re-read the ToS at enablement; this is our interpretation, not a legal determination |

### Sources (retrieved 2026-11-06)

- Rate limits / keyless behavior: https://developers.tron.network/reference/rate-limits
  (officially: "Production requests should include `TRON-PRO-API-KEY`",
  no-key traffic "strict rate limiting or rejection"), plus measured
  keyless 3 RPS + 429/5s-suspension behavior per
  https://boltrpc.io/blog/trongrid-rate-limits (measured 2026-10-24).
- Free-plan quotas: https://trongrid.me/price/ ("Basic — Free:
  100,000 Requests/Day, 3 API Keys") and BoltRPC's fetch of TronGrid's
  plan-config JSON (15 RPS/key, 100K/day shared across the account's keys).
- Signup flow (email only, no card):
  https://trongrid.zendesk.com/hc/en-us/articles/900007033343-Get-started
- Endpoint docs: https://developers.tron.network/reference/trongrid-v1-api-overview
- **Plan-flux caveat:** TronGrid's v1.12.0 note (Oct 2026) announces paid
  plans and a "new Free plan of 10,000 req/day" for new accounts while
  existing Basic accounts keep 100K/day (BoltRPC, 2026-10-24). Quota
  figures MUST be re-read from the TronGrid dashboard at enablement —
  do not hard-code 100K/day into sizing.

### Calls-per-ensure worst case (RA-5 arithmetic)

Under the Phase-3 design (`docs/tron-indexer-semantics.md`), one
`EnsureIndexed` costs at most:

```
1  head            walletsolidity/getnowblock
2  bridge          walletsolidity/getblockbynum(From) + getblockbynum(To)
                   (first step only — resolved ms window rides the token)
M × (2 pages       /v1/accounts/.../transactions + /transactions/trc20
   + C fan-out)    walletsolidity/gettransactioninfobyid per unique
                   trc20 txID unresolved by the anchor map, C = trc20
                   page-size clamp
```

With `MaxPagesHop0 = 5` (`internal/transaction/service.go:49-57`) and
trc20 clamp `C = 50` (chosen in Phase 3): worst case = `1 + 2 + 5×52 =
`**263 calls/ensure**`; HopN (2 pages) = 107; Hop3 (1 page) = 55.

- Keyed (100K/day shared across 3 keys): ~380 worst-case hop-0 ensures
  per day — fits MVP lazy-index volumes with margin; typical ensures are
  far cheaper (1–2 steps, few trc20 rows).
- Keyless (3 RPS, shared per-IP): ~88 s saturated per worst-case ensure
  on a quota shared with every keyless caller on the IP — **unfit for
  sustained indexing; dev fallback only**.
- The 100K/day figure is the current Basic plan; at the reported 10K/day
  new-Free-plan level the model still fits (~38 worst-case ensures/day)
  but with far less headroom — another reason quota must be re-read at
  enablement.
- Per-request budget: `Budget` (max_provider_requests) charges
  `UpstreamCalls` post-hoc (`service.go:339-344`) — a tron Budget
  config must tolerate ~60 calls in a single step; the clamp is the
  only bound.

## Rejected candidates

| Candidate | Failing req | Reason (verified 2026-11-06) |
|---|---|---|
| **Tatum** | 5 | Free plan = **100,000 lifetime credits** (not monthly) at 3 RPS (https://docs.tatum.io/docs/plans-limits; "100K Lifetime credits" per https://tatum.io/start-for-free). TRON account history = `GET /v3/tron/transaction/account/{address}` at **5 credits/call**, ≤200 rows, `next`-fingerprint + `min/maxTimestamp` + `order=block_timestamp,asc` (https://docs.tatum.io/reference/tronaccounttx) — a TronGrid-shaped mirror carrying the same missing-block-number gap, with **zero additional value**. 100K lifetime credits = 20K calls ever ≈ 76 worst-case ensures before the account is dead. Fails the calls-per-ensure model outright |
| **Ankr** | 1, 5 | Advanced API chain list (https://www.ankr.com/docs/advanced-api/overview/, retrieved 2026-11-06): 16 EVM chains + Stellar namespace — **TRON absent**. Free TRON access is standard JSON-RPC only (https://www.ankr.com/web3-api/chains-list/tron/), which cannot enumerate per-address TRC-20 history. Freemium AAPI tier exists (30 req/min) but does not cover TRON — even paid AAPI cannot serve this use case |
| **Covalent / GoldRush** | 1 | Chain list (https://goldrush.dev/chains/, retrieved 2026-11-06): 47 mainnets — zero TRON entries. Requirement 1 fails at the chain-support level; quotas irrelevant |

## Out-of-table exclusions (named, per RA-10-adjacent)

| Candidate | Why not evaluated as a TxProvider |
|---|---|
| **Tronscan** | Explorer UI; its API surface is undocumented/unofficial and its terms prohibit automated extraction — no free API contract to build on |
| **GetBlock / Chainstack / QuickNode / dRPC** | Paid node-RPC providers; none offers a free per-address TRC-20 history index endpoint |
| **Public TRON full-node APIs** (grpc/HTTP `wallet/*`) | Node endpoints expose broadcast/state reads, not the indexed `/v1/accounts` history API — cannot enumerate transfers per address |

## `Enabled()` recommendation for Phase 5

**Key-required**: `Enabled(net) = apiKey != "" && chains[lowercase(net.Code)]`
— matching chainscan (`internal/provider/chainscan/adapter.go:150-152`)
and nodereal (`internal/provider/nodereal/adapter.go:144-145`).

Rationale: keyless 3-RPS shared quota is measured-unfit for sustained
indexing (dev fallback only); requiring a key makes the dormant adapter
physically incapable of emitting traffic until a key is provisioned —
defense-in-depth for a §15-gated package. Layering note: `network.Network`
carries no `IsActive` (`internal/network/resolver.go:12-18`) — activeness
is the resolver's gate, not the adapter's.
