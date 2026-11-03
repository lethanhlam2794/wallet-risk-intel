# Devin Review — Round 3

Verdict: CHANGES_REQUESTED

Every round-2 fix is verifiably in place and the full suite is green on
real Postgres/Redis — but the headline fix has a **residual defect in the
same class it was required to close**: `json.Valid` gates Go syntax, while
Postgres JSONB input is stricter. Three reachable payload shapes still roll
back the audit row, and the new envelope path can itself emit a
Postgres-illegal `\u0000` escape. Empirically proven end-to-end below.

---

## Round-2 finding verification (N1–N4)

### N1 — `json.Valid(lerr.Response)` gate: **PARTIAL — residual MEDIUM defect**

`internal/enrichment/service.go:339-354`:

```go
payload := []byte(`{"attempt":true}`)
if lr != nil && len(lr.Raw) > 0 {
    payload = lr.Raw
} else if lerr != nil && len(lerr.Response) > 0 {
    if json.Valid(lerr.Response) {
        payload = lerr.Response
    } else {
        payload, _ = json.Marshal(map[string]any{
            "upstream_error_body": string(lerr.Response),
        })
    }
}
```

Checklist answers, each verified by executing real code + real Postgres:

**(a) `json.Marshal` of a map can never produce invalid JSONB — FALSE at
the Postgres layer.** Marshal always emits valid JSON *syntax*, but a
literal NUL byte in the body becomes a `\u0000` escape that Postgres
rejects. Proven: `json.Marshal({"upstream_error_body":"\x00\x01err"})` →
`{"upstream_error_body":"\u0000\u0001err"}` (valid Go JSON); then
`SELECT '{"upstream_error_body":"\u0000\u0001err"}'::jsonb` →
`ERROR: unsupported Unicode escape sequence / \u0000 cannot be converted
to text`. So a binary-ish upstream error body containing `\x00` →
`json.Valid` false → envelope → **Postgres still rejects the INSERT →
audit row lost.** The "safe" path isn't.

**(b) Valid non-object JSON (`"str"`, `123`, `[1,2]`) — ACCEPTABLE +
honest.** `json.Valid` true → verbatim; `jsonb_typeof` returns
`string|number|array` for each on real Postgres — JSONB accepts scalar
roots, and the payload remains the verbatim upstream body (honest
evidence). ✓

**(c) Empty `lerr.Response` — still `{"attempt":true}`.** The
`len(lerr.Response) > 0` guard at `service.go:342` preserves the marker
fallback. ✓

**(d) Partial-UTF8 tail — `json.Valid` does NOT reject invalid UTF-8**
(proven: `json.Valid([]byte("{\"e\":\"caf\xe9\"}"))` → `true`). The
`string()` cast + `json.Marshal` substitutes U+FFFD, so the envelope can
never emit invalid UTF-8 — that substitution is PG-safe and acceptable.
**But** the same tolerance cuts the other way: Go-valid JSON carrying
`\u0000`, an unpaired surrogate (`\ud800`), or raw non-UTF-8 bytes inside
strings all pass `json.Valid` and go verbatim — and Postgres rejects all
three (`invalid input syntax for type json` / `unsupported Unicode escape
sequence` / `invalid byte sequence for encoding "UTF8": 0xe9` — all
proven live above).

**Same edge on the untouched `lr.Raw` branch** (`service.go:340-341`): a
success body containing `\u0000`/surrogate escapes or non-UTF-8 bytes in
string fields parses fine in the adapter (`json.Unmarshal` substitutes
U+FFFD) → `Raw` persisted verbatim → Postgres rejects → row lost on the
**success** path too. Reachable: serializers (Python `json.dumps`,
Jackson, Newtonsoft) routinely emit `\u0000` for NUL bytes inside
exception/report text — PublicAML entity fields carry exactly such
upstream content.

