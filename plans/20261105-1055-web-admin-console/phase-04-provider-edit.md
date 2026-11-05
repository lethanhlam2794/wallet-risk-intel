---
phase: 4
title: "Provider edit — Config tab (PATCH discipline)"
status: completed
priority: P1
dependencies: [3]
---

# Phase 4: Provider Edit — Config Tab (`/providers/:code`)

## Overview

The highest-risk surface: PUT body must contain ONLY dirty fields;
blanked inputs are omissions, explicit clears are confirmed overrides;
`api_key` is write-only; response `warnings[]` must render.

## Requirements

- Functional:
  - Page: `GET /api/v1/admin/provider-configs/{code}` on mount;
    `404` → not-found state; tabs `Config` | `History` (copied `Tabs`
    primitive).
  - Fields:
    - `api_key`: `type="password"`, `autocomplete="new-password"`,
      NEVER prefilled; placeholder `set ••••4242` or `not set`; empty
      input ⇒ key omitted from PUT; non-empty ⇒ replace value;
      client-side reject `enc:` prefix (inline error, no submit).
      **Two-state only** (`untouched | set`) — NO Clear affordance:
      `api_key:""` semantics are not defined by the contract, so the
      UI must not offer it (A3).
    - `base_url`: text input prefilled with stored value (`""` shown
      blank when null); edits mark dirty; blanking the input alone
      does NOT enqueue `""` — clearing requires the explicit **Clear**
      button + `ConfirmDialog` ("sends explicit empty override —
      stored value cleared, no env fallback"). **Clear hidden/
      disabled when stored `base_url` is `null`** (A3 — a null→""
      write is meaningless).
    - `enabled_chains`: multi-select checkbox list (bordered
      fieldset; network code + name) populated from
      `GET /api/v1/networks`; stored `null` ⇒ visible "env default"
      caption (all-unchecked must NOT read as "none" — the caption is
      required, A3); any toggle ⇒ replace array; empty selection ⇒
      **Clear-to-empty confirm** ("disables this provider on every
      chain") then `enabled_chains: []`. One-way asymmetry:
      `[]`→`null` (restore env default) is impossible by contract —
      patch schema is `array`, not `array|null` — UI copy must not
      imply reversibility.
    - `status`: `Select` active/inactive.
  - Save: PUT with dirty-field body ONLY; **disabled while a save is
    in-flight** (double-click guard, A3) and **disabled when
    `buildPatch()` is empty** (fully-elided ⇒ zero requests — pinned
    behavior); `400` → map `error.message` + `error.allowed_hosts`
    next to `base_url`; `404` → not-found state; `429`/`5xx` →
    `toast` + retry affordance.
  - Post-write: render `warnings[]` — `last_active_member` &
    `no_enabled_chains` as destructive `Alert`, `requires_restart` as
    warning note, **unknown `code` values as a generic warning note**
    (forward-compat, A7); refetch view (or use returned `data`) and
    show post-write state incl. persistent "restart required" badge.
- Non-functional: no plaintext key in DOM state beyond the controlled
  input value; PUT body never contains untouched keys (hard gate in
  tests).

**Tabs state persistence (A2)**: copied `TabsContent` unmounts
inactive content by default — switching Config⇄History would destroy
form dirty state and remount+refetch History. `ui/tabs.tsx` forwards
props to `TabsPrimitive.Content` (verified: `React.ComponentProps
<typeof TabsPrimitive.Content>` spread) ⇒ pass **`forceMount`** to
BOTH `TabsContent`s in `provider-detail-page.tsx`; add the
tab-switch persistence test (phase-06).

## Architecture

- `provider-config-form.tsx`: per-field state machine —
  `{kind:"untouched"} | {kind:"set",value} | {kind:"cleared"}` for
  `base_url`, `enabled_chains`; `api_key` is two-state
  `{untouched|set}` only; `status` dirty when !== stored.
  `buildPatch()` produces `ProviderConfigPatch` (absent = untouched)
  and **elides `set` fields whose value equals stored**
  (`base_url`/`status` strict-equal; `enabled_chains` set-equality —
  type-then-revert emits no key, A3). ConfirmDialog
  (`components/confirm-dialog.tsx`) gates `cleared` transitions and
  empty-chain selection.
- `provider-detail-page.tsx`: fetch + tab layout + error states;
  owns `historyActivated` flag (latched once-true on first
  History-tab activation)
  passed to `provider-history.tsx` so history fetches lazily once and
  persists under `forceMount`.
- `enabled_chains` UI: checkbox list (network code + name) inside a
  bordered fieldset — deterministic, testable, no nested-popover
  pitfalls. (Dropdown-menu variant optional polish, NOT required.)

## Related Code Files

- Create: `src/features/providers/provider-detail-page.tsx`,
  `src/features/providers/provider-config-form.tsx`,
  `src/components/confirm-dialog.tsx`
- Modify: `src/App.tsx` (route), `src/components/index.ts` only if
  barrel needed — prefer direct imports, do NOT modify copied files.
- Delete: none

## Implementation Steps

1. Red: `tests/provider-form.test.tsx` — the critical suite:
   - dirty-only PUT: edit `base_url` ⇒ body `{base_url: "..."}` and
     NO other keys (assert exact key set).
   - blank `base_url` input ⇒ `base_url` ABSENT from body.
   - Clear action + confirm ⇒ `base_url: ""` present.
   - `enabled_chains` empty + confirm ⇒ `enabled_chains: []`; without
     confirm no request fires.
   - `api_key` empty ⇒ absent; typed ⇒ `api_key: "..."`; `enc:` ⇒
     inline error, zero requests; **no Clear control rendered for
     `api_key`**.
   - `status` change ⇒ `{status:"inactive"}` only.
   - **Elision (A3)**: edit `base_url` then revert to stored value ⇒
     body omits `base_url`; fully-elided patch ⇒ Save disabled and
     zero PUTs; `enabled_chains` reordered-but-equal ⇒ omitted.
   - **Save disabled while PUT in-flight** (double-click ⇒ 1 request).
   - `warnings[]` render: all three codes map to correct Alert
     severity; unknown `code` ⇒ generic warning note;
     `requires_restart` badge persists post-write.
   - `400` with `allowed_hosts` ⇒ field error shows echoed hosts.
2. Implement `confirm-dialog.tsx`, form, detail page.
3. Green + refactor.

## TDD Notes

- Fetch mock must model exact envelope incl. `{data, warnings}` PUT
  response; capture `JSON.stringify(body)` and assert parsed key set
  — not string containment.
- Use `user-event` for input/checkbox/dialog flows; assert dialog
  appears before any PUT.

## Success Criteria

- [x] Every PATCH-semantics test green (omit vs explicit-empty both
      directions)
- [x] `api_key` never prefilled; `enc:` rejected client-side
- [x] Warnings render with correct severities after save
- [x] Field-level 400 errors incl. `allowed_hosts`
- [x] `enabled_chains` options = live `/networks` codes
- [x] Lint/tsc/tests/build all green

## Risk Assessment

- Semantic inversion (`""` sent when input merely cleared visually)
  — guarded by `cleared`-vs-`untouched` state machine + dedicated
  assertions.
- Radix `Select` inside forms + jsdom — setup.ts already mocks
  pointer capture / ResizeObserver (copied).
- Empty `enabled_chains` array vs `null`: UI copy must make the
  distinction explicit ("env default" vs "disabled everywhere").
