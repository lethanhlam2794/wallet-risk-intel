# Devin Critic — Round 1: Risk Scoring & Screening API (Spec Phase 5)

**Verdict: CHALLENGED** — structurally complete, well-grounded against real
code, no fatal architectural flaw. Eleven required amendments before
consensus; the most dangerous is a silent "UNAVAILABLE folded as empty"
path that would violate `unknown ≠ safe`.

---

## 0. Structural Completeness Audit

Roadmap declares **4 phases**; `ls` confirms all four spec files exist:

| # | File | Exists |
|---|------|--------|
| 1 | `phase-01-screening-persistence.md` | ✓ |
| 2 | `phase-02-scoring-engine.md` | ✓ |
| 3 | `phase-03-screening-orchestration.md` | ✓ |
| 4 | `phase-04-screenings-api.md` | ✓ |

Structural gate: **PASS**.

## 1. Collaborator Verification (all confirmed against source)

| Claim | Verified | Evidence |
|---|---|---|
| `sanctions.Service.LookupWallet` | ✓ | `file:///home/lethanhlam/Desktop/Profile/check-wallet/internal/sanctions/service.go#L49` — `(ctx, networkID int64, networkCode, normalized string) (*WalletLookup, string, *time.Time, error)` |
| `label.Service.ListLabels` | ✓ | `file:///home/lethanhlam/Desktop/Profile/check-wallet/internal/label/service.go#L112` — same freshness triple, **but paginated, hard cap 200** (`#L135`) |
| `enrichment.Service.Enrich` | ✓ | `file:///home/lethanhlam/Desktop/Profile/check-wallet/internal/enrichment/service.go#L93` — `(ctx, net network.Network, rawAddress, normalized, opts Options)`. Internally calls `wallets.GetOrCreate` (`#L117`); deadline default 25 s (`#L83`) |
| `wallet.Service.GetOrCreate` | ✓ | `file:///home/lethanhlam/Desktop/Profile/check-wallet/internal/wallet/service.go#L20` |
| `wallet.ResolveAndNormalize` | ✓ | `file:///home/lethanhlam/Desktop/Profile/check-wallet/internal/wallet/address.go#L55` — returns `(network.Network, normalized, error)` |
| `httpserver.Deps` + routes | ✓ | `file:///home/lethanhlam/Desktop/Profile/check-wallet/internal/httpserver/server.go#L37` — `/api/v1` group already chains `IdentifyClient → limiter → RequireRole("reader","admin")` (`#L81-85`) |
| `middleware` auth/errors | ✓ | `file:///home/lethanhlam/Desktop/Profile/check-wallet/internal/middleware/auth.go#L65` `ClientFrom` → `ClientIdentity{ID,Name,Role}` (api_clients.id is `BIGSERIAL`, migration 000001 L157 — `client_id BIGINT FK` is correct); `errors.go` envelope `{error:{code,message,request_id}}` |
| `cache` keys | ✓ | `file:///home/lethanhlam/Desktop/Profile/check-wallet/internal/platform/cache/keys.go` — `WalletScreening` builder does not exist yet; plan adds it |
| Migrations numbering | ✓ | `000006_provider_observations` is latest → `000007` is correct |
| sqlc conventions | ✓ | `sqlc.yaml`: pgx/v5, `emit_empty_slices`, UUID columns → `pgtype.UUID`; app-side `uuid.New()` precedent at `internal/ingestion/service.go#L192` |
| openapi "screenings absent" | ✓ | `api/openapi.yaml#L13`, version `0.2.0` — phase 4 correctly removes the line and bumps |
| Provider exposures | ✓ | Only `publicaml` emits `provider.Exposure` (`internal/provider/publicaml/mapper.go#L125-139`); `exposureItem.Hops` is **upstream-arbitrary**, not bounded to 2 (`model.go#L60`) |
| risk_categories vocab | ✓ | Migration `000005` seeds TERRORISM/DARKNET/EXTREMISM/FROZEN (plan weights) + MIXER as `SERVICE_TYPE` |

## 2. Rulings on the Six Contested Decisions

### 2.1 Synchronous `POST /screenings` → 201 + full result (vs spec §9.6 `202`+`PENDING`)

**ACCEPTED as a documented deviation.** Spec §9.6's `202/PENDING` implies a
job queue that does not exist in this monolith; a synchronous execution is
strictly more useful, preserves every §1.2/§21 invariant (idempotency,
statuses, evidence), and there is a working precedent in-repo for honest
status codes (`POST /admin/ingestion-runs` returns real `202` because it is
truly async — `handler.go#L81`). Faking `PENDING` then completing inline
would be the worse lie. Conditions:

