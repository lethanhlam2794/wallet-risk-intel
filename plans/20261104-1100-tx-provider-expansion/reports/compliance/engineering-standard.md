# Global Engineering & Review Standard (Master Rulebook)

> **Scope**: Mandatory for all Analysis, Architecture Design, Coding, Testing, and
> Code Review performed by AI Agents and engineers across all projects.

---

## 0. Rule Priority & Change Risk Classification

This is the reference frame applied throughout the rulebook. Every section
below (test ratio, feature flag, rollback, observability...) should be read and
its application level adjusted according to the two tools in this section.

### 0.1 Rule Priority (When Rules Conflict)

This priority resolves engineering trade-offs, not instruction authority or
authorization to act. A security finding does not authorize unrelated edits,
credential rotation, deployment or publication. Report evidence and scope first.

When two rules conflict (e.g. Surgical Changes says don't touch code outside
scope, but a security hole sits right next to the code being changed), apply
this priority order, highest first:

1. **Safety / Security** — user data safety, preventing exploit.
2. **Data Integrity** — correctness and integrity of data (race conditions, data loss).
3. **Correctness** — right business requirements, right spec.
4. **Compatibility** — don't break clients/systems currently consuming the API/schema.
5. **Operational Safety** — safe deploy/rollback.
6. **Maintainability** — readable, maintainable long-term.
7. **Performance** — speed/resource optimization.
8. **Simplicity** — minimal, few abstractions (KISS/YAGNI).

**Conflict protocol**: When a conflict is detected, the Agent MUST NOT pick one
rule and silently ignore the other. The Agent MUST:

1. State clearly: *"Conflict detected between [Rule A] and [Rule B]"*.
2. Briefly explain the nature of the conflict.
3. Choose the higher-priority rule per the list above.
4. Note the deviation from the lower-priority rule, with reason, in the PR
   description or commit message.

### 0.2 Rule Binding Level (MUST / SHOULD / MAY)

Not every rule has the same binding level. Classify every rule when reading:

- **MUST / MUST NOT** — Absolute. A violation MUST be treated as a defect, no
  implicit exceptions. Deviating requires the conflict protocol in 0.1.
- **SHOULD / SHOULD NOT** — Apply by default, but deviation is allowed with a
  clear, publicly stated reason (never silent).
- **MAY** — Context-dependent, not required; left to the agent/engineer.

Unlabeled "PHẢI"/"BẮT BUỘC"/"CẤM" in this standard map to MUST; "nên",
"cân nhắc", "khuyến nghị" map to SHOULD/MAY by context (e.g. Circuit Breaker
in Section 8 is SHOULD/MAY, not MUST).

### 0.3 Change Risk Classification

Not every change needs the strictest level of every rule. Before starting, the
Agent MUST classify the change into one of 5 risk levels; the strictness of
Test Saturation (Section 4), Deployment Safety (Section 6) and Observability
(Section 7) scales accordingly:

| Risk | Description / Example | Minimum required |
|------|------------------------|------------------|
| **R0** | Typo, comment, internal rename with no behavior change | No dedicated test required; fast review |
| **R1** | Non-critical UI, display changes | Unit/E2E tests appropriate to the change |
| **R2** | Normal API/business logic | Full tests (see default ratio in Section 4) + contract alignment |
| **R3** | Schema changes and other high-risk changes without an R4 trigger | + Security review + applicable migration plan + rollback strategy |
| **R4** | Critical / high-impact (definition below) | + Full verification (6-Point Protocol) + full Observability + Idempotency Key (when applicable, Section 3) + Production Rollout Verification |

**R4 (Critical / High-Impact) definition**: A change is R4 when it meets **at
least one** of:

- Directly involves **payment / financial balance** (creating transactions, deducting money, refunds).
- Directly involves the **authentication / security boundary** (login, privilege grant, token verification, password change).
- Is an **irreversible operation on important data at scale** (bulk delete, unrecoverable production data migration).
- Has **very large failure impact** (many users affected at once, core system downtime), regardless of technical nature.