**Impact — identical mechanism to round-2 finding 1:** INSERT errors →
`persist()` false → transaction rolls back → the whole audit row
(status, `requested_chain`, `http_status`, `latency_ms`, `error` JSONB)
is lost; surfaced honestly as `persisted:false` + warn log
(`service.go:302-307`). The contract — "every fetch persists an audit
row" / remediation doc "the audit row now always persists" — remains
violable on reachable input. Narrower than the HTML-body case (requires
upstream bodies carrying `\uXXXX` escapes or NUL/non-UTF-8 bytes) but
genuinely reachable, and it is the exact defect class the round was
called to close.

**Required fix:** the gate must match Postgres input rules, not Go's
scanner — e.g. sanitize the envelope string (`strings.ToValidUTF8` +
strip `\x00`, or base64 the raw bytes) and apply equivalent normalization
to every payload source (`lr.Raw` included); a `\u0000`/NUL/non-UTF-8
test against real Postgres proves the row lands. One-line-ish either way.

### N2 — `RequestedChain` stamping: **FIXED**

Every error path that completes (or enters) an HTTP exchange now stamps
the correct literal token:

| Path | Evidence |
|---|---|
| goplus Do-path | `goplus/adapter.go:143` → `net.ChainReference` (`"1"`/`"56"`) |
| goplus map-path (MALFORMED envelope, DEGRADED code≠1, `mapResult` error) | `adapter.go:152` → `net.ChainReference`; `mapResult` errors are `*LookupError` via `provider.Errorf` (`mapper.go:59`) so the `err.(*LookupError)` assertion lands |
| publicaml Do-path | `publicaml/adapter.go:167` → `chain` (`"ETH"`/`"BSC"`); stamped *before* `enrichError` copies (`adapter.go:188-193` — `cp := *le` keeps the stamp + `Response`) |
| publicaml map-path (MALFORMED envelope, DEGRADED `{error}`, `mapEntity` errors) | `adapter.go:174` → `chain`; `mapEntity` errors all `provider.Errorf` (`mapper.go:80,101`) |
| chainabuse Do-path | `adapter.go:148` → `net.Code` |
| chainabuse unmarshal MALFORMED | `adapter.go:154-156` → `net.Code` |

No missed post-dispatch path. Pre-dispatch failures correctly fall back
to `net.Code`: goplus `buildRequest` (`adapter.go:137-139`), publicaml
`chainByCode` miss (`adapter.go:142-146` — zero traffic asserted by
`TestLookup_UnknownNetworkRejectedClientSide`), marshal/`NewRequest`
(`adapter.go:152-159`), chainabuse `url.Parse`/`NewRequest`
(`adapter.go:133-143`). Honest: no token was dispatched (or none exists).
For chainabuse the API is address-keyed — `net.Code` is the honest scope
record, test-documented (`adapter_test.go:102-104`).

### N3 — New tests honest: **FIXED** (round-2's wider list only partially covered — advisory)

- `TestEnrich_PersistsNonJSONErrorBody` (`service_test.go:278-307`) —
  HTML 502 body → real Postgres row: `persisted:true`,
  `status=UPSTREAM_5XX`, `requested_chain="1"` (asserts the stamp
  end-to-end), payload `JSONEq` the exact envelope. Reproduces the
  round-2 defect scenario; before the fix it would fail on `Persisted`.
- `TestEnrich_PersistsJSONErrorBodyVerbatim` (`service_test.go:309-328`)
  — `{"error":"unknown chain"}` stored verbatim.
- `TestEnrich_DuplicateProviderFilterDedupes` (`service_test.go:330-339`)
  — `["goplus","goplus"]` → one block, one call.

Assertions are real, not weakened. Advisory: round-2's recommended list
also named refresh+Redis-outage `bypassed`, https→http downgrade, and
ctx-cancel breaker-skip cases — those remain untested (checked
`client_test.go`, `service_test.go`, `tests/` — no coverage added for
them).

### N4 — Scratch-DSN guard: **FIXED, fail-closed**

