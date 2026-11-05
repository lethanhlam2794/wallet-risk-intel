---
phase: 2
title: "API client, auth boundary, login, shell, routing"
status: completed
priority: P1
dependencies: [1]
---

# Phase 2: API Client, Auth Boundary, Login, Shell, Routing

## Overview

Thin typed fetch layer + sessionStorage-only credential store + login
page validating the key against `GET /api/v1/admin/provider-configs` +
route guard + minimal app shell.

## Requirements

- Functional:
  - `api.ts`: `apiFetch<T>(path, {method, body, key})` unwraps
    `{data}` envelope; throws `ApiError{status,code,message,
    allowed_hosts?,request_id?}` on `{error}` or non-2xx; never
    stringifies the key into logs/URLs; supports returning
    `{data, warnings}` for PUT.
  - `auth.ts`: `getKey/setKey/clearKey` on `sessionStorage` key
    `cw.admin.key` ONLY. Never `localStorage`, cookie, URL, console.
  - Login page: password input (`type="password"`,
    `autocomplete="off"`) + submit → `GET
    /api/v1/admin/provider-configs` with candidate key:
    `200` → store + `navigate("/providers")`;
    `401` → "invalid API key" inline error;
    `403` → "key valid but lacks admin role" inline error;
    network/`5xx` → retryable error state ("service unreachable,
    retry").
  - Guard: no key ⇒ `<Navigate to="/login">`; `401` mid-session ⇒
    `clearKey()` + redirect to `/login` with "session expired" notice.
  - Shell: header with app name + Logout button (clears key + router
    state → `/login`).
  - Routes: `/login`, `/providers`, `/providers/:code`; `*` ⇒ redirect
    to `/providers`.
- Non-functional: zero secrets in DOM attributes/logs; all fetch calls
  bounded by `AbortSignal.timeout(15_000)`.

## Architecture

- `auth-context.tsx` holds `apiKey` state initialized from
  `sessionStorage`; exposes `login(key)`, `logout()`,
  `handleUnauthorized()` (401 hook registered into `api.ts` via a
  **single-slot** subscriber — `onUnauthorized(cb)` replaces any prior
  callback and returns an unregister fn; avoids circular imports).
- `apiFetch` signature: `apiFetch(path, {key, method, body, signal,
  skipUnauthorizedHandler?})` → `Promise<Envelope<T>>` where
  `Envelope<T> = {data: T, warnings?: ProviderConfigWarning[]}`. On
  `401` it calls the registered unauthorized handler **unless
  `skipUnauthorizedHandler`** — the login probe MUST pass
  `skipUnauthorizedHandler: true` so a wrong candidate key produces
  the inline "invalid API key" error instead of a session-expired
  redirect (A1). Mid-session `401` (stored key rejected) → handler →
  `clearKey()` + redirect `/login?reason=session_expired`.
- `Network` type = `{code,name,chain_id,namespace}` — verified
  `openapi.yaml` L84–103 (`{data:[Network]}`).
- Types (`lib/types.ts`) mirror openapi.yaml exactly —
  `ProviderConfigView`, `ProviderConfigPatch` (all-optional),
  `ProviderConfigWarning{code,field?,network?,message}`,
  `ProviderConfigChange`, `Network{code,name,chain_id,namespace}`,
  `ApiKeyMask{set,last4}`.

## Related Code Files

- Create: `src/lib/api.ts`, `src/lib/auth.ts`, `src/lib/types.ts`,
  `src/auth/auth-context.tsx`, `src/auth/auth-guard.tsx`,
  `src/app-shell.tsx`, `src/features/login/login-page.tsx`,
  `src/App.tsx` (final routes)
- Modify: `src/main.tsx` (wrap in `AuthProvider` + `BrowserRouter`)
- Delete: none

## Implementation Steps

1. Red: `tests/api.test.ts` — envelope unwrap, error mapping incl.
   `error.allowed_hosts` on 400, `warnings` passthrough, 401 hook
   invoked, 15s timeout, key only in `X-API-Key` header (assert
   header; assert not in URL/body).
2. Red: `tests/auth.test.tsx` — 200 stores key + navigates; 401 error;
   403 role error; 5xx retryable; logout clears sessionStorage; guard
   redirects when no key; `enc:` keys still reach server on login
   (server decides — client does NOT pre-filter login attempts);
   **login-401 must NOT trigger the session-expired redirect**
   (assert inline "invalid API key" + still on `/login` + zero calls
   to the unauthorized handler — A1).
3. Implement `lib/types.ts`, `lib/api.ts`, `lib/auth.ts`,
   `auth/auth-context.tsx`, `auth-guard.tsx`, `app-shell.tsx`,
   `login-page.tsx`, wire `App.tsx` routes.
4. Green: run suite to pass; refactor.

## TDD Notes

- Mock `fetch` with `vi.stubGlobal`; assert call URL = `/api/...`
  (same-origin relative), headers contain `X-API-Key: <key>`,
  `Authorization` absent.
- RTL: render login under `MemoryRouter`; assert `sessionStorage`
  contents directly.

## Success Criteria

- [x] `tests/api.test.ts` + `tests/auth.test.tsx` green
- [x] Login page renders and routes per matrix above (200/401/403/5xx)
- [x] `sessionStorage` is the only persistence (`localStorage`
      untouched — test asserts)
- [x] `npm run lint && npx tsc -b && npm test -- --run` exit 0

## Risk Assessment

- Circular import `api.ts ↔ auth-context` — solved by subscriber
  registration (`onUnauthorized(cb)`), tested.
- StrictMode double-mount → double login probe: acceptable (GET,
  idempotent); document.
- Router v7 `data` APIs unnecessary — use plain `<Routes>` components
  (`BrowserRouter`, `Routes`, `Route`, `Navigate`, `useNavigate`,
  `useParams`).
