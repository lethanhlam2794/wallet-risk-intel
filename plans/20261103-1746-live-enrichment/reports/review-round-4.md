# Devin Review — Round 4

Verdict: APPROVED

**Audit-context caveat (G-001 rule 10):** the working tree changed mid-audit —
`internal/enrichment/service.go` was edited at 22:31 and 22:33 (+ tests) while
the review was in flight. Two of the residual edges I discovered and proved
empirically (uppercase-D surrogate escapes; upstream-controlled `Msg` → unsanitized
`error` JSONB) were fixed in the final state, and the byte-level sanitizer was
rewritten from regexes into a parity-aware scanner, which also closed a third
edge I proved (escaped-backslash fake-pair). All verification below was
performed against the **final** state (service.go @ 22:33:51, service_test.go @
22:34:00): final-state reads, a /tmp replica probe, live `psql ::jsonb` casts,
and focused/full/race test runs that all postdate the last edit.

---

## Round-3 finding verification (N1–N4)

### N1 — `jsonbSafe`/`jsonbText`/`auditPayload`: **FIXED** (final implementation: parity-aware scanner)

`service.go:334-407` — `jsonbText` (ToValidUTF8 + literal-`\x00`→U+FFFD),
`jsonbSafe` = `jsonbEscapeClean(jsonbText(b))` — a sequential byte scanner that
consumes `\\` as a unit (odd-run backslashes mark real escapes; text after an
escaped backslash is literal), rewrites `\u0000` and lone surrogates to
`\ufffd`, and copies valid surrogate pairs verbatim. `auditPayload`
(`service.go:440-449`): sanitized body → `json.Valid` → verbatim; else
`{"upstream_body": jsonbText(raw)}` envelope; empty → `{"attempt":true}`.

| Checklist item | Result | Evidence |
|---|---|---|
| (a) `\u0000`→`\ufffd`, lone `\ud800`→`\ufffd`, land | ✓ | probe output + `psql` rejects originals; tests `nul-escape`/`lone-surrogate`/`lone-surrogate-uppercase-D` PASS on real Postgres |
| (b) valid pair `\ud83d\ude00` preserved verbatim, lands | ✓ | `{"e": "😀"}` on `::jsonb`; test `valid-surrogate-pair-preserved` PASS |
| (c) invalid UTF-8 (`caf\xe9`) → U+FFFD, lands | ✓ | test `invalid-utf8-inside-json` (real `0xe9` byte) asserts `caf\uFFFD` decoded — PASS |
| (d) literal NUL inside JSON → sanitized, lands | ✓ | test `literal-nul-inside-json` PASS |
| (e) binary error body → envelope, lands | ✓ | test `binary-error-body` PASS; `\x01` survives as legal `\u0001` (probed `::jsonb` accepts `\u0001`–`\uffff` range incl. noncharacters) |
| (f) `\x00` sentinel collision | MOOT | scanner version has **no sentinel** — pairs are copied verbatim `s[i:i+12]`; `jsonbText` strips literal NULs before scanning regardless (`service.go:348`) |
| (g) non-adjacent "pair" = lone surrogates | ✓ correct | `x\ud83dY\ude00` → `x\ufffdY\ufffd` (probe); Postgres rejects non-adjacent surrogates as unpaired — rewrite is the Postgres-accurate semantics |
| (h) output always valid JSON for valid input | ✓ | every branch either copies verbatim or writes `\ufffd` at a real-escape position; 28-case probe battery all `valid: true`; plus the `json.Valid` gate → envelope as a second layer |
| (i) envelope cannot emit `\u0000`/surrogates | ✓ | `jsonbText` yields valid UTF-8 without NUL before marshal; marshal never emits `\udXXX` (raw UTF-8) and `\u0000` requires U+0000 (stripped); `\u0001`+ verified PG-legal |
| (j) scalar roots verbatim, honest | ✓ | `'"str"'/'123'/'[1,2]'` all accepted by `::jsonb`; verbatim upstream body preserved |
| (k) `lr.Raw` > `lerr.Response`; empty → `{"attempt":true}` | ✓ | `service.go:462-467` precedence; `:448` marker |