- openapi.yaml must document the synchronous contract (201 + full §9.7
  body) and the deviation recorded in plan §3.2 — the plan already flags
  it; phase 4 must keep that note.
- Keep `PENDING` in the row CHECK vocabulary **only if** the two-step
  claim lifecycle (§4.1 below) is adopted; otherwise it is dead vocabulary
  that invites ambiguity.
- Recommend `Location: /api/v1/screenings/{id}` on 201 and an explicit
  decision on the replay status code (recommend returning the same stored
  body with `Idempotent-Replay: true`).

### 2.2 `screening_results` + `request`/`result` JSONB beyond spec §7.14

**ACCEPTED with a type correction.** Extra columns are additive, not a
violation; audit-faithful replay is a legitimate goal. **But** PostgreSQL
`jsonb` normalizes key order, whitespace, and numerics — phase 4's
success criterion "returns persisted body byte-faithfully" is
**false under jsonb**: the GET body will differ byte-wise from the POST
body. Either:

- store `result` as `json` (preserves raw text) or `text` if byte fidelity
  is a real requirement; or
- keep `jsonb` and reword the criterion to "semantically identical"
  (field-for-field equality), which is honestly sufficient — `request`
  may stay `jsonb` since `request_hash` is computed pre-storage.

### 2.3 Verification scaling (×1.0/×0.7/×0.5/×0) + per-category weights

**CHALLENGED — acceptable with two documented decisions.** The spec itself
marks its weights illustrative (§10.2), the policy is versioned, and
`disputed ×0` correctly implements "reported ≠ verified". Two gaps the
phase file must make explicit rather than leave implicit:

- **MIXER → 0**: spec §7.11 classifies MIXER as `SERVICE_TYPE`, so the
  plan scores it 0. Spec-literal, but AML-screening convention treats
  mixer attribution as risk-relevant (obfuscation tooling). Policy v1
  should record a deliberate decision — either a modest non-zero MIXER
  weight or an explicit "classification-only" rationale in the policy
  table — rather than inheriting 0 silently.
- **`confidence` ignored**: `wallet_labels.confidence ∈
  {unknown,low,medium,high}` (000005 L57) is unused; only
  `verification_status` scales. Spec §10.1 lists "Data Confidence" as a
  distinct risk dimension. Fold confidence into the scaling factor or
  document its deferral to a later policy version.

### 2.4 Provider-reported exposures contributing capped points (10/5, cap 20)

**CHALLENGED — acceptable under three conditions.** Spec §10.2's exposure
weights describe *transaction-graph* exposure; provider-reported exposure
(publicaml `outbound_exposure` only, verified) is weaker evidence.
Crediting it is defensible **if**:

1. every exposure entry carries `basis: "provider_reported"` and never
   appears under `transaction_evidence` (invariant: a provider claim is
   not a tx hash);
2. `hops` values come from upstream unbounded — items with `hops > 2`
   must be listed-informational-only and never scored (§10.2: beyond 2
   hops excluded from MVP scoring); `max_hops` should also filter them;
3. the `coverage` block reports `tracing: NOT_IMPLEMENTED` — already in
   the plan; keep it.

A cleaner alternative for the record: route these into `other_signals`
rather than `exposure_risk`, since the spec's exposure dimension is
graph-derived. I will not block on which block they land in — I **will**
block on the three conditions above.

### 2.5 Not implementing `wallet:screening:*` cache in MVP

**ACCEPTED — the omission is the safer design.** Every non-idempotent POST
is an audit event that must persist a fresh row; a result cache can only
serve replays, which `screening_requests` already handles correctly and
durably. Worse, the §14 key embeds only `{policy_version}` — a naive
5-minute cache could serve a verdict computed against a *superseded* OFAC
snapshot mid-window, which is exactly the stale-sanctions trap §14 warns
about elsewhere. Skipping avoids it. Conditions: keep the key builder for
parity with §14 naming (harmless, unused), and record the rationale in the
plan (already present — carry it into phase-03 notes).

### 2.6 Is scoring v1 missing a spec-required dimension?

