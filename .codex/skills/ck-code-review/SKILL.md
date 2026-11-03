---
name: ck-code-review
description: "Review code quality with evidence-based rigor. Supports input modes: pending changes, PR number, commit hash, and codebase scan. Focuses on bugs, regressions, maintainability, reliability, and verification gaps."
metadata:
  author: claudekit
  version: "2.1.0"
  category: utilities
  keywords: "review, quality, verification, reliability"
  argument-hint: "[#PR | COMMIT | --pending | codebase [parallel]]"
  when-to-use: "Invoke to review diffs, PRs, commits, or full codebases."
---

# Code Review

Production-readiness code review with technical rigor, evidence-based claims, and verification over performative responses. Reviews focus on production risks, regression paths, and whether the implementation matches the requested change.
## Prerequisite Compliance Gate (Fail-Closed)

Before reviewing any code, diff, or spec, the reviewer MUST execute:
1. `view_file` on the canonical human learning ledger: `~/Desktop/Profile/learning/learning.md` (or `/home/lethanhlam/Desktop/Profile/learning/learning.md`). Do NOT load `learning-metadata.json` into the prompt — it is machine-of-record consumed by the CLI only.
2. Identify applicable Principles (`[G-XXX]`) and Patterns (`[P-XXX]`) for the touched domain (e.g. `[P-011]` for account types, `[G-001]` for verification, `[G-002]` for concurrency/locking, `[P-020]` for DB test isolation).
3. Load `references/review-rulebook.md` and apply each rule whose scope matches the changed domain. Rulebook checks supplement, but do not replace, the ledger's evidence and lifecycle.

`learning-ledger-sync.sh --summary`, `--audit`, and metadata health checks are
optional diagnostics. Their failure MUST NOT block or abort a code review; report
the ledger issue separately and continue reviewing source, tests, and runtime
evidence. Candidate details may still come from `--check '<root cause>'` / `--read`
when those commands are available.

**FAIL-CLOSED STOP CONDITION**:
If `AGENTS.md` or `learning.md` cannot be read or was omitted from prompt paths:
- STOP immediately. Return `Status: BLOCKED` or `Status: NEEDS_CONTEXT`.
- Detail: "Cannot access canonical compliance sources (AGENTS.md or learning.md). Review aborted."
- Do NOT guess approximate paths, do NOT search heuristically, do NOT proceed with review, and do NOT produce a `DONE` or `APPROVED` verdict.

## Input Modes

Auto-detect from arguments. If ambiguous or no arguments, prompt via `AskUserQuestion`.

| Input | Mode | What Gets Reviewed |
|-------|------|--------------------|
| `#123` or PR URL | **PR** | Full PR diff fetched via `gh pr diff` |
| `abc1234` (7+ hex chars) | **Commit** | Single commit diff via `git show` |
| `--pending` | **Pending** | Staged + unstaged changes via `git diff` |
| *(no args, recent changes)* | **Default** | Recent changes in context |
| `codebase` | **Codebase** | Full codebase scan |
| `codebase parallel` | **Codebase+** | Parallel multi-reviewer audit |

**Resolution details:** `references/input-mode-resolution.md`

### No Arguments

If invoked WITHOUT arguments and no recent changes in context, use `AskUserQuestion` with header "Review Target", question "What would you like to review?":

| Option | Description |
|--------|-------------|
| Pending changes | Review staged/unstaged git diff |
| Enter PR number | Fetch and review a specific PR |
| Enter commit hash | Review a specific commit |
| Full codebase scan | Deep codebase analysis |
| Parallel codebase audit | Multi-reviewer codebase scan |

## Core Principle

**YAGNI**, **KISS**, **DRY** always. Technical correctness over social comfort.
**Be honest, be brutal, straight to the point, and be concise.**

Default assumption: reviewed code may be AI-assisted. Do not trust polished shape, confident comments, or happy-path tests. Verify behavior, project-rule compliance, and scope discipline from evidence.

No rubber-stamp reviews. The reviewer is not trying to please the author or preserve momentum; the reviewer enforces the rulebook and blocks defects, regressions, hidden scope drift, and AI-slop patterns.

