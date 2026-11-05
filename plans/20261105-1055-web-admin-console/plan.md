---
title: "Web Admin Console (apps/web)"
status: completed
created: 2026-11-05
feature_slug: web-admin-console
blockedBy: []
risk_level: R3
source_prompt: docs/prompts/20261105-1044-codex-web-admin-console.md
---

# Web Admin Console — `apps/web`

## Overview

Scaffold `apps/web` — a standalone React 19 + TS strict + Vite + Tailwind v4
admin console for the check-wallet backend. MVP scope: admin API-key login +
provider-config management (list / edit / audit history). The component
library at `/home/lethanhlam/Desktop/Profile/component` is **copied, not
re-generated** (no `shadcn init`; registry style `radix-nova` + oklch tokens
already encode the look).

The Go backend (`apps/api`, `:8010`) is a **frozen contract** — not modified.
The SPA reaches it same-origin via Vite `server.proxy['/api'] →
http://localhost:8010`; no CORS changes.

## Verified Anchors (scouted, do not re-derive)

- Backend contract: `apps/api/api/openapi.yaml` lines 1074–1263,
  1844–1919. 12 provider codes: `alchemy, blockscout, ankr, covalent,
  moralis, tatum, bitquery, oklink, tokenview, chainbase, chainscan,
  nodereal`.
- Envelope: success `{data:...}` (+ optional top-level `warnings:[]` on
  PUT); error `{error:{code,message,request_id,...}}` — `400` may carry
  `error.allowed_hosts: string[]`.
- `ProviderConfigView.enabled_chains` is `string[] | null`; `base_url`,
  `updated_by`, `updated_at` nullable; `api_key` is always masked
  `{set:boolean,last4:string|null}`.
- `GET /api/v1/networks` → `{data: [{code,name,chain_id,namespace}]}`
  (reader+ role).
- Component repo (READ-only source): deps `react@^19.3.1`,
  `radix-ui@^1.8.1` (monolithic), `@tanstack/react-table@^8.21.3`,
  `sonner`, `lucide-react`, `cn`, `tw-animate-css`,
  `@fontsource-variable/geist`, `@tailwindcss/vite@^4.3.3`,
  `vitest@^5.0.5` + RTL + jsdom; resolved `vite@8.3.4` (via vitest).
  `vite.config.ts` already uses `vitest/config` + `tests/setup.ts`.
  `@/*` alias → `./src/*`; barrel `@/components`.
- **No `alert-dialog` primitive exists** — confirm dialogs compose
  `ui/dialog` + `ui/button`.
- Copy list: `src/components/**`, `src/lib/utils.ts`, `src/index.css`,
  `src/vite-env.d.ts`, `components.json`, `eslint.config.js`,
  `tsconfig*.json`, `vite.config.ts`, `public/favicon.svg`,
  `tests/setup.ts`, `tests/components/**`.
- Exclude: `src/demo/**`, `tests/blind/**`, `tests/app.test.tsx`
  (demo-bound), `dist/`, `plans/`, `docs/`, `.git`, `package-lock.json`,
  source `README.md`, `src/App.tsx`/`main.tsx`/`index.html` (rewritten,
  not copied).
- Env: `apps/api/.env` holds `API_BOOTSTRAP_ADMIN_KEY=dev-admin-*`,
  `API_BOOTSTRAP_READER_KEY=dev-reader-*`; Postgres `:5433` + Redis
  `:6380` already up (`api-postgres-1`, `api-redis-1`); API server must
  be started (`go run ./cmd/api`) for live verification. Secrets never
  enter the repo or committed files.
- Toolchain: node v24.19.0, npm 11.17.0.
- Only new dep beyond copied set: `react-router-dom`.

## Scope Locks

- Create/modify **only** under `apps/web/` (plus this plan's own
  `reports/` evidence). Do not modify `apps/api/**`, root `Makefile`,
  `README.md`, `docs/`, existing `plans/**`, `.env*`.
- "Do NOT touch `plans/`" in the source prompt = do not modify existing
  plan artifacts; this pipeline's own plan dir is new bookkeeping,
  consistent with 9 prior plans.
- `api_key` is write-only in UI: never prefilled, never rendered
  plaintext, rejected client-side if it starts with `enc:`.
- Admin key in `sessionStorage` only — never localStorage/cookie/URL/
  logs; sent only as `X-API-Key` to same-origin `/api/*`.
- PUT body = dirty fields only. Blanked input ⇒ field omitted; explicit
  "Clear" + confirm ⇒ explicit `""`/`[]` override.

## Architecture

```
apps/web/
  package.json            # copied dep set + react-router-dom
  vite.config.ts          # copied + server.proxy['/api']→:8010
  tsconfig{,.app,.node}.json, eslint.config.js, components.json
  index.html              # adapted: lang="en", title "Check Wallet Admin"
  .gitignore              # node_modules, dist, .env*, coverage
  public/favicon.svg
  src/
    main.tsx              # BrowserRouter
    App.tsx               # routes: /login /providers /providers/:code + guard
    index.css             # copied tokens (radix-nova / oklch)
    lib/
      utils.ts            # cn (copied)
      api.ts              # fetch wrapper: X-API-Key, envelope unwrap, ApiError
      auth.ts             # sessionStorage key store (get/set/clear + subscribe)
      types.ts            # ProviderConfigView/Patch/Change/Warning/Network
    components/           # copied wholesale (ui/*, data-table, empty-state, index.ts)
    components/confirm-dialog.tsx  # composes ui/dialog for explicit-clear confirms
    app-shell.tsx         # header: app name, logout
    auth/auth-context.tsx # session key state, login()/logout(), 401 drop
    auth/auth-guard.tsx   # unauth → /login
    features/
      login/login-page.tsx
      providers/providers-page.tsx        # DataTable of 12 codes
      providers/provider-detail-page.tsx  # tabs: Config | History
      providers/provider-config-form.tsx  # PATCH discipline + warnings
      providers/provider-history.tsx      # keyset "Load more"
      providers/columns.tsx               # DataTable column defs
  tests/
    setup.ts, components/** (copied library tests)
    api.test.ts, auth.test.tsx, provider-form.test.tsx, history.test.tsx
```

