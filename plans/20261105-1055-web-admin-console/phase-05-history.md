---
phase: 5
title: "History tab — audit pagination"
status: completed
priority: P2
dependencies: [3]
---

# Phase 5: History Tab — Keyset-Paginated Audit

## Overview

Audit table inside `/providers/:code` History tab:
`GET /api/v1/admin/provider-configs/{code}/changes?cursor&limit=50`,
"Load more" driven by `next_cursor`.

## Requirements

- Functional:
  - Columns: `field`, `previous_value → new_value`, `actor_id`
    (`actor_type` tooltip/suffix), `changed_at` (UTC, readable).
  - `field === "api_key"` ⇒ values render as masked shape
    `{set,last4}` (`"••••4242"` / `"not set"`), never raw; other
    fields render stored value (arrays joined or JSON inline).
  - First page loads when the History tab is first activated (parent
    passes `active` flag set on first activation); "Load more" button
    while `next_cursor` non-empty (and non-`""` — schema types it
    `string`, treat `""` as end); appended rows accumulate (no
    replace).
  - Empty history ⇒ empty-state row; error ⇒ Alert + retry.
  - **`forceMount` on both `TabsContent`s (A2)**: copied
    `TabsContent` unmounts inactive children by default, which would
    destroy Config dirty state on switch and refetch History every
    activation. With `forceMount`, History state + accumulated pages
    persist across tab switches; Config form dirty state survives.
  - `actor_id: null` renders as `"—"` (A7); `actor_type` as suffix/
    tooltip.
- Non-functional: cursor opaque pass-through (never parsed, sent
  verbatim URL-encoded); `limit=50` constant; UTC timestamps
  displayed ISO-ish.

## Architecture

- `provider-history.tsx`: state `{items, nextCursor, loading,
  loadingMore, error}`; `loadMore()` appends; table = copied `Table`
  primitives (not DataTable — no sorting needed) inside Card.
  Receives `active: boolean` prop — fetch fires when `active` first
  becomes true (and on provider `code` change, resetting state).
- `provider-detail-page.tsx` renders `<TabsContent forceMount
  value="config">` and `<TabsContent forceMount value="history">`;
  tracks `historyActivated` via `Tabs onValueChange`.

## Related Code Files

- Create: `src/features/providers/provider-history.tsx`
- Modify: `src/features/providers/provider-detail-page.tsx` (Tabs)
- Delete: none

## Implementation Steps

1. Red: `tests/history.test.tsx` — page1 returns `items:[2 rows] +
   next_cursor`; page2 (called with cursor) returns more +
   `next_cursor:""` ⇒ rows appended, button hidden; masked
   api_key entry renders `••••` shape, raw never shown; empty ⇒
   empty state; cursor passed verbatim in query param;
   **`actor_id:null` ⇒ `"—"`**; **no fetch until tab activated**;
   **switching Config→History→Config preserves dirty form state and
   accumulated history rows (forceMount — A2)**.
2. Implement component, wire tab.
3. Green + refactor.

## TDD Notes

- Assert fetch call #2 URL contains `cursor=<opaque>` verbatim
  (URL-encoded passthrough).
- Assert cumulative row count after load-more.

## Success Criteria

- [x] Keyset pagination appends, button hides at end
- [x] Masked `api_key` history entries only
- [x] Lazy first-fetch on tab activation
- [x] Tests/lint/tsc/build green

## Risk Assessment

- `previous_value/new_value` untyped (`any` in spec) — normalize via
  `formatChangeValue(field, value)` helper handling
  string/array/object/null; unit-test the helper with every field
  type incl. api_key object shape.
