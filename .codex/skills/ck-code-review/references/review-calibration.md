---
name: review-calibration
description: Calibrate review depth, severity, confidence, business correctness, and false-positive handling
---

# Review Calibration

## Purpose

Keep reviews proportional, actionable, and trustworthy. Review mode identifies the input;
it does not determine how deeply every risk dimension must be inspected. Classify depth
before reviewing, then classify severity and confidence independently for each finding.

## Review Depth

Choose depth from actual behavior and blast radius:

| Depth | Typical scope | Required treatment |
|---|---|---|
| Light | R0/R1, localized and reversible, no public contract or sensitive boundary | Diff/scope check, affected validation, focused verification, quick applicability scan of production risks |
| Standard | R2, multi-file behavior, internal contract or persistent state | Trace affected flow, inspect all applicable production-risk dimensions, focused negative tests |
| Deep | R3/R4, money, identity, security, migrations, public contracts, external side effects, cross-process workflows | Full applicable matrices, failure timing, compatibility, rollback, observability, and integration/runtime evidence |

Increase depth for broad fan-out, shared libraries, hot paths, weak tests, ambiguous ownership,
irreversible writes, or mixed-version deployment. Decrease it only when evidence proves the
change is isolated and reversible. A codebase scan is breadth-first: identify hotspots, then
review those deeply; do not apply maximum depth mechanically to every file.

Security and data-integrity checks are a minimum floor at every depth. A one-line change at
an authorization, query, deserialization, financial, or deletion boundary is not a light
review merely because the diff is small.

## Severity and Merge Triage

Assign severity from reachable impact, exploitability, affected scope, reversibility, and
availability of a safe workaround—not from checklist category or confidence.

| Severity | Meaning | Merge treatment |
|---|---|---|
| BLOCKER | Reachable security bypass, data loss/corruption, wrong financial finality, irreversible harmful side effect, or primary path cannot run | Must fix and re-verify before merge |
| MAJOR | Significant correctness, isolation, compatibility, availability, or resource defect with material user impact | Fix before merge or obtain explicit owner risk acceptance |
| MINOR | Limited-impact defect or maintainability/test gap with a safe workaround | Non-blocking; track with concrete rationale |
| NIT | Cosmetic or optional improvement with no meaningful correctness or operational impact | Optional; keep sparse |

Do not inflate severity because a topic sounds sensitive. Prove runtime reachability and
impact. Conversely, do not downgrade a severe defect because the fix is difficult.

## Confidence

Report confidence separately from severity:

| Confidence | Evidence standard |
|---|---|
| CONFIRMED | Direct source/runtime path plus authoritative contract or reproduction proves the defect |
| HIGH | Strong cross-layer static evidence; only environment-specific execution remains |
| MEDIUM | Plausible defect with partial path or contract evidence; targeted verification is required |
| LOW | Hypothesis worth preserving, but key preconditions or semantics are unverified |

Use labels such as `[BLOCKER][MEDIUM]`, never silently promote a suspicion to confirmed.
A credible safety-critical candidate may pause approval pending verification, but the report
must state what evidence is missing and the narrowest check that can resolve it.

## Finding Adjudication and False Positives

Before accepting a candidate finding:

1. Re-open the exact source and surrounding control flow; line references can drift.
2. Prove the path is runtime-reachable from a real entrypoint and applicable identity/state.
3. Identify the violated direct requirement, authoritative contract, or invariant.
4. Inspect callers, adapters, schema, configuration, and tests for a guard or guarantee that
   disproves the concern.
5. Distinguish an explicit owner decision from an accidental implementation choice. Do not
   reverse a verified user decision without presenting the trade-off.
6. Seek an independent oracle or focused reproduction when mocks or static reading cannot
   settle the contract.
7. Record the candidate as ACCEPTED, REJECTED, DISPUTED, or NEEDS_VERIFICATION.

Rejected findings must include a short reason and the evidence that disproved them. Do not
include rejected candidates in the actionable finding count. If evidence conflicts, stronger
primary/runtime evidence wins; unresolved conflicts remain DISPUTED rather than being merged
into a confident claim.

## Domain and Business-Logic Correctness

Spec compliance is not enough when the implementation contains formulas or implicit domain
rules. Extract business invariants from direct requirements and authoritative owners, then
test them independently of the implementation.

Inspect, where applicable:

- eligibility by role, tenant, account type, lifecycle state, and effective date;
- formulas, units, currency conversion, rounding, precision, caps, floors, and ordering;
- discount/fee precedence, mutually exclusive rules, aggregation windows, and timezone;
- boundary examples: zero, threshold, just below/above, maximum, duplicate, and reversal;
- conservation laws such as balanced debits/credits or totals matching component rows;
- caller differences that bypass a rule enforced only in one entrypoint.

Build a small truth table or worked examples from the requirement. Compare code output to
those expected results; do not copy the implementation formula into the test oracle. If the
business source is missing or contradictory, report NEEDS_CONTEXT instead of inventing it.

## Finding Format

Use one record per root cause:

```text
[SEVERITY][CONFIDENCE] Title
Status: ACCEPTED | DISPUTED | NEEDS_VERIFICATION
Location: file:line
Invariant: direct requirement or authoritative contract
Runtime path: entrypoint → sink/outcome
Evidence: source, schema, test, or runner output
Impact: reachable user/production effect and blast radius
Correction: smallest compliant fix
Verification: missing or completed regression/integration check
```

Do not combine independently occurring root causes into one finding. Do not split the same
failure mechanism into multiple findings merely because it appears in several layers.
