# Prompt: codex — web-admin-console

> Created: 2026-11-05 10:44 | Target: codex | Template: M (agentic task brief)

## Prompt

````
### Objective
Scaffold `apps/web` — an admin console (React 19 + TS strict + Vite +
Tailwind v4) for the check-wallet backend — MVP scope: admin API-key
login + provider-config management (list / edit / audit history). Reuse
the vendored component library by COPYING it, not re-generating.

### Context (carry forward — verified anchors, do not re-derive)
- Monorepo root `/home/lethanhlam/Desktop/Profile/check-wallet`; Go/Gin
  backend lives at `apps/api` (module `checkwallet`), listens on
  `HTTP_ADDR=:8010`. Backend is a FROZEN contract — do not modify it.
- Auth: `X-API-Key` header on every request; roles `reader`/`admin`.
  Admin routes reject `reader` with `403`. Errors use the envelope
  `{"error":{"code","message",...}}`; success uses `{"data":...}` plus
  optional top-level `{"warnings":[...]}` on PUT.
- Live admin surface (contract: `apps/api/api/openapi.yaml`):
  - `GET /api/v1/admin/provider-configs` → `{data: ProviderConfigView[]}`
    (12 tx-provider codes; `configured:false` = no DB row yet, defaults/
    env in effect; first PUT materializes the row).
  - `GET /api/v1/admin/provider-configs/{code}` → `{data: ProviderConfigView}`
  - `PUT /api/v1/admin/provider-configs/{code}` → `{data, warnings?[]}`
  - `GET /api/v1/admin/provider-configs/{code}/changes?cursor&limit` →
    `{data:{items, next_cursor}}` (limit ≤100; keyset — page with the
    returned `next_cursor`).
  - `GET /api/v1/networks` (reader+) → registered network codes for the
    `enabled_chains` multi-select.
- `ProviderConfigView` = `{source_code, configured, api_key:{set,last4},
  base_url, enabled_chains, status(active|inactive), catalog_active,
  effective_active, requires_restart[], updated_by, updated_at}`.
- `ProviderConfigPatch` = whitelisted `{api_key, base_url, enabled_chains,
  status}`. CRITICAL SEMANTICS: an absent JSON key leaves the column
  untouched; `""` and `[]` are EXPLICIT overrides (they clear/disable —
  they do NOT fall back to env). `api_key` rejects values starting with
  `enc:`; `base_url` must be `https` and on the adapter SSRF allowlist
  (400 echoes `allowed_hosts`); `enabled_chains` elements must be
  registered network codes.
- `warnings[]` codes: `requires_restart` (stored ≠ in-effect — only
  `status` is runtime-effective, TTL ~30s), `no_enabled_chains`,
  `last_active_member` (failover chain left with no active member).
- Component library to copy from (verified on disk):
  `/home/lethanhlam/Desktop/Profile/component` — React 19, TS strict,
  Vite, Tailwind CSS v4 (`@tailwindcss/vite`), shadcn registry
  `radix-nova`, lucide icons, sonner toasts, @tanstack/react-table,
  vitest + RTL + jsdom. Primitives: `src/components/ui/*` (button, card,
  dialog, input, label, select, table, tabs, badge, alert, skeleton,
  spinner, tooltip, dropdown-menu, pagination, empty, separator,
  sonner); composites: `src/components/{data-table,empty-state}.tsx`;
  tokens: `src/index.css` (`:root` oklch semantic tokens); utils:
  `src/lib/utils.ts`; config: `components.json`, `eslint.config.js`,
  `tsconfig*.json`, `vite.config.ts`, `package.json`.

### Files & Scope
- Primary (create): `apps/web/**` — full standalone Vite app
  (`package.json`, `vite.config.ts`, `tsconfig*`, `index.html`,
  `eslint.config.js`, `src/`). No root npm workspace wiring yet.
- Copy-then-adapt FROM the component repo (READ-only source):
  `src/components/**`, `src/lib/utils.ts`, `src/index.css`,
  `components.json`, eslint/tsconfig/vite configs, and the dependency
  set in its `package.json`.
- Do NOT copy: `src/demo/**`, `tests/blind/**`, `dist/`, `plans/`,
  `docs/`, `.git`, `package-lock.json`, README of the source repo.
- Do NOT touch: `apps/api/**`, root `Makefile`, `README.md`, `docs/`,
  `plans/`, `.env*`. The backend contract is fixed.
- New deps allowed beyond the copied set: `react-router-dom` only.
  No state-management/query libraries — a thin fetch wrapper suffices.

### Constraints
- Dev server: Vite proxy `server.proxy['/api'] → 'http://localhost:8010'`
  so the SPA is same-origin; do NOT add CORS or touch backend config.
- Admin key handling (auth boundary — MUST):
  - Login page collects the API key; validate by calling
    `GET /api/v1/admin/provider-configs`: `200` = admin ok, store +
    route to `/providers`; `401` = "invalid key"; `403` = "key valid but
    lacks admin role"; network/5xx = retryable error state.
  - Store the key in `sessionStorage` ONLY (survives reload, dies with
    the tab). NEVER `localStorage`, NEVER a cookie, NEVER in URLs,
    NEVER `console.log` it. Send it only as `X-API-Key` to same-origin
    `/api/*`. Logout clears storage + router state; `401` mid-session
    drops the key and returns to login with a notice.