**Mostly covered; requires an explicit mapping.** §10.1's seven dimensions
land as: (1) direct sanctions ✓, (2) direct labels ✓, (3) indirect
exposure ✓ provider-reported-only per §2.4, (4) service classification ✓
0-weight. Dimensions **5 Data Confidence** (unused — see §2.3), **6 Data
Freshness**, **7 Provider Coverage** surface via `data_completeness` /
`provider_status` / `PARTIAL` folding — which is the right mechanism
(they modulate completeness, not guilt), but the phase-02 spec must
include the dimension→mechanism table so a reviewer can verify nothing
§10.1 names is dropped. Currently implicit — make it explicit.

## 3. Phase-by-Phase Critique

### Phase 1 — screening persistence — **CHALLENGED**

Solid skeleton; underspecified in three places:

- **`screening_requests` lifecycle is undefined.** `screening_result_id`
  nullability depends on the claim protocol (§4.1) — pick one and write
  the column accordingly. Add `claimed_at TIMESTAMPTZ` (or reuse
  `created_at` + a state column) or stale in-flight claims can never be
  detected.
- **`idempotency_key TEXT` unbounded.** Add `CHECK
  (char_length(idempotency_key) BETWEEN 1 AND 255)` and define allowed
  charset at the handler; TEXT with no bound invites abuse.
- **`screening_status` CHECK adds `PENDING` beyond spec §7.14's four
  values** — fine if the claim-first lifecycle is adopted (a PENDING row
  is then a real state), dead vocabulary otherwise. Decide with §4.1.
- Verified-consistent: `risk_score` CHECK per spec handles NULL correctly
  (three-valued logic); `client_id`→`api_clients(id)` BIGINT matches
  000001; `wallet_id` FK matches spec verbatim; down-migration convention
  (DROP + seed cleanup) is established.

### Phase 2 — scoring engine — **CHALLENGED**

The pure-function design is right and the test list is strong. Required:

- **Define the finding-dedup key construction** (`kind, source, category,
  evidence_ref`?) — the spec's mirror-dedup rule is meaningless without
  it. A `wallet_labels` SCAM and a publicaml SCAM observation are
  different sources and must *not* dedupe; two adapters surfacing the
  same upstream record ideally would — state the achievable key and the
  limitation.
- **"closed vocabulary" reason codes are actually templates**
  (`DIRECT_LABEL_<CAT>_<VERIF>`) — fine, but prefer structured reason
  objects (`{code, category, verification_status, weight}`) with a truly
  closed `code` enum; string-templated codes make API consumers parse
  suffixes.
- **Define "malicious category" lookup for provider observations** —
  observations carry a category string, not a `category_type`; specify
  whether scoring maps via `risk_categories` (DB truth, handles dynamic
  categories → OTHER) or a static policy table, and reconcile with the
  "unknown categories → OTHER(0)" fallback.
- **Under-specified sub-rules**: provider SANCTIONED observations
  (LEGAL-type) score 0 under the literal "+10 per malicious observation"
  wording — inconsistent with a SANCTIONED *label* scoring up to 60;
  decide + document. Multiple provider scores → take max? `exposure` and
  `other` caps: define precedence order in the formula.
- See §2.3 for MIXER/confidence documentation requirements.

### Phase 3 — screening orchestration — **CHALLENGED (highest-risk phase)**

- **UNAVAILABLE ≠ empty — the plan's most dangerous silent trap.**
  `sanctions.LookupWallet` returns *success* with `data_status =
  UNAVAILABLE` when no snapshot is ever published (`service.go#L205-226`:
  `currentSnapshot` → `("", nil, nil)` → `fetchLookup` still runs against
  empty tables → returns a clean-looking non-match). `label.ListLabels`
  behaves identically (`service.go#L209-218`). If the status fold keys
  "input usable" off *error* rather than *data_status*, a system with no
  snapshots at all returns COMPLETED + "no sanctions found" — a direct
  `unknown ≠ safe` invariant violation. **The fold MUST treat
  AVAILABLE→usable, STALE→degraded-input (PARTIAL), UNAVAILABLE→not
  usable**, and this must be an explicit test case.
- **Label pagination truncation.** `ListLabels` caps at 200 items/page
  (`service.go#L135`); scoring over `Filters{Limit:200}` silently drops
  labels 201+. Loop `NextCursor` to exhaustion or add a dedicated
  full-set accessor (`loadSet` is already the cached unit — cheap).
  Silent truncation = missed malicious label = under-scored verdict.