A single, small-scope, irreversible operation not in these groups (e.g. manual
admin action on one record) defaults to **R3**, unless another factor pushes it
to R4.

The Agent MUST state the chosen risk level at the start of implementing any
change at R2 or above, so reviewers know the strictness to apply.
Classify actual behavior changed, not keywords in file names: an auth README
typo is not a verifier change; a policy changing a security boundary is not R0
merely because it is Markdown.

---

## 1. Core Engineering Mindset

- **Think Before Coding**: Do not guess ambiguous requirements. When multiple
  approaches exist, MUST present them with trade-offs before writing code.
- **Simplicity First (KISS & YAGNI)**:
  - Write the minimum code that fully solves the problem. No speculative features.
  - MUST NOT create abstractions for single-use code.
  - MUST NOT add config flags (flexibility/configurability) unless requested.
  - Ask: *"Would a senior engineer call this over-complicated?"* If yes, simplify.
- **Surgical Changes**:
  - Touch only the files and lines needed to complete the goal.
  - MUST NOT refactor unrelated adjacent code.
  - Follow existing repo style; clean up unused variables/imports YOUR changes created.
  - Exception: if a Security/Data Integrity issue sits outside scope, apply the
    conflict protocol (0.1) instead of silently ignoring or fixing it unreported.
- **Goal-Driven Execution**: Turn every request into independently verifiable
  goals (clear success criteria → code → run real verification).
- **Scoped Pull Requests**: Each PR/commit should serve one goal. Diff should be
  reviewable in one focused pass — reference ~400 lines of real logic diff
  (excluding generated/lock/migration-seed files). This is a **warning signal,
  not a hard gate**: reviewers may accept larger diffs for legitimate reasons
  (large migrations, non-splittable features) but MUST record the reason in the
  PR description.
