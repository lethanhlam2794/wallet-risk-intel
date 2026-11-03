# Review Checklist

Use this checklist when reviewing or finalizing QA automation changes.

## Code Quality

- Test name describes behavior and expected outcome.
- Test has meaningful assertions.
- Selectors follow the repo's preferred locator strategy.
- No arbitrary sleeps.
- No hardcoded production secrets or irreversible production actions.
- Test data is unique or isolated.
- Cleanup exists where data pollution is likely.
- Page objects and fixtures match existing project style.

## Coverage

- Happy path is covered.
- At least one negative or validation path is covered for important flows.
- API status or persisted state is verified where UI alone is insufficient.
- Critical user journeys are tagged for smoke/regression if the repo uses tags.

## Maintainability

- Reused behavior is extracted only when it reduces real duplication.
- Assertions are not hidden in helpers unless that is the local convention.
- Error messages and failure points are clear.
- Tests can run independently and in parallel.

## Final Verification

- Run the narrowest relevant test command first.
- Run a broader suite if shared fixtures, config, or helpers changed.
- Report exact commands and outcomes.
