---
name: spec-compliance-review
description: Independent review lane checking implementation and business behavior against authoritative requirements
---

# Spec Compliance Review

## Purpose

Verify implementation matches what was requested. Well-written code that doesn't match
requirements is still wrong. This lane is independent from safety and production-risk
review: a spec failure blocks approval, but MUST NOT prevent security, data-integrity,
runtime-flow, or data-loss checks from running.

## When to Use

- After implementing features from a plan
- Before code quality review pass
- When plan/spec exists for the work being reviewed

## Process

1. **Load spec/plan** — Read the plan.md or phase file that defined this work
2. **List requirements** — Extract every requirement, acceptance criterion
3. **Extract domain invariants and worked examples** — especially formulas, units,
   eligibility, precedence, thresholds, lifecycle rules, and role/account differences
4. **Build an invariant-to-runtime proof row** for each material requirement and
   invariant. Rewrite it as a falsifiable proposition; identify its runtime owner; trace
   entrypoint to observable side effect; exercise applicable negative paths; verify
   deployment/configuration reachability; then actively seek evidence that disproves the
   candidate conclusion.
5. **Check each requirement and invariant** against actual implementation:
   - Present? → PASS
   - Missing? → MISSING (blocks approval; continue the independent safety lane)
   - Extra (not in spec)? → EXTRA (flag for removal unless justified)
6. **Verdict:**
   - All requirements met, no unjustified extras → PASS
   - Missing requirements → FAIL → blocks approval and requires correction/re-review
   - Unjustified extras → WARN → discuss with user
7. **Continue the independent safety lane regardless of verdict.** A FAIL may reduce
   non-safety maintainability work, but never suppress review for security, data loss,
   financial integrity, unsafe external effects, or severe runtime failures.

## Checklist Template

| # | Falsifiable invariant | Runtime owner | Entrypoint → side effect | Negative path before sink | Deployment/config proof | Disproof attempted | Status |
|---|-----------------------|---------------|--------------------------|---------------------------|-------------------------|--------------------|--------|
| 1 | [input/state → observable result] | [component] | [concrete path] | [rejection evidence] | [wiring/runner/env] | [guard/test/runtime evidence] | PASS/MISSING/EXTRA/NEEDS_VERIFICATION |

## Red Flags

- Skipping spec review because "code looks good"
- Accepting extra features without spec justification
- Treating spec review as optional
- Treating spec failure as permission to skip an independently reachable safety defect
- Copying the implementation formula into the expected-value oracle
- Marking PASS from code presence without identifying the runtime owner
- Testing a service with injected configuration that the deployed process never supplies
- Confirming happy-path output without proving invalid data is rejected before its sink
