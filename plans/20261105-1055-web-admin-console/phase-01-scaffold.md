---
phase: 1
title: "Scaffold apps/web + component library copy"
status: completed
priority: P1
dependencies: []
---

# Phase 1: Scaffold `apps/web` + Component Library Copy

## Overview

Create the standalone Vite app skeleton and copy the vendored component
library wholesale (no `shadcn init`, no re-generation). App must install
clean, boot dev server on `:5173`, and proxy `/api` → `:8010`.

## Requirements

- Functional: `npm install` clean; `npm run dev` serves `:5173`;
  `vite.config.ts` proxies `server.proxy['/api'] → 'http://localhost:8010'`;
  `npm run build`, `npm run lint`, `npm test -- --run` wired.
- Non-functional: TS strict (inherited `tsconfig.app.json`: strict +
  `noUncheckedIndexedAccess` + `erasableSyntaxOnly`); `.gitignore`
  covers `node_modules/`, `dist/`, `.env*`, `coverage/`.

## Architecture

Copy list (source `/home/lethanhlam/Desktop/Profile/component`, READ-only):

- `src/components/**` (ui/*, data-table.tsx, empty-state.tsx, index.ts)
- `src/lib/utils.ts`, `src/index.css`, `src/vite-env.d.ts`
- `components.json`, `eslint.config.js`, `tsconfig.json`,
  `tsconfig.app.json`, `tsconfig.node.json`, `vite.config.ts`
- `public/favicon.svg`
- `tests/setup.ts`, `tests/components/**` (library unit tests — free
  regression safety for copied primitives)
- `package.json` dep/devDep sets **verbatim** + add
  `react-router-dom` (only allowed new dep)

Do NOT copy: `src/demo/**`, `tests/blind/**`, `tests/app.test.tsx`,
`dist/`, `plans/`, `docs/`, `.git`, `package-lock.json`, source
`README.md`, `src/App.tsx`, `src/main.tsx`, `index.html`.

Adaptations (written fresh, not copied):

- `package.json`: name `check-wallet-admin`; scripts `dev/build/
  lint/preview/test` copied; add `test:run: "vitest run"`.
- `vite.config.ts`: keep plugins/alias/test block; add
  `server: { proxy: { "/api": "http://localhost:8010" } }`.
- `index.html`: `lang="en"`, `<title>Check Wallet Admin</title>`,
  favicon `/favicon.svg`.
- `src/main.tsx`: StrictMode + `BrowserRouter` + `<App/>` + `<Toaster/>`.
- `src/App.tsx`: route table placeholder (full wiring in phase 02).
- `.gitignore`: `node_modules`, `dist`, `.env`, `.env.*`, `coverage`,
  `*.tsbuildinfo` exception note — `tsconfig` uses
  `node_modules/.tmp` for buildinfo so just node_modules suffices.

## Related Code Files

- Create: `apps/web/**` (whole tree per plan.md architecture)
- Modify: none
- Delete: none

## Implementation Steps

0. **Existence-check every copy-list path before copying** (fail
   loudly if any is missing): `src/components/**`, `src/lib/utils.ts`,
   `src/index.css`, `src/vite-env.d.ts`, `components.json`,
   `eslint.config.js`, `tsconfig.json`, `tsconfig.app.json`,
   `tsconfig.node.json`, `vite.config.ts`, `public/favicon.svg`,
   `tests/setup.ts`, `tests/components/**`, `package.json`. Record the
   verified list in `reports/copy-manifest.txt` (inside the plan
   reports dir, not the repo).
1. `mkdir -p apps/web/{src/{lib,components,features/{login,providers},auth},tests,public}`.
2. Copy the copy-list files preserving structure.
3. Write adapted `package.json`, `index.html`, `src/main.tsx`,
   `src/App.tsx`, `.gitignore`.
4. Patch `vite.config.ts` with the `/api` proxy block.
5. `cd apps/web && npm install` — must exit 0.
6. **Vite resolution gate** (deterministic, not assumed): run
   `npm ls vite` and `npx vite --version`. If either fails, add
   `"vite": "^8.3.4"` to devDependencies, reinstall, and record the
   deviation in `reports/` (prompt's "react-router-dom only" refers to
   feature deps; vite is already transitively present — pinning makes
   resolution deterministic on a lockfile-less install).
7. Boot check: `npm run dev` serves on 5173 (curl HTML 200), then stop.
8. Baseline: `npm run lint`, `npx tsc -b`, `npm test -- --run`,
   `npm run build` — all exit 0 (copied component tests may already pass).

## TDD Notes

- Phase is scaffolding; the "tests" are the build/lint/test gates +
  copied library unit tests continuing to pass in the new location.
- Red anchor: before copying, `apps/web` does not exist; after, all
  gates green.

## Success Criteria

- [x] `cd apps/web && npm install` exit 0
- [x] `npm run dev` serves `:5173` HTML (curl 200); `/api` proxy block
      present in `vite.config.ts`
- [x] `npm run lint` exit 0
- [x] `npx tsc -b` exit 0
- [x] `npm test -- --run` exit 0 (copied component suite green)
- [x] `npm run build` exit 0, `dist/` produced and git-ignored
- [x] No files outside `apps/web/` touched; no secrets in tree
      (`grep -r "dev-admin\|cw_" apps/web/src apps/web/tests` empty)

## Risk Assessment

- `vite` absent from copied deps → resolved transitively via vitest 5
  (lock showed 8.3.4). If `npm run dev` fails on resolution, add
  `"vite": "^8.3.4"` devDep and record the deviation in reports/.
- react-router-dom version drift: use `^7` (React 19 peer-ok); if
  registry resolution fails, fall back to `^6.30.0` and record.
- `verbatimModuleSyntax` + `erasableSyntaxOnly` will reject sloppy
  imports — enforce `import type` discipline everywhere downstream.