**Plus a fix beyond the checklist:** the `error` JSONB column is now sanitized —
`persist()` `service.go:470-476` copies `ErrorInfo` and runs `jsonbText` on
`Message` before marshal. Verified end-to-end: upstream
`{"error":"bad key\u0000refused"}` → `json.Unmarshal` decodes literal NUL →
`env.Error` → `Msg` (publicaml `adapter.go:191` `cp.Msg` and `:208` `%s`) →
pre-fix `errJSON` emitted `\u0000` → `::jsonb` **rejected**
(`\u0000 cannot be converted to text`) → row lost. Post-fix output
`{"code":"degraded","message":"publicaml: bad key�refused"}` → legal. Column
inventory is now fully covered: `payload` (sanitized), `error` (sanitized),
`requested_chain` TEXT (internal constants only — "ETH"/"1"/net.Code),
`status` (vocabulary), ints/floats/timestamptz.

### N2 — New test integrity: **FIXED / honest**

- `TestEnrich_PersistsJSONBHostileBodies` (service_test.go:331-431) — **10
  subtests** on real Postgres, asserting **decoded** payload values: the
  correct oracle, since `::jsonb` normalizes escapes on output (asserting the
  stored escape spelling would be testing the wrong layer). Row landing is
  proved by `Persisted:true` + SELECT-back. The two parity cases added in the
  final state assert the right split semantics:
  `escaped-backslash-then-lone-low` expects `\ud83d` (literal text preserved) +
  U+FFFD (real lone low neutralized); `escaped-backslash-literal-surrogate-text`
  expects `\uD800` literal text verbatim (fidelity preserved — the old regex
  would have mangled it to `\ufffd`). 10/10 PASS on current code.
- `TestEnrich_RefreshWithRedisDownStillMarksBypassed` — refresh + dead Redis
  (port 6399) → asserts `OK` + `cache:"bypassed"` + `!Cached` — exercises the
  real fail-open path (`refreshAllowed` → `(true,true)` → bypass + fetch).
- `TestRedirectPolicy_SchemeDowngradeRefused` — pure-function table (6
  assertions): same-host downgrade refused, multi-hop https→https→http refused
  (via[0]-anchored), same-scheme allowed both ways, cross-host refused both
  schemes. Gap (advisory): `len(via)>=3` too-many-redirects branch uncovered.
- `TestDo_CallerCancelDoesNotFeedBreaker` — `BreakerFailures=1` is maximally
  sensitive (a single wrongly-counted failure opens the circuit; the final
  healthy request would fail `CIRCUIT_OPEN` and the test catches it explicitly
  by kind-check + `require.NoError`). **Deterministic**: the handler blocks on
  `<-r.Context().Done()` — the request can only end via caller cancel, so
  `ctx.Err() != nil` is guaranteed at classification; the cancel provably
  reaches mid-flight state.
- `git diff` integrity on tracked files: identical to R3 (17 files, +709/−29).
- Advisory: the new `error`-column sanitizer has **no real-DB test** (a `Msg`
  carrying `\x00` → row lands with sanitized `error` JSONB is unasserted —
  the hostile-bodies tests only cover `payload`).

### N3 — `redirectPolicy` extraction: **FIXED, behavior identical**

`client.go:152-165`, wired at `:138`. Semantics match the pre-extraction
closure R3 verified (`via[0]` scheme/host anchoring, ≤3-hop bound,
`ErrUseLastResponse` = surface 3xx → `attempt()` classifies `>=300` as internal,
non-retryable). via[0]-anchored downgrade check catches multi-hop
https→https→http (compares target scheme to the *origin* scheme — independent
of intermediate hops). CheckRedirect contract preserved: nil=follow,
ErrUseLastResponse=surface, generic error → `url.Error` → TRANSPORT (retryable,
same as before). Pure-function test coverage added (above).

### N4 — Full-diff regression re-audit: **no regressions**

- `persist()` ordering: raw select (`lr.Raw` → `lerr.Response`) →
  `auditPayload` (sanitize → `json.Valid` → verbatim / envelope / marker) →
  `errJSON` (sanitize → marshal) → insert + prune in one tx. Correct.
- Input bounds: `lerr.Response` only ever from `readSnippet` (`client.go:257,262`,
  `io.LimitReader 4<<10` = 4 KB — the only `Response:` setters in the tree);
  `lr.Raw` ≤ 4 MB (`MaxBodyBytes`). Rewrites are byte-≤1:1 (`\ufffd` ≡ `\uXXXX`
  6 bytes; copies verbatim) — sanitize cannot balloon.
