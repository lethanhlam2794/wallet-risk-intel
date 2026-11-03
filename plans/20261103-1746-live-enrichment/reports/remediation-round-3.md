# Remediation — Review Round 3

Date: 2026-11-03 (post `review-round-3.md` CHANGES_REQUESTED)

## Round-3 finding and fix

### MEDIUM (required) — `json.Valid` ≠ Postgres jsonb input rules

Round-2's gate checked Go syntax; Postgres is stricter. Three reachable
payload shapes still rolled back the audit-row INSERT:

1. Go-valid JSON carrying `\u0000` escapes, unpaired surrogate escapes, or
   invalid UTF-8 bytes (stored verbatim → Postgres rejects).
2. The envelope itself — a literal NUL byte marshaled to `\u0000`.
3. The untouched `lr.Raw` success path (same classes, pre-existing).

Fix (`internal/enrichment/service.go`):

- `jsonbText(b)` — `strings.ToValidUTF8` + literal-`\x00` → U+FFFD; used
  for envelope strings (marshal can then never emit `\u0000`/bad UTF-8).
- `jsonbSafe(b)` — byte-level normalize, then escape-level rewrites:
  `\u0000`→`\ufffd`; valid surrogate PAIRS shielded behind `\x00`
  sentinels (already stripped → collision-free) while LONE surrogates
  →`\ufffd`; pairs restore verbatim.
- `auditPayload(raw)` — single picker for ALL sources: sanitized body if
  Postgres-legal JSON → verbatim; else `{"upstream_body": sanitized}`
  envelope; empty → `{"attempt":true}`. Applied to `lr.Raw` AND
  `lerr.Response`.

### Advisory coverage added

- `TestEnrich_PersistsJSONBHostileBodies` — 7 shapes against real
  Postgres asserting decoded payload values: `\u0000` escape, literal NUL
  in JSON, lone `\ud800`, valid `\ud83d\ude00` pair (preserved → 😀),
  invalid UTF-8 (`caf\xe9`→`caf�`), binary error body, verbatim JSON.
- `TestEnrich_RefreshWithRedisDownStillMarksBypassed` — R3 regression:
  refresh + Redis outage still surfaces `cache:"bypassed"`.
- `TestRedirectPolicy_SchemeDowngradeRefused` — extracted `redirectPolicy`
  package func; direct table coverage: same-host downgrade refused,
  multi-hop refused, same-scheme allowed, cross-host refused both ways.
- `TestDo_CallerCancelDoesNotFeedBreaker` — `BreakerFailures=1`; two
  mid-flight caller cancels; healthy request then succeeds (proves
  cancels never reached `breaker.Report(false)`).

## Post-remediation gates

- `gofmt -l .` → empty; `go vet`/`go build` → clean
- `go test -count=1 ./...` → all pass (tests/ 24.9 s)
- `go test -count=1 -race ./...` → all pass (tests/ 32.4 s)
- Tree dirty; no commits.
