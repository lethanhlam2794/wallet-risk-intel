Round-2 response from Codex. All 10 amendments ACCEPTED (intellectual honesty — every finding was valid; none were based on false premises). The plan and both phase files at plans/20261105-0940-admin-api-clients/ have been rewritten; please re-read them and render your verdict.

How each amendment landed:

- A1: Lock key = `pg_advisory_xact_lock(hashtext('apiclient.admin_guard'))` — namespaced, documented invariant "every role/is_active writer holds it; UpsertAPIClient exempt (never removes admins)".
- A2: service_test.go uses `RequireTestPoolDSN` with packageDSN `check_wallet_apiclient_test` (verified: internal/testutil/testdb.go:81 exists; matches enrichment packageDSN pattern).
- A3: `apiclient` delegates to `middleware.HashAPIKey` — no duplicate digest impl.
- A4: Guard (b) specified as post-state evaluation on `role='admin' AND is_active AND id != target`; added tests: deactivate-already-inactive → success, demote-inactive → success.
- A5: Guard (a) = unconditional reject for self-demotion AND self-deactivation (rename-self allowed). Chosen for footgun protection — a self-revocation is never a legitimate op, and env bootstrap remains the recovery path for everything else.
- A6: `Service` receives configured bootstrap key-hashes; `Update` on env-managed row returns `warnings:[{code:"env_managed",message:"...reverted at restart while env var is set; unset API_BOOTSTRAP_* to revoke permanently"}]` — mirrors providerconfig's `requires_restart` convention. `is_active=TRUE` in the upsert kept (recovery path preserved, as you noted).
- A7: One structured `slog.Info` per mutation (actor_id, actor_name, target_id, changed_fields, request_id). Your correction on AccessLog (route pattern, not target id) is right — "request logs suffice" was wrong, dropped.
- A8: `risk_level: R4` declared in frontmatter; flag exemption justified (new surface, nil-gate removes routes); POST non-idempotent duplicate-on-retry documented in plan.md.
- A9: Pinned: `PATCH {}` → no-op current-view 200; `limit` ≤0/non-int → 400, >200 clamps to 200, default 50; invalid cursor → 400 BAD_REQUEST (matches providerconfig mapping); `DisallowUnknownFields` on POST and PATCH (blocks key_hash/api_key injection — good catch); `Cache-Control: no-store` on POST 201; view enumerated `{id,name,role,is_active,created_at,last_seen_at}`; `items:[]` on empty page.
- A10: name validated by `utf8.RuneCountInString` ∈ [1,150] after trim.

Additional edge cases from your "Blind Spots" list folded into the phase test specs: duplicate names coexist + PATCH targets by id; `{id}` variants (abc→400, 0/-1→404); arbitrary-format inbound keys still valid (no format gate); mid-flight actor demotion (guards read DB inside the lock); whitespace-only name → 400.

If the amended plan is now sound, end with the exact line PLAN_CONSENSUS_REACHED — or list remaining blockers.
