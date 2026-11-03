Rebuttal / amendment resolution — Round 2. All 9 required amendments (F1–F9)
are accepted and applied to the plan files. Summary of what changed:

- **F1** (phase-03): `chain` now comes from an explicit static map
  `{"ethereum":"ETH","bsc":"BSC"}`; fixture test must assert the literal
  request-body chain token per network; the literal token is persisted in
  `provider_observations.requested_chain` (spec §4.6).
- **F2** (phase-01/05): synchronous path bounded — parent fan-out deadline
  ~25 s (env-tunable); limiter consumes one token per HTTP *attempt*;
  limiter wait bounded → fail fast `rate_limited`+`retry_after`; 429 never
  sleeps in-request (typed error carries `retry_after` verbatim); in-request
  retry only for 5xx/transport inside remaining budget.
- **F3** (phase-05): `refresh=1` guarded by per-key Redis cooldown
  `provider:refresh:{src}:{net}:{addr}` SET NX EX 60 — inside cooldown the
  flag degrades to a normal request (cache allowed); a cache miss still
  fetches.
- **F4** (phase-05): fan-out via `errgroup.Group` — `WithContext`-derived
  sibling cancellation explicitly forbidden; each provider outcome recorded
  independently.
- **F5** (phase-01): `provider_observations` DDL pinned — CHECK on status
  vocabulary, index `(source_id, wallet_id, fetched_at DESC)`, explicit
  column types, `requested_chain TEXT`, prune inside insert transaction
  with documented transient-overflow tolerance.
- **F6** (phase-01): allowed-host sets hardcoded per adapter (mirroring
  `ofac.defaultAllowedHosts`); configured base URL validated at
  construction; https-only + test-only insecure hook.
- **F7** (phase-01): §13 extension rationale recorded in provider.go doc
  comment.
- **F8** (phase-02): `honeypot_related_address` demoted to OTHER (concede —
  association flag ≠ scam verdict); `result:null`/`{}` → OK-empty parse +
  fixture test added.
- **F9** (phase-05): openapi `info.description` staleness fixed in-scope;
  docs state provider_score ≠ internal risk, `0` ≠ safe under degraded,
  `retry_after` units, `cache_expires_at` semantics, DISABLED semantics;
  Authorization headers never logged.
- Minors applied: `screened_at`→`enriched_at`; `refresh` via
  `strconv.ParseBool`; empty `providers=` = all; `signals` emits `{}`.

Updated files: `plan.md` (Design decisions section retitled "post-Critic
round 1 — F1–F9 applied" + Stage 1 Debate Summary), `phase-01`, `phase-02`,
`phase-03`, `phase-05`.

Please re-verify the amended plan at
`plans/20261103-1746-live-enrichment/` and emit your verdict:
`PLAN_CONSENSUS_REACHED` or `CHANGES_REQUESTED` + remaining amendments.

End your reply with:
Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
Summary: one or two sentences
