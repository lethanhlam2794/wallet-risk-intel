Round 2 — amended plan for consensus check.

I accept all 6 of your required amendments and verified each claim against the code before applying. The plan at
`/home/lethanhlam/Desktop/Profile/check-wallet/plans/20261106-1555-non-evm-expansion/plan.md`
and its phase files are updated as follows:

1. **`bn.code` joins (Blind Spot #1) — CONCEDED, adopted the fix (not the documentation-only alternative).** Verified: `queries/labels_staging.sql` L19-21 + L43-47 and `queries/labels_publish.sql` L15, L60, L100 all match `bn.code` unfiltered; `wallets.network_id` FK (`000001_init.up.sql` L16) has no cascade, so a publish-hole would indeed break the down-migration. Phase-02 now requires `AND bn.is_active` (and `bn2.is_active` inside the IN-subqueries) on all five sites + `sqlc generate` + a `tests/` case staging `network_code='solana'` asserting it remains orphan-rejected. Semantics-preserving: every code matchable today is already active. Also added to the plan as design decision **D4b** and to the risk matrix.
2. **`testdb_integration_test.go` L23 — CONCEDED.** Verified `assert.Equal(t, 2, networks)`. Phase-02 now enumerates updating it to assert the seed code SET `{bsc, ethereum, solana, tron}` with the solana/tron `is_active=false` note — spec change, not weakened assertion.
3. **`'evm'` census — CONCEDED.** Verified `queries/wallets.sql` takes `address_type` as `$4`; literal sites are `transactions.sql:27`, `ofac_publish.sql:54`, `labels_publish.sql:9`, `stablecoin_freeze.sql:58` + `service.go:25` (the real blocker: `GetOrCreate` lacks a format/type param). Phase-04 audit requirements corrected.
4. **OpenAPI `Address` `pattern` — CONCEDED.** Phase-03 now requires the Network marking text to explicitly state the `Address` pattern (`^(0x)?[0-9a-fA-F]{40}$`, L1636) stays EVM-only, making the unexercisable combination explicit; the pattern itself is untouched and listed as a pre-enablement blocker in the audit doc.
5. **Naming — CONCEDED.** Reconciled to `AddressFormat` / `AddressFormatForNamespace` / `Network.AddressFormat()` in `internal/network/address_format.go` (phase-01 updated; `Format` naming removed).
6. **Risk matrix — CONCEDED.** Added rows for orphan-check drift, seeded-count assertion break, and the Address-pattern contradiction.

Your positive confirmations are recorded in plan §6 (debate summary): the 5 existing `solana`-as-invalid-network API tests, providerconfig `enabled_chains` resolver-derived validation, `000024` CROSS JOIN being `WHERE bn.code='ethereum'`-scoped, and OFAC staging keeping TRX/SOL as `CHAIN_SPECIFIC`/NULL network_id.

One clarification on your nit re: `ON CONFLICT` — the plan now specifies `ON CONFLICT DO NOTHING` (no target), which covers BOTH uniques while still erroring on genuine value drift — chosen deliberately.

Please re-verify the amended plan + phase files (`ls plans/20261106-1555-non-evm-expansion/`) and either:
- emit **PLAN_CONSENSUS_REACHED** with any residual notes, or
- enumerate remaining required amendments.

End your final reply with:
Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
Summary: one or two sentences
Concerns/Blockers: optional