`tests/provider_observations_test.go:178-185` —
`require.NotEqual(t, testutil.TestDSN(), dsn)` at `:183`: if
`TEST_DATABASE_URL` lacks the `check_wallet_test` substring the
`strings.Replace` no-ops → `dsn == TestDSN()` → `require` fails → the
schema-mutating test refuses to run on the shared DSN (a loud failure,
not a silent skip — correct posture). `_test` suffix guard intact:
`RequireTestPoolDSN` → `assertTestDatabase` (`testutil/testdb.go:36-46,
86`). Live proof below: `check_wallet_rollback_test` still exists and the
shared DB's seeds are intact.

---

## Regression re-audit (round-1 fixes + full diff)

All round-1 fixes intact; no new defects found beyond finding 1:

- **R1 scratch DB** — `RequireTestPoolDSN` used with derived
  `check_wallet_rollback_test` (`provider_observations_test.go:178-185`).
- **R3 refresh guard** — `refreshAllowed` returns `(granted, cacheDown)`
  (`cache.go:90-108`): `!refresh`→`(false,false)` zero Redis calls;
  `rdb==nil`/SetNX err → `(true,true)` fail-open + outage flag;
  `service.go:187-189,225-227` → refresh+outage → `cache:"bypassed"`.
  Cooldown still single `SET NX EX` before `get`, ordered correctly.
- **R4 sibling-cancel test** — `service_test.go:392-411` genuinely fails
  a sibling (instant `ErrUpstream5xx`) while the 40 ms sibling completes;
  asserts PARTIAL + `UPSTREAM_5XX`/`OK` per source.
- **R5 dedup** — `selectProviders` `seen` set (`service.go:150-163`);
  `seen` marked before the existence check → duplicate-unknown ordering
  identical to pre-change.
- **R6 OpenAPI nits** — example `evidence` is
  `{"field","value","data_source"}`; `persisted` doc says "reflects the
  original fetch's outcome" on cached hits — accurate to
  `service.go:207`.
- **R7 caller-cancel breaker filter** — `client.go:199-203`: report only
  when `ctx.Err() == nil`; attempt timeouts (`attCtx` dead, caller alive,
  `client.go:230-231`) still report. Under-counts only alongside a dead
  caller — conservative direction.
- **MISC-8a** — same-host `https→http` refused via `via[0]` scheme
  comparison (`client.go:147-151`) — multi-hop downgrade covered.
- **MISC-8b precedence** — `lr.Raw` → `lerr.Response` → marker
  (`service.go:339-354`) — see finding 1 for the residual edge.
- **Diff scope** — `git diff` on all 17 tracked files re-read: purely
  additive (config keys/env, `ProviderObservation` model, `Deps` +
  nil-gated route, `ProviderRefresh` key helper, provider collectors,
  `RequireTestPoolDSN` extraction, scoped count in
  `api_wallets_test.go`, 5-source assertion strengthening). No pre-phase
  behavior touched; `api_wallets_test.go` narrowing is honest
  (get-or-create idempotency per wallet, correctly scoped for parallel
  packages).

Advisories (non-blocking):
- `metrics.ProviderHooks(source)` ignores its `source` arg — labels come
  from the client `cfg.Source` (correct values today; dead param).
- `entry.LatencyMS` assigned twice (`service.go:288` vs `:295`) — harmless.
- `int4Ptr64` truncates int64→int32 for `latency_ms` — bound ~24 days,
  inconsequential.
- `tests/enrichment_*_test.go` share `check_wallet_test` + Redis DB 9 —
  sequential within the package and `provider_observations` writers live
  in dedicated DBs, so no new hazard (pre-existing API-test convention).

## Standing invariant spot-check results

- **Unknown ≠ safe**: `INSUFFICIENT_DATA` when nothing attempted
  (`service.go:107-110`); `"0"` flags kept as signals, never verdicts
  (`goplus/mapper.go:78-79`); `aml_score:0` asserted as data
  (`publicaml/adapter_test.go:103-114`).
- **Failure never flattened**: typed statuses end-to-end (`statusFor`,
  `errorInfo`); negative cache embeds `status` and replays it verbatim
  (`service.go:195-210`) at 60 s TTL (`cache.go:70-73`) — cannot replay
  clean; API-level proof `enrichment_failure_test.go:110-118`.
