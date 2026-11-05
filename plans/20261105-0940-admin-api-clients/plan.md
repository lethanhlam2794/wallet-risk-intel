---
title: "Admin API Clients Management"
status: completed
created: 2026-11-05
feature_slug: admin-api-clients
blockedBy: []
risk_level: R4
---

# Admin API Clients Management

## Overview

Partners authenticate with `X-API-Key` resolved against `api_clients`
(migrations/000001). Today keys exist only via env bootstrap
(`API_BOOTSTRAP_*` → `UpsertAPIClient` at api start); there is **no
endpoint to create, list, or revoke client keys**. This plan adds the
admin surface powering the future `apps/web` console:

- `GET  /api/v1/admin/clients` — list clients (never key material)
- `POST /api/v1/admin/clients` — server generates `cw_<rand>` key,
  stores SHA-256, returns plaintext **exactly once**
- `PATCH /api/v1/admin/clients/{id}` — rename / change role /
  deactivate-reactivate, guarded against admin lockout

No schema migration: `api_clients(id,name,key_hash,role,is_active,
created_at,last_seen_at)` already carries everything needed.

**Risk level R4** (engineering standard §0.3): touches the auth
boundary — credential issuance + privilege grant. Feature flag
justification: not needed — this is a new endpoint surface with zero
existing consumers, and `Deps.Clients == nil` already removes the
routes entirely (the existing nil-gate pattern). POST is intentionally
**not idempotent**: a retried create mints a second live key whose
plaintext is never retrievable; duplicates are list-visible and
revocable — accepted semantics, documented.

## Design Decisions

| Topic | Decision | Rationale |
|---|---|---|
| Key format | `cw_` + base64url(32 bytes crypto/rand) ≈ 47 chars | identifiable, high entropy, URL-safe |
| Key storage | SHA-256 hex only, via `middleware.HashAPIKey` | raw keys never stored/logged (AGENTS §12); single digest impl — `apiclient` delegates, no duplicate |
| Plaintext disclosure | Once, in POST 201 body `api_key`; response carries `Cache-Control: no-store` | industry standard (Stripe-style); shared-cache retention of key material closed |
| Patch writes | Column-scoped UPDATE queries | repo convention (provider_configs.sql) — concurrent patches don't clobber; also preserves `last_seen_at` (mutated by `TouchAPIClient`) |
| Lockout guard (a) | Reject self-demotion (admin→reader) and self-deactivation unconditionally | footgun protection; never a legitimate op; rename-self allowed |
| Lockout guard (b) | Post-state check: reject when mutation removes an *active admin* and zero other `role=admin AND is_active` rows remain (`id != target`) | allows no-op transitions (demote/deactivate already-inactive → success); pinned SQL predicate |
| Guard serialization | `SELECT pg_advisory_xact_lock(hashtext('apiclient.admin_guard'))` at tx start | namespaced key — ingestion already uses raw `source_id` lock space; serializes A↔B concurrent demote without FOR UPDATE deadlock. Invariant: every role/is_active writer holds it (`UpsertAPIClient` exempt — never removes admins) |
| Guard error | `409` + stable code `ADMIN_LOCKOUT` | stable error codes (spec §9) |
| Bootstrap interplay | PATCH on env-managed row (hash ∈ configured bootstrap key hashes) succeeds but returns `warnings:[{code:"env_managed",message:"…reverted at restart while env var is set; unset API_BOOTSTRAP_* to revoke permanently"}]` | `UpsertAPIClient` re-sets `is_active=TRUE` on every boot — an unqualified 200 would be a false-success revocation; mirrors providerconfig `requires_restart` warning convention. Recovery path (env bootstrap) preserved — alternative of dropping `is_active=TRUE` from upsert rejected |
| Mutation audit | One structured `slog` domain log per mutation (actor_id, actor_name, target_id, changed_fields, request_id) | AccessLog logs the route *pattern*, not the concrete target id or fields — credential lifecycle is the highest-value audit surface; no new table |
| Pagination | Keyset cursor `(created_at,id)` DESC + `next_cursor`; `limit` default 50, >200 clamps to 200, ≤0/non-int → 400 | repo convention (provider_config_changes); empty page returns `items:[]` not null |
| Invalid cursor | 400 `BAD_REQUEST` | matches providerconfig mapping of ErrInvalidCursor |
| Empty PATCH `{}` | No-op → returns current view (200) | matches providerconfig PUT {} semantics |
| Body whitelist | `DisallowUnknownFields` on POST **and** PATCH | unknown-key rejection; also blocks a `key_hash`/`api_key` injection field |
| `name` validation | trim + `utf8.RuneCountInString` ∈ [1,150] | VARCHAR(150) counts characters, Go `len()` counts bytes |
| View fields | `id, name, role, is_active, created_at, last_seen_at` — never `key_hash`/`api_key` | enumerated, asserted by tests |
| Roles | `reader` (partners) + `admin` (internal) both creatable | named admin keys replace shared bootstrap-admin |
| Audit table | Skipped (no new table) | domain log + `last_seen_at` covers MVP; table is a later phase if needed |

## Phase Roadmap

| # | Phase | Status | Scope |
|---|-------|--------|-------|
| 1 | Domain + persistence | completed | sqlc queries, `internal/apiclient` service (generate/hash/list/create/patch guards + env_managed warnings + domain log), service tests on `check_wallet_apiclient_test` |
| 2 | HTTP surface + contract | completed | `httpserver.ClientHandler`, Deps wiring, openapi.yaml examples, API tests |

## Risk Assessment

- **Admin lockout race**: A↔B concurrent demote → serialized by
  namespaced advisory xact lock; post-state count check inside tx.
  Mid-flight actor demotion is safe: guards read DB state inside the
  lock, and `IdentifyClient` re-resolves per request anyway.
- **Bootstrap resurrection**: env-seeded row PATCHed → response carries
  `env_managed` warning; operator told revocation persists only after
  unsetting `API_BOOTSTRAP_*`.
- **Key leak via logs**: raw key only in 201 body; AccessLog logs route
  pattern not body (verified); `no-store` on 201; tests assert key
  absent from subsequent GET and all list bodies.
- **Duplicate names**: allowed (no unique constraint); `name` is display
  metadata, `id` is identity; PATCH targets by id.
- **Arbitrary-format keys remain valid**: no format gate on inbound
  `X-API-Key` — bootstrap/legacy keys keep working; `cw_` is only the
  *generated* shape.
- **Lost 201 response / retry**: second live key minted, plaintext
  unrecoverable by design — list-visible, revocable.

## Stage 1 Debate Summary

Devin Critic session `shaded-chemistry`, round 1: verdict CHALLENGED —
no fatal flaws; 10 amendments (A1–A10) covering: namespaced advisory
lock key, package-private test DSN, single hash implementation,
post-state guard math + inactive edge cases, explicit self-guard scope,
bootstrap-resurrection `env_managed` warning, structured mutation log,
R4 classification + flag/retry justification, pinned contract edges
(empty PATCH, limit bounds, cursor code, POST field whitelist,
`no-store`, `items:[]`, view enumeration), rune-based name validation.

Codex conceded all 10 — all are spec-level fixes; none alter the
architecture. Amendments folded into plan + phase files (this revision).

Round 2 (same session): **PLAN_CONSENSUS_REACHED** — all amendments
verified landed, zero overclaims. Residual non-blocking implementer
notes folded in: `request_id` passed from handler into Update (not
re-derived), guard (a) only rejects self-demote/deactivate (self
no-op/rename pass), `env_managed` covers all PATCH mutations on env
rows including the upsert's name/role reset.
