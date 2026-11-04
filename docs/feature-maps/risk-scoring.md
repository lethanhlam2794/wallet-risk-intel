# Feature Map — Risk Scoring & Screening API (Phase 5)

## Endpoints

| Method | Path | Purpose | Handler |
|---|---|---|---|
| POST | `/api/v1/screenings` | Synchronous screening → `201` + §9.7 result; `Idempotency-Key` header | `screening.Handler.Create` |
| GET | `/api/v1/screenings/{id}` | Stored result body, byte-identical | `screening.Handler.GetByID` |

## Packages

| Package | Role |
|---|---|
| `internal/scoring` | Pure deterministic engine: `Evaluate(FindingSet) → Verdict` under `PolicyV1`; no IO/clock/RNG |
| `internal/screening` | Orchestrator: wallet resolve → parallel gather (sanctions/labels/enrichment) → availability fold → map findings → score → compose → persist |
| `internal/db` | sqlc-generated: `screening_results`, `screening_requests` (claim state machine) |

## Scoring policy `v1.0.0` (`internal/scoring/policy.go`)

| Signal | Weight |
|---|---|
| Direct OFAC sanctions match | flat 100 + `DIRECT_SANCTIONS` flag (no factors) |
| Malicious labels / provider observations | base weight × verification × confidence (verified 1.0 → disputed 0; high 1.0 → unknown 0.7); direct block = **max**, never sum |
| Provider-reported exposures | 1-hop 10, 2-hop 5, ×0.7 reported factor; hops>2 & hops>max_hops unscored; block cap 40, **sum** |
| Provider scores | band map (≥80→20, ≥50→10, <50→0); max band only; block cap 30 |
| Exchange/gambling/service/unknown categories | weight 0 (`LABEL_CLASSIFICATION`) |
| Total | `min(100, direct + exposure + other)`; NULL when no usable dimension |

## Persistence (`migrations/000007_screenings.sql`)

| Table | Role |
|---|---|
| `screening_results` | Terminal rows: `risk_score` (nullable), `risk_level`, `screening_status` CHECK (COMPLETED/PARTIAL/INSUFFICIENT_DATA/FAILED), `scoring_policy_version`, `findings`, `provider_status`, `request`, `result` (**`json`** — byte-faithful replay) |
| `screening_requests` | Idempotency claims: `(client_id, idempotency_key)` UNIQUE, `request_hash`, `claimed_at`, `screening_result_id` (NULL = in-flight) |

## Invariants (enforced + regression-tested)

- unknown ≠ safe: `INSUFFICIENT_DATA` + NULL score + `Blocks{}` zeroed
- non-match ≠ safe: `matched:false`, `evaluated:true`
- reported ≠ verified: verification factors scale weights
- disputed/revoked: disputed → 0; revoked excluded upstream
- exchange/gambling: classification, weight 0
- provider failure: typed status → `PARTIAL`; `DISABLED` ≠ failure
- direct/indirect separation: exposures `provider_reported` only, never ownership
- no double-counting: dedup keeps strongest evidence; single-block membership
- idempotent replay byte-identical (`json` column + stored body)
- 1-hop ≠ ownership: `basis: provider_reported`, no ownership claims

## Raw captures

`docs/feature-maps/assets/screening-{sanctioned,scam-label,exchange-classification,unknown-partial,idempotent-create}.json`