- **DISABLED = zero outbound**: service early-returns before any fetch,
  cache write, or persist (`service.go:179-184`); adapter self-guards
  (`chainabuse/adapter.go:129-132`); counting-transport test proves 0
  calls (`chainabuse/adapter_test.go:42-56`).
- **`provider_score` ≠ internal score**: passthrough only
  (`fillData`); documented in OpenAPI, README, `provider.go:133-136`.
- **reported ≠ verified**: all three mappers emit
  `VerificationStatus:"reported"`; OpenAPI enum `[reported, unverified]`.
- **Direct vs indirect separation**: `mapEntity` puts
  `direct_attribution` → Observations and `outbound_exposure.items` →
  Exposures only (`publicaml/mapper.go:97-140`); exposures never fold
  back (`fillData`).
- **Resilience invariants post-R7**: limiter token per attempt inside the
  retry loop, bounded wait + reservation return on cancel
  (`limiter.go:24-51`); 429 never sleeps/retries (`client.go:239-247`);
  retries only 5xx/transport/attempt-timeout; semaphore cap; breaker
  consecutive-only + single half-open probe (`breaker.go`); same-host
  redirects ≤3 hops + downgrade refusal; 4 MB body cap; per-request
  allowlist — all intact.
- **No secrets/Authorization logged** — grep across new code: zero hits;
  only the env-var *name* in the disabled notice (`main.go:167`).
- **Provenance/DDL**: CHECK vocabulary 11 statuses live-verified;
  `requested_chain`/`payload` NOT NULL; seeds `api`-typed; down
  symmetric; prune in-tx.

## Fresh verification evidence

| Command | Result |
|---|---|
| `gofmt -l .` | empty (exit 0) |
| `go vet ./...` | clean (exit 0) |
| `go build ./...` | clean (exit 0) |
| `go test -count=1 ./...` | **ALL PASS** — `internal/enrichment` 1.672 s, `tests` 24.874 s on real `check_wallet_test`/`check_wallet_enrichment_test`/`check_wallet_rollback_test` + Redis |
| `go test -count=1 -race ./...` | **ALL PASS** — `tests` 87.409 s, race-clean |
| `go mod tidy -diff` | empty — go.mod/go.sum canonical |
| `sqlc generate` in `/tmp/sqlc-r3` copy + `diff -r internal/db` | **IDENTICAL** — no hand-edits to generated code |
| `psql check_wallet_test` | `schema_migrations` v6 dirty=f; 5 `data_sources` (3 `api`, 2 `snapshot`, all active); `provider_observations_status_check` = 11-value CHECK verified via `pg_get_constraintdef` |
| `psql ::jsonb` probes | `'"str"'/'123'/'[1,2]'` accepted (N1b ✓); `'{"e":"\ud800"}'` → invalid input syntax; `'{"e":"\u0000"}'` + `'{"upstream_error_body":"\u0000\u0001err"}'` → unsupported Unicode escape; `convert_from(…0xe9…,'UTF8')` → invalid byte sequence (finding 1 proof) |
| `go run` scratch `/tmp/r3-check` | `json.Valid` returns true for `\ud800`, `\u0000`, raw `\xe9`-in-string, truncated-multibyte tail — and false for HTML/NUL bodies; envelope marshal emits `\u0000` escapes (finding 1 proof) |
| `redis-cli -p 6380 -n {0,9,10} SCAN` | DB0 `sanctions:current_snapshot`; DB9 empty; DB10 `provider:publicaml:ethereum:0x…0e01` (exact key pattern) — no stray keys |
| `git status --short` start vs end | identical, 31 entries — audit ran read-only; only this report added |

**Fix summary for next round:** required — finding 1 (payload must be
Postgres-JSONB-safe, not merely `json.Valid`: sanitize or envelope
`lerr.Response` **and** `lr.Raw`, incl. the NUL/`\u0000`/surrogate/
non-UTF-8 classes; add a real-DB test feeding those shapes).
Recommended — the three remaining uncovered remediation behaviors
(refresh+outage `bypassed`, https→http downgrade, ctx-cancel breaker
skip).

Status: DONE
