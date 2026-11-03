# Devin Review — Round 1

Verdict: CHANGES_REQUESTED

One required fix (test-infra isolation defect on the shared database) plus
advisory findings. The production code itself is faithful to the plan and
the domain invariants — every Critic amendment F1–F9 is verifiably
implemented, all invariant boundaries hold, and the fresh verification
suite is green. This is not a design rejection; the blocker is a
nondeterministic test the implementer's own rules already ban.

---

## Per-amendment verification (F1–F9: fixed? evidence file:line/test)

| # | Verdict | Evidence |
|---|---------|----------|
| F1 — PublicAML static chain map | **FIXED** | `internal/provider/publicaml/mapper.go:151-154` — `chainByCode = {"ethereum":"ETH","bsc":"BSC"}`; never `strings.ToUpper`; unknown code → typed `ErrInternal` at `adapter.go:142-146` before any wire call. `adapter_test.go:116-142` asserts the literal request body `chain=="ETH"`/`"BSC"` and a single-element `addresses` array; `TestLookup_UnknownNetworkRejectedClientSide` proves zero upstream traffic for unmappable codes. `requested_chain` persisted verbatim (`service_test.go:245-253` asserts `"ETH"` in DB). |
| F2 — Bounded synchronous path | **FIXED** | Parent fan-out deadline 25 s env-tunable: `service.go:82,123` + `ENRICHMENT_DEADLINE` (`config.go`). Token per HTTP attempt: `client.go:168-187` — `limiter.Wait` sits *inside* the retry loop (proven by `TestDo_LimiterConsumesTokenPerAttempt`, `client_test.go:198-222`). Bounded limiter wait: `limiter.go:30-38` — `MaxLimiterWait` default 250 ms → fail-fast `rate_limited` carrying `RetryAfter: delay`; cancelled wait returns the reservation (`limiter.go:42-50`). 429 never sleeps/retries: `client.go:230-238` returns typed `ErrRateLimited` immediately — test asserts `hits==1` with `MaxRetries=2` (`client_test.go:91-108`). Retries bounded to transport/attempt-timeout/5xx only (`client.go:206-257`); caller-cancel never retried. |
| F3 — `refresh=1` quota guard | **FIXED** | `cache.go:90-103` — `SET NX EX 60` on `provider:refresh:{src}:{net}:{addr}` (`keys.go:32-36`). In-cooldown → `bypass=false` → normal cached request (`service.go:182-185`); a miss still fetches. `TestEnrich_RefreshBypassesThenCooldownAllowsCache` proves both halves. |
| F4 — No sibling-cancel fan-out | **FIXED (code); test gap noted (issue #4)** | `service.go:126-136` — plain `errgroup.Group{}` (no `WithContext`), each goroutine writes a disjoint `res.Providers[i]` and returns nil; a failing provider cannot fabricate `ctx-canceled` on siblings. Verified by construction; the dedicated test does not actually exercise a failing sibling (see findings). |
| F5 — Pinned `provider_observations` DDL | **FIXED** | `migrations/000006_*.up.sql` — CHECK over 11-status vocabulary (verified live via `pg_get_constraintdef`), `requested_chain TEXT NOT NULL`, `payload JSONB NOT NULL`, nullable `provider_score/error/http_status/latency_ms`, `fetched_at TIMESTAMPTZ NOT NULL DEFAULT now()`, index `(source_id, wallet_id, fetched_at DESC)`. Prune inside the same tx: `service.go:331-357` (`pool.Begin` → `qtx.Insert` → `qtx.Prune` → `Commit`). Seeds `goplus/publicaml/chainabuse` as `source_type='api'` (verified in DB: 5 sources). Down migration symmetric (deletes seeds, drops table; rollback test exists). |
| F6 — Hardcoded allowlists | **FIXED** | `defaultAllowedHosts` per adapter (goplus:29, publicaml:30, chainabuse:23); `validateBaseURL` enforces https + allowlist at construction (e.g. `goplus/adapter.go:110-124`), test-only `WithInsecureHTTP`/`WithAllowedHosts` hooks. `TestNew_RejectsDisallowedBaseURL` + `TestNew_RejectsHTTPInProduction`. Cook-report claim verified: the allowlist genuinely makes env-URL stubbing impossible — that is the feature, not a gap. |
| F7 — §13 extension documented | **FIXED** | `provider.go:119-125` — `LookupResult` doc states it is a deliberate spec §13 extension carrying raw payload, provider score, literal requested chain, fetch timestamp per §4.6; `Provider` interface doc records `Enabled()` rationale (`provider.go:143-151`). |
| F8 — GoPlus mapping + OK-empty | **FIXED** | `goplus/mapper.go:18-45` — exact confident mappings (sanctioned/phishing/money_laundering/stealing_attack/mixer/darkweb), `honeypot_related_address`→OTHER, `contract_address`/`reinit`/`data_source` signals-only; unmapped `"1"` flags → OTHER never dropped. `result:null`/`{}` → OK-empty (`mapper.go:54-56`) + tests `TestLookup_NullResultParsesOKEmpty`/`TestLookup_EmptyResultParsesOKEmpty`. `"0"` flags → zero observations (verified on real clean fixture). |
| F9 — OpenAPI/docs + no auth logging | **FIXED** | `api/openapi.yaml` — `info.description` now phases 1–4, labels no longer "absent"; route docs state `provider_score`≠internal risk, `0`≠safe under non-OK, `retry_after` seconds, `cache_expires_at` semantics, DISABLED=zero-calls/not-attempted, refresh cooldown, negative-cache non-replay. Grep for `Authorization`/`apiKey` near `log`/`slog`/`print` across non-test code: **zero hits**. Chainabuse-disabled logged at boot without the key (`main.go:170`). |

Additional contract spot-checks: `enriched_at` (not `screened_at`) used
(`model.go:40`); `signals` emits `{}` not `null` (initialized maps +
`TestEnrich_SignalsNeverNull`); `strconv.ParseBool` for refresh
(`handler.go:46-53`, strict → 400 on garbage); `providers=` empty→all /
unknown→400 (`selectProviders`); `cache:"bypassed"` marker on Redis
outage (`service.go:219-221`, API-proven in
`enrichment_failure_test.go:28-71`); `persisted:false` without status
mutation on DB failure (`service.go:292-299`); DISABLED excluded from
`attempted` → `INSUFFICIENT_DATA` (`service.go:425-451`); negative cache
entries self-describing (`cache.go:16-29`) and replaying with original
status (`service.go:188-205` + `TestEnrich_NegativeCacheReplaysAsNonClean`
+ API-level `enrichment_failure_test.go:110-118`); singleflight per
(src,net,addr) (`service.go:210-212`, coalescing proven); 4 MB body cap
(`client.go:259-266`); same-host-only redirects with 3-hop bound
(`client.go:138-148`); per-request allowlist re-check (`client.go:164`).

No typed-nil `*LookupError` returned as `error` anywhere — the three
`var le *provider.LookupError` declarations are all `errors.As` targets
(correct usage).

---

## New issues introduced (severity, file:line, evidence, why it matters)

### 1. MEDIUM — Migration-rollback test mutates schema on the SHARED `check_wallet_test`

`tests/provider_observations_test.go:172-193` —
`TestProviderObservations_MigrationRollback` calls
`database.MigrateTo(dsn, 5)` (verified: `migrate.go:33-46` →
`m.Migrate(5)` rolls the schema DOWN, dropping `provider_observations`
and the three `api` data-source seeds) on `check_wallet_test`, the same
database every parallel package shares.

Two flake directions, both real under `go test ./...` package
parallelism:

- A sibling package's `RequireTestPool` → `database.Migrate(dsn)`
  (`testdb.go:88`) re-ups to v6 between the down-migration and the
  `assert.False(exists)` checks → the rollback test fails spuriously.
- Any sibling asserting seeds/schema (`internal/testutil`
  `TestRequireTestPool_SeededSchema` now asserts all 5 data-source
  codes; enrichment API tests resolve `GetSourceByCode('goplus')`)
  can observe the v5 state → spurious failures elsewhere.

It passed twice in this run; the window is small but the invariant is
already acknowledged by the implementation itself — `service_test.go:82-86`
created `check_wallet_enrichment_test` for exactly this hazard and
`testdb.go:79-83` documents why sharing fails. A schema-mutating test is
the extreme case of that rule and should never have stayed on the shared
DB. Both `go test` runs passing is timing luck, not safety (P-020/G-006).

**Required fix:** point the rollback test at a dedicated scratch
`_test`-suffixed DSN via `testutil.RequireTestPoolDSN` (the mechanism
already exists) — e.g. `check_wallet_rollback_test`.

### 2. LOW — `requested_chain` falls back to `net.Code` on failure rows

`internal/enrichment/service.go:308-313` — when `LookupAddress` errors,
`lr` is nil so the persisted `requested_chain` becomes `"ethereum"`/
`"bsc"` instead of the literal token actually sent upstream (`"1"`/`"56"`
for GoPlus, `"ETH"`/`"BSC"` for PublicAML). For pre-wire failures
(circuit_open, rate_limited, disabled) there *is* no literal token, so
the fallback is honest; but for post-send failures (5xx, timeout after
dispatch) the audit row records a different value than §4.6's "lưu chain
requested" intends. Adapters would need to carry the requested chain in
`LookupError` to fix this completely.

### 3. LOW — `refresh=1` + Redis outage loses the `bypassed` marker

`service.go:182-183` + `cache.go:90-101` — with Redis down,
`refreshAllowed` fails open → `bypass=true` → the cache `get` is skipped
→ `cacheDown` stays false → response omits `cache:"bypassed"` even
though the outage occurred and the write silently failed. The
observability contract ("Redis outage visible, never silent") holds on
the normal path but not the refresh path.

### 4. LOW — `TestEnrich_SiblingFailureDoesNotCancelOthers` doesn't test what it names

`internal/enrichment/service_test.go:330-342` — both fakes *succeed*
(fast + slow); no sibling fails, so the F4 scenario (fast-failing
provider while a slow sibling is in-flight) is never exercised. The code
is correct by construction (verified: plain `errgroup.Group`, no derived
ctx), and `TestEnrich_PartialOnMixedOutcomes` covers failure recording —
but the named guarantee has no direct test. Give `fast` an `err` return.

### 5. LOW — `?providers=goplus,goplus` produces duplicate provider blocks

`service.go:142-159` — no dedup in `selectProviders`; duplicate names in
the filter yield two fan-out goroutines coalesced by singleflight and
two identical blocks in `providers[]`. Cosmetic contract noise.

### 6. LOW — OpenAPI example/doc nits

- Example `evidence: {"flag": "sanctioned"}` — GoPlus evidence is
  actually `{"field","value","data_source"}` (`goplus/mapper.go:85-87`).
- `persisted` doc says "whether **this request** wrote a row", but on a
  cached hit it reports the *original* fetch's persist outcome
  (`service.go:201`). Semantics are defensible; the doc text overclaims.

### 7. LOW — Caller cancellation counts as a breaker failure

`client.go:187-195` — `breaker.Report(false)` runs for every attempt
error including non-retriable caller-ctx cancellation. Five client
disconnects (or the 25 s parent deadline firing on healthy-but-slow
upstream) can open the circuit for 60 s. Defensible (cancel usually
means upstream slowness) but worth a comment or a filtered Report.

### 8. INFORMATIONAL — under-surfaced PublicAML fields / misc

- `aml_score_breakdown.outbound_direct`, `frozen_direct`,
  `attributed_infra_by`, `behavioral_risk` are parsed-tolerated but not
  surfaced in `Signals` (preserved verbatim in the persisted `payload`).
  Conservative under-reporting — not an invariant violation.
- Same-host `https→http` downgrade redirects are permitted by
  `CheckRedirect` (`client.go:138-148`) — a compromised fixed-allowlist
  upstream could pull Authorization over plaintext. Far-fetched; noted.
- Failure rows persist `payload={"attempt":true}` rather than the
  bounded `LookupError.Response` snippet — typed `error` JSONB carries
  the detail; acceptable.
- `go.mod` shows `kylelemons/godebug` as new indirect — `go mod tidy
  -diff` confirms `go.mod`/`go.sum` are canonical; harmless.

---

## Regression check (phases 1–3 paths unchanged? evidence)

Yes — every modified pre-existing file is additive or equivalent:

- `provider.go` — new on-demand section appended after line 87; snapshot
  types (`File`, `Snapshot`, `SnapshotSource`, `CombinedChecksum`)
  byte-identical.
- `server.go` — `Deps.Enrichment` field + nil-gated route block added
  (`server.go:100-103`), same pattern as `Labels`/`Sanctions`/`Admin`;
  no existing route touched.
- `main.go` — `buildEnrichment` added; Deps wiring additive; existing
  services untouched.
- `config.go`, `metrics.go`, `keys.go`, `models.go`, `.env.example`,
  `README.md` — additive fields/keys/docs only; no existing
  keys/collectors renamed.
- `testdb.go` — `RequireTestPool` now delegates to `RequireTestPoolDSN`
  with the same `_test` guard + migrate + truncate sequence; behavior
  preserved.
- `tests/api_wallets_test.go` — count scoped to
  `normalized_address=$1` (**honest fix**, not weakened): the asserted
  invariant is get-or-create idempotency for *this* wallet, and the
  global `count(*)` was inherently racy under cross-package parallelism.
  The scoped assertion proves the same thing more correctly.
- `testdb_integration_test.go` — 2→5 sources with exact code list —
  *strengthened* by migration seeds, not relaxed.
- No skipped/deleted tests (only pre-existing env-gated skip in
  `ofac/parser_real_test.go:18`); providers are httptest-only; Chainabuse
  zero-traffic proven via counting transport
  (`adapter_test.go:42-56`).

---

## Fresh verification evidence (commands actually run + results)

| Command | Result |
|---|---|
| `gofmt -l .` | empty (exit 0) |
| `go vet ./...` | clean (exit 0) |
| `go build ./...` | clean (exit 0) |
| `go test -count=1 ./...` | **ALL PASS** — incl. `checkwallet/tests` 37.235 s and `internal/enrichment` 1.7 s on real `check_wallet_test`/`check_wallet_enrichment_test` + Redis |
| `go test -count=1 -race ./...` | **ALL PASS** — `tests` 73.8 s, race-clean fan-out |
| `sqlc generate` in `/tmp` copy + `diff -r` on `internal/db` | **identical** — no hand-edits to generated code |
| `go mod tidy -diff` | empty — go.mod/go.sum canonical |
| `psql check_wallet_test`: `schema_migrations` | version `6`, dirty `f` |
| `psql check_wallet_test`: `data_sources` | `chainabuse,goplus,ofac_sdn,publicaml,wallet_attribution` (`api`/`snapshot` typed, active) |
| `psql check_wallet_test`: `pg_constraint` | `provider_observations_status_check` = 11-value CHECK + pkey + 2 FKs verified |
| `psql check_wallet` (dev) | version `6`; `provider_observations` exists |
| `redis-cli -n {0,9,10} SCAN provider:*` | DB 10 holds `provider:{goplus,publicaml}:ethereum:0x…0e01` — exact `provider:{src}:{net}:{addr}` pattern; DBs 0/9 empty (test-flushed) |
| `git status` pre/post audit | identical (31 entries) — review ran read-only |

**Acceptance-criteria sweep:** all plan criteria met (typed failure
statuses, cache/refresh semantics, DISABLED zero-calls, green toolchain,
OpenAPI examples). The live-GoPlus/PublicAML curl evidence is the cook's
claim — consistent with DB state (migration 6 on dev DB) and not
independently re-executed here.

**Fix summary for the next round:** required — move
`TestProviderObservations_MigrationRollback` onto a scratch `_test` DB;
recommended — findings 2–7 are small, well-scoped improvements (fail-open
marker on refresh path, dedup providers filter, make the F4 test
actually fail a sibling, literal requested_chain on post-send failures,
breaker cancel classification, two doc nits).

Status: DONE
