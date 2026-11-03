e9b71edf56d07483956872fd074d0d54806f543b8ce119bd967f60fd11967cee
I'll start by invoking the relevant review skill and reading the mandatory compliance inputs.All 5 phase files exist, matching the 5-row roadmap. Now let me read the phase files and the rest of the engineering standard.Structural audit passes (5/5 phase files). Now deep codebase scouting — let me map the actual repo structure and read the cited files.The module exists with phases 1–3 built. `x/sync` is already vendored but `x/time` is not. Now let me read the cited files in parallel.Core claims verified so far: `cache.Provider` exists, `wallet.GetOrCreate` on read is established, Deps nil-gated pattern matches. Now migrations, queries, main wiring, openapi, and the spec.`data_sources.source_type` is unconstrained VARCHAR (only 'snapshot' used so far); `risk_categories` seeds cover all the categories the plan maps to. Now let me check the redis wrapper, openapi spec, downloader patterns, and locate spec sections.Spec found. Let me read the critical sections: §4.4–4.6 provider rules, §13–17 requirements, §19 Phase 4, and §9 endpoint surface.Spec §13 shows a different `Provider` interface signature than the plan proposes. Let me read §19 Phase 4 scope, §9 endpoint contracts, and §7.4 data_sources.Now checking migrations registration, openapi conventions, env example, and the label handler's query-param conventions.Nearly complete. Let me verify the last details: `ResolveAndNormalize`, the wallet-attribution category map precedent, ingestion metrics hook pattern, and wallet/address helpers.Verification complete. I've audited all 5 phase files against the spec, the codebase, the engineering standard, and the learning ledger. All cited files exist and claims hold up except where noted below. Here is the full adversarial critique.

---

# Devin Critic — Round 1 Critique: `live-enrichment` plan

## Executive Assessment

**Structurally complete**: 5/5 phase files exist matching the roadmap. **Factually well-grounded**: every codebase claim I checked is true — `cache.Provider(src,net,addr)` exists in `keys.go`, `wallet.GetOrCreate`-on-GET is established precedent (`walletHandler`, server.go:187), `ChainReference` seeds (`'1'`/`'56'`) map verbatim to GoPlus `chain_id`, `data_sources.source_type` is an unconstrained `VARCHAR(30)` so `'api'` is legal, `x/sync` is vendored, `CodeInvalidSource` exists for the `providers=` filter, `testutil.TruncateAll` auto-covers the new table, and the Deps nil-gated registration pattern matches `Labels`/`Sanctions`/`Admin`.

The domain invariants are handled honestly: provider observations stay out of `wallet_labels` (correct — volatile data would corrupt the fingerprint/revocation lifecycle), `provider_score` ≠ `risk_score`, `reported` never `verified`, direct/indirect separation via `Exposures`, `code!=1` → DEGRADED, `"0"` flags are data-not-verdicts, spec §17 metric names exact.

However, I found **one concrete defect** (PublicAML chain token), **two real production risks** (unbounded in-request wait/retry; `refresh=1` quota-burn vector), **one correctness trap** (errgroup cancellation semantics), and several DDL/SSRF/mapping gaps requiring amendment. Verdict: **CHANGES_REQUESTED**.

---

## Phase-by-Phase Critique

### Phase 1 — Provider Foundation: **CHALLENGED**

