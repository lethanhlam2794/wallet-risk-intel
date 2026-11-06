# Review Round 1 — TRON Enablement Prep (ck-code-review --pending)

Date: 2026-11-06 · Reviewer: Devin Review · Scope: pending diff (16 modified files)
+ untracked `internal/provider/trongrid/` (3 files), `internal/wallet/service_test.go`,
`docs/tron-{indexer-semantics,provider-decision}.md`. Plan:
`plans/20261106-1946-tron-enablement-prep/plan.md`. Declared risk: R3.

## Verdict: CHANGES_REQUESTED

One [Major] defect — the trx stream does not clamp `limit` to TronGrid's
≤200 cap, so the adapter fails 100% of `LookupTransfers` calls under the
shipped indexer default (`PageSize=1000`). Everything else verified clean;
all binding invariants are real and test-asserted.

## Executive Summary

The change is well-constructed and honest: INV-1/INV-2 are enforced in
code (not just comments), the 23-test suite is real (exact-call-count
assertions, replay determinism, zero-traffic proof — all httptest, zero
real-network calls), `address_type` generalization is fail-closed with
byte-identical EVM stamping, ~30 audited file:line citations in the docs
all verify, zero migration/go.mod/generated-code drift, TRON stays
`is_active=FALSE`, and the adapter is genuinely unwired. However, one
contract violation hides behind the test page-size: TronGrid rejects
`limit≥201` (the project's own probe evidence), and the real indexer sends
`PageSize=1000` — so the dormant adapter cannot complete a single step as
shipped. One-line clamp + test fixes it.

## Verification Evidence (fresh runner output, this session)

| Gate | Result |
|---|---|
| `gofmt -l .` (apps/api) | empty ✓ |
| `go vet ./...` | exit 0, silent ✓ |
| `go build ./...` | exit 0 ✓ |
| `go test -count=1 ./internal/provider/trongrid/` | **23/23 PASS**, 20.5s (counted: 23 RUN, 23 PASS) ✓ |
| `go test -count=1 ./internal/wallet/` | PASS 0.7s — `TestGetOrCreate_AddressTypeByFormat`, `…UnknownFormatFailsClosed` on real `check_wallet_wallet_test` DB ✓ |
| `go test -count=1` screening + transaction + enrichment | PASS (6.7s / 12.3s / 6.1s) ✓ |
| `go test -count=1 ./tests/` | PASS 67.1s — incl. `non_evm_networks_test.go` (is_active=FALSE gate) ✓ |
| `go test -race -count=1` trongrid + wallet | PASS 21.6s / 5.2s ✓ |
| `go test ./...` | 36 packages `ok`, zero FAIL ✓ |
| `git diff --name-only` on `migrations/`, `go.mod`, `go.sum`, `internal/db/` | **all empty** ✓ |
| trongrid imports outside its package | **zero** (`grep -rn trongrid` → only the package itself) ✓ |
| `GetOrCreate(` call sites | 4 prod + 10 test + `WalletStore` iface — all pass `net network.Network` (grep enumeration matches audit inventory) ✓ |
| `Enabled(net)` precedent | byte-identical shape to chainscan `adapter.go:150-152` / nodereal `adapter.go:144-145` ✓ |
| JSON `Error`/`error` case | verified empirically — `json:"Error"` tag matches both casings ✓ |

Doc-citation spot-check (all verified): `service.go:296-300` writeDisabled,
`:339-344` post-hoc settle, `:398-408` head-lag, `:432` both-Kinds,
`:444-449` streamSeen-from-Streams, `:450-457` streamMaxBlock pre-buildRows
(`:479`), `:49-57` page tiers 5/2/1; `identity.go:32` non-pointer
BlockNumber, `:88,93,104` NormalizeEVM, `:188-191` pending rule,
`:197-210` `{n|t}:{hash}.{k}`; `resolver.go` Network has no `IsActive`
(`LoadResolver`→`ListActiveNetworks`); `address_tron.go:22-40` base58check
verbatim; migrations `000001:20` (VARCHAR(30) no CHECK), `000008:22`
(nullable block_number), `:33` (UNIQUE), `000026` (is_active=FALSE);
OpenAPI `^(0x)?[0-9a-fA-F]{40}$`; all `NormalizeEVM` upstream sites.

## Detailed Findings

### [Major] trx stream `limit` unclamped — guaranteed HTTP 400 under default indexer config

- `apps/api/internal/provider/trongrid/adapter.go:283` — the trx page call
  passes `size` (= `opts.PageSize`, defaulting to 100 inside the adapter)
  verbatim as the TronGrid `limit` parameter. There is no `min(size, 200)`
  clamp — unlike trc20 (`min(size, trc20PageClamp)`, `:315`) and unlike
  nodereal (`size>1000 → 1000`, `adapter.go:283-285`).
- The real caller sends `PageSize: s.cfg.PageSize` (`service.go:433`),
  default **1000** (`config.go:283` `TXINDEX_PAGE_SIZE`, `service.go:47`
  `defaults()`). TronGrid rejects `limit≥201` with HTTP 400 — the project's
  own probe evidence, quoted verbatim in `docs/tron-provider-decision.md`
  req-4 row (`{"success":false,"error":"A valid limit by parameter is
  required."}`).
- Result: at enablement with default config, the first `LookupTransfers`
  → `limit=1000` → `ErrClient4xx` → FAILED on every ensure. The 23 tests
  never see this because all fixtures use `PageSize=0 → 100` — a
  fixture-doesn't-drive-the-real-contract gap ([G-006]/[P-047]).
- Fix: clamp the trx page size (`trxPageMax = 200`, cite the probe) and
  add a test asserting the emitted `limit≤200` when `PageSize>200`.

### [Minor] `ret`/`contract` non-parallel arrays silently grade missing results as success

- `apps/api/internal/provider/trongrid/mapper.go:80` — `isErr := i <
  len(r.Ret) && r.Ret[i].ContractRet != "SUCCESS"`. A `ret` array shorter
  than `contract[]` (an upstream contract break — doc §e asserts they are
  parallel) silently yields `IsError=false` — optimism on malformed
  evidence, the conservative-wrong direction for a risk store. Also,
  semantics-doc §e phrasing ("`!= 'SUCCESS'` on **any** ret element") is
  ambiguous vs the implemented parallel-index semantics.
- Fix: either `ErrMalformed` on `len(r.Ret) != len(r.RawData.Contract)`,
  or document the lenient choice + tighten the doc wording.

### [Minor] Envelope-error paths untested

- `acctEnvelope.success=false` → `ErrDegraded` (`adapter.go:415`) and
  `txInfoEnvelope.Error != ""` → `ErrDegraded` (`adapter.go:511`) have no
  fixture — only the `{}` miss and happy txinfo paths are covered. Both
  are one-line additions to the existing table tests.

### [Minor] Multi-contract native tx claim untested

- Semantics doc §e requires iterating `raw_data.contract[]` ("`[0]`-only
  would miss them"); no fixture emits a tx with ≥2 `TransferContract`
  elements, so the iteration claim is code-reviewed but not test-proven.

### [Nit] `'evm'` literal line-number drift in the docs

- The added `--` comment blocks shifted every literal +3 lines:
  `transactions.sql:27→30`, `ofac_publish.sql:54→57`,
  `labels_publish.sql:9→12`, `stablecoin_freeze.sql:58→61`.
  `docs/non-evm-audit.md` and `docs/tron-indexer-semantics.md` §f still
  cite the pre-insert numbers — cosmetic, but fix in docs whose entire
  value is file:line precision.

### [Nit] `pipeline-log.md` "all 37 packages ok"

- Fresh `go test ./...` reports 36 `ok` lines (no-test-file packages
  excluded from that count). Cosmetic.

### [Nit] final-report "only stdlib added"

- The adapter imports `golang.org/x/time/rate` — pre-existing dep
  (`go.mod:17`), not stdlib; accurate claim is "go.mod untouched" (true).

### [Nit] Failed-step `UpstreamCalls` under-report (precedent-consistent)

- Calls inside a failed step are discarded with the nil page, so the
  request-global budget under-counts real attempts. Identical to
  chainscan's convention — recorded for completeness, not a new defect.

## Invariant Proof Loop (adjudicated)

| Claim | Verdict | Evidence |
|---|---|---|
| INV-1: cursor never advances past unresolved rows | ACCEPTED | trc20 resolution failure → `nil` page + typed `ErrDegraded`; token discarded with the step; `TestLookup_INV1_UnresolvedTrc20` asserts nil page + exactly `txInfoRetries`=3 calls |
| INV-2: every emitted `Transfer.Stream` ∈ `page.Streams` | ACCEPTED | `page.Streams` is a constant `[trx, trc20]` pair; mappers can only emit those two values; `TestLookup_INV2_StreamsDeclared` + `KindsFilter` |
| No block-less TRC-20 row possible | ACCEPTED | `mapTrc20Row` requires a resolved block; `txInfo` returns `nil,nil` on miss → `ErrDegraded`; `TestLookup_NoZeroedBlocks` proves real `9764845` |
| `Enabled()=false` precedes all HTTP | ACCEPTED | gate at `HeadBlock:172`/`LookupTransfers:232` before `post`/`get`; `TestDisabled_ZeroTraffic` counts 0 requests; resilience `checkHost` is defense-in-depth |
| Ascending per-stream order | ACCEPTED | `checkAscending` on each emitted stream slice; violation → `ErrDegraded`; monotonicity assumption honestly stated in doc §b1 |
| Replay determinism / ascending assertions real | ACCEPTED | token-carried ms window (`encodeToken`/`decodeToken`); `TestLookup_ReplayDeterminism` deep-equal on full structs; `TokenCarriesWindow` asserts `getblockbynum` count stays 2 across steps |
| `UpstreamCalls` exact | ACCEPTED | asserted 4 / 1 / 5 / 3 across tests, incl. bridge + fan-out |
| `ErrUnknownAddressFormat` fail-closed | ACCEPTED | `service.go:31-34` returns before write; test asserts `ErrorIs` + zero rows on real DB |
| EVM stamped byte-identical | ACCEPTED | `ON CONFLICT DO UPDATE` no-op preserves first-insert stamp; test asserts `"evm"` on refetch |
| Caller/network mismatch possible | REJECTED | all 4 prod callers pass the same `net` the address was normalized under (screening:146 `raw`/`req.Address` order unchanged) |
| `'evm'` SQL comments are semantics-free | ACCEPTED | leading `--` blocks only; `internal/db/` generated code zero-diff |
| Dormant artifact | ACCEPTED | zero imports outside package; `init()` allowlist inert; `/networks` still bsc+eth (tests pkg PASS) |
| Zero new deps / no secrets / no raw SQL | ACCEPTED | go.mod untouched; `x/time` pre-existing; test key `"k"`, local `devonly` test-DB DSN per `testutil` convention; parameterized SQL only |
| Migration safety | ACCEPTED | zero migration files touched; `blockchain_networks` untouched; tron `is_active=FALSE` (000026) |
| Tests honest, httptest-only, zero real-network calls | ACCEPTED | all tests route through `WithAllowedHosts(httptest host)` or fail construction; no weakened assertions found; no `t.Skip` |

## Required Actions to Approve

1. **[Major]** Clamp the trx page `limit` to ≤200 (`min(size, 200)` or
   named `trxPageMax` citing the probe) and add a test proving the sent
   `limit≤200` when `opts.PageSize > 200`.
2. **[Minor]** Decide and document/fix the `ret`/`contract` non-parallel
   `IsError` path (recommend `ErrMalformed` on length mismatch).
3. **[Minor]** Add fixtures: `success:false` envelope, txinfo `Error`
   field, multi-`TransferContract` tx.
4. **[Nit]** Refresh the `'evm'` literal line numbers in
   `docs/non-evm-audit.md` + `docs/tron-indexer-semantics.md` §f; correct
   "37 packages" → 36 and "only stdlib" → "go.mod untouched" in the
   pipeline log / final report.

Items 2–4 may be addressed or explicitly accepted by the owner; item 1 is
a must-fix correctness defect (it invalidates the "code-complete dormant
adapter" claim under the shipped default config).
