---
name: production-risk-review
description: Review contracts, state, failures, concurrency, persistence, migrations, authorization, tests, and operations
---

# Production Risk Review

## Purpose

Find defects that polished code and passing happy-path tests conceal. Apply each dimension
that intersects the changed behavior; record `N/A` with evidence instead of silently
skipping it. Scale depth by risk, especially for money, identity, data loss, external side
effects, and irreversible operations.

This review handles software production risk. It does not authorize live mutations,
deployment, secret access, destructive database operations, or scope expansion. Redact
credentials, personal data, raw external payloads, and customer records from evidence.

## 1. Claim-to-Evidence Matrix

Convert every implementation claim, spec requirement, and acceptance criterion into a
row before judging code quality.

| Claim | Runtime owner | Evidence | Negative evidence | Verdict |
|---|---|---|---|---|
| `<behavior>` | `<component>` | `<source/test/output>` | `<missing/contradiction>` | `PROVEN/PARTIAL/FAIL` |

- Reject claims supported only by comments, names, unchecked boxes, or author summaries.
- Distinguish code presence from runtime reachability and runtime reachability from tested
  behavior.
- Treat a plan as requirements evidence, never as proof the implementation works.

## 2. Contract Tracing

At every process, module, persistence, queue, and network boundary, compare:

- input fields, types, units, nullability, defaults, and validation;
- output/result envelopes and terminal/provisional status semantics;
- exception classes, error codes, timeout and cancellation semantics;
- retry, idempotency, ordering, delivery, and acknowledgement guarantees;
- serialization differences such as casing, number precision, dates, enums, and IDs.

Flag any adapter that flattens distinct outcomes. Examples: transport uncertainty and
authoritative rejection both becoming `ok: false`; driver tuples mocked as plain arrays;
missing monetary values coerced to zero.

## 3. State-Machine Review

List states and transitions from actual code and schema. Check:

- initial, provisional, terminal, cancelled, expired, and manual-review states;
- who owns each transition and what evidence authorizes it;
- invalid or backward transitions;
- terminal states that release locks, fences, or permit new work;
- cross-layer enum/constraint mismatches;
- stale writers overwriting a newer state.

For each terminal transition, ask: “What authoritative fact proves finality?” If no fact
exists, keep the state unresolved.

## 4. Failure-Boundary Matrix

Place failure points around every side effect:

| Boundary | Questions |
|---|---|
| Before persistence | Can retry safely reconstruct intent? |
| After persistence, before send | Is dispatch recoverable and distinguishable? |
| During send | Can the peer have received a partial/full request? |
| After peer acceptance, before response | Is the outcome ambiguous and fenced? |
| After response, before local commit | Can reconciliation recover without replay? |
| During restart/failover | Which durable state survives? |

Also inspect timeout, disconnect, malformed response, cancellation, process kill, partial
success, dependency unavailability, and exhausted retry behavior.

## 5. Source-of-Truth Review

Identify the authoritative owner for identity, schema, money, status, and completion.

- Separate ACK, chat/UI text, cache, balance delta, projection, and derived records from
  authoritative ledger or persisted source-of-truth evidence.
- Trace read models back to their writers and lifecycle.
- Verify correlation uses the correct tenant/account/entity, amount/unit, operation ID,
  and finality window.
- Do not infer external success by reading downstream success code without proving all
  upstream gateways and external prerequisites can succeed.

## 6. Concurrency and Idempotency Review

Check duplicate and overlapping execution through:

- simultaneous callers, retries, queue redelivery, cron overlap, and multi-node workers;
- atomic compare-and-swap (CAS), unique constraints, transactions, locks, and leases;
- lease expiry, renewal, ownership checks, fencing tokens, and stale completion writes;
- idempotency-key scope plus request-hash conflict behavior;
- when unresolved fences are created and released;
- crash safety before and after an irreversible side effect.

Do not accept process-local booleans or in-memory maps as distributed coordination.

## 7. Persistence and Schema Review

Read entities, data definition language (DDL) migrations, queries, and actual driver/ORM
contracts together.

