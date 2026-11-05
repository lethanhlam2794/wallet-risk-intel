---
phase: 6
title: "Test saturation, data-testid contract & verification gates"
status: completed
priority: P1
dependencies: [2, 3, 4, 5]
---

# Phase 6: Saturation Tests + Feature-Map Anchors + Gates

## Overview

Close out: blind/randomized test saturation, `data-testid` anchor
contract (consumed by pipeline Stage 5/6 E2E + feature map), full
verification gates, scope audit.

## Requirements

- Functional:
  - `data-testid` anchors (stable contract for E2E):
    - login: `login-form`, `login-key-input`, `login-submit`,
      `login-error`
    - shell: `app-header`, `logout-button`
    - list: `providers-table`, `provider-row-<code>`,
      `restart-badge-<code>` (when applicable)
    - detail: `provider-tabs`, `tab-config`, `tab-history`,
      `config-form`, `apikey-input`, `baseurl-input`,
      `baseurl-clear`, `chains-fieldset`, `chain-check-<code>`,
      `status-select`, `save-button`, `warnings-region`,
      `field-error-base_url`
    - history: `history-table`, `load-more`, `history-empty`
  - Blind saturation: randomized-envelope property tests —
    generated `ProviderConfigView[]` (random configured/status/
    requires_restart/chains) rendered → invariants asserted
    (masked key never raw, restart badge iff length>0,
    configured badge iff false); randomized PATCH-body property test
    (random dirty subset ⇒ body key-set === dirty set with
    value-elision applied); **tab-switch persistence case** — dirty
    Config form + accumulated History rows survive Config→History→
    Config round-trip under `forceMount` (A2); ≥10 iterations,
    deterministic seeds, zero flakes.
  - Gates: `npm run lint`, `npx tsc -b`, `npm test -- --run`,
    `npm run build` — all exit 0.
  - Scope audit: `git status` shows only `apps/web/` + plan dir;
    secret grep over `apps/web` clean; `.env*`/`dist/` ignored.
- Non-functional: tests stay honest — no `skip`, no weakened
  assertions; fetch mocks model exact envelopes.

## Architecture

- `tests/blind/` dir inside `apps/web/tests/` — randomized spec
  files (`saturation.test.tsx`) generating envelopes via seeded
  PRNG helpers (`tests/helpers/gen.ts`).
- Verification commands run from `apps/web/`.

## Related Code Files

- Create: `tests/helpers/gen.ts`, `tests/blind/saturation.test.tsx`,
  `apps/web/docs/` (pipeline Stage 6 outputs land here — docs only)
- Modify: components to add `data-testid` attrs (minimal diffs)
- Delete: none

## Implementation Steps

1. Red: write saturation specs first — they must fail where anchors
   missing.
2. Add `data-testid` attributes to components.
3. Green saturation + all unit suites.
4. Run gates sequentially, capture exit codes into plan reports.
5. `git status` + secret grep audit.

## TDD Notes

- Seeded PRNG (mulberry32) — deterministic failures reproducible.
- 10 iterations × distinct seeds; suite must pass 10/10 runs
  consecutively (Stage 4 loop harness re-runs the whole file).

## Success Criteria

- [x] `data-testid` contract complete (all anchors present in DOM)
- [x] Blind saturation 10/10 iterations green
- [x] lint / tsc -b / vitest --run / build — exit 0 each
- [x] `git status` clean outside `apps/web/` + `plans/<this-plan>/`
- [x] Secret scan over `apps/web` returns nothing

## Risk Assessment

- jsdom + jsdom-only APIs (scrollIntoView) — setup.ts mocks already
  copied; add mocks only if a new gap appears.
- Randomized tests could flake on RTL async — wrap queries in
  `findBy*`/`waitFor` with explicit boundaries.
