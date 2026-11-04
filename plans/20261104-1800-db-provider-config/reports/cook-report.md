# Cook Report — DB-managed provider config (phases 01–04)

Status: **all four phases implemented, tested, and committed.** Strict
TDD per phase (failing test → implement → green → refactor); one
conventional commit per phase; plan + phase frontmatter flipped to
`status: completed` with outcome notes.

## Per-phase outcomes

| Phase | Deliverable | Commit | Tests added |
|---|---|---|---|
| 01 | `provider_configs` + `provider_config_changes` schema, sqlc queries | `b633f12` | migration up/down + constraint checks on `check_wallet_test` |
| 02 | descriptor map (12 providers), resolver, AES-256-GCM keystore, `provider.AllowedHostsFor`, boot wiring | `b32cba2` | resolver truth-table / env-parity / fatal-error tests, keystore round-trip/tamper/marker tests |
| 03 | `StatusCache` (TTL ~30s) + kill-switch inside the chain walk | `11bc1a7` | status-source truth table, TTL window, last-known/cold-start, all-inactive disabled verdict, `-race` clean |
| 04 | masked role-gated admin API + audit history + OpenAPI | `bc7f533` | service tests (masked write/audit, validation, last-active, KEK write, pagination) + API tests (role gate, 404, wire masking, whitelist, warnings) |

## Commits (phase order)

- `b633f12` feat(providerconfig): provider_configs + provider_config_changes schema
- `b32cba2` feat(providerconfig): DB-over-env resolution layer for tx providers
- `11bc1a7` feat(providerconfig): runtime kill-switch in the tx chain walk
- `bc7f533` feat(providerconfig): admin provider-config API with masked audit trail

## Pinned-semantics coverage

- **DB-over-env:** `""`/`[]` are explicit overrides; only NULL falls
  back. Absent row ⇒ byte-identical env behaviour (rollback path).
- **Boot-fatal:** resolution/decrypt/malformed-JSON/allowlist errors
  name provider+field; `enc:`-prefixed plaintext with KEK set is fatal;
  never silent env fallback.
- **Kill-switch:** `data_sources.is_active AND provider_configs.status`,
  TTL-cached, goroutine-safe; missing row ⇒ active; DB error ⇒
  last-known (cold ⇒ active + log). Inactive members reuse the exact
  disabled-skip path — zero upstream traffic, no failover trigger,
  all-inactive ⇒ disabled verdict attributed to chain[0].
- **Admin API:** masked `{set,last4|null}` (last4 suppressed <8 chars);
  whitelist {api_key, base_url, enabled_chains, status}; `enc:` input
  rejected; SSRF via exported `provider.AllowedHostsFor` echoed in the
  400; unknown code ⇒ 404; FOR UPDATE + column-scoped writes; masked
  per-field audit rows in the same tx; `requires_restart` +
  `warnings[]` (drift, no_enabled_chains, last_active_member);
  cursor/`next_cursor` pagination; actor = `middleware.ClientFrom(c)`
  name as `actor_id`/`updated_by`, `actor_type="api_client"`.

## Verification

- `go build ./...` clean; `go vet ./...` clean; `gofmt -l .` empty;
  `go test -count=1 ./...` green (all packages incl. `tests/`).
- `go test -race` clean on providerconfig/transaction kill-switch paths.
- Migration 000020 verified up+down on `check_wallet_test`; invalid
  JSONB object and invalid status rejected by CHECK constraints.
- Boot smoke (phase 2): provider origin logs show `db|env|default`
  labels only — no secret values; api resolved all 12 providers.
- No secrets committed; `.env` untouched (still ignored); all key
  fixtures obviously fake (`fake-…`).

## Deviations

- Phase-01 queries added beyond the required five (FOR UPDATE, insert,
  column-scoped updates, touch, effective-status) — needed by phases
  3–4, all in `queries/provider_configs.sql`.
- Phase-04 PUT additionally requires `https` scheme on `base_url`
  (matches the adapters' construction-time rule; prevents writing a
  boot-breaking value).
- api_key audit `previous_value`/`new_value` both use the masked object.
- Change rows written in one tx share `changed_at`; ordering falls to
  the uuid key — documented; pagination stays stable.
- One flaky assertion fixed during full-suite run (same-timestamp audit
  ordering is uuid-tie-broken — test now asserts on the full page).

## Follow-ups (not in scope)

- `/metrics` and log-based alerting are unaffected; consider a counter
  for kill-switch skips if operators want visibility.
- `ProviderConfigs` routes mount only when `Deps.Admin != nil` (the
  existing admin-group gate).
