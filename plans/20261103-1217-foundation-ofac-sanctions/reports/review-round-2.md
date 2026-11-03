# Devin Review — Round 2 (Remediation Verification)
Verdict: APPROVED

Re-reviewed remediation range `d5faa7f..HEAD` = `15638cd` (33 files,
+1,049/−193) **plus** `2a5710e`, which landed during this audit and completes
W3 — both commits verified. Read-only audit: nothing modified except this
report; all evidence below is fresh runner/psql output from this session.

## Per-Finding Verification

| # | Finding | Fixed? | Evidence (file:line / test) |
|---|---------|--------|------------------------------|
| 1 | [HIGH] SSRF redirect bypass | YES | `CheckRedirect` re-validates scheme+host via shared `checkURLAllowed` on every hop, 10-hop cap (`internal/provider/ofac/downloader.go:107-115,129-140`). `FinalURL` recorded on `Snapshot` (`provider/provider.go:13`, `downloader.go:244`) and logged per run (`internal/ingestion/service.go:234-237`). `TestDownload_RedirectToNonAllowlistedHostRejected` and `TestDownload_AllowlistedRedirectWorks` (asserts `FinalURL`) both PASS live. |
| 2 | [MED] `http://` accepted | YES | `checkURLAllowed` requires https unless `insecureHTTP` (`downloader.go:130`). `WithInsecureHTTP()` present only in `downloader_test.go:23,166`; grep confirms zero occurrences under `cmd/`. `TestNewDownloader_RequiresHTTPS` PASS. |
| 3 | [MED] Unbounded Retry-After / wedged run | YES | `wait = min(retryAfter, d.maxRetryWait)`, cap 5m (`downloader.go:27,161`). `Enqueue` executes under `context.WithTimeout(context.Background(), s.runTimeout)` = 45m (`service.go:44,128`). `TestDownload_RetryAfterCapped` (1-year hint completes <2s) PASS. |
| W1 | Redis snapshot pointer staleness | YES | `cache.CurrentSnapshot` deleted (`internal/platform/cache/keys.go` — no pointer builder); `currentSnapshot` reads `LatestSnapshotPublished` directly (`internal/sanctions/service.go:205-214`); ingestion `NewService` no longer takes Redis at all. Tests honestly re-sourced version from `sanctions_snapshots` (`tests/sanctions_lookup_test.go:196-198,234-236`) — not weakened. `TestSanctions_CacheVersionedKeys` PASS. |
| W2 | Hard-delete of references/entity-programs | YES | Migration `000004` adds `revoked_at` (+`first_seen_at`/`last_seen_at`) to both tables and `uq_sanctions_reference_identity`. Publish now upsert + revoke-absent pairs (`queries/ofac_publish.sql:25-50,113-143`; `internal/ingestion/publish.go:63-68,81-86`). ON CONFLICT targets verified against live `pg_indexes`: expression index matches `(sanctions_entity_id, reference_type, COALESCE(title,''), COALESCE(reference_url,''))` exactly; `(sanctions_entity_id, program_id)` matches the table PK. Staging `uq_stg_references` enforces the identical tuple → batch can never double-affect a row. `TestIngestion_ProvenanceSoftRevoke` asserts revoke AND in-place revive preserving `first_seen_at` — PASS (4.43s), strong assertions, zero skips. |
| W3 | `data_status` on detail endpoints | YES | `ProgramDetail`/`EntityDetail` carry both fields (`service.go:305-306,328-329`); OpenAPI `EntityDetail.data_status`/`last_synced_at` and `ProgramDetail` same (api/openapi.yaml:566,577). Caught mid-audit: `Entity` handler had dropped `detail.DataStatus` from its explicit response map — fixed by `2a5710e` (landed during this session): emits `detail.DataStatus`/`detail.LastSyncedAt`, removes redundant second `DataStatus` DB call, and the test now pins `data_status` on BOTH detail endpoints (`tests/sanctions_lookup_test.go:260,271-272`). `TestSanctions_EntityAndProgramEndpoints` PASS at HEAD. |
| W4 | Contract drift | YES | `phase-05-sanctions-query-api.md:53-62` — explicit "Contract note (Devin Review W4 — accepted deviation)" declaring the implemented shape canonical (`external_entity_id` = true record id; `source.code` = provider), plus amendment note for W1 at :48-51. |
| W5 | Stale-run sweep lacks liveness | YES | `ingestion_runs.heartbeat_at` column (live-verified); `TouchRunHeartbeat` `WHERE status='RUNNING'` cannot revive finished rows (`queries/ingestion_runs.sql:28-29`); sweep keys off `COALESCE(heartbeat_at, started_at)` (`:34,40`); 30s ticker in `execute()` with `defer close(hbStop)` — goroutine exits on every return path, no leak (`service.go:201-215`). 30s beat vs 1h reap = wide margin. |