Verify before implementing. Ask before assuming. Evidence before claims.

## Practices

| Practice | When | Reference |
|----------|------|-----------|
| **Review calibration** | Every review: choose depth, severity, confidence, and adjudicate findings | `references/review-calibration.md` |
| **Spec compliance** | Plan/spec work; independent from the mandatory safety lane | `references/spec-compliance-review.md` |
| **Runtime flow tracing** | Cross-module behavior, external integrations, stateful workflows | `references/runtime-flow-tracing.md` |
| **Production risk review** | Contracts, validation, state, failures, compatibility, data, auth, resources, operations | `references/production-risk-review.md` |
| Receiving feedback | Unclear feedback, external reviewers, needs prioritization | `references/code-review-reception.md` |
| Requesting review | After tasks, before merge, stuck on problem | `references/requesting-code-review.md` |
| Verification gates | Before any completion claim, commit, PR | `references/verification-before-completion.md` |
| Edge case scouting | After implementation, before review | `references/edge-case-scouting.md` |
| **Checklist review** | Pre-landing, `/ck:ship` pipeline, security audit | `references/checklist-workflow.md` |
| **Task-managed reviews** | Multi-file features (3+ files), parallel reviewers, fix cycles | `references/task-management-reviews.md` |

## Quick Decision Tree

```
SITUATION?
│
├─ Input mode? → Resolve diff (references/input-mode-resolution.md)
│   ├─ #PR / URL → fetch PR diff
│   ├─ commit hash → git show
│   ├─ --pending → git diff (staged + unstaged)
│   ├─ codebase → full scan (references/codebase-scan-workflow.md)
│   ├─ codebase parallel → parallel audit (references/parallel-review-workflow.md)
│   └─ default → recent changes in context
│
├─ Calibrate depth from risk and blast radius (references/review-calibration.md)
├─ Received feedback → STOP if unclear, verify if external, implement if human partner
├─ Completed work from plan/spec:
│   ├─ Lane A: Spec + business compliance (references/spec-compliance-review.md)
│   ├─ Lane B: Safety/integrity/runtime risk — ALWAYS run, independent of Lane A
│   │   └─ Cross-module? → Trace runtime flow → Apply production-risk review
│   ├─ Merge + adjudicate findings → severity + confidence + dedup
│   └─ Verification gate → Run required tests/builds before claims
├─ Completed work (no plan) → Scout → Code quality → Verification
├─ Pre-landing / ship → Load checklists → Two-pass review → Verification
├─ Multi-file feature (3+ files) → Create review pipeline tasks (scout→review→fix→verify)
└─ About to claim status → RUN verification command FIRST
```

### Review Protocol

**Stage 0 — Calibration** (load `references/review-calibration.md`)
- Choose light, standard, or deep review from risk and blast radius, not input mode alone
- Apply the safety/data-integrity floor even for a tiny diff

**Stage 1 — Independent Review Lanes**
- Lane A: spec compliance and business-rule correctness
- Lane B: security, data integrity, runtime flow, and production risk
- A spec failure blocks approval but MUST NOT suppress Lane B or conceal a more severe defect

**Mandatory Invariant-to-Runtime Proof Loop**

For every material invariant, requirement, and acceptance criterion, MUST:
1. Rewrite it as a falsifiable proposition with an observable PASS/FAIL outcome.
2. Identify the runtime owner responsible for enforcing it. Treat a missing or duplicated
   owner as evidence, not as an implicit implementation detail.
3. Trace a real entrypoint or trigger through every boundary to the persisted, external,
   or user-visible side effect. Code presence alone does not prove reachability.
4. Trace relevant malformed, stale, conflicting, wrong-owner, timeout, and failure inputs.
   Prove rejection occurs before persistence or another irreversible side effect.
5. Verify deployed reachability through applicable module/DI wiring, process manifests,
   environment variables, feature flags, cron/job registration, migration runners, and
   topology. A service that passes in isolation but cannot run as deployed is a failure.
