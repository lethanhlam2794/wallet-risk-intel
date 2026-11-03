# Devin Review — Round 2

Verdict: CHANGES_REQUESTED

All 7 round-1 findings + MISC-8 misc items are verifiably fixed with
correct mechanics — the rollback test is isolated, the requested-chain
stamp threads through correctly, and the suite is green on real
Postgres/Redis. The blocker is a **new defect introduced by the
remediation itself**: MISC-8b's `LookupError.Response` persist branch
feeds arbitrary upstream bytes into a `JSONB` column, so a non-JSON
error body (the common 5xx gateway-page case) now rolls back the entire
audit row that used to persist cleanly.

---

## Round-1 finding verification (R1–R7, MISC-8)

| # | Verdict | Evidence |
|---|---------|----------|
| R1 — rollback test on shared `check_wallet_test` | **FIXED** | `tests/provider_observations_test.go:177-180` — `strings.Replace(testutil.TestDSN(), "check_wallet_test", "check_wallet_rollback_test", 1)` → `testutil.RequireTestPoolDSN(t, dsn)`. `_test` suffix guard still enforced: `RequireTestPoolDSN` → `assertTestDatabase` (`testutil/testdb.go:86`, guard at `testdb.go:36-46`). Live proof: `check_wallet_rollback_test` exists at schema v6 with `provider_observations` present; `check_wallet_test` remains at v6 with all 5 seed `data_sources` intact — the down-migration ran on the scratch DB, not the shared one. `go test -run MigrationRollback` → PASS (0.481 s). |
| R2 — `requested_chain` fallback on failure rows | **FIXED** (residual scope note below) | `provider.go:181-184` adds `LookupError.RequestedChain`; `StampRequestedChain` at `provider.go:203-207`. Stamped on the `cli.Do` error path in all three adapters with the correct literal token: goplus `adapter.go:143` → `net.ChainReference` (`"1"`/`"56"`), publicaml `adapter.go:167` → `chain` (`"ETH"`/`"BSC"`), chainabuse `adapter.go:148` → `net.Code`. Service `requestedChain(lr, lerr, net)` at `service.go:317-326` — preference order result → error → `net.Code` exactly as specified; persisted via `service.go:302` → `RequestedChain: reqChain` at `service.go:358`. |
| R3 — `refresh=1` + Redis outage lost `bypassed` | **FIXED** | `cache.go:90-108` — `refreshAllowed` returns `(granted, cacheDown)`: `!refresh` → `(false,false)` early return; `s.rdb==nil` → `(true,true)`; `SetNX` error → `(true,true)`; `SetNX` ok → `(ok,false)`. `service.go:187-189` — `bypass := refresh && granted`; `cacheDown := s.rdb == nil || guardDown`; refresh + outage → bypass → fetch → `out.Cache = "bypassed"` (`service.go:225-227`). Cooldown semantics unchanged: `SetNX EX s.cooldown` (60 s default, `service.go:78-79`) is still the single guard call, still ordered before `get` — one bypass per window per key. |
| R4 — sibling-cancel test never failed a sibling | **FIXED, honest** | `service_test.go:329-348` — `fast` returns `&provider.LookupError{Kind: ErrUpstream5xx}` instantly; `slow` sleeps 40 ms then succeeds. Asserts aggregate `StatusPartial`, `"UPSTREAM_5XX"` on goplus, `"OK"` on publicaml — a genuinely failing sibling while the other is in-flight, not a weakened assertion. |
| R5 — `?providers=goplus,goplus` duplicate blocks | **FIXED** | `service.go:142-164` — `selectProviders` (the remediation doc calls it `resolveProviders`; the actual symbol is `selectProviders` — doc nit only) dedupes via a `seen` set → one block for duplicate names; unknown names still `return nil, ErrUnknownProvider` (→ 400 at `handler.go:65-68`). |
| R6 — OpenAPI evidence example / `persisted` doc | **FIXED** | `api/openapi.yaml` — example evidence now `{ "field": "sanctioned", "value": "1", "data_source": "SlowMist,BlockSec" }`, matching `goplus/mapper.go:85-87`. `persisted` doc now reads "on a `cached:true` hit it reflects the original fetch's outcome, not this request" — accurate to `service.go:207` (`pr.Persisted = e.Persisted`). |
| R7 — caller cancel counted as breaker failure | **FIXED** | `client.go:199-203` — `if ctx.Err() == nil { c.breaker.Report(false) }` with rationale comment. Attempt timeouts (`attCtx.Err() != nil` while `ctx.Err() == nil`, `client.go:230-231`) still report — only the caller-dead case is filtered. `TestDo_BreakerOpensAfterConsecutiveFailures` still passes. |
| MISC-8a — https→http same-host redirect | **FIXED** | `client.go:147-151` — `via[0].URL.Scheme == "https" && req.URL.Scheme == "http"` → `ErrUseLastResponse` → surfaced as typed `ErrInternal` via the `>=300` branch (`client.go:260-262`). Comparing to `via[0]` (original) also refuses multi-hop `https→https→http`. |
| MISC-8b — persist payload precedence | **FIXED but introduced a new defect — see finding 1** | `service.go:339-344` — precedence is exactly `lr.Raw` → `lerr.Response` → `{"attempt":true}` marker. |

