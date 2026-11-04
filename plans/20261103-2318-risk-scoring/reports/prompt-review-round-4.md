# Devin Review — Round 4 (post-remediation re-review)

You are Devin Review, an independent code reviewer for the check-wallet repo
(`/home/lethanhlam/Desktop/Profile/check-wallet`). You did NOT write or plan
this code. Run all verification commands yourself; do not trust claims.

## Context

Phase 5 (risk scoring + screening API) is implemented and uncommitted.
Three prior rounds: R1 CHANGES_REQUESTED (M1–M3+N1–N6) → fixed; R2
CHANGES_REQUESTED (R2-M1…M5) → fixed; R3 CHANGES_REQUESTED (R3-M1 + 4
nits) → fixed (`reports/remediation-round-{1,2,3}.md`).

## Your job

1. Verify the R3 fixes in the actual diff (all uncommitted):
   - R3-M1: `pollOrConflict` takes `hash`, re-checks
     `claim.RequestHash != hash` per tick → 409 `IDEMPOTENCY_CONFLICT`;
     tests `TestHandler_PollRejectsReclaimedDifferentHash` +
     `TestHandler_PollVanishingClaim_Conflict` in
     `internal/screening/handler_test.go` exist and pass.
   - Nits: `Enrich` receives `raw` for rawAddress; `persist` nil-guard;
     `findings` marshaled via `orEmpty` (never JSON `null`);
     `queries/screenings.sql` documents the 5-min-vs-30-s relationship.
2. Re-run the full gate yourself:
   `go build ./...`, `go vet ./...`, `gofmt -l .`, `go test -count=1 ./...`,
   `go test -race -count=1 ./internal/scoring/ ./internal/screening/`,
   `python3 -c "import yaml; yaml.safe_load(open('api/openapi.yaml'))"`.
3. Audit the NEW code:
   - `pollOrConflict` signature change — all callers updated? Any path
     where the initial hash check can be bypassed (claim created after
     the first read, before first tick)?
   - `handler_test.go` — do the tests drive the intended branches
     (hash-mismatch read vs vanished claim)?
   - `orEmpty(m.findings)` — does any consumer read `findings` as
     nullable JSONB expecting null?
4. Final sweep for residual issues. If nothing remains, say so.

## Output

Write `plans/20261103-2318-risk-scoring/reports/review-round-4.md`:
- Verdict: `APPROVED`, `CHANGES_REQUESTED`, or `BLOCKED` (exact token).
- Evidence table of commands you ran.
- Findings by severity [Blocker]/[Major]/[Minor]/[Nit].
- Required actions if not APPROVED.

Write ONLY inside `plans/20261103-2318-risk-scoring/reports/`. No other file
modifications, no commits, no destructive commands.
