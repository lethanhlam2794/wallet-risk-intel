# Cook Evidence — web-admin-console

Working dir: `apps/web`. All commands run from repo root or `apps/web` as noted.
Toolchain: node v24.19.0, npm 11.17.0.

## Phase 1 — Scaffold + component library copy

Copy manifest: `reports/copy-manifest.txt` (all 14 copy-list paths
existence-checked OK before copy).

| Command | Exit | Evidence |
|---|---|---|
| `npm install` | 0 | 499 packages, 3m; 15 vulns (2 low, 13 moderate — none critical/high) |
| `npm ls vite` | 0 | `vite@8.3.5` deduped via `@tailwindcss/vite`, `@vitejs/plugin-react`, `vitest@5.1.0` — resolution gate PASSED, no vite devDep pin needed |
| `npx vite --version` | 0 | `vite/8.3.5 linux-x64 node-v24.19.0` |
| `npm run dev` + `curl :5173` | 0 | VITE v8.3.5 ready 451ms; `CURL_HTTP=200`; `<title>Check Wallet Admin</title>` |
| `npm run lint` | 0 | 0 errors, 1 warning (pre-existing `react-hooks/incompatible-library` on copied `data-table.tsx` — file is read-only per scope) |
| `npx tsc -b` | 0 | clean |
| `npm test -- --run` | 0 | 7 files / 54 tests passed (copied component suite) |
| `npm run build` | 0 | `dist/` produced: index.html + assets (294.78 kB JS, 57.38 kB CSS) |
| `grep -rn "dev-admin\|cw_" src tests` | 1 | clean — no secrets |
| `git status --short` | — | only `?? apps/web/` + plan dir (+ pre-existing untracked `docs/prompts/` file from Stage 1, untouched) |