---

## New issues introduced by remediation (severity, file:line, evidence)

### 1. MEDIUM — `lerr.Response` persisted verbatim into `payload JSONB`: non-JSON upstream error bodies now roll back the entire audit row

`internal/enrichment/service.go:339-344`:

```go
payload := []byte(`{"attempt":true}`)
if lr != nil && len(lr.Raw) > 0 {
    payload = lr.Raw
} else if lerr != nil && len(lerr.Response) > 0 {
    payload = lerr.Response
}
```

`payload` lands in `provider_observations.payload JSONB NOT NULL`
(`migrations/000006_provider_observations.up.sql`; param is `[]byte` at
`internal/db/provider_observations.sql.go:29` — Postgres validates the
JSONB input server-side). `lerr.Response` is `readSnippet(resp.Body)` —
raw upstream bytes capped at 4 KB, **not** JSON (`client.go:252` on 5xx,
`client.go:258` on 4xx). Two reachable invalid-JSON shapes:

- Non-JSON error bodies — HTML/plaintext gateway pages are the *normal*
  case for upstream 5xx (Cloudflare/nginx `502 Bad Gateway` pages), and
  arbitrary 4xx bodies.
- A valid-JSON upstream body > 4 KB — `readSnippet` truncates mid-token
  at 4 KB (`client.go:352-355`) → invalid JSON.