- **Orphaned PENDING rows on client disconnect.** If the claim row is
  inserted first and the client disconnects mid-screening (up to ~25 s),
  the ctx-cancelled pipeline leaves the request claimed and the result
  row non-terminal forever. Run gather→score→persist under
  `context.WithoutCancel(ctx)` (Go 1.21+; go.mod is 1.27) with its own
  bounded deadline so the audit row always lands terminal and the
  idempotent replay can still fetch it — or add a documented sweeper.
- **§9.7 field list is incomplete in the plan.** The spec lists 14 fields
  including `evidence`; the plan enumerates `findings` refs but never an
  `evidence` response block. Enumerate all 14 fields in the phase-03
  model and define `evidence` content (label ids, sanctions entity ids,
  provider observation sources).
- **`include_*` semantics matrix missing** — see §4.2.
- Verified good: enrichment-nil tolerance, deterministic-serialization
  concern already flagged, "sanctions flag survives total provider
  outage" is in success criteria.

### Phase 4 — screenings API — **CHALLENGED**

- **Idempotency claim protocol is the load-bearing gap** — §4.1.
- **Missing screening metrics** (spec §17 mandates
  `screening_requests_total`, `screening_duration_seconds`,
  `screening_partial_total`): no phase adds them. `Deps.Metrics` already
  exists — wire counters + `screening_id` log field in phase 3/4.
- **Missing test cases** — §4.4.
- Verified good: auth chain inherited from the `/api/v1` group (no new
  middleware needed), `ClientFrom` supplies the FK principal, error-code
  conventions (`IDEMPOTENCY_CONFLICT` needs adding to `errors.go`),
  `max_hops` 0–2 reject-not-clamp is the right posture.

## 4. Identified Blind Spots & Edge Cases

### 4.1 Idempotency lifecycle (must be specified end-to-end)

"UNIQUE + insert-then-select replay" hides three distinct designs with
different failure modes:

- **Insert-first (claim)**: request row committed with `result_id NULL`,
  screening runs, then `UPDATE result_id`. Loser on `23505` sees a
  committed row — if `result_id` is NULL the screening is *in flight*:
  bounded-poll until deadline, return `409 IDEMPOTENCY_IN_FLIGHT`, or run
  anyway (duplicate result rows)? **And if the winner crashes, the key is
  poisoned forever** unless stale-claim detection exists.
- **Insert-after**: both requests run the full ~25 s screening (duplicate
  provider calls), then `INSERT ... ON CONFLICT` — the loser's insert
  blocks on the winner's speculative tuple until commit, then replays.
  Correct but serializes concurrent identical POSTs on a DB connection.
- Pick one, write the state machine, define `screening_result_id`
  nullability, in-flight behavior (poll bound + terminal response), and
  stale-claim recovery. My recommendation: insert-first +
  `context.WithoutCancel` pipeline (fixes both the race and the
  disconnect orphan) + bounded poll for the loser.

Also required:

- **`request_hash` over the canonical request** — sha256 of a fixed
  struct marshal (resolved network, normalized address, defaulted
  options), never raw body bytes: `{"a":1,"b":2}` vs `{"b":2,"a":1}` are
  the same request and must not produce a false `409`.
- **Key validation**: empty header = absent (not an error); malformed /
  oversized → `400 BAD_REQUEST`; define charset bound.
- **Replay scope**: result row fetched via `screening_result_id`; define
  replay status code + confirm `Idempotent-Replay: true` semantics.

### 4.2 `include_*` / `max_hops` semantics matrix (must be a table in phase 3)

- `include_sanctions=false` → direct_sanctions block must read
  **"not evaluated"**, never empty-as-clean; completeness echoes it.
  (Sanctioned wallet + sanctions off = the canonical trap.)
- `include_labels=false` → same.
- `include_exposure=false` → exposures block omitted/empty + exposure
  score 0 + coverage `disabled_by_request`. Do provider calls still run?
  Provider observations feed `other_signals`/`provider_status` — the spec
  options have no provider toggle; decide and document (recommend: fan-out
  always runs; `include_exposure` gates only the exposures section).
- `max_hops` → recorded in `coverage.max_hops_requested`; provider
  exposures filtered to `hops ≤ max_hops`; `hops > 2` never scored.
- All-include-false request → pick `INSUFFICIENT_DATA` (honest) or
  `400`; document.
- Options defaults when `options` omitted: unspecified — define
  (recommend `include_*=true`, `max_hops=2`, `refresh_providers=false`).

### 4.3 Additional edge cases to name in the plan

- **Synchronous latency**: worst case ≈ enrichment deadline (25 s) + local
  lookups; `http.Server` has no `WriteTimeout` (verified `cmd/api/main.go#L123`)
  so it survives; document client-timeout guidance >30 s in openapi.
