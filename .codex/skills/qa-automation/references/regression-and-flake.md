# Regression And Flake

## Tier Model

- `@smoke`: critical path, fast, deterministic, runs on every commit or PR.
- `@sanity`: core feature checks, runs on PR.
- `@regression`: broader behavior coverage, runs before merge or nightly.
- `@slow`: long-running tests, usually excluded from fast PR checks.
- `@quarantine`: known flaky test under investigation, excluded from blocking CI with a clear reason.

Use the repo's existing tags if they differ.

## Regression Selection

Choose scope from risk:

- Small UI copy or style change: affected tests plus smoke.
- Form validation or flow logic change: affected flow, error states, and smoke.
- Auth, payments, registration, routing, shared components, or API contract changes: affected tests plus broader regression.
- Major refactor or release candidate: full regression across configured browser projects.

## Flaky Investigation

Classify the failure before fixing:

- Selector drift: locator matches nothing or multiple elements.
- Timing race: assertion runs before state settles.
- Test data collision: duplicate users, reused records, unclean state.
- Environment issue: base URL, env vars, browser install, service unavailable.
- Product bug: actual behavior violates expected user outcome.

Use traces, screenshots, videos, console logs, and network logs when available.

## Repair Rules

- Replace brittle locators with role, label, text, or test id locators.
- Replace sleeps with specific waits or web-first assertions.
- Make test data unique and cleanup-aware.
- Narrow overly broad assertions.
- Do not increase global timeouts to hide one unstable condition.
- Quarantine only when the root cause cannot be fixed immediately and CI needs protection.

## Useful Commands

Use package scripts when present. Otherwise common Playwright commands are:

```bash
npx playwright test
npx playwright test --grep @smoke
npx playwright test --grep-invert @quarantine
npx playwright test --project=chromium
npx playwright test --last-failed
npx playwright show-report
```
