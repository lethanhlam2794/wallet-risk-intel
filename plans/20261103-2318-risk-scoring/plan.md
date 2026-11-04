---
plan: risk-scoring
title: "Risk Scoring & Screening API (Spec §19 Phase 5)"
status: completed
risk: R3
created: 2026-11-03
source-spec: docs/tailieu.md (Technical Design Specification v2.0)
depends-on:
  - plans/20261103-1217-foundation-ofac-sanctions
  - plans/20261103-1530-public-wallet-labels
  - plans/20261103-1746-live-enrichment
---

# Plan — Risk Scoring & Screening API (Spec Phase 5)

## 1. Scope

Spec §19 Phase 5 tasks: versioned risk policy, direct/indirect separation,
reason codes, persisted screening history, screening API
(`POST /api/v1/screenings` + `GET /api/v1/screenings/{id}`, §9.6–9.7).

Deliverable per spec: "Explainable wallet risk assessment."

## 2. Explicitly out of scope

- Transaction tracing / own-graph exposures (Phase 6) — `exposures`
  carries provider-reported items only, tagged `basis: provider_reported`.
- Cross-chain tracing, 3+ hops (Phase 7 — separately approved by user).
- Automatic enforcement decisions — scores are advisory, heuristic.
- New paid providers.

## 3. Grounded design (scouted + debated 2026-11-03)

### 3.1 Inputs to one screening run

| Input | Collaborator | Availability signal (NOT error-only) |
|---|---|---|
| Direct OFAC sanctions | `sanctions.Service.LookupWallet` | `data_status`: AVAILABLE / STALE / UNAVAILABLE |
| Public labels | `label.Service.ListLabels` (cursor-looped, no 200-truncation) | `data_status` same vocabulary |
| Provider signals/observations/exposures | `enrichment.Service.Enrich` | per-provider `status` (OK/typed failure/DISABLED) |
| Wallet row (FK target) | `wallet.Service.GetOrCreate` | hard error → FAILED |
| Freshness | `DataStatus` on sanctions + label services | feeds `data_completeness` |

`LookupWallet`/`ListLabels` return *success* with `UNAVAILABLE` when no
snapshot is published — the fold MUST key on data_status, never error
(`unknown ≠ safe` invariant; Critic round-1 top finding).

### 3.2 Lifecycle: synchronous compute + claim-first idempotency

- `POST /screenings` executes **synchronously** under a bounded deadline,
  returns **HTTP 201** + full §9.7 body + `Location` header. Documented
  deviation from the §9.6 `202`+`PENDING` example — spec example implies
  a job queue that does not exist; faking PENDING then completing inline
  would be the worse lie. PENDING remains an API-level transient state
  expressed via the claim row, never a persisted `screening_status`.
- Pipeline (gather→score→persist) runs under
  `context.WithoutCancel(ctx)` with its own deadline: a client
  disconnect mid-screening still lands the terminal row so replays can
  fetch it. Result INSERT + claim UPDATE happen in **one transaction**.
- `GET /screenings/{id}` → stored result or 404. Any reader may fetch any
  result UUID (results are not client-private — documented).

### 3.3 Idempotency protocol (Critic amendment 1 — full state machine)

`screening_requests` is the claim table: `(client_id, idempotency_key)`
UNIQUE, `request_hash` = sha256 over the **canonical request** (resolved
network, normalized address, defaulted options — never raw body bytes),
`screening_result_id` NULL-able, `claimed_at TIMESTAMPTZ`.

- New key → INSERT claim → run pipeline → one tx: INSERT result +
  UPDATE claim.result_id.
- Conflict (23505) → load claim → **compare `request_hash` FIRST, before
  any branch**: different hash + same key → `409 IDEMPOTENCY_CONFLICT`
  in every claim state (terminal, in-flight, stale) — a key is bound to
  its payload for its whole lifecycle; takeover never erases another
  request's record.