- **FAILED-row semantics**: a `FAILED` row is only persistable after
  `wallet_id` resolution; pre-resolution failures (DB down) correctly
  500 with no row — state this boundary.
- **GET authorization**: any reader fetches any result by UUID —
  acceptable (results aren't client-private) but say so.
- **`screened_at`**: set explicitly at insert so the stored result body
  and the column agree; replay preserves the original timestamp.
- **Retention**: `screening_requests`/`screening_results` unbounded —
  acceptable for MVP; note it as deferred.
- **PENDING-row GET** (if two-step lifecycle): define the response body
  for a non-terminal row, or guarantee rows are only visible terminal.

### 4.4 Required test additions (phase 3/4)

1. Concurrent identical POSTs → exactly one screening + defined behavior
   for the loser (replay or bounded conflict per §4.1).
2. Same key + different payload → `409 IDEMPOTENCY_CONFLICT`.
3. **No published snapshots** → `INSUFFICIENT_DATA`, `risk_score` NULL —
   the acceptance-critical unknown≠safe case.
4. Sanctioned wallet + `include_sanctions=false` → no `DIRECT_SANCTIONS`
   flag + completeness says not-evaluated (must not read clean).
5. All providers failed + sanctions OK → `PARTIAL`, flag set, failure
   statuses visible.
6. Disputed label → 0 contribution; revoked label → excluded.
7. Client disconnect mid-screening → row still reaches terminal state
   (if WithoutCancel adopted) — otherwise a stale-claim test.
8. Replay returns the stored result (semantically/byte per §2.2).
9. Malformed/oversized `Idempotency-Key` → 400; absent key → works.
10. `GET` unknown-UUID → 404; role enforcement (anon → 401).

## 5. Required Plan Amendments (all must land before sign-off)

1. **Phase 1/4**: write the full idempotency claim protocol — row
   lifecycle (claim→terminal), `screening_result_id` nullability,
   conflict-path behavior for the loser, stale-claim recovery, canonical
   `request_hash` input, key validation rules, replay status code.
2. **Phase 1**: bound `idempotency_key` (CHECK length 1–255); decide
   `PENDING` vocabulary with the lifecycle; pick `result` column type per
   §2.2 (`json`/`text` for byte fidelity or reword the criterion).
3. **Phase 3**: explicit status-fold rule — `AVAILABLE`→usable,
   `STALE`→degraded, `UNAVAILABLE`→not-usable, per input; never error-only.
4. **Phase 3**: collect **all** active labels (cursor loop or dedicated
   accessor) — no silent 200-truncation.
5. **Phase 3**: enumerate all 14 §9.7 response fields incl. `evidence`;
   define its contents.
6. **Phase 3**: `include_*`/`max_hops`/defaults semantics matrix (§4.2).
7. **Phase 3**: detached-context or sweeper decision for
   disconnect-orphaned PENDING rows.
8. **Phase 2**: dimension→mechanism table for all seven §10.1
   dimensions; document MIXER=0 and confidence-unused decisions; define
   dedup finding key, provider-observation category_type mapping,
   SANCTIONED-observation handling, multi-provider-score rule, block
   precedence order.
9. **Phase 2/3**: exposures conditions — `basis: provider_reported` on
   every item, `hops>2` informational-only, never under
   `transaction_evidence`.
10. **Phase 3/4**: add the three spec §17 screening metrics + screening_id
    log field.
11. **Phase 4**: add the §4.4 test list (esp. #3, #4, #5 — the
    invariant-critical cases).

## 6. What the Plan Gets Right (for the record)

- Every claimed collaborator exists with the claimed signature; the plan
  was genuinely scouted, not imagined.
- Cross-chain/unresolved OFAC listings correctly stay out of the direct
  block (invariant preserved; `WalletLookup` already separates them).
- Provider failures propagate as typed statuses — never clean results.
- Skipping the screening result cache is *more* correct than building it.
- Sync-201 is the honest contract; faking 202+PENDING would be worse.
- Versioned pure-function scoring + determinism tests target the spec's
  reproducibility bar.
- `DIRECT_SANCTIONS` flag independent of score ✓; `disputed ×0` ✓;
  GAMBLING/EXCHANGE 0-weight ✓.

---

No `PLAN_CONSENSUS_REACHED` — 11 required amendments outstanding; all are
specification-level, none require redesign.