- `errJSON` separation is correct: `safe := *pr.Error` — the *persisted* copy
  is sanitized; the wire/cache `pr.Error` keeps the provider's verbatim
  message (legal wire JSON; Redis is byte-safe).
- No other Postgres-bound column carries upstream-derived text
  (`requested_chain` TEXT — internal constants only; `Observation.Evidence`
  never reaches a jsonb column).
- Allocations on the persist path: one ToValidUTF8 pass + one scanner pass +
  one builder — bounded by the 4 MB body cap, once per fetch. Not hot-loop.

## Residual edges in the sanitizer

**Found and proved mid-audit; all fixed in the final audited state.**

| Edge (as discovered) | Severity at discovery | Status | Evidence |
|---|---|---|---|
| Uppercase-D surrogate escapes (`\uD800`, `\uDC00`, `\uDBFF`…) slipped the regexes (`\\ud` required lowercase `d`) → verbatim → Postgres rejects; mixed-case pairs were half-mangled into still-rejected output (`\ud83d\uDE00`→`\ufffd\uDE00`) | MEDIUM (same class as R3) | **FIXED** — scanner hex-parse is case-insensitive (`isHex4`/`parseHex4`); `lone-surrogate-uppercase-D` test PASS | `psql`: `{"e":"\uD800"}` → `invalid input syntax`; probe: pre-fix passthrough vs post-fix `\ufffd` |
| `error` JSONB column never sanitized — upstream-controlled `env.Error` → `Msg` → `json.Marshal` emits `\u0000` → INSERT rejected → row lost | MEDIUM (same blast radius; a normal JSON `{"error":…}` 4xx/5xx or `{error}`-on-200 body triggers it) | **FIXED** — `service.go:470-476` `jsonbText(safe.Message)` | `::jsonb` rejected pre-fix marshal; post-fix marshal output legal (probed) |
| Escaped-backslash fake-pair: `{"e":"\\ud83d\udc00"}` — regex matched `\ud83d\udc00` across the `\\` boundary, shield+restore produced byte-identical output → Postgres parses `\\`+literal `ud83d`+**real lone `\udc00`** → reject | LOW-MEDIUM (same class, narrower reachability) | **FIXED** — regexes replaced by parity-aware scanner: `\\` consumed as a unit → `ud83d` kept literal, `\udc00`→`\ufffd` → lands; `escaped-backslash-then-lone-low` test PASS | `psql`: `{"e":"\\ud83d\udc00"}` → `Unicode low surrogate must follow a high surrogate`; probe: verbatim pre-fix vs `\\ud83d\ufffd` post-fix |

Post-scanner residual enumeration (28-case battery + Postgres rules): Postgres
jsonb rejects exactly `\u0000`, unpaired `\ud800`–`\udbff` (not followed by
`\udc00`–`\udfff`), lone `\udc00`–`\udfff`, and invalid UTF-8 — every class is
now rewritten or rejected upstream (`ToValidUTF8`/NUL strip) with `json.Valid`
→ envelope as the catch-all. `\U` escapes, truncated `\u`, `\x`-escapes, and
structural garbage all remain verbatim → `json.Valid` false → envelope →
lands. **No remaining reachable payload shape rolls back the audit row.**

Bonus correction vs the regex version: literal `\ud8xx`/`\\u0000` *text* (legal
input) is now preserved verbatim instead of being mangled — better evidence
fidelity.

Remaining advisories (non-blocking):
- `error`-column sanitizer untested against real Postgres (LOW — code verified
  correct; add a `Msg:"bad\x00key"` → row lands case).
- `redirectPolicy` `len(via)>=3` branch untested (LOW — trivial pure function).
- `enrichError` still puts upstream `env.Error` verbatim on the *wire*
  `ErrorInfo.Message` — intentional (provider's own text; wire JSON tolerates
  the `\u0000` escape); only the persisted copy is sanitized — correct split.

## Standing invariant spot-check results

- **Unknown ≠ safe**: `aggregate` → `INSUFFICIENT_DATA` when nothing attempted
  (`service.go:500-514`); `fillData` keeps `"0"` flags as data.
- **Failure never flattened**: typed statuses end-to-end; negative cache
  embeds `status` + replays verbatim at 60 s TTL (`cache.go:70-73`); refresh
  guard fail-open + `bypassed` marker (`cache.go:90-108`, `service.go:227-229`).