- Match columns, types, units, defaults, nullability, indexes, foreign keys, and checks.
- Validate raw driver return shapes and affected-row semantics from runtime truth.
- Check tenant/account filters on reads, writes, updates, and reconciliation claims.
- Check atomicity across every read-modify-write sequence.
- Test success plus zero-row/conflict/failure behavior for critical primitives.

## 8. Migration Lifecycle Review

Review migrations in four distinct states:

1. fresh install;
2. upgrade from the real previous schema and existing data;
3. replay after full or partial application;
4. rollback/drain/cutover under the documented deployment sequence.

Check expand-contract staging, lock/statement timeouts, backfill safety, schema-qualified
catalog checks, constraint validation, indexes, manifest ordering, compatibility windows,
and dedicated integration-database isolation. `CREATE ... IF NOT EXISTS` does not upgrade
the shape of an existing object.

## 9. Authentication, Authorization, and Isolation

Trace identity from the runtime gateway, not from type annotations or caller-supplied
objects.

- Prove authentication/guard/middleware execution.
- Prove role/capability authorization at the mutation boundary.
- Prove tenant, organization, user, and account ownership filters.
- Check indirect object references, background jobs, internal service calls, and admin
  bypass paths.
- Prefer not-found behavior when existence disclosure crosses an ownership boundary.

## 10. Input Validation and Injection Surface

Review untrusted data independently from authentication. Authentication proves who may
call; validation proves whether supplied data is safe and meaningful.

- Inventory HTTP parameters, headers, files, WebSocket frames, queue messages, CLI args,
  environment/config values, database-derived strings, and third-party responses.
- Check allowlists, canonicalization order, length/range/depth limits, Unicode and encoding,
  duplicate keys, unknown fields, numeric overflow, and malformed serialization.
- Trace values into SQL/ORM raw clauses, shells, templates, file paths, URLs, redirects,
  object deserialization, dynamic imports, logs, and response headers.
- Check SQL/NoSQL/command/template/header injection, path traversal, server-side request
  forgery (SSRF), unsafe deserialization, prototype pollution, archive extraction, and
  unrestricted upload risks where applicable.
- Verify validation occurs before side effects and is enforced again at the owner boundary
  when data can bypass the public gateway.
- Prefer parameterization and safe APIs over filters or escaping. Test bypass payloads at
  the actual sink, not only DTO validation in isolation.

## 11. Backward and Forward Contract Compatibility

Review API, event, queue, worker, cache, and external adapter evolution separately from
database migration lifecycle.

- Identify every producer and consumer, including old deployments during rolling rollout.
- Check removed/renamed fields, required-versus-optional changes, defaults, enum expansion,
  status/error semantics, units, precision, ordering, and pagination/cursor behavior.
- Verify older consumers tolerate new fields and enum values; verify newer consumers handle
  messages or responses from older producers.
- Check additive versus breaking changes, version negotiation, deprecation windows, dual
  read/write periods, and downgrade behavior.
- Require contract/consumer tests when compatibility cannot be proven within one repository.
- Treat a wire-compatible shape with changed meaning as a breaking change.

## 12. Mock and Test Truthfulness

Audit whether tests prove production behavior:

- Compare mocks with real signatures, result shapes, failures, timing, and ordering.
- Detect tests that assert the implementation’s mistake instead of the requirement.
- Require negative tests for rejected, ambiguous, timeout, duplicate, stale, and wrong-owner
  paths where applicable.
- Use a real database/Redis/queue or contract test when the correctness claim depends on
  server-side atomicity, indexes, leases, or driver semantics.
- Require a red reproduction for fixes and fresh output for completion claims.

Passing tests are evidence only for the behavior they actually exercise.

## 13. Blast Radius and Rollback Safety

Estimate impact before accepting rollout controls:

- affected users, tenants, accounts, regions, workers, queues, tables, and external systems;
- whether failure corrupts source data, derived data, money, identity, or only presentation;
- propagation speed and whether retries, caches, fan-out, or scheduled jobs amplify impact;
- canary/cohort boundaries, exposure limits, and how quickly admission can be stopped;
- rollback after new-format data, messages, cache entries, or side effects already exist;
- mixed-version safety during deploy and rollback;
- forward-fix, compensation, drain, backfill, restore, and manual-handoff requirements.