- Provider list page (`/providers`): `DataTable` of all 12 codes —
  status badge (`effective_active`), `configured:false` rows marked
  "defaults/env", `requires_restart.length>0` → persistent
  "restart required" badge, masked key column (`••••4242` / "not set").
- Provider edit page (`/providers/:code`, tabs: Config / History):
  - `api_key` input is write-only: NEVER prefill, never display
    plaintext, placeholder shows masked state (`set ••••4242` /
    `not set`); empty input = field OMITTED from PUT body; typing =
    replace. Client-side reject values starting with `enc:`.
  - PATCH discipline (critical): build the PUT body from dirty fields
    ONLY. A blanked-out `base_url` input must NOT silently send `""` —
    clearing requires an explicit "Clear" action + `AlertDialog`
    confirm ("sends explicit empty override — stored value cleared,
    no env fallback"). Same for `enabled_chains`: an empty selection
    requires a confirm that it "disables this provider on every chain".
  - `enabled_chains`: multi-select populated from `GET /api/v1/networks`.
  - `status` toggle: `active`/`inactive` select.
  - After PUT: render `warnings[]` — `last_active_member` and
    `no_enabled_chains` as destructive `Alert`, `requires_restart` as
    a warning note; refetch the view and show post-write state.
  - Map `400` to field errors (surface echoed `allowed_hosts` next to
    `base_url`); `404` → not-found state; `429`/5xx → toast + retry.
- History tab: audit table (field, previous→new, actor, changed_at);
  masked `api_key` values rendered as `{set,last4}` shape, never raw;
  "Load more" driven by `next_cursor` (no cursor → hide button).
- Routing: `/login`, `/providers`, `/providers/:code`; guard redirects
  unauthenticated → `/login`. App shell: minimal header (app name,
  key name?, logout) — designed to add pages later.
- Tests (vitest + RTL, `tests/` or colocated):
  - login: 200 stores key + navigates; 401 error; 403 role error;
    sessionStorage cleared on logout.
  - PATCH body: only dirty fields present — MUST assert a blank
    `base_url` input produces NO `base_url` key in the request JSON,
    and that explicit clear produces `""` (both via fetch mock).
  - `api_key` input never prefilled; `enc:` prefix rejected client-side.
  - `warnings[]` render after save; `requires_restart` badge visible.
  - History: next_cursor pagination appends items.
  - No test may weaken assertions to pass; mock fetch must model the
    real envelope shapes above.

### Acceptance Criteria
- [ ] `cd apps/web && npm install` clean; `npm run dev` serves `:5173`
      with `/api` proxied to `:8010`.
- [ ] Admin key login → `/providers` renders all 12 provider codes with
      real backend data; `configured:false` visibly marked.
- [ ] Reader key → "lacks admin role" state (no crash, no data fetch).
- [ ] Edit `chainscan` `base_url` → request JSON contains ONLY that
      field (verified via test/devtools); untouched fields absent.
- [ ] Clearing `enabled_chains` shows confirm, then sends `[]`;
      response warnings rendered; `restart required` badge persists.
- [ ] History tab pages with Load more; masked key entries only.
- [ ] `npm run lint`, `tsc -b`, `npm test -- --run`, `npm run build`
      all exit 0; zero console errors in a manual login→edit→save pass.
- [ ] No file outside `apps/web/` modified; no secret committed;
      `.env*` and `dist/` in `apps/web/.gitignore`.

### Stop Conditions
- Pause and ask before: editing anything outside `apps/web/`; adding
  deps beyond the copied set + `react-router-dom`; changing the backend
  contract to "fit the UI"; committing real API keys.
- Checkpoint after each phase: scaffold+copy → api client+login →
  providers list → edit form+PATCH discipline → history → tests.

### Session Strategy
- Read `apps/api/api/openapi.yaml` (provider-configs + schemas sections)
  and the component repo's `src/index.css`, `data-table.tsx`,
  `package.json`, `vite.config.ts`, `tsconfig*.json` FIRST, then copy.
- Copy the component tree wholesale, then customize — do not re-run
  `shadcn init`; the registry style (`radix-nova`) and tokens already
  encode the look.
- One thin `src/lib/api.ts` (fetch + `X-API-Key` + envelope unwrap +
  typed errors); pages compose primitives — no new abstraction layers.
- Only make changes directly requested. Do not add extra pages
  (clients, screenings, metrics), features, or abstractions beyond this
  brief — later pages land in later prompts.
- Verification before declaring done: run lint + typecheck + tests +
  build yourself and paste exit codes.
````

## Strategy

Front-loaded verified contracts (envelope shapes, PATCH explicit-zero
semantics, masked-key invariants, warning codes) so the agent cannot
misencode the API; Scenario-Gate findings became hard constraints
(session-only key storage, omit-vs-clear PUT discipline, warning
surfacing) plus binary acceptance criteria; scope-locked to `apps/web/`
with an explicit copy-not-regenerate component strategy.

## Notes

Start Codex from repo root (`/home/lethanhlam/Desktop/Profile/check-wallet`).
Backend should be reachable on `:8010` for live verification; deps via
`make -C apps/api deps-up` (Postgres :5433, Redis :6380) + bootstrap keys
from `apps/api/.env` if not already running.

This prompt is for an agentic tool with real system access. Review the
scope locks, forbidden actions, and stop conditions before pasting.
Confirm file paths, directories, and permissions match the actual project.