- **DISABLED = zero traffic**: `service.go:181-186` early-return before
  fetch/cache/persist; `chainabuse` self-guards without a key; counting-fake
  asserts 0 calls.
- **`provider_score` ≠ internal score**: passthrough only; documented
  (`provider.go:133-136`).
- **reported ≠ verified**: all mappers emit `VerificationStatus:"reported"`
  (grep — goplus:96, publicaml:71/117, chainabuse:53).
- **Direct vs indirect**: `mapEntity` keeps `direct_attribution`→Observations,
  `outbound_exposure.items`→Exposures only (previously verified, unchanged).
- **Resilience**: limiter token per attempt; 429 never sleeps; retries only
  5xx/transport/attempt-timeout; caller-cancel never feeds breaker
  (`client.go:203-207`); same-host redirects ≤3 + downgrade refusal; 4 MB cap;
  per-request allowlist — all intact.
- **No secrets logged**: grep for apiKey/Authorization/Bearer in enrichment →
  zero hits; `Response`/`Msg` paths never carry credentials.
- **DDL**: `provider_observations` CHECK vocabulary 11 statuses; payload NOT
  NULL; error nullable; down-migration symmetric (previously verified).
- **Provenance**: raw upstream body persists (sanitized only where Postgres
  requires) with chain token, status, latency, fetched_at.

## Fresh verification evidence

| Command | Result |
|---|---|
| `gofmt -l internal/ tests/` (final state) | empty (exit 0) |
| `go vet ./internal/enrichment/ ./internal/provider/resilience/` + `go vet ./...` | clean |
| `go build ./...` | clean |
| `go test -count=1 -run 'HostileBodies|PersistsNonJSONErrorBody|RefreshWithRedisDown' -v ./internal/enrichment/` | **ALL PASS** — 10/10 hostile-body subtests incl. both escaped-backslash parity cases, on real `check_wallet_enrichment_test` |
| `go test -count=1 -run 'RedirectPolicy|CallerCancel' -v ./internal/provider/resilience/` | **ALL PASS** (0.073 s) |
| `go test -count=1 ./...` | **ALL PASS** — `internal/enrichment` 5.575 s, `tests` 26.419 s (uncontended run on final code) |
| `go test -count=1 -race ./...` | **ALL PASS** — `internal/enrichment` 26.8 s, `tests` 102.096 s, race-clean |
| `sqlc generate` → `diff -r /tmp/sqlc-r4/internal/db internal/db` | **IDENTICAL** — generated code matches checked-in |
| `go mod tidy -diff` | empty — canonical |
| `psql ::jsonb` probes | `\uD800`/`\uDC00`/`\ud800` rejected; `\uD83D\uDE00`+`\ud83d\uDE00` → 😀 accepted; `\ufffd\uDE00`/`\\ud83d\udc00` rejected (edge proofs); `\u0001`/`\u001f`/`\uffff`/`\ufffe`/`\u007f`/`\ud7ff`/`\ue000` accepted; scalars `"str"`/`123`/`[1,2]` accepted; `{"code":…,"message":"…\u0000…"}` rejected (errJSON proof) |
| `/tmp/r4-probe` (exact replicas, both versions) | pre-fix: `\uD800`/`\uDC00`/`\\ud83d\udc00` passthrough verbatim + mixed-pair half-mangle; post-fix: all neutralized/preserved correctly across 28 cases |
| `redis-cli -p 6380 -n {0,9,10} SCAN` | DB0 `sanctions:current_snapshot`; DB9 `rl:1:…` (api-test rate-limit key); DB10 `provider:publicaml:ethereum:0x…0e01` — no strays |
| `git status --short` start vs end | identical, 31 entries — audit read-only; only this report added |
| `pg_stat_activity` during failures | second `go test -race` process (PID 270440) caught TRUNCATE-ing shared `check_wallet_test` tables mid-run — explains transient failures below |

**Environmental note:** my first full-suite and targeted runs showed transient
failures (`duplicate key api_clients`, `ingestion already running`, `no rows in
result set`, deadlock 40P01) — all caused by a *second* `go test -count=1 -race
./...` process running concurrently against the shared `check_wallet_test` DB
(TRUNCATE observed in `pg_stat_activity`). Re-run uncontended: full suite and
race both 100 % PASS on the final code. Not a code defect — a shared-test-DB
scheduling collision; worth a note in `AGENTS.md` that `go test ./...` must not
overlap with itself on this machine.

Status: DONE