- Same hash → branch on claim state:
  - `result_id` set → replay: `200` + stored result body +
    `Idempotent-Replay: true`.
  - `result_id` NULL + `claimed_at` fresh (< 5 min) → in-flight: bounded
    poll (250 ms × deadline 30 s); terminal → replay; timeout →
    `409 IDEMPOTENCY_IN_FLIGHT`.
  - `result_id` NULL + `claimed_at` stale (≥ 5 min — owner presumed
    dead) → takeover: DELETE claim + re-INSERT + run pipeline.
- Key rules: header absent = no idempotency (each POST creates a new
  row); empty/whitespace = absent; length 1–255 + charset
  `^[A-Za-z0-9._~-]+$` else `400 BAD_REQUEST`. Same key + different
  `request_hash` → `409 IDEMPOTENCY_CONFLICT`.
- `screened_at` set explicitly at result insert so column and stored
  body agree; replays preserve the original.

### 3.4 Scoring policy `v1.0.0` (reproducible, pure function)

`internal/scoring` — `Evaluate(FindingSet) → Verdict`, zero IO,
deterministic output ordering.

**Blocks & precedence** (order fixed):

```
score = min(100, direct_max + min(20, exposure_block) + min(30, other_block))
```

- `direct_max` = MAX of direct finding weights (never a sum — mirror
  evidence cannot stack).
- **Direct block**:
  - OFAC direct match (active, this network, CHAIN_SPECIFIC) → flat 100
    (official source — no confidence scaling) + `DIRECT_SANCTIONS` flag
    independent of score.
  - Category weight × verification factor × confidence factor,
    `weight = round(base × verif × conf)`:
    - base: RANSOMWARE 95, HACK 90, PHISHING 90, TERRORISM 90, SCAM 85,
      DRAINER 85, SANCTIONED 85, MONEY_LAUNDERING 80, DARKNET 75,
      EXTREMISM 70, FROZEN 60.
    - verif: verified 1.0, reported 0.7, unverified 0.5, disputed 0.
    - conf: high 1.0, medium 0.9, low 0.8, unknown 0.7.
    - Applies to `wallet_labels` AND provider observations (provider
      observations are always reported/unverified; a provider SANCTIONED
      observation scores via the same table — no special-casing).
  - SERVICE_TYPE / ENTITY_TYPE / OTHER → 0, classification-only reasons.
    **MIXER=0 is deliberate** (spec §7.11 SERVICE_TYPE; classification
    not crime; a sanctioned mixer is still caught by the OFAC
    dimension). Documented for future policy revision.
- **Single-block-membership rule**: every finding is scored in exactly
  one block. Provider observations are scored ONCE — via the direct
  block's category×verif×conf table (or the 0-weight classification
  path). They never reappear in the other block — no double counting.
- **Exposure block (cap 20)**: provider-reported indirect exposures only —
  each tagged `basis: provider_reported`, filtered by `max_hops`,
  `hops > 2` informational-only and never scored, NEVER under
  `transaction_evidence`. Weights: 1-hop 10, 2-hop 5 — both × reported
  factor (0.7). Own-graph weights land in Phase 6 policy bump.
- **Other block (cap 30)**: max `provider_score` band contribution only
  (≥80 → +20, ≥50 → +10; reported, never ground truth; max not sum)
  plus any future non-observation signals (none exist in P5).
- **Dedup key** (block-scoped): `{kind}:{category}:{source}:{ref}` —
  ref = label fingerprint for labels, provider observation identity for
  provider rows. Cross-source mirrors are *different* findings by design;
  intra-source duplicates collapse. Residual mirror-stacking is bounded
  by the block caps — documented limitation.
- **`risk_level`**: CRITICAL ≥80, HIGH 60–79, MEDIUM 30–59, LOW 0–29,
  UNKNOWN when score NULL. LOW never means safe — `data_completeness`
  always accompanies it.