Data flow: `auth-context` owns `apiKey` (sessionStorage-backed);
`api.ts` reads it per call; `401` outside the login probe ⇒
`auth.clearKey()` + router redirect to
`/login?reason=session_expired` (the login probe passes
`skipUnauthorizedHandler` and surfaces inline errors instead).

## Phase Roadmap

| Phase | Title | Depends |
|---|---|---|
| 01 | Scaffold + component library copy | — |
| 02 | API client, auth boundary, login, shell, routing | 01 |
| 03 | Providers list page | 02 |
| 04 | Provider edit — Config tab (PATCH discipline) | 03 |
| 05 | History tab (keyset pagination) | 03 |
| 06 | Test saturation + verification gates | 02–05 |

## TDD Contract (per phase)

Every phase defines Red tests first. Fetch is always mocked with the
real envelope shapes (`{data}`, `{error}`, `{data,warnings}`); no test
weakens assertions.

## Risk Matrix

| Risk | Mitigation |
|---|---|
| `""` vs absent-key PUT semantics inverted | dedicated tests assert both directions; Clear requires explicit dialog confirm |
| API key leakage (logs, DOM, storage) | sessionStorage only; write-only input; `console.*` grep in verification; `autocomplete="off"`, `type="password"` |
| vitest watch hangs CI | `npm test -- --run`; scripts also add `test:run` alias |
| `vite` not a direct dep in copied set | vitest 5 carries vite dep; lock shows 8.3.4; verify `npm run dev` boots in phase 01 |
| `enabled_chains: null` vs `[]` confusion | null = env/unset (display "env default"), `[]` = explicit disable-everywhere (confirm required) |
| No alert-dialog primitive | `confirm-dialog.tsx` composes `Dialog` (verified exports in `components/index.ts`) |
| Stage 5 backend dependency | start `go run ./cmd/api` + verify `GET /api/v1/networks` before browser pass |
| npm registry availability for react-router-dom | pin `react-router-dom@^7` (React 19-compatible); fallback `^6` line if resolution fails |

## Stage 1 Debate Summary

**Round 1 — Devin Critic (session `shrouded-sphere`)**: verdict
`CONSENSUS_WITHHELD`. Structural audit passed (6/6 phase files).
Raised 7 amendments; all accepted and merged:

- **A1** login-probe 401 must not fire the session-expired handler →
  `apiFetch` gains `skipUnauthorizedHandler`; login test pins it
  (phase-02).
- **A2** Radix `TabsContent` unmounts inactive children → Config
  dirty state would be destroyed on tab switch + History refetched
  every activation. Fixed via `forceMount` on both contents
  (verified `ui/tabs.tsx` forwards props); lazy-fetch now driven by
  parent `historyActivated` flag; persistence test added (phase-04,
  05, 06).
- **A3** PATCH discipline hardening: `buildPatch()` elides
  value-equal fields (set-equality for arrays); `api_key` restricted
  to `untouched|set` — no Clear (contract-undefined semantics);
  `base_url` Clear disabled when stored null; Save disabled
  in-flight and when patch fully elides to `{}` (phase-04).
- **A4** phase-01 gains copy-list existence check +
  `reports/copy-manifest.txt` + deterministic vite-resolution gate.
- **A5** phase-03 gains data-table prop anchor gate.
- **A6** `/api/v1/networks` shape verified `openapi.yaml` L84–103 →
  `{data:[{code,name,chain_id,namespace}]}` (phase-02).
- **A7** `actor_id:null` → `"—"`; unknown warning codes → generic
  warning note (phase-04/05).

Critic's unverifiable items (tool-rejected): tabs `forceMount`
forwarding — orchestrator verified `TabsContent` spreads
`React.ComponentProps<typeof TabsPrimitive.Content>`; `data-table`
props — verified (`loading`,`emptyMessage`,`pageSize`,`onRowClick`,
`getRowId`); `tests/components` — 7 files exist; tsconfig flags —
verified strict + `noUncheckedIndexedAccess` + `erasableSyntaxOnly`;
vite 8.3.4 — resolved via vitest dep.

**Round 2 — Devin Critic re-adjudication**: all A1–A7 verified
in-file; `PLAN_CONSENSUS_REACHED`. Two non-blocking nits applied:
`plan.md` 401-wording now carves out the login probe;
`historyActivated` documented as latched once-true.

Critic session: `shrouded-sphere` (rounds 1–2).

## Out of Scope (explicit)

Client-management UI, screenings UI, metrics, dashboards, websockets,
state-management/query libraries, backend changes, CORS, root workspace
wiring.
