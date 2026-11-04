# Stage 5 — E2E Verification (backend, no-UI adaptation)

**Date**: 2026-11-04
**Mode**: `SKIPPED-NO-UI` for browser automation — this product is a
backend-only Go REST service; there is no UI surface to drive. Per the
codex-vibe adaptation rule, E2E verification uses the real HTTP API flow
against live Postgres (127.0.0.1:5433 `check_wallet`) and Redis
(127.0.0.1:6380), with SQL assertions and latency checks.

## Environment

- API binary built from the working tree: `go build -o /tmp/checkwallet-api ./cmd/api`
- Server: `:8090` against dev DB + dev Redis (real OFAC snapshot: 1075
  wallets; real labels snapshot: 108,792 labels; live provider fan-out
  to free-tier GoPlus/PublicAML — no paid keys).
- Auth: `X-API-Key: dev-reader-key-change-me` (bootstrap reader role).

## Evidence — real calls against the running server

### E1 · OFAC-sanctioned ETH wallet (`0x252a8b…53bce`, real SDN listing)

`POST /api/v1/screenings` → **201** in 1.998 s (cold provider calls)

```
status: COMPLETED | score: 100 | level: CRITICAL
flags: ['DIRECT_SANCTIONS'] | matched: true
blocks: direct 100 | exposure 0 | other 20 (live provider score band)
```

→ OFAC match pins score at 100 with the independent `DIRECT_SANCTIONS`
flag; provider `other` block contributes up to cap but the flag is the
authoritative signal.

### E2 · SCAM-labeled wallet (`0x759e0f…b8f817`)

```
status: COMPLETED | score: 64 | level: HIGH | labels: 1
reasons: DIRECT_LABEL + 3×PROVIDER_OBSERVATION
```

### E3 · EXCHANGE-labeled wallet (`0x46ce2d…34cb9`) — classification ≠ crime

```
status: PARTIAL | score: 0 | level: LOW | labels: 1
reasons: LABEL_CLASSIFICATION (weight 0) + PROVIDER_FAILED
```

→ Exchange label contributes 0 (entity information only) AND a provider
failure produced `PARTIAL` — never converted to a clean result.

### E4 · Unknown wallet (`0x…dead`, not in any dataset)

```
status: PARTIAL | score: 0 | level: LOW | flags: [] | matched: false
sanctions completeness: AVAILABLE + usable
```

→ Non-match is honestly reported as `matched:false` with a LOW score —
not "proven safe"; degraded provider leg keeps status PARTIAL.

### E5–E7 · Idempotency end-to-end

| Check | Evidence |
|---|---|
| Same key + same payload | `Idempotent-Replay: true` header; body **byte-identical** to the 201 response |
| `GET /screenings/{id}` | **byte-identical** stored body (json column fidelity) |
| Same key + different payload | `409 IDEMPOTENCY_CONFLICT` with stable envelope + request_id |
| DB claim row | `screening_requests` shows `attached = true` |

### E8 · Sanctioned wallet + `include_sanctions:false`

```
status: COMPLETED | score: 80 | flags: [] | evaluated: false | matched: false
sanctions completeness: NOT_REQUESTED
```

→ Exclusion is explicit (`evaluated:false`, `NOT_REQUESTED`), never a
silent non-match; provider signals still score independently (80).

### E9 · Latency sample (5 distinct addresses)

`3.71 s / 0.14 s / 1.10 s / 0.16 s / 0.16 s` — all far under the 30 s
pipeline deadline; provider cache makes warm calls ~0.15 s.

### E10 · Error paths (stable codes + request_id envelope)

| Call | Result |
|---|---|
| No API key | `401 UNAUTHORIZED` |
| `network: solana` | `400 INVALID_NETWORK` |
| `address: notanaddress` | `400 INVALID_ADDRESS` |
| `options.max_hops: 5` | `400 BAD_REQUEST` |
| `GET` unknown uuid | `404 NOT_FOUND` |

## SQL verification

```sql
SELECT id, screening_status, risk_score, risk_level,
       scoring_policy_version FROM screening_results
ORDER BY screened_at DESC LIMIT 6;
-- COMPLETED 80 CRITICAL v1.0.0 | COMPLETED 64 HIGH v1.0.0
-- PARTIAL 0 LOW v1.0.0 (×2) | COMPLETED 64 HIGH | COMPLETED 100 CRITICAL
-- all fresh (<10 min old)

SELECT client_id, idempotency_key,
       screening_result_id IS NOT NULL AS attached
FROM screening_requests;
-- 2 | e2e-run-001 | t   (claim attached to its result row)
```

## Verdict: PASSED

Every §9.6/§9.7 behavior verified end-to-end on the real stack:
deterministic scoring, direct/indirect separation, honest PARTIAL on
provider failure, byte-identical replay + GET, claim state machine,
stable error codes, and latency inside budget. Server left running for
reference; binary at `/tmp/checkwallet-api`.