- **Git Diff Integrity Check**: After implementing, before reporting
  completion, the Agent MUST run `git diff`, `git diff --cached` (to catch
  staged changes — `git diff` alone doesn't show them) and `git status` to check:
  - [ ] Out-of-scope files touched?
  - [ ] Unintended file/line deletions?
  - [ ] Generated/build output committed by mistake?
  - [ ] Debug code (`console.log`, `print`, temp comments) left behind?
  - [ ] Secrets/credentials leaked in the diff?
  - [ ] Dependency (`package.json`, lockfile) changes outside intent?
  - [ ] Migration files missing vs. description?
  - [ ] Tests modified/weakened just to pass (see Section 4 — No Test Manipulation)?

---

## 2. Security & Data Isolation (Zero-Trust)

- **Never Trust Client Identity**: Derive acting identity from trusted
  authenticated context. Client-supplied resource/target-user IDs never grant
  authority. Explicitly authorized admin operations can accept target IDs.
  `@RequestUser()` is a convention for projects using that existing decorator,
  not a requirement to add NestJS or JWT to every stack.
- **Strict Ownership Scoping (Anti-IDOR)**: Every user-data query and mutation
  (Find, Update, Delete) MUST enforce actual owner/tenant/account scope at the
  data-access boundary. `where: { id, userId }` is an example, not a substitute
  for the repository's real tenant or administrative authorization contract.
- **Safe 404 on Unowned Resources**: When a user tries to operate on a resource
  they don't own, prevent existence scanning using the documented resource
  contract. Projects adopting non-enumerating 404 MUST retain it; do not
  silently change an existing consumed 403/404 contract.
- **Dual-Key Pattern & Information Masking**:
  - The bigint/UUID-v4 pair below is a convention for projects explicitly using
    it, not a global database migration requirement. Preserve existing public
    identifier contracts and disclosure protection in all stacks.
  - **Internal Key (`id: bigint`)**: database-internal only, for B-Tree index
    and FK join optimization.
  - **Public Key (`id_public: uuid v4`)**: MUST be used for all public API to
    hide internal IDs and prevent resource enumeration.
  - MUST NOT leak stack traces, DB driver error details, or internal config
    strategy to the client.
- **Secret & Credential Hygiene**:
  - MUST NOT hardcode secrets, API keys, connection strings, private keys in
    source or committed files (including tests/fixtures).
  - All secrets MUST come from environment variables or a Secret Manager
    (Vault, AWS Secrets Manager, GCP Secret Manager...).
  - If a secret is found leaked in git history, report immediately and rotate
    the credential — don't just delete the line.
- **Dependency Vulnerability Scanning**: Run vulnerability scanners (`npm
  audit`, `pip-audit`, `snyk`, `osv-scanner`...) periodically or in CI. MUST NOT
  add a new dependency with a known Critical/High vulnerability and no fix,
  unless a clear risk-acceptance note from the owner exists.
- **Dependency Introduction Policy**: Before adding a new dependency, answer:
  - Does native functionality (current language/framework) solve it?
  - Does an existing dependency in the repo?
  - Is the package actively maintained, with no serious vulnerability history?
  - MUST NOT add a package just to solve a few lines that could be written
    directly — violates KISS/YAGNI (Section 1).
- **Sensitive Data in Logs**: MUST NOT log passwords, tokens, secrets, card
  numbers, or identifying PII (national ID, full phone, full email) in
  plaintext. Mask/redact before logging (see also Section 7).

---

## 3. Data Integrity & Concurrency

- **No Naive Find-then-Mutate**: Application-layer checks cannot prevent race
  conditions. Every uniqueness rule, quota, or financial balance MUST have a
  database-layer gate:
  - Use **Database Constraints** (CHECK, Partial Unique Index
    `WHERE deleted_at IS NULL`).
  - Map exact driver errors through the existing adapter contract; PostgreSQL
    duplicate `23505` to HTTP409 is an example for compatible HTTP services.
  - Use **Atomic Operations** or **Distributed Locks** (Redis NX / Redlock) for
    critical tasks (place order, deduct money, resume progress).
    Prefer an existing sufficient atomic operation over adding a lock service.
- **Idempotent Migrations**: Prove safe retry/recovery for the actual runner.
  Replay-all migrations require object AND column-shape guards; history-based
  runners require recovery from partial failure. `IF NOT EXISTS` must not hide
  incompatible types or constraints. Keep staged expand-contract requirements.
- **Idempotent Side-Effect APIs**: Idempotency Key is NOT mandatory for every
  side-effect API — it is mandatory when the side effect risks duplicates or
  unwanted consequences on client retry, typically R3/R4 APIs
  (`POST /payment`, `POST /withdraw`, `POST /order`, `POST /transfer`). For
  these, MUST support a client-provided **Idempotency-Key** header, store the
  first result, and return it verbatim for repeated calls within the same
  key/window. For natively retry-safe side effects with another dedup mechanism
  (e.g. `POST /send-email` dedup by message ID at the queue), Idempotency Key
  is SHOULD/MAY, not MUST (see 0.2).
- **Backward-Compatible Schema Changes**: Schema-changing migrations
  (rename/drop columns, type changes) MUST be backward-compatible with the code
  currently running (expand-contract: add new column → migrate data → update
  code to use it → drop the old column in a later deploy).

---

## 4. Test Saturation Standard

- **Risk-Based Happy Path / Negative-Edge Ratio**: Not a fixed quota for every
  module; it depends on the Risk Level (0.3):

  These are approximate scenario-design heuristics, not test-count quotas.
  Preserve owner-set coverage thresholds; do not add tests just to meet a ratio.

  | Risk Level | Example module | Happy Path | Negative/Edge |
  |---|---|---|---|
  | R0/R1 | Pure utility, non-critical UI | ~60–70% | ~30–40% |
  | R2 | Normal business logic/API (default) | ~40% | ~60% |
  | R3/R4 | Auth, payment, untrusted input parsing | ~20–30% | ~70–80% |

  Negative/edge cases include: boundary data (negative, zero, empty arrays,
  malformed UUID), state conflicts (duplicate active registration, expired
  operations, unauthorized access), infrastructure errors (DB timeout, driver
  error).
- **Anti-Circular Testing**: Independent tests MUST NOT copy implementation
  logic verbatim into a mock; tests must define their own expected input/output.
- **No Test Manipulation**: The Agent MUST NOT modify, weaken, delete, skip,
  mock-around, or reduce assertions of existing tests just to make the suite
  pass — unless it proves the test itself deviates from the original
  spec/requirement. If an existing test must change: (1) explain why, (2)
  reference the spec/requirement showing the old test was wrong, not the
  implementation.
- **No Unnecessary Tests**: Don't create tests just to inflate coverage. Each
  test must protect a real behavior, invariant, or regression risk — apply
  KISS/YAGNI (Section 1) to test code too.
- **Coverage Is a Signal, Not a Goal**: Coverage % MUST NOT be the sole
  criterion for test quality — 100% coverage ≠ 100% correct behavior. Evaluate
  in order: business invariants → failure modes → security boundaries →
  concurrency → regression cases → coverage %.
- **Fresh Verification Evidence**: Run relevant checks against final behavior
  changes and report command, exit status and evidence. Permitted docs-only/R0
  changes use relevant diff/link/syntax checks and state why runtime tests are
  not applicable. Pre-existing unrelated failures stay failures: report their
  baseline evidence separately, never relabel them PASS or N/A.
- **Boundary Input Validation Testing**: Negative tests MUST include boundary
  validation (DTO/schema) cases — missing fields, wrong types, over-length,
  injection characters — so the validation layer works independently of
  business logic.

---

## 5. Disciplined Code Review (The 6-Point Review Protocol)

When doing code review or acceptance, MUST verify against all **6 checkpoints**,
scaling strictness to the declared Risk Level (0.3):

### 1. Independent Oracle
MUST NOT rely only on unit-test mocks created by the implementer to conclude.
Cross-check with independent sources of truth: Database Constraints (CHECK,
UNIQUE), actual Migration Schema, Type System and real behavior, plus the Git
Diff Integrity Check (Section 1).

### 2. Primary Source Alignment
MUST NOT accept "tests pass" as the endpoint. Open and compare every Field,
Data Type, Nullability, Constraint, Endpoint contract and Auth Guard against
the original spec/plan (`plan.md`, `specs/`, Validation Log, API specs). If an
endpoint is already consumed by other clients, any contract change MUST follow
backward compatibility or versioning (`/v2/...`, deprecation header) before
approval.

### 3. No Hallucination / Evidence Before Claims
MUST NOT answer with certainty without opening and reading the actual source
file. If not yet verified from source, say clearly: *"Not yet verified from
source/spec"* — never guess "it's fine" or "not critical".
- **Runtime Gateway & Precondition Truth ([P-021])**: MUST NOT deduce that a
  downstream operation, record creation, or side-effect happens merely because
  its handler code exists. Every runtime claim MUST trace upstream from the
  entrypoint and verify that all prerequisites (credential validity against
  external systems, session establishment, locks, balance gates) actually
  succeed. Distinguish real external credentials from mock/virtual simulation
  entities (e.g. `iq_crm`) that cannot authenticate against live 3rd-party APIs.

### 4. Authentic Metrics Only
All numbers (test pass/fail, coverage, run time, compile errors, linter
warnings) MUST come directly from real runner output (`vitest`, `jest`, `tsc`,
`biome`). MUST NOT hand-count or estimate. Read coverage % as a reference
signal, not a final conclusion (Section 4).

### 5. Senior Pragmatism (KISS / YAGNI)
Evaluate code on minimalism and real efficiency. Push back on over-engineering
(extra classes, event buses, message queues when direct code is simpler and
safe).

### 6. SOLID & Architecture Audit
- **SOLID**: These patterns are **tools applied when there is a real reason**
  (complexity, change frequency, boundary isolation) — **not a mandatory
  checklist** to create a full class stack for every feature. Mechanically
  creating Controller/Service/Mapper/Resolver/Interface/Adapter for a simple
  feature directly violates KISS/YAGNI (Section 1).
  - *SRP*: Consider splitting Controller (HTTP/Pipes), Service (business),
    Mapper (view filtering), Resolver (account resolution) when complexity
    genuinely demands it.
  - *OCP / DIP*: Use interfaces/abstract tokens when plugging in new modules
    without changing old logic; use callbacks to decouple core from UI/Bot
    context when many contexts consume one core.
  - *LSP*: Stub/Fake/Noop replacements MUST honor 100% of the contract without
    throwing unusual errors.
  - *ISP*: Keep interfaces small — only methods actually needed.
- **Architecture**:
  - Respect existing boundaries and reuse the responsibility owner.
  - Add an adapter/layer only for a current contract, incompatible external
    semantics or demonstrated complexity. Do not mechanically create a
    Controller/Service/Repository/Mapper stack or remove an existing required interface.
  - **Declare failure modes**: For changes at R3+, identify at least 1
    performance bottleneck under load and a mid-flight failure scenario
    (power loss / network drop) with a rollback strategy.

---

## 6. Operational Readiness

### 6.1 Deployment Safety
- R3/R4 features (payment flows, auth, bulk user-data changes) MUST ship with a
  **Feature Flag** so they can be disabled instantly without a code rollback.
  For R0–R2, feature flags are optional.
  Disabling a feature MUST NOT disable authorization or integrity checks.
- Every migration PR MUST state a **rollback scenario**: if the deploy fails
  mid-way, can data and schema return to a safe state? (see Expand-Contract in
  Section 3).
- MUST NOT deploy a schema-breaking change and its dependent code in the same
  release on multi-instance (rolling) systems — old instances break on the new
  schema.

### 6.2 Health Check & Graceful Shutdown
- Every long-running network/production service (API server, background
  service receiving continuous traffic) MUST expose health-check endpoints
  (`/healthz`, `/readyz`) distinguishing *liveness* (process alive) from
  *readiness* (ready for traffic, DB/cache connected).
- CLI, worker, cron, migration utility, one-off script: the literal rule does
  not apply (no `/healthz`). Use appropriate mechanisms instead: standard exit
  codes, heartbeat rows in a monitoring table, lock files, or periodic liveness
  logs for long-running workers.
- On shutdown signals (SIGTERM), a traffic-receiving service MUST stop taking
  new requests, finish in-flight requests within a reasonable timeout, then
  exit — avoid cutting mid-transaction.

### 6.3 Production Rollout Verification
For R3+ changes, especially payment/auth: **Deploy ≠ Done**. Post-deploy
confirmation MUST follow:

```
Deploy → Health check → Smoke test → Check error rate
       → Check latency (p95/p99) → Check critical business metric → Confirm rollout
```

Only "Done" after **Verify → Observe → Confirm**, not stopping at Deploy.

---

## 7. Observability & Logging

- **Structured Logging**: Production service logs MUST be structured (JSON),
  not free-form `console.log`. Preserve CLI stdout/data protocols; diagnostics
  belong on the designated log channel. Secret masking applies to every stack.
- **Correlation / Trace ID**: Every service request MUST carry a `request_id`/`trace_id`
  across the whole call chain (including cross-service calls) to trace a
  specific error flow.
- **Log Level Discipline**: Distinguish `debug` / `info` / `warn` / `error`.
  Expected business errors (validation failures, 404 from IDOR) MUST NOT be
  logged at `error` level — avoid alert noise.
- **Sensitive Data Masking**: Per Section 2 — mask/redact all secrets, tokens,
  PII before logging.
- **Minimum Metrics for Critical Flows**: R3/R4 flows (auth, payment, quota)
  MUST have minimum metrics — error rate, latency (p95/p99), throughput —
  before the feature is production-ready.
- **Actionable Alerting**: Alerts MUST link to a runbook or concrete handling
  steps; MUST NOT create alerts nobody knows how to act on.

---

## 8. Resilience & I/O Contracts

- **Explicit Timeout**: Every out-of-process call (DB query, HTTP to
  service/3rd-party API, cache) MUST have an explicit timeout — never the
  library's unbounded default.
  An inherited shared-client/session timeout suffices when tracing the actual
  call proves it applies; do not duplicate timeout constants at every caller.
- **Retry Policy**: Retry only transient errors (timeout, 5xx, network), with
  exponential backoff + jitter; MUST NOT blindly retry 4xx or business errors.
- **Circuit Breaker**: For unstable or high-load external dependencies,
  consider a circuit breaker to prevent cascading failure when the dependency
  goes down (SHOULD/MAY).
- **Input Validation at Boundary**: Validate untrusted HTTP/CLI/file/queue input
  at its real entry boundary before business use, with runtime-enforced checks.
  Use the stack's existing mechanism; TypeScript annotations alone do not
  validate runtime input. In NestJS projects, preserve class DTO/ValidationPipe
  and shared pagination conventions. Avoid redundant type checks only when
  every caller demonstrably passes the same validated boundary.
- **API Backward Compatibility & Versioning**: When changing a consumed API
  contract, MUST: add new fields as optional, never change type or remove old
  fields in the same version. If a breaking change is unavoidable, create a new
  version (`/v2`) and keep the old one with a clear deprecation timeline,
  notifying consumers in advance.

---

## 9. Definition of Done (DoD)

Before reporting a change as "Done", the Agent MUST check off the checklist for
the declared Risk Level (0.3) — **not one fixed checklist for every change**,
to avoid turning DoD into a robot checklist (running tsc/lint/test/migration
for a comment-only PR). Each tick MUST have **concrete evidence** (real command
output, not verbal assertion — per Section 5.3). Checklists are cumulative:
higher risk DoD = lower risk DoD + additions.

**Base DoD (every change, including R0):**
- [ ] Requirement implemented per original request (Section 5.2)
- [ ] Scope respected — no out-of-scope code changes
- [ ] Git diff self-reviewed last (Git Diff Integrity Check, Section 1); for
  non-Git global files use scoped before/after snapshots and hashes instead

**R1 (+ Base):**
- [ ] Tests for the changed part pass — with real runner output
- [ ] Lint/typecheck on affected part if the language/tooling supports it

**R2 (+ R1):**
- [ ] Full typecheck passes (real `tsc` or equivalent output)
- [ ] Full lint passes (real linter output)
- [ ] Negative/edge scenarios cover actual risk; ratios are not quotas (Section 4)
- [ ] API/contract changes aligned (Section 5.2, Section 8)

**R3 (+ R2):**
- [ ] Security checked accordingly (Section 2)
- [ ] DB migration verified (idempotent, backward-compatible — Section 3)
- [ ] Deployment/rollback plan stated (Section 6.1)

**R4 (+ R3):**
- [ ] Idempotency Key applied if the side effect risks duplicates on retry (Section 3)
- [ ] Full observability added (Section 7)
- [ ] Production Rollout Verification done: Deploy → Verify → Observe → Confirm (Section 6.3)

Use "N/A - reason" only for inapplicable items (such as no DB migration in a
policy change). Applicable checks that could not run are "NOT RUN - reason";
failures are "FAILED - evidence". Do not mark the corresponding gate complete.