- **Reason codes**: structured objects `{code, category,
  verification_status, weight, evidence_ref}` — closed `code` enum
  (`DIRECT_SANCTIONS_MATCH`, `DIRECT_LABEL`, `LABEL_CLASSIFICATION`,
  `PROVIDER_OBSERVATION`, `PROVIDER_SCORE`, `EXPOSURE_PROVIDER`,
  `CROSS_CHAIN_LISTING`, `SNAPSHOT_STALE`, `PROVIDER_FAILED`,
  `NOT_EVALUATED`), never string-suffix parsing for consumers.

**§10.1 dimension → mechanism table** (all seven mapped):

| Dimension | Mechanism |
|---|---|
| 1 Direct Sanctions | direct block, OFAC match, flag |
| 2 Direct Malicious Labels | direct block, category×verif×conf |
| 3 Indirect Exposure | exposure block (provider-reported only in P5) |
| 4 Service Classification | classification-only reasons, 0 weight |
| 5 Data Confidence | confidence factor on label/observation weights |
| 6 Data Freshness | `data_completeness` + STALE→PARTIAL fold |
| 7 Provider Coverage | `provider_status` block + status fold |

### 3.5 Status fold (explicit — amendment 3)

Per-input availability: `AVAILABLE`→usable, `STALE`→usable-but-degraded,
`UNAVAILABLE`→not usable, `NOT_REQUESTED`→excluded (include_X=false is
never a failure and never "clean" — the block reads `evaluated:false`).
Provider `DISABLED` is **excluded from the fold** — it is configuration,
not failure (mirrors `enrichment.aggregate` which omits DISABLED from
`attempted`); it is still reported in `provider_status` verbatim.

- `COMPLETED`: every requested input usable and none degraded.
- `PARTIAL`: ≥1 usable input + ≥1 degraded/failed input (DISABLED does
  not degrade).
- `INSUFFICIENT_DATA`: zero usable inputs → `risk_score` NULL,
  `risk_level` UNKNOWN. (All-include-false + providers unusable lands
  here too.)
- `FAILED`: internal error (DB/pipeline) — persisted when a wallet row
  exists; pre-resolution DB failure correctly 500s with no row.

### 3.6 include_*/max_hops semantics matrix

| Option | Effect |
|---|---|
| `include_sanctions=false` | `direct_sanctions.evaluated=false`; sanctions dimension excluded; completeness echoes NOT_REQUESTED |
| `include_labels=false` | same for labels |
| `include_exposure=false` | `exposures` omitted + `coverage.disabled_by_request`; exposure block 0 |
| `refresh_providers` | maps to `enrichment.Options.Refresh` (cooldown-guarded) |
| `max_hops` (0–2, default 2) | recorded in `coverage.max_hops_requested`; filters provider-reported exposures; own-graph N/A in P5 |
| `options` omitted | all include_*=true, max_hops=2, refresh=false |

Provider fan-out **always runs** (observations feed `provider_status` +
other block); `include_exposure` gates only the exposures section.
All-include-false is legal → providers-only screening → possibly
INSUFFICIENT_DATA.

### 3.7 Persistence (migration 000007)

- `screening_results`: spec §7.14 verbatim columns + `request JSONB` +
  `result JSON` (type `json`, NOT `jsonb` — jsonb re-normalizes and
  breaks byte-faithful replay; success criterion = byte-identical body).
  CHECK keeps the spec's four statuses (PENDING is never persisted).
- `screening_requests` per §3.3: `idempotency_key` CHECK
  `char_length BETWEEN 1 AND 255`; charset enforced at handler.
- Indexes: `(wallet_id, screened_at DESC)`, `(client_id)`.
- Retention unbounded — acceptable for MVP, deferred note.

### 3.8 API surface

- `POST /api/v1/screenings`: body per §9.6; validation via
  `wallet.ResolveAndNormalize`; `max_hops` 0–2 (reject, not clamp);
  roles reader+admin. Errors: INVALID_NETWORK/INVALID_ADDRESS/BAD_REQUEST
  /IDEMPOTENCY_CONFLICT/IDEMPOTENCY_IN_FLIGHT/INTERNAL.