Feature flags and kill switches limit execution only when every entrypoint observes them.
They do not reverse data already written or external side effects already accepted. Require
a concrete rollback or roll-forward path for those states.

## 14. Resource and Performance Review

Check bounded resource use under expected load and failure conditions:

- N+1 database/HTTP calls, missing indexes, full scans, lock contention, and transaction
  duration;
- unbounded queries, pagination, loops, recursion, retries, queues, caches, maps, buffers,
  histories, logs, metrics labels, and dead-letter retention;
- memory, socket, file descriptor, thread, timer, listener, subscription, and connection
  lifecycle leaks;
- payload size, serialization cost, algorithmic complexity, hot-path allocation, and
  synchronous/blocking work;
- inbound rate limits by the correct identity/scope and outbound throttling against
  provider quotas, including distributed enforcement and retry-after behavior;
- backpressure, concurrency caps, timeouts, cancellation, shedding, and graceful shutdown.

Require measurements, query plans, load tests, or boundedness proofs in proportion to risk.
Do not report speculative micro-optimizations without a plausible production impact path.

## 15. Operational Safety and Observability

Check runtime control and recovery:

- defaults and startup/runtime semantics of feature flags;
- independent admission kill switch versus reconciliation/drain capability;
- bounded retries, backoff, dead-letter/manual-review paths, and recovery ownership;
- behavior during dependency outage, restart, deploy overlap, and partial rollback.

Do not accept a kill switch that abandons unresolved durable work unless the spec explicitly
defines and approves that trade-off.

Verify observability can diagnose a real incident rather than merely proving logs exist:

- Logs: include bounded operation/correlation IDs, state transition, component, outcome,
  error class, attempt and latency without secrets, PII, raw frames, or unbounded payloads.
- Metrics: expose actionable rates, errors, latency, saturation, backlog, oldest age, stuck
  work, retries, ambiguity and protocol drift with bounded label cardinality.
- Alerts: define owner, severity, threshold, evaluation window, noise controls and runbook;
  verify thresholds reflect service objectives and expected baseline rather than arbitrary
  constants.
- Traces: propagate correlation/trace context across HTTP, queues, workers, daemons and
  external adapters; preserve links across async boundaries and retries.
- Incident reconstruction: prove an operator can follow one operation end-to-end and
  distinguish rejected, failed, timed out, retried, ambiguous and recovered outcomes.
- Failure visibility: never swallow errors or emit success metrics before durable finality.

## 16. Domain and Business-Logic Correctness

Verify formulas and domain decisions independently from structural spec compliance:

- identify the authoritative owner for eligibility, fee/discount precedence, units,
  rounding, caps, thresholds, effective dates, account roles, and lifecycle rules;
- construct boundary examples or a truth table from requirements rather than copying the
  implementation formula;
- check aggregation and conservation invariants, including mixed currencies/units and
  paired financial entries;
- trace every caller that can reach the calculation so a rule enforced at one entrypoint
  cannot be bypassed by a worker, cron, admin path, or internal service;
- report missing or contradictory business authority as NEEDS_CONTEXT, not as an invented
  requirement.

Use `references/review-calibration.md` for the full business-correctness procedure.

## Review Output

First adjudicate candidate findings using `references/review-calibration.md`. Report only
accepted or explicitly disputed findings in the main results, ordered by severity and then
confidence. For every finding include:

1. violated requirement or invariant;
2. exact runtime path and failure mechanism;
3. source/test evidence;
4. user or production impact;
5. smallest compliant correction;
6. missing regression or integration test.

Use the four severity levels BLOCKER, MAJOR, MINOR, and NIT. Report confidence separately
as CONFIRMED, HIGH, MEDIUM, or LOW. Rejected false positives do not count as findings, but
retain a concise rejection reason when it helps explain a disputed review decision.

Then report verified fixes, commands actually run, skipped gates with reasons, and the final
verdict. Never upgrade `PARTIAL` evidence to `PASS` for momentum.