Either way the `INSERT` errors with `22P02 invalid input syntax for type
json` → `persist` returns false → the transaction rolls back → **the
whole audit row is lost** — `requested_chain`, `status`, `http_status`,
`latency_ms`, and the typed `error` JSONB all gone, not just the
payload. Pre-remediation, failure rows always persisted the
`{"attempt":true}` marker successfully; the fix's stated goal
("never an empty payload") is now achieved at the cost of "sometimes no
row at all". This is exactly the degraded-failure path the audit table
exists for (F5, plan.md:53-54, README "Every fetch persists a
provider_observations audit row").

Impact is honestly surfaced — `persisted:false` + warn log
(`service.go:303-307`) and provider status is untouched — so this is an
audit-integrity loss, not a hidden clean verdict. Consistent with
round-1's bar for MEDIUM: a reachable real-world path violating a stated
contract.

**Required fix:** validate before selecting — e.g. only use
`lerr.Response` when `json.Valid(lerr.Response)`, otherwise wrap the
snippet in an envelope (`{"upstream_error_body": "<string>"}` or
base64) — or always wrap. One-line guard or one-line envelope.

### 2. LOW — residual `requested_chain` fallback on adapter-side post-response errors

R2's stamp covers `cli.Do` failures only. Adapter-local mapping errors
created via `provider.Errorf` carry no `RequestedChain`: goplus
`mapResponse` DEGRADED (`adapter.go:163-166`) / MALFORMED
(`adapter.go:160-161`) / `mapResult` errors (`mapper.go:58-59`);
publicaml `mapResponse` DEGRADED/MALFORMED (`adapter.go:202-207`,
plus `mapEntity` errors at `adapter.go:220-222`); chainabuse
`json.Unmarshal` MALFORMED (`adapter.go:153-154`). For these, `lr==nil`
and `lerr.RequestedChain==""` → `requestedChain` falls back to
`net.Code` → persisted rows record `"ethereum"`/`"bsc"` even though the
literal token (`"1"`/`"56"`, `"ETH"`/`"BSC"`) *was* dispatched upstream
and a response was received. Same defect class as round-1 finding 2,
narrower surface — the HTTP exchange completed, so §4.6 "lưu chain
requested" arguably applies. A `StampRequestedChain` call on the
`mapResponse`/`mapEntity` error returns would close it.

### 3. LOW — new remediation behaviors have no direct tests

Verified by code reading only; none of the new behaviors are exercised:

- Stamped `RequestedChain` on error paths — adapter tests assert
  `res.RequestedChain` on *success* only (`goplus/adapter_test.go:68`,
  `publicaml/adapter_test.go:66`, `chainabuse/adapter_test.go:104`);
  `TestEnrich_PersistsFailureRowToo` (`service_test.go:257-276`) uses a
  bare `LookupError` and never asserts `requested_chain` — the persist
  of the literal token on failure is untested.
- `refresh=1` + Redis outage → `cache:"bypassed"` — the outage test
  (`tests/enrichment_failure_test.go:28-71`, `service_test.go:218-231`)
  uses no refresh; the guardDown→marker path is unexercised.
- `?providers=goplus,goplus` dedup — no test.
- `https→http` redirect refusal — `TestDo_CrossHostRedirectRefused` /
  `TestDo_SameHostRedirectFollowed` exist (`client_test.go:295-325`)
  but no downgrade case.
- `ctx.Err() != nil` → skip `Report(false)` — no test asserting the
  breaker stays closed across caller cancels.

Each is a small, reachable regression surface; one assertion per
behavior would cover all of them.

### 4. INFORMATIONAL — misc

- `strings.Replace(TestDSN(), "check_wallet_test", …)`
  (`provider_observations_test.go:178`) is literal-substring dependent:
  a custom `TEST_DATABASE_URL` lacking that exact substring silently
  no-ops → the "dedicated" DB becomes whatever the env points to (still
  `_test`-guarded, but possibly shared). Deriving the scratch name from
  the parsed DB name would be env-robust.
- 429 bodies are not captured into `Response` (`client.go:244-247` sets
  no `Response`) → 429 rows persist the `{"attempt":true}` marker;
  `Retry-After` is captured. Pre-existing shape, consistent with the
  marker design — noted only.
- Stamping pre-wire failures (circuit_open, rate_limited, limiter
  saturation) records the resolved-but-never-sent token. Arguably more
  accurate than `net.Code` for audit intent ("the token that would have
  been sent"); the field doc (`provider.go:182-184`) now says
  "resolved before dispatching", which covers it. No action.
- Remediation doc names `resolveProviders`; the real symbol is
  `selectProviders`. Doc nit.

---

## Re-audit results (regressions + contract checks)

- **`refreshAllowed` signature change** — cooldown semantics preserved:
  `SetNX key 1 EX s.cooldown` is the only Redis call in the guard, still
  issued exactly once, still ordered before `get`. `(false,false)` early
  return on `!refresh` preserves the zero-Redis-call path. No ordering
  bug: guard → get → singleflight → fetch → put is unchanged.
- **`StampRequestedChain` vs wrapped errors** — safe. `cli.Do` returns
  every error unwrapped via `c.fail(err)` (`client.go:325-341`); all
  producers construct `*LookupError` (`limiter.go:28,33,47`,
  `breaker.go:57,65`, every `lookupErr`/`provider.Errorf` site in
  `client.go`). The direct `err.(*LookupError)` assertion therefore
  lands on the real pointer at all three call sites. publicaml order is
  correct: stamp (`adapter.go:167`) mutates `*le` in place *before*
  `enrichError` copies it (`cp := *le`, `adapter.go:188`) — the copy
  keeps `RequestedChain` and `Response`. Caveat for future callers: the
  assertion is not `errors.As` — a wrapped error would silently no-op;
  fine today since no caller wraps before stamping.
- **Dedup error ordering** — preserved. `seen[name]=true` is set before
  the existence check (`service.go:155-159`), so `?providers=x,y,z` with
  unknown `y` errors at the first `y` exactly as before; duplicate
  unknown names (`y,y`) also error at the first occurrence, identical to
  pre-change.
- **Breaker starvation** — none. The only skipped report is
  `ctx.Err() != nil` (caller cancel/deadline). Attempt timeouts still
  report (`client.go:230-231` → `ctx.Err()==nil` → `Report(false)`), as
  do transport/5xx/4xx/3xx/body failures. A concurrent failure alongside
  a dead caller ctx under-counts, never over-counts — conservative
  direction, and a cancelled request is dead anyway. Comment at
  `client.go:200-201` documents the rationale.
- **§5 invariants / plan contract** — no new violations found: `cache:
  "bypassed"` is an explicit marker (never a status); typed failure
  statuses and the CHECK vocabulary unchanged; DISABLED still excluded
  from `attempted` (`service.go:448-451`); negative entries still replay
  with original status; provider_score still passthrough-only;
  exposures still separate from observations; no auth material logged.
  Pre-existing files re-diffed — all additive/equivalent, no scope creep
  (17 modified files match the round-1 set; tree grew by 0 files vs
  round-1's 31 `git status` entries).

---

## Fresh verification evidence (commands actually run + results)

| Command | Result |
|---|---|
| `gofmt -l .` | empty (exit 0) |
| `go vet ./...` | clean (exit 0) |
| `go build ./...` | clean (exit 0) |
| `go test -count=1 ./...` | **ALL PASS** — `tests` 25.999 s, `internal/enrichment` 1.020 s on real `check_wallet_test`/`check_wallet_enrichment_test`/`check_wallet_rollback_test` + Redis |
| `go test -count=1 -race ./...` | **ALL PASS** — `tests` 66.115 s, race-clean |
| `go test -count=1 -v -run MigrationRollback ./tests/` | PASS 0.481 s — against `check_wallet_rollback_test` |
| `sqlc generate` in `/tmp/sqlc-check-r2` copy + `diff -r internal/db` | **IDENTICAL** — no hand-edits to generated code |
| `go mod tidy -diff` | empty — go.mod/go.sum canonical (`kylelemons/godebug` indirect remains, benign) |
| `psql check_wallet_test` | `schema_migrations` v6 dirty=f; `data_sources` = chainabuse/goplus/ofac_sdn/publicaml/wallet_attribution (`api`/`snapshot` typed, active); `provider_observations_status_check` = 11-value CHECK verified via `pg_get_constraintdef` |
| `psql check_wallet_rollback_test` | exists, v6 dirty=f, `provider_observations` present (post-test re-up state), 0 rows — proof the rollback test ran on its own DB |
| `psql check_wallet` (dev) | v6 dirty=f |
| `redis-cli -p 6380 -n {0,9,10} SCAN` | DB10: `provider:{goplus,publicaml}:ethereum:0x…0e01` (exact key pattern); DB9: `rl:1:…` rate-limit key; DB0: `sanctions:current_snapshot` — no stray keys |
| `git status --short` start vs end | identical, 31 entries — audit ran read-only; only this report added |

**Fix summary for next round:** required — finding 1 (JSONB-validity of
persisted `lerr.Response`; one-line `json.Valid` guard or envelope).
Recommended — finding 2 (stamp adapter-side map errors to close the
`requested_chain` gap), finding 3 (five small assertions covering the
remediation behaviors).

Status: DONE
