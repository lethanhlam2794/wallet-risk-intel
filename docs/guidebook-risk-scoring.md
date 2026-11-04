# Guidebook — Risk Scoring & Screening API (Spec Phase 5)

A step-by-step operational guide to the synchronous screening endpoint
and the deterministic v1 scoring engine shipped by
`plans/20261103-2318-risk-scoring/` (4 review rounds → APPROVED).
Every response below is a **real capture** from the dev environment —
live Postgres (`check_wallet`) with a real OFAC snapshot (1,075 listed
wallets), the wallet-attribution snapshot (108,792 labels), and live
free-tier provider fan-out.

Raw captures: `docs/feature-maps/assets/screening-*.json`.

---

## 1. Prerequisites

```bash
docker compose up -d            # postgres :5433, redis :6380
go run ./cmd/migrate up         # schema at version 7 (screenings)
DATABASE_URL=postgres://wallet:devonly@127.0.0.1:5433/check_wallet?sslmode=disable \
REDIS_URL=redis://127.0.0.1:6380/0 HTTP_ADDR=:18080 \
  go run ./cmd/api &
```

Dev bootstrap keys: `dev-reader-key-change-me`, `dev-admin-key-change-me`.

## 2. The Endpoints

```
POST /api/v1/screenings            synchronous run → 201 + full result
GET  /api/v1/screenings/{id}       stored result body, byte-identical
```

`POST` accepts an `Idempotency-Key` header (`[A-Za-z0-9._~-]{1,255}`).
Same key + same canonical request replays the stored result
(`Idempotent-Replay: true`, HTTP 200); same key + different payload →
`409 IDEMPOTENCY_CONFLICT`. A claim whose owner died (>5 min, no result)
is takeover-eligible; a live in-flight claim makes followers wait ≤30 s
then answers `409 IDEMPOTENCY_IN_FLIGHT`.

Request body:

```json
{"network": "ethereum",
 "address": "0x252a8bd2319d8a555b872990601221b3a2053bce",
 "options": {"include_sanctions": true, "include_labels": true,
             "include_exposure": true, "max_hops": 2,
             "refresh_providers": false}}
```

All options default on; `max_hops` must be 0–2 (`400` otherwise).

## 3. Happy path — OFAC-sanctioned wallet

```bash
curl -s -X POST http://127.0.0.1:8090/api/v1/screenings \
  -H "X-API-Key: dev-reader-key-change-me" \
  -H "Content-Type: application/json" \
  -d '{"network":"ethereum","address":"0x252a8bd2319d8a555b872990601221b3a2053bce"}'
```

`screening-sanctioned.json` (abridged):

```json
{"data": {"status": "COMPLETED", "risk_score": 100, "risk_level": "CRITICAL",
  "direct_sanctions": {"evaluated": true, "matched": true,
    "entries": [{"entity_name": "…", "match_scope": "CHAIN_SPECIFIC"}]},
  "scoring": {"policy_version": "v1.0.0", "score": 100,
    "flags": ["DIRECT_SANCTIONS"],
    "blocks": {"direct": {"subtotal": 100}, "exposure": {"subtotal": 0},
               "other": {"subtotal": 20}},
    "reasons": [{"code": "DIRECT_SANCTIONS_MATCH", "weight": 100}]}}}
```

An OFAC match scores flat **100** — verification/confidence factors do
not discount official listings — and sets the `DIRECT_SANCTIONS` flag
independently of the number.

## 4. Label-driven score — SCAM wallet

`screening-scam-label.json`: `score: 64, level: HIGH` —
`DIRECT_LABEL` (SCAM, reported → `85 × 0.7 ≈ 60`) is the direct max;
provider observations add the residual.

## 5. Classification ≠ crime — EXCHANGE wallet

`screening-exchange-classification.json`: `status: PARTIAL, score: 0,
level: LOW`, reason `LABEL_CLASSIFICATION` (weight 0) + `PROVIDER_FAILED`.
Two invariants in one response: exchange labels carry no weight, and a
provider failure produces `PARTIAL`, never a clean pass.

## 6. Unknown ≠ safe — wallet in no dataset

`screening-unknown-partial.json`: `score: 0, level: LOW,
matched: false`, sanctions `AVAILABLE + usable`. When **no** dimension is
usable, the contract is stronger still: `status: INSUFFICIENT_DATA`,
`risk_score: null`, `risk_level: "UNKNOWN"` — and all block subtotals
stay 0 (a nonzero subtotal beside a NULL score can never leak; review
M3).

## 7. Idempotency end-to-end

```bash
curl -X POST …/screenings -H "Idempotency-Key: run-001" -d '{…}'   # 201
curl -X POST …/screenings -H "Idempotency-Key: run-001" -d '{…}'   # 200 replay
```

Second call returns `Idempotent-Replay: true` and a **byte-identical**
body (the `result` column is `json`, not `jsonb` — replay fidelity is
literal). `GET /api/v1/screenings/{id}` returns the same bytes again.
Same key + different payload → `409 IDEMPOTENCY_CONFLICT`.

## 8. Reading `provider_status` / `data_completeness`

- `providers.{source}`: verbatim per-source status — `OK`, typed
  failures (`TIMEOUT`, `UPSTREAM_5XX`, `RATE_LIMITED`, `CIRCUIT_OPEN`,
  `MALFORMED`, `TRANSPORT`), or `DISABLED` (configuration — never counted
  as a failure in the fold).
- `provider_status.fanout`: wholesale enrichment-pipeline failure
  (named field, never a pseudo-provider key).
- `sanctions`/`labels`: snapshot `data_status` — `AVAILABLE`, `STALE`
  (usable but degraded → `PARTIAL` + `SNAPSHOT_STALE` reason),
  `UNAVAILABLE`, `NOT_REQUESTED`.
- `data_completeness.providers.{usable,degraded,disabled}` and per-
  dimension `usable` flags explain the status fold:
  `0 usable → INSUFFICIENT_DATA`; `usable + degraded → PARTIAL`; else
  `COMPLETED`.

## 9. SQL verification

```sql
SELECT id, screening_status, risk_score, risk_level, scoring_policy_version
FROM screening_results ORDER BY screened_at DESC;
-- FAILED rows appear here as audit artifacts when a post-resolution
-- pipeline error occurred (they are never claim-attached — retries work).

SELECT idempotency_key, screening_result_id IS NOT NULL AS attached
FROM screening_requests;          -- claim → result linkage

SELECT claim_stale FROM (
  SELECT claimed_at < NOW() - INTERVAL '5 minutes' AS claim_stale
  FROM screening_requests) t;      -- DB-computed staleness
```

## 10. Determinism & policy versioning

Same inputs + `scoring_policy_version = v1.0.0` ⇒ byte-identical verdict
(engine sorts findings, dedups by `{kind}:{category}:{source}:{ref}`
keeping the **strongest** evidence — verified > reported > unverified >
disputed, then confidence, then highest provider score). Scores are
heuristics, not probabilities; `risk_score` may be NULL under
`INSUFFICIENT_DATA`; the direct-sanctions flag never depends on the
number.