6. Attempt to disprove the candidate with callers, guards, schema constraints, tests, and
   runtime output. Record ACCEPTED, REJECTED, DISPUTED, or NEEDS_VERIFICATION with evidence.

Do not mark an invariant PASS until all applicable proof-loop fields are covered. Use the
proof matrix in `references/spec-compliance-review.md`; mark a field `N/A` only with a
concrete reason.

**Adversarial Review Pass**

For R2+ changes, cross-module behavior, persistent state, external integrations, or
security/data-integrity boundaries, select every applicable pass below after the proof loop:

- **Counterexample:** construct the smallest input that could violate the invariant.
- **Temporal/interleaving:** reorder timeout, retry, late response, restart, cron overlap,
  and concurrent execution events.
- **Differential contract:** compare producer response, consumer type, entity/DDL, SQL,
  configuration, and test fixture for semantic drift in names, types, units, precision,
  nullability, and status meaning.
- **Metamorphic input:** change response order, casing, precision, duplicate rows, or
  equivalent serialization; invariant-preserving inputs must preserve the required result.
- **Fault injection:** inspect or reproduce crashes before/after persistence, transport
  loss, Redis/DB failure, rollback, cancellation, and worker timeout.
- **Test adequacy:** verify fixtures activate the intended branch and mocks preserve the
  runtime contract; passing tests are evidence only for the behavior they exercise.
- **Ownership graph:** enumerate every producer, consumer, writer, reader, scheduler,
  adapter, and configuration owner for the data or state; flag duplicated or bypassed owners.

Each selected pass MUST produce one of: a finding with evidence, a rejected candidate with
the disproof, or `N/A` with a concrete reason. Do not report a pass merely because the code
contains a guard or a test exists. Record newly verified recurring failure mechanisms in the
learning ledger; keep these review techniques in this skill.

**Schema/Constraint Defense-in-Depth Pass**

For every schema change, JSONB migration, persistence refactor, or validation boundary,
apply the `Typed-to-JSONB Constraint Preservation` rule in
`references/review-rulebook.md`. At minimum, construct missing-key, JSON-null, wrong-type,
boundary, and duplicate counterexamples; evaluate PostgreSQL CHECK expressions for
TRUE/FALSE/UNKNOWN; compare old typed DDL guarantees with the new representation; and
state whether each claim was verified by a real database test, static proof, or remains
unverified. DTO tests and repository mocks cannot prove database constraints.

**Stage 2 — Finding Adjudication and Quality**
- Deduplicate by failure mechanism and runtime path, not wording or file alone
- Attempt to disprove each candidate finding before accepting it
- Assign severity and confidence independently using `references/review-calibration.md`
- Apply maintainability and performance depth appropriate to Stage 0

**Final Verification**
- Runs AFTER Stage 2 passes
- Re-run the relevant tests, build, lint, or manual reproduction
- Verify accepted findings are fixed and no new regression is introduced
- BLOCKER findings block merge; MAJOR findings require a fix or explicit owner risk acceptance
- Safety-critical candidates with incomplete evidence block approval pending targeted verification,
  but must be labeled with their actual confidence rather than reported as confirmed

## Receiving Feedback

**Pattern:** READ → UNDERSTAND → VERIFY → EVALUATE → RESPOND → IMPLEMENT
No performative agreement. Verify before implementing. Push back if wrong.

**Full protocol:** `references/code-review-reception.md`

## Requesting Review

**When:** After each task, major features, before merge

**Process:**
1. **Scout edge cases first** (see below)
2. Get SHAs: `BASE_SHA=$(git rev-parse HEAD~1)` and `HEAD_SHA=$(git rev-parse HEAD)`
3. Dispatch code-reviewer subagent with: WHAT, PLAN, BASE_SHA, HEAD_SHA, DESCRIPTION
4. Fix BLOCKER immediately; fix MAJOR or obtain explicit owner risk acceptance

**Full protocol:** `references/requesting-code-review.md`

## Edge Case Scouting

**When:** After implementation, before requesting code-reviewer

