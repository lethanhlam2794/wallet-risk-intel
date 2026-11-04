---
phase: 4
title: Admin API — masked reads, whitelisted writes, audit rows
status: pending
priority: high
dependencies: [2]
---

## Overview
Role-gated admin endpoints to manage provider_configs + read audit
history. Keys never returned in plaintext; every mutation writes
provider_config_changes with masked key values.

## Requirements
- `GET /api/v1/admin/provider-configs` (list) + `GET .../{code}` —
  api_key serialized as `{"set":bool,"last4":"wxyz"}` or null.
- `PUT .../{code}` — whitelisted fields {api_key, base_url,
  enabled_chains, status}; status enum-validated; enabled_chains
  elements validated against registered network codes; base_url must
  satisfy adapter SSRF allowlist (reject non-allowlisted hosts — same
  check adapters apply).
- Every changed field ⇒ provider_config_changes row in same tx;
  api_key old/new recorded masked `{"set":bool,"last4":string|null}`.
- `GET .../{code}/changes?cursor&limit` — newest first, `next_cursor` envelope.
- Admin role required (existing middleware); 400 not 500 on bad input.
- OpenAPI `api/openapi.yaml` updated with schemas + examples.

## Related Code Files
- Create: internal/httpserver/provider_configs.go (+_test.go)
- Create: internal/providerconfig/service.go (write tx + audit)
- Modify: internal/httpserver/server.go (routes), api/openapi.yaml

## Implementation Steps
1. Red: handler tests — masking, whitelist rejection, invalid status,
   SSRF-unsafe base_url, non-admin 403, change rows written.
2. Implement service + handler + routes + openapi.
3. Green: httpserver + providerconfig suites.

## Success Criteria
- [ ] PUT then GET ⇒ masked key only; changes endpoint shows audit row
- [ ] Whitelist + SSRF + role enforcement covered by tests
- [ ] No plaintext key in response/log/audit (grep test)

## Risk Assessment
Medium — secret-handling surface; masking contract pinned in tests.

## Amendments (critic R1)
- **Effective-on disclosure:** GET/PUT responses carry
  `requires_restart:["api_key","base_url","enabled_chains"]` and PUT adds
  `warnings[]` when stored≠in-effect (mirrors greatify warnings pattern).
  status is the only runtime-effective field — disclose this in OpenAPI.
- **Encryption-on-write:** api_key writes pass through the phase-02
  keystore (encrypt iff KEK set); reject client values already bearing
  an `enc:` prefix (400).
- **SSRF surface:** phase-02 must export `provider.AllowedHostsFor(code)
  []string` (adapter packages publish their allowlist); PUT base_url
  validates against it and echoes the allowed hosts in the 400 error.
  OpenAPI discloses base_url is bounded by compiled-in hosts
  (chainscan=near-useless).
- **Unknown code ⇒ 404** (never FK-500); list endpoint marks registered
  tx providers lacking a row as PUT-discoverable.
- **Lost update:** column-scoped UPDATE of patched fields only (or
  SELECT … FOR UPDATE) inside one tx; concurrent two-field PUT test.
- **Mask pin:** `{"set":bool,"last4":string|null}`; omit last4 when
  key length < 8.
- **Actor:** `middleware.ClientFrom(c)` — `actor_id` = client Name
  (varchar), `actor_type` = "api_client"; `updated_by` same source.
- **Pagination:** conform to repo cursor convention (`cursor`/`next_cursor`)
  on `/changes` — deviates from greatify, conforms to AGENTS §9.
- **Last-active warning:** PUT `status=inactive` on the last active
  member of a chain category ⇒ response warning (greatify parity).