1. **`Provider` interface deviates from spec §13 without documenting it.** Spec: `LookupAddress(ctx, network string, address string) ([]Observation, error)`. Plan: `LookupAddress(ctx, net network.Network, normalized string) (*LookupResult, error)` + `Enabled()`. The deviation is *defensible* — `[]Observation` alone cannot carry the raw payload, provider status, chain-requested, and fetch timestamp that spec §4.6 *requires* persisting, and `network.Network` supplies both `ChainReference` (GoPlus) and `Code` (PublicAML). But the plan presents it as "the spec §13 contract" — it is an extension and must be documented as such in `provider.go` (F7). `Enabled()` is acceptable (spec's "not every provider supports every interface" governs Provider-vs-BulkSource-vs-SnapshotSource separation, which the plan honors).

2. **`provider_observations` DDL is underspecified** (F5): no CHECK on `status` (repo convention CHECKs enums — `api_clients.role`, `wallet_labels.confidence`), no index for `select-latest`/retention prune (`(source_id, wallet_id, fetched_at DESC)` needed or prune becomes a scan), no explicit handling of spec §4.6's *"lưu chain requested"* (derivable via `wallet_id→network`, but spec asks for the requested chain verbatim — record `requested_chain TEXT` or in evidence). Retention prune race is benign *within* one process (singleflight serializes same-key inserts) but can transiently keep 26 rows cross-process — document as bounded/self-healing and prune in the insert tx.

3. **"allowlist base URLs from config" is SSRF-hostile wording** (F6). Spec §16 demands a *fixed* allowlist. If env supplies both URL and allowlist, the SSRF boundary is operator-mutable. Must mirror `ofac/downloader.go`: hardcoded `defaultAllowedHosts` per adapter, configured base URL validated against it at construction (fail fast at startup), https-only in prod, http via test hook (`WithInsecureHTTP` pattern).

4. **Retry/limiter semantics under-specified for a synchronous endpoint** (F2): "limit→semaphore→breaker→attempt loop" doesn't state whether the rate limiter gates each *attempt* or each *request* — it must be per-attempt or retries triple-burn the upstream quota. `limiter.Wait(ctx)` with an unbounded wait means requests pile goroutines at 30/min while inbound allows 120/min. And honoring `Retry-After` capped at 30s *inside* an HTTP request yields worst-case ~90s latency (8s×3 + 30s×2) against an `http.Server` with no `WriteTimeout`. Specify: per-attempt token; bounded limiter wait (fail fast → typed `rate_limited` + `retry_after` when reservation delay exceeds threshold); on 429 prefer returning the typed error over in-request sleep; overall fan-out parent deadline (~20–30s).

### Phase 2 — GoPlus Adapter: **CHALLENGED**

- **`honeypot_related_address → SCAM` is an inference** violating the plan's own "confident mappings only" rule and spec §4.4 (*"không suy diễn category từ field không liên quan"*). A honeypot *association* flag is not a scam verdict. Demote to `OTHER` (raw flag survives in `Signals`/evidence) or add a sourced justification (F8). All other mappings are defensible verbatim-translations; `blacklist_doubt → OTHER` is exactly right.
- **`result` toleration unspecified**: unknown addresses may return `result: null` or `{}` — must parse as OK-empty (zero observations, populated Signals), not `malformed`. Requires a fixture test (F8).
- Resilience defaults (8s/30rpm/4 conc/≤2 retry/5-fail-60s) match free-tier reality. `contract_address` quirk handling correct. Bearer auth is optional — fine.

### Phase 3 — PublicAML Adapter: **CHALLENGED** (concrete defect)

- **`chain = upper(network.Code)` is wrong** (F1). `network.Code` seeds are `'ethereum'`/`'bsc'` (migration 000001:167-169); `upper('ethereum')` = `'ETHEREUM'` ≠ `'ETH'` that PublicAML expects (plan's own Sources table documents `"ETH"|"BSC"`). Only BSC works, by accident. Requires explicit static map `{ethereum→ETH, bsc→BSC}` (or per-network provider chain token) — plus a request-body fixture test asserting `chain=="ETH"` for an ethereum lookup. This is exactly the class of bug the spec's batch-vs-chain-specific warning exists to prevent silently corrupting.
- Otherwise strong: per-address chain-specific calls honor the §4.6 quirk; `aml_score` → `provider_score` passthrough; `outbound_exposure`→`Exposures` keeps direct/indirect separate; `sanctioned:true` stays a provider claim never merged with OFAC; empty `entities` = OK-empty not safe; 15/min ≈ 900/hr under ~1k/hr quota is correctly conservative; `top_n:5` + 4MB cap is reasonable.

### Phase 4 — Chainabuse Adapter: **ACCEPTED**

The risk posture is correct: `Enabled()==false` without key, `DISABLED` status with `no_api_key` reason, zero outbound calls proven via counting transport, docs-shaped fixtures explicitly marked provisional (G-001: honest about unverifiable runtime truth). Limiter 1/min + breaker 3/120s is appropriately paranoid for a 10-call/month tier. Note: Basic-auth key:key construction is docs-sourced-unverified — the required provisional marking covers this; also ensure the client never logs the Authorization header (spec §16 secret hygiene) — fold into F9 note.

### Phase 5 — Enrichment Service + API: **CHALLENGED**

- **`errgroup` trap** (F4): phase text says "errgroup (bounded)". If the implementer uses `errgroup.WithContext`, the first provider error cancels siblings' in-flight calls → they report `context canceled` instead of real statuses → fabricated failures in `provider_status`. Must specify: siblings never cancel each other (plain `errgroup.Group` or explicit per-provider result collection); each provider's outcome is recorded independently. On caller-ctx cancel, report actual per-provider outcomes, not cancellation lies.
- **`refresh=1` is a quota-burn footgun** (F3): any `reader` key can loop `?refresh=1` and drain the *shared* GoPlus 30/min upstream quota, degrading the endpoint globally for all clients. The plan has no refresh throttle. Require one of: admin-role-gating for `refresh`, per-key refresh cooldown (`provider:refresh:{src}:{net}:{addr}` SET NX EX 30–60s → serve cached or 429), or a minimum-cache-age precondition for refresh to apply.
- **Endpoint choice is correct** — dedicated sub-resource matches the `/labels` convention; embedding live upstream calls into `/wallets/{net}/{addr}` would burn quota and couple latency on every wallet read. GetOrCreate-on-GET is established precedent.
- **Aggregate statuses correct**: `providers=chainabuse` (disabled) → `INSUFFICIENT_DATA` is the right informative answer; FAILED still returns 200 + metadata (provider failure ≠ request failure) — consistent with spec §15.
- **OpenAPI debt** (F9): `info.description` is *already stale* — it claims "phases 1–2" scope and "labels … intentionally absent" while the labels path exists (pre-existing defect, but phase-05 touches this file and must fix it). New-route docs must state explicitly: `provider_score` is not the internal risk score; `provider_score:0` under non-OK status is not safe (§4.6); `retry_after` units; `cache_expires_at` semantics.
- Minor (not blocking): `screened_at` → prefer `enriched_at`/`checked_at` (don't pre-commit Phase-5 vocabulary); `refresh` should parse via `strconv.ParseBool` for consistency with `active_only`; `providers=` empty value → define as "all" not error; empty `Signals` map should emit `{}` not `null` (`emit_empty_slices` covers slices only).

---

## Identified Blind Spots & Edge Cases

- **Retry-After cap of 30s was calibrated for a batch downloader** (OFAC cap is 5min for a detached run). Inside a synchronous request it must be min(hint, remaining-request-budget, ~2–5s) or skipped entirely — the response already models `error.retry_after`, so returning the hint is the better honoring.
- **Negative-cache masking**: 60s degraded entries are fine and self-describing; confirm a cached degraded entry served on hit still reports `cached:true` + original `status` (not re-marked OK) — phase test list covers "negative cache replay marked non-clean" ✓.
- **`INSUFFICIENT_DATA` is unreachable in default config** (goplus `Enabled()` is always true) — acceptable, status is still right to model, but tests must exercise it via the `providers=` filter selecting only-disabled sources.
- **Cross-process limits documented** (in-process limiter/breaker single-instance) — acceptable for MVP, correctly flagged in phase-01 risks.
- **Chainabuse `data_sources` seed**: seed `is_active=true` — "registered" ≠ "adapter enabled"; DISABLED comes from the adapter, not the row. Keep consistent.

---

## Answers to the Plan's 5 Open Questions

1. **Dedicated `/enrichment` route — correct.** Embedding in `/wallets/{net}/{addr}` would fire upstream calls on every wallet read (quota burn + latency coupling + new failure modes on a currently upstream-free endpoint). Sub-resource matches `/labels` precedent; additive contract change is honest.
2. **Append-log + bounded retention — correct.** Latest-only upsert destroys the flapping/degraded-history evidence spec §4.6 demands ("lưu nguyên response" over time, degraded tracking). Keep 25-per-key prune, add the composite index, prune in insert tx.
3. **`x/time` — accept.** Same golang.org/x family already vendored (`x/sync`); hand-rolling a correct concurrent token bucket is a false economy (KISS argues *for* the dependency here, not against).
4. **Keep the Chainabuse adapter, disabled and provisional.** The DISABLED status path is needed regardless to explain why a registered source is silent; the docs-shaped client is small, httptest-covered, and cannot execute without a key — zero default-config runtime risk. Deleting it only re-buys the work later.
5. **`top_n` bounded + verbatim entity storage.** `top_n:5` bounds counterparty items; store the returned `entities[i]` object verbatim (4MB cap as backstop), document that evidence completeness is deliberately bounded and refetchable. Don't re-slice nested arrays post-hoc — that's evidence mutation.

---

## Required Plan Amendments

- **F1** — Phase-03: replace `chain = upper(network.Code)` with an explicit static map `{ethereum:ETH, bsc:BSC}` (or per-network provider-chain token); add a fixture test asserting the request body's chain value per network.
- **F2** — Phase-01/05: bound the synchronous call — (a) parent ctx deadline for the whole fan-out (~20–30s, env-tunable); (b) rate limiter consumes per HTTP *attempt*, not per request; (c) limiter wait bounded — fail fast to typed `rate_limited`+`retry_after` when reservation delay exceeds a small threshold; (d) on 429 return the typed error carrying `retry_after` rather than sleeping in-request (in-request retry reserved for 5xx/transport with short jittered backoff within remaining budget).
- **F3** — Phase-05: add a `refresh=1` throttle — pick one: admin-gate `refresh`, per-key Redis cooldown (`provider:refresh:{src}:{net}:{addr}` SET NX EX), or minimum cache-age rule. As spec'd, any reader can burn the shared upstream quota.
- **F4** — Phase-05: forbid sibling-cancel on provider error — no `errgroup.WithContext` derived cancellation (or equivalent); each provider outcome recorded independently; on caller cancel report true per-provider statuses.
- **F5** — Phase-01: pin `provider_observations` DDL — CHECK constraint on `status`; index `(source_id, wallet_id, fetched_at DESC)`; explicit column types (payload JSONB NOT NULL, provider_score DOUBLE PRECISION NULL, error JSONB NULL, http_status INT, latency_ms INT, fetched_at TIMESTAMPTZ NOT NULL DEFAULT now()); record literal requested chain (spec §4.6 "lưu chain requested"); retention prune inside the insert transaction with documented transient-overflow tolerance.
- **F6** — Phase-01: fix allowlist wording — per-provider allowed hosts hardcoded in adapter code (mirror `ofac.defaultAllowedHosts`); configured base URL validated against it at construction; https-only default with test-only insecure hook.
- **F7** — Phase-01: document the deliberate §13 interface extension in `provider.go` (LookupResult exists to satisfy §4.6 persistence requirements []Observation can't carry; `Enabled()` rationale recorded).
- **F8** — Phase-02: demote `honeypot_related_address` to `OTHER` (or attach sourced justification for SCAM); require `result:null`/`{}` → OK-empty parsing + fixture test.
- **F9** — Phase-05: OpenAPI + contract docs — fix the stale `info.description` (still says "phases 1–2", "labels absent"); document `provider_score` ≠ internal risk, `0` ≠ safe under degraded status, `retry_after` units, `cache_expires_at`, DISABLED semantics; never log Authorization headers (Chainabuse Basic auth).

Verdict: **CHANGES_REQUESTED** — F1–F9 must be incorporated before implementation. None is fatal; all are precisely fixable.

---

Status: DONE_WITH_CONCERNS
Summary: Plan is structurally complete and mostly spec-faithful with excellent invariant discipline, but I found one concrete defect (PublicAML `upper(network.Code)` yields 'ETHEREUM' not 'ETH'), two production risks (unbounded in-request retry/limiter waits; unthrottled `refresh=1` quota burn), an errgroup cancellation trap, and several DDL/SSRF/OpenAPI gaps — 9 required amendments (F1–F9), verdict CHANGES_REQUESTED.
Concerns/Blockers: None blocking review; all findings are actionable within the existing plan structure.
