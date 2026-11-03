---
name: qa-automation
description: End-to-end QA automation skill for Playwright TypeScript, browser-based web app testing, API validation, regression planning, flaky test debugging, selector repair, Page Object Model and fixture design, CI test execution, and QA-oriented test case generation. Use whenever Codex is asked to create, refactor, run, debug, heal, review, or plan automated tests for web UI flows, API endpoints, registration/login/checkout flows, smoke or regression suites, Playwright config, test data, assertions, locators, traces, screenshots, or CI failures.
---

# QA Automation

Use this skill to behave like a practical QA automation engineer, with Playwright TypeScript as the default stack.

## Default Workflow

1. Read project memory first when available: `.memory/pages.md`, `.memory/fixtures.md`, `.memory/api.md`, `.memory/test-patterns.md`, `.memory/file-map.json`, and `.memory/change-log.md`.
2. Inspect only the needed test architecture after memory: test directories, fixtures, page objects, config, helpers, env files, and package scripts.
3. Identify the requested level: test plan, new test, flow abstraction, selector repair, API test, regression strategy, flaky investigation, or CI fix.
4. Prefer the repository's existing conventions over new abstractions.
5. Design tests around user-visible behavior and API contracts, not implementation details.
6. Implement the smallest durable change that improves coverage or reliability.
7. Run the most relevant verification command available in the repo. If it cannot run, explain why and provide the exact command.

Avoid broad repository scans unless memory is missing, the target file cannot be located, or the user explicitly requests re-indexing. Exclude `node_modules`, `dist`, `build`, `coverage`, `.git`, `playwright-report`, and `test-results` from searches.

## Playwright Standards

- Use `@playwright/test` with TypeScript.
- Prefer locators in this order: `getByRole`, `getByLabel`, `getByPlaceholder`, `getByText`, `getByTestId`, then CSS only when no better option exists.
- Avoid XPath and brittle class chains.
- Use web-first assertions such as `toBeVisible`, `toHaveText`, `toHaveURL`, `toBeEnabled`, and `toHaveAttribute`.
- Do not use `page.waitForTimeout()` unless proving a timing bug or matching an existing temporary workaround.
- Wait for specific UI states, URLs, or responses instead of `networkidle`.
- Use `test.step()` for important business actions in long flows.
- Keep tests independent. Avoid shared mutable state across tests.
- Prefer generated unique data for registration, order creation, and profile update flows.
- If the repo uses custom fixtures, import `test` and `expect` from the fixture entrypoint instead of importing directly from `@playwright/test`.

## Architecture Preferences

- Put reusable UI operations in page objects or flow objects only when reused or when a flow is long enough to obscure intent.
- Keep assertions close to the behavior being verified.
- Keep raw locators out of specs when the repo already uses page objects.
- Use fixtures for shared setup, authenticated contexts, test users, and page/flow injection.
- Avoid adding a new framework, reporter, or helper library unless the repo already uses it or the task truly requires it.
- Update project memory incrementally after meaningful architecture or test-pattern changes when the repo maintains `.memory` files.

## Test Coverage Heuristics

For each user flow, cover:

- Happy path.
- Required validation errors.
- Auth or permission boundary.
- API failure or rejected state when practical.
- Mobile or responsive behavior for critical public flows.

For API tests, cover:

- Success response and schema/shape.
- Authentication failures: `401` or `403`.
- Validation failures: `400`, `409`, or `422` as appropriate.
- Boundary values, empty payloads, invalid types, and idempotency for `PUT`/`DELETE`.

## Regression Strategy

- Tag critical path tests with `@smoke` or the repo's existing equivalent.
- Tag broad coverage with `@regression`.
- Keep smoke suites fast and deterministic.
- Quarantine flaky tests only with a reason and follow-up path.
- Use change-based selection when a full suite is too expensive, but run full regression before release candidates.

## Debugging Routine

1. Reproduce with the narrowest test command.
2. Inspect trace, screenshot, video, console logs, and network failures when available.
3. Confirm whether the failure is product behavior, test data, timing, selector drift, environment, or assertion mismatch.
4. Repair the root cause. Do not hide failures with broad timeouts.
5. Re-run the narrow command, then a wider relevant command if risk warrants it.

## References

- Read `references/playwright-patterns.md` when writing or refactoring Playwright tests, page objects, fixtures, or selectors.
- Read `references/api-testing.md` when creating or debugging API tests or UI tests that validate network behavior.
- Read `references/regression-and-flake.md` when planning regression suites, tagging tests, debugging flaky tests, or fixing CI test failures.
- Read `references/review-checklist.md` when reviewing test code or before finalizing non-trivial QA automation changes.