- `GET /api/v1/screenings/{id}`: UUID → stored body byte-identical, 404.
- Synchronous worst-case latency ≈ enrichment deadline (~25 s) + local
  lookups; openapi documents the sync contract + >30 s client timeout
  guidance. `http.Server` has no WriteTimeout — verified safe.
- `api/openapi.yaml`: endpoints + schemas + examples + `screenings` tag;
  remove "screenings intentionally absent"; version bump.
- §14 `wallet:screening:*` cache: key builder added for naming parity
  but **unused** — POSTs are audit events, replays are served by
  `screening_requests` durably; a 5-min result cache could serve
  verdicts computed on a superseded OFAC snapshot (Critic agrees:
  omission is the safer design).

### 3.9 Observability (spec §17 — added per Critic)

- Metrics: `screening_requests_total` (by status),
  `screening_duration_seconds`, `screening_partial_total` — wire via
  existing `Deps.Metrics`.
- Log fields: `request_id`, `screening_id`, `network`, `status`,
  `error_code` — never credentials.

## 4. Phase roadmap

| # | Phase file | Deliverable |
|---|---|---|
| 1 | `phase-01-screening-persistence.md` | Migration 000007 + sqlc queries + models |
| 2 | `phase-02-scoring-engine.md` | `internal/scoring` pure engine + policy v1 + reason codes |
| 3 | `phase-03-screening-orchestration.md` | `internal/screening` service: gather→score→persist |
| 4 | `phase-04-screenings-api.md` | Handlers, idempotency, metrics, openapi, wiring, integration tests |

## 5. Risk assessment

- Spec contract deviation (201 vs 202/PENDING; extra columns; `json` vs
  `jsonb`) — debated, resolved; documented.
- Double counting — dedup key + direct-block max + block caps.
- `unknown ≠ safe` — data_status-keyed fold + INSUFFICIENT_DATA/NULL.
- Reported ≠ verified — verif×conf scaling; disputed ×0.
- Provider failure → typed provider_status + PARTIAL, never clean.
- Idempotency races — claim state machine + single-tx commit + takeover.
- Stale sanctions mid-flight — no result cache; data_status recomputed.

## 6. Stage 1 Debate Summary

**Round 1** (`devin -p` Critic session `rhinestone-scale`,
`reports/critique-round-1.md`): verdict CHALLENGED — 11 required
amendments; structural gate passed; all claimed collaborators verified
against source.

Concessions adopted (all 11):

1. Full idempotency claim state machine (§3.3) — insert-first +
   WithoutCancel + single-tx result+claim + bounded-poll loser +
   stale-claim takeover.
2. `idempotency_key` CHECK 1–255 + charset; `result` column `json` for
   byte fidelity; PENDING dropped from persisted vocab.
3. Explicit data_status-keyed fold (§3.5) — the UNAVAILABLE-as-clean
   trap is now a named test case.
4. Full label collection via cursor loop (no silent 200-truncation).
5. All 14 §9.7 fields enumerated incl. `evidence` block definition.
6. include_*/max_hops/defaults matrix (§3.6).
7. Detached-context pipeline adopted (§3.2).
8. §10.1 dimension→mechanism table; MIXER=0 rationale; confidence folded
   (high 1.0/medium 0.9/low 0.8/unknown 0.7); dedup key + category→type
   mapping via risk_categories; provider SANCTIONED observations scored
   via same table; multi provider_score = max; precedence order fixed.
9. Exposure conditions: `basis: provider_reported`, hops>2
   informational-only, never transaction_evidence, max_hops filter.
10. §17 metrics + `screening_id` log field (§3.9).
11. §4.4-critique test list landed in phase-03/04 success criteria.

No contested defenses remaining; see `reports/` for round artifacts.