**Process:**
1. Invoke `/ck:scout` with edge-case-focused prompt
2. Scout analyzes: affected files, data flows, error paths, boundary conditions
3. Review scout findings for potential issues
4. Address BLOCKER gaps before approval; retain unresolved safety candidates for verification

**Full protocol:** `references/edge-case-scouting.md`

## Task-Managed Review Pipeline

**When:** Multi-file features (3+ changed files), parallel code-reviewer scopes, review cycles with BLOCKER/MAJOR fix iterations.

**Fallback:** Task tools (`TaskCreate`/`TaskUpdate`/`TaskGet`/`TaskList`) are CLI-only — unavailable in VSCode extension. If they error, use `TodoWrite` for tracking and run pipeline sequentially. Review quality is identical.

**Pipeline:** scout → review → fix → verify (each a Task with dependency chain)

```
TaskCreate: "Scout edge cases"         → pending
TaskCreate: "Review implementation"    → pending, blockedBy: [scout]
TaskCreate: "Fix blocking issues"      → pending, blockedBy: [review]
TaskCreate: "Verify fixes pass"        → pending, blockedBy: [fix]
```

**Parallel reviews:** Spawn scoped code-reviewer subagents for independent file groups (e.g., backend + frontend). Fix task blocks on all reviewers completing.

**Re-review cycles:** If fixes introduce new issues, create cycle-2 review task. Limit 3 cycles, escalate to user after.

**Full protocol:** `references/task-management-reviews.md`

## Verification Gates

**Iron Law:** NO COMPLETION CLAIMS WITHOUT FRESH VERIFICATION EVIDENCE

**Gate:** IDENTIFY command → RUN full → READ output → VERIFY confirms → THEN claim

**Requirements:**
- Tests pass: Output shows 0 failures
- Build succeeds: Exit 0
- Bug fixed: Original symptom passes
- Requirements met: Checklist verified

**Red Flags:** "should"/"probably"/"seems to", satisfaction before verification, trusting agent reports

**Full protocol:** `references/verification-before-completion.md`

For verbose commands, use the shared token-efficient wrapper at
`../_shared/references/token-efficient-verification.md`. A shortened log is
acceptable only when it still proves the claim: success must show the runner
summary and exit code `0`; failure must show targeted error context and the
original non-zero exit code. If evidence is ambiguous, rerun narrowly.

## Integration with Workflows

- **Subagent-Driven:** Scout → Review → Verify before next task
- **Pull Requests:** Scout → Code quality → Verify → Merge
- **Task Pipeline:** Create review tasks with dependencies → auto-unblock through chain
- **Cook Handoff:** Cook completes phase → review pipeline tasks → all complete → cook proceeds
- **PR Review:** `/ck:code-review #123` → fetch diff → full review pipeline on PR changes
- **Commit Review:** `/ck:code-review abc1234` → review specific commit with full pipeline

## Codebase Analysis Subcommands

| Subcommand | Reference | Purpose |
|------------|-----------|---------|
| `/ck:code-review codebase` | `references/codebase-scan-workflow.md` | Scan & analyze the codebase |
| `/ck:code-review codebase parallel` | `references/parallel-review-workflow.md` | Ultrathink edge cases, then parallel verify |

## Learning Pattern Ledger
Post-review learnings, defect postmortems, and edge-case patterns must be recorded in the global ledger:
- **Ledger Path**: `~/Desktop/Profile/learning/learning.md`
- Follow the Codex Learning Pattern Ledger format (`[P-XXX]`).

## Bottom Line

1. Resolve input mode first — know WHAT you're reviewing
2. Calibrate depth from risk and blast radius
3. Keep spec and safety review independent
4. Trace cross-module runtime behavior before accepting outcome claims
5. Triage by severity and confidence; adjudicate false positives
6. Evidence before claims

Verify. Scout. Question. Then implement. Evidence. Then claim.
## Workflow Position

**Typically follows:** `/ck:cook` (review after implementation), `/ck:fix` (review after bug fix)
**Typically precedes:** `/ck:ship` (ship after review passes)
**Related:** `/ck:scout` (scout before reviewing), `/ck:test` (test before reviewing)