Files written fresh (not copied): `package.json` (name `check-wallet-admin`,
dep set verbatim + `react-router-dom@^7`, `test:run` script added),
`index.html` (lang="en", title), `src/main.tsx` (StrictMode+BrowserRouter+
Toaster), `src/App.tsx` (route placeholder), `.gitignore`
(node_modules/dist/coverage/.env/.env.*/*.tsbuildinfo),
`vite.config.ts` (copied + `server.proxy['/api']→http://localhost:8010`).

Status: **completed** — all success criteria checked in phase file.

## Phase 2 — API client, auth boundary, login, shell, routing

TDD: `tests/api.test.ts` (11 tests) + `tests/auth.test.tsx` (9 tests)
written first; initial run failed on missing modules (expected Red).
Green after implementation.

| Command | Exit | Evidence |
|---|---|---|
| `npx vitest run tests/api.test.ts tests/auth.test.tsx` (Red) | 1 | module-not-found on `@/lib/api` — expected Red |
| `npx vitest run tests/api.test.ts tests/auth.test.tsx` (Green) | 0 | 20/20 passed |
| `npm run lint` | 0 | 0 errors, 2 warnings (copied data-table + `useAuth` context-export — standard pattern) |
| `npx tsc -b` | 0 | clean |
| `npm test -- --run` | 0 | 9 files / 74 tests passed |
| `npm run build` | 0 | dist rebuilt, 340.26 kB JS |

Files created: `src/lib/{types,api,auth}.ts`,
`src/auth/{auth-context,auth-guard}.tsx`, `src/app-shell.tsx`,
`src/features/login/login-page.tsx`, stub `providers-page.tsx` +
`provider-detail-page.tsx` (real impl in phases 3/4), `src/App.tsx`
(final routes), `src/main.tsx` (+AuthProvider).

Design note (bug found by Red test): the 401 handler originally did
`clearKey()` + imperative `navigate("/login?reason=session_expired")`
— the two commits raced and `LoginPage`'s authed-redirect clobbered the
reason (guard then bounced to plain `/login`). Fixed by keeping a
`sessionExpired` flag in auth state; the guard renders the redirect
atomically. `apiFetch` still calls `onUnauthorized` on 401 unless
`skipUnauthorizedHandler` (single-slot, A1-verified by tests).

Status: **completed**.

## Phase 3 — Providers list page

A5 anchor gate re-verified on the copied file: `data-table.tsx` props
`data/columns/loading/emptyMessage/onRowClick/getRowId/pageSize/
initialSorting/className` + `INTERACTIVE_ELEMENTS` /
`data-row-click-ignore` mechanism — matches contract, no wrapper needed.
`provider-row-<code>` testid lives on the row's code-cell span (DataTable
owns row markup; clicks bubble to the row handler — same E2E affordance).

| Command | Exit | Evidence |
|---|---|---|
| `npx vitest run tests/providers-list.test.tsx` | 0 | 9/9 passed (all 12 codes, configured badge, masked key, restart badge, chains null/[]/list, error+retry, 403, row click→detail, skeleton loading) |
| `npm run lint` | 0 | 0 errors, 2 warnings (unchanged baseline) |
| `npx tsc -b` | 0 | clean |

Note: `react-hooks/set-state-in-effect` (eslint-plugin-react-hooks v7,
error-level) fired on synchronous state resets in the fetch effect —
refactored so resets happen in the retry click handler; effect only
fetches. Test fix: tightened an over-broad regex that matched both Alert
title and description (assertion bug, not impl).

Files: `src/features/providers/providers-page.tsx`,
`src/features/providers/columns.tsx`; `tests/providers-list.test.tsx`.

Status: **completed**.

## Phase 4 — Provider edit (PATCH discipline)

| Command | Exit | Evidence |
|---|---|---|
| `npx vitest run tests/provider-form.test.tsx` | 0 | 20/20 passed |
| `npm run lint` | 0 | 0 errors, 2 baseline warnings |
| `npx tsc -b` | 0 | clean |

Covered by the suite (exact-key-set assertions on parsed PUT bodies):
dirty-only PUT; blanked `base_url` ⇒ omitted; Clear+confirm ⇒ `""`;
Clear hidden when stored null; `enabled_chains` empty+confirm ⇒ `[]`;
cancel ⇒ selection kept + zero PUT; whole-array replace; env-default
caption on null; `api_key` never prefilled / typed ⇒ sent / `enc:` ⇒
inline error + zero requests / NO Clear control; status ⇒ `{status}`;
elision on revert-to-stored (Save disabled, zero PUTs); chains
reordered-but-equal ⇒ omitted; Save disabled in-flight (double-click ⇒
1 request); warnings[] severity map incl. unknown code ⇒ generic note;
400 `allowed_hosts` ⇒ field error; 404 ⇒ not-found; forceMount
Config→History→Config dirty-state persistence.

Files: `src/components/confirm-dialog.tsx` (composes ui/dialog — no
copied file touched), `src/features/providers/provider-config-form.tsx`
({input,cleared} base_url model + {untouched|set} chains model +
buildPatch elision), `src/features/providers/provider-detail-page.tsx`
(Tabs + forceMount + latched `historyActivated` + formEpoch remount on
save), stub `provider-history.tsx` (real impl phase-05).

Status: **completed**.

## Phase 5 — History tab (keyset pagination)

| Command | Exit | Evidence |
|---|---|---|
| `npx vitest run tests/history.test.tsx` | 0 | 11/11 passed |
| `npm run lint` | 0 | 0 errors, 3 warnings (+1: `formatChangeValue` export) |
| `npx tsc -b` | 0 | clean |
| `npm test -- --run` | 0 | 12 files / 114 tests passed |

Covered: lazy first-fetch (no `/changes` call until tab activated);
limit=50; opaque cursor verbatim URL-encoded (`cursor=…%3D` asserted);
append accumulation; Load-more hidden on `next_cursor:""`; masked
`api_key` rows (`••••4242`/`not set`, raw-key regex absent);
`actor_id:null` ⇒ `—` + actor_type suffix; UTC timestamp; empty state;
error + retry; forceMount round-trip preserves dirty form AND
accumulated rows with zero refetch; `formatChangeValue` unit table.

Files: `src/features/providers/provider-history.tsx` (real impl —
derived `loading` avoids set-state-in-effect; render-time reset on
`code` change; render-time reset uses React's props-change pattern).

Status: **completed**.

## Phase 6 — Saturation, anchors, gates, audit

| Command | Exit | Evidence |
|---|---|---|
| `npx vitest run tests/blind/saturation.test.tsx` | 0 | 9/9 passed (deterministic — identical back-to-back run) |
| `npx tsc -b` | 0 | clean |
| `npm run lint` | 0 | 0 errors, 3 warnings (2 copied-lib + 1 component-export — unchanged baselines) |
| `npm test -- --run` | 0 | 13 files / 123 tests passed |
| `npm run build` | 0 | built in ~0.7s; 516 kB JS chunk (size warning only — radix + table) |
| `git status --short` | — | only `apps/web/` + `plans/20261105-1055-web-admin-console/` untracked; `docs/prompts/` pre-existing input |
| `git check-ignore -v apps/web/dist apps/web/.env` | 0 | both matched by `apps/web/.gitignore` |
| secret grep over `apps/web/src` + `tests` | — | clean — only match is the literal fixture `"new-secret-key"` in a test |

Refactor for testability: `buildPatch` + field-state types extracted to
`src/features/providers/patch.ts` (pure module) — same function the
form calls, so the property test exercises production code, not a copy.
No copied file under `src/components/**` was modified.

Anchors verified present (in state): login-form/-key-input/-submit/
-error; app-header, logout-button; providers-table, provider-row-<code>
×12, restart-badge-<code>; provider-tabs, tab-config/-history,
config-form, apikey-input, baseurl-input/-clear, chains-fieldset,
chain-check-<code> ×6, status-select, save-button, warnings-region +
warning-<code>, field-error-base_url; history-table, load-more,
history-empty.

Saturation: 10 seeded list-render iterations (masked-key cell exact,
restart badge iff len>0, configured badge iff false, chains rendering);
15 pure buildPatch key-set iterations + 4 UI-driven iterations
(captured PUT body key-set === independent oracle incl. clear ⇒ "",
[] chains via check-uncheck, elision ⇒ zero PUTs); seeded tab-switch
persistence.

Fix during phase-6: generator typed `requires_restart` as
`RestartField[]` (field names, not chain codes); key-mask invariant
scoped to the key cell (row textContent concatenates cells).

Status: **completed**.
