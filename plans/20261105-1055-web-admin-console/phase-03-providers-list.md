---
phase: 3
title: "Providers list page"
status: completed
priority: P2
dependencies: [2]
---

# Phase 3: Providers List Page (`/providers`)

## Overview

`DataTable` rendering all 12 transaction-provider rows from
`GET /api/v1/admin/provider-configs`, with status/config/key/restart
indicators; row click navigates to `/providers/:code`.

## Requirements

- Functional:
  - Columns: `source_code`, effective status badge
    (`effective_active` → Active/Inactive), `configured` marker
    (`configured:false` → "defaults/env" badge), masked key
    (`api_key.set ? "••••" + (last4 ?? "") : "not set"`), `base_url`
    (or "—"), `enabled_chains` (null → "env default"; `[]` → "none";
    else join), `requires_restart` → persistent "restart required"
    badge when `length > 0`.
  - Loading state → skeleton rows (DataTable `loading` prop); error
    state → inline Alert with retry; empty (never expected — backend
    always returns 12) → `emptyMessage`.
  - Row click → `/providers/:code` (respect DataTable
    `INTERACTIVE_ELEMENTS` ignore mechanism).
  - `403` (reader key somehow stored) → "lacks admin role" state, no
    crash; `401` → handled globally (phase 02).
- Non-functional: `data-testid` anchors on table region and key
  interactive elements (see feature-map contract in phase 06).

## Architecture

- `providers-page.tsx`: `useEffect` fetch on mount via `apiFetch`;
  local `useState` for `{rows, loading, error}`; render `DataTable`
  with `columns` from `columns.tsx`.
- `columns.tsx`: `ColumnDef<ProviderConfigView>[]`; badge cells use
  copied `Badge` variants (`success`/`warning`/`secondary`/
  `destructive` as appropriate).
- No pagination state needed (12 rows); `pageSize={50}` single page.

## Related Code Files

- Create: `src/features/providers/providers-page.tsx`,
  `src/features/providers/columns.tsx`
- Modify: `src/App.tsx` (route element)
- Delete: none

## Implementation Steps

0. **Anchor gate (A5)**: re-read copied `apps/web/src/components/
   data-table.tsx`; confirm prop names `data`, `columns`, `loading`,
   `emptyMessage`, `pageSize`, `onRowClick`, `getRowId`,
   `initialSorting` and the `data-row-click-ignore` /
   `INTERACTIVE_ELEMENTS` mechanism before writing columns (verified
   in orchestrator scouting — this is a Cook-time gate).
1. Red: `tests/providers-list.test.tsx` — fetch mock returns 12-row
   envelope; assert all 12 `source_code`s render; `configured:false`
   rows show "defaults/env"; masked key shows `••••4242` / "not set";
   `requires_restart.length>0` shows badge; error envelope → Alert;
   row click navigates.
2. Implement page + columns.
3. Green + refactor.

## TDD Notes

- Fixture envelope copied verbatim from openapi example shape
  (extended to 12 codes).
- Assert by `data-testid` + text — not by class names.

## Success Criteria

- [x] All 12 provider codes listed from envelope
- [x] `configured:false` visually marked
- [x] Masked key rendering correct (`set+last4` / `not set`)
- [x] `requires_restart` badge persistent per row
- [x] Tests green; lint/tsc/build green

## Risk Assessment

- DataTable `onRowClick` + inner interactive elements — use
  `data-row-click-ignore` on any in-cell button.
- `enabled_chains: null` vs `[]` — render distinct labels; test both.