**Observation-level fixes (all confirmed):** recovery middleware emits the stable
error envelope ordered after RequestID (`internal/middleware/recovery.go`,
`internal/httpserver/server.go:56`); `GetRun` splits `ErrNoRows`→404 vs
other→500 (`internal/ingestion/handler.go:91-95`); `TriggerRun` 400s malformed
JSON and 400s unknown source (`handler.go:39-44,61-63`);
`rate_limit_store_errors_total` counter registered and wired to the fail-open
hook (`internal/platform/metrics/metrics.go:68-71`, `server.go:61-67`);
`backoffFor` clamped — `b <= 2` early-returns so `rand.Int63n` never sees ≤0
(`downloader.go:172-180`).

## New Issues Introduced

1. **LOW — heartbeat goroutine shares the pinned conn, and is never joined.**
   `execute()`'s heartbeat calls `qc.TouchRunHeartbeat(context.Background(),…)`
   on the same `*pgxpool.Conn` that staging (`imp.Import` via `qc`) and publish
   (`conn.Begin` tx) use (`internal/ingestion/service.go:201-214,251,269`).
   pgx conns are not safe for concurrent use: overlapping calls return
   `connLockError "conn busy"`, so beats are silently skipped during the
   stage/publish window (bounded ~minutes; the 1h reap threshold is unaffected,
   and during the longest phase — download — the conn is idle and beats land).
   Residual risks: (a) a beat that loses the post-`close(hbStop)` select race
   can execute `TouchRunHeartbeat` while `advisoryUnlock`/`conn.Release()` run
   on the same conn — `advisoryUnlock` ignores errors (`locks.go:25`), so a
   conn-busy there leaks the session-scoped advisory lock until pool conn
   expiry; (b) a beat reaching `conn.Exec` after `Release()` dereferences
   `c.res == nil` inside `pgxpool.Conn.Conn()` → panic in a goroutine with no
   `recover()` → process crash. Window is nanosecond-scale (ticker-ready at
   exactly the close instant) — flag for fix, not a merge blocker. Recommended:
   run the heartbeat through `s.q()` (pool conn — the run row is committed, no
   pinning needed) and join the goroutine via a done channel before `execute`
   returns.

2. **TRIVIAL — dead code:** `staleRunAfter` const unused (`service.go:39`, the
   SQL hardcodes `INTERVAL '1 hour'`); `programRef` helper has no callers
   (`internal/sanctions/service.go:386`).

3. **NOTE:** `2a5710e` bundles `reports/prompt-review-r2.md` into a code commit
   — harmless docs churn. `phase-04` doc still describes the removed
   `sanctions:current_snapshot` pointer (:41,53,109); the phase-05 amendment
   records the change, acceptable as historical record.

## Regression Check Results

- `ingestion.NewService` signature (rdb dropped): all 7 call sites updated —
  `cmd/api/main.go:73`, `cmd/worker/main.go:78`, 5 test sites; `go build ./...`
  clean.
- `sanctions:current_snapshot` / `cache.CurrentSnapshot`: zero code
  references remain (only docs/reports).
- Heartbeat goroutine: `defer close(hbStop)` registered before all early
  returns → no goroutine leak; not awaited — see New Issue 1.
- Migration `000004` down is symmetric (drops all added columns/indexes);
  `check_wallet_test` live-verified: `schema_migrations` v4, `dirty=f`.
- `ListProgramsForEntities` (live-lookup path) filters `sep.revoked_at IS
  NULL` (`queries/sanctions.sql:68`); `ListEntityPrograms`/`ListEntityReferences`
  intentionally return revoked rows with `Revoked` flag — audit surface, not a
  leak; `CountProgramEntities` filters revoked links.
- `TestIngestion_ProvenanceSoftRevoke`: asserts revoke AND revive with
  `first_seen_at` preserved — PASS, no skips.
- `sanctions_programs` themselves are never revoked — consistent with
  pre-remediation behavior; links carry the lifecycle.

## Fresh Verification Evidence

- `go build ./...` — clean (exit 0, at HEAD `2a5710e`)
- `go vet ./...` — clean; `gofmt -l .` — empty
- `go test ./...` — all packages PASS (`tests` 8.56s incl. live `check_wallet_test`)
- `go test -run` targeted: `TestDownload_RedirectToNonAllowlistedHostRejected`,
  `TestDownload_AllowlistedRedirectWorks`, `TestDownload_RetryAfterCapped`,
  `TestNewDownloader_RequiresHTTPS`, `TestIngestion_ProvenanceSoftRevoke`,
  `TestSanctions_EntityAndProgramEndpoints`, `TestSanctions_CacheVersionedKeys`
  — all PASS
- `psql check_wallet_test`: `schema_migrations` v4 `dirty=f`;
  `uq_sanctions_reference_identity` index def matches ON CONFLICT target;
  `heartbeat_at` + all `revoked_at`/`first_seen_at`/`last_seen_at` columns
  present with expected nullability
- `git status`: clean except orchestrator's `prompt-review-r2.md` (untracked at
  audit start, later committed in `2a5710e`)
