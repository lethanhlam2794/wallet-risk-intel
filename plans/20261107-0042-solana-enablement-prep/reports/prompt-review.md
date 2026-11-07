You are Devin Review, an independent code-review worker. You did NOT write this code — review it adversarially with zero confirmation bias. You have read-only tools; do not edit anything.

Working directory: `/home/lethanhlam/Desktop/Profile/check-wallet` (Go module root `apps/api/`).

### Compliance Inputs (MANDATORY — read before any work)
- Project rules: /home/lethanhlam/Desktop/Profile/check-wallet/AGENTS.md
- Engineering standard: ~/.codex/rules/engineering-standard.md
- Learning ledger: /home/lethanhlam/Desktop/Profile/learning/learning.md
- Reports dir: /home/lethanhlam/Desktop/Profile/check-wallet/plans/20261107-0042-solana-enablement-prep/reports/
If AGENTS.md or learning.md is unreadable: STOP and return `Status: NEEDS_CONTEXT` — do not guess paths or proceed.

### Review scope

The Solana enablement-prep increment — a **dormant** `provider.TxProvider` adapter plus its decision/semantics docs. All changes are committed; review the full increment:

```
git log --oneline 456c5f9..HEAD        # 4 commits
git diff 456c5f9..HEAD                 # complete diff
git show dec9300 --stat                # the adapter commit
```

Files under review:
- `apps/api/internal/provider/solanarpc/adapter.go` — two-phase collect-then-emit adapter over keyless Solana JSON-RPC
- `apps/api/internal/provider/solanarpc/mapper.go` — jsonParsed tx → transfers extraction + owner attribution
- `apps/api/internal/provider/solanarpc/adapter_test.go` — 23 named-fixture tests
- `apps/api/internal/provider/solanarpc/adapter_blind_test.go` — randomized property suite
- `docs/solana-indexer-semantics.md`, `docs/solana-provider-decision.md` (new)
- `docs/non-evm-provider-discovery.md`, `docs/non-evm-audit.md` (updated)
- `plans/20261107-0042-solana-enablement-prep/` (plan + phase + report docs)

### Contract under test

`provider.TxProvider` at `apps/api/internal/provider/provider.go:282-288`: per-stream ascending-block emission is REQUIRED — the indexer's contiguous-coverage watermark (`internal/transaction/service.go:493-559`, `streamMaxBlock−1`) is only sound under bottom-anchored emission. The binding invariants (INV-S1..S5) are in `docs/solana-indexer-semantics.md`.

### What to audit (evidence-based — cite file:line for every finding)

1. **Coverage-honesty correctness (highest weight)**: does the collection phase truly emit ZERO rows until the desc frontier passes below `min(FromBlock, floor)`? Can ANY path emit a top-anchored slice? Check: short-page bottoming, `before` cursor exclusivity, foreign-seed anchoring, `len(rows) < sigPageSize` exhaustion semantics, the `maxCollectSigs` cap path, mid-emission token continuation.
2. **Ordering**: cross-step global ascending `(slot, transactionIndex, sig)`; per-stream non-decreasing BlockNumber.
3. **Truncation honesty**: floor > FromBlock ⇒ emitted rows but Finished=false forever ⇒ `partial`+`history_truncated` (verify the composition against service.go's watermark/status logic, not just the adapter).
4. **Extraction correctness**: meta.err exclusion, fee never a transfer, mint resolution (transferChecked vs bare transfer + tokenBalances), owner attribution fallback to raw ATA, `transferWithSeed`, Token-2022 program, NULL blockTime → zero time, IsError=false.
5. **Error typing**: ErrDisabled before HTTP, malformed vs degraded vs internal classification, JSON-RPC error mapping, no token advance on unresolvable tx (INV-S3).
6. **Bounded work**: per-step sig-page bound, emitCap, seedWalk bound, batch size, token-size cap; UpstreamCalls counts real HTTP requests (batch=1).
7. **Dormancy/stop conditions**: no production imports, init() only registers allowlist, Enabled() gated, no API key anywhere, no DDL/wiring/wattrFiles/OpenAPI/warmindex changes.
8. **Test honesty**: no weakened assertions, fixtures reflect real upstream shapes (getSignaturesForAddress desc order, exclusive cursors, jsonParsed ix shape), blind suite actually randomizes (BLIND_SEED), replay determinism asserted.
9. **Docs accuracy**: claims in the two new docs match code/probe evidence in `reports/probe-solana-rpc.md` and `reports/audit-drift-check.md`.

Run `go test ./internal/provider/solanarpc/` and `go vet ./...` yourself to confirm evidence.

### Output

Write your findings to `plans/20261107-0042-solana-enablement-prep/reports/review-round-1.md` with severity-tagged findings (Critical/Major/Minor/Nit, each with file:line + why it matters) and a verdict line:

`Verdict: APPROVED | CHANGES_REQUESTED`

End your final reply with:
Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
Summary: one or two sentences
Concerns/Blockers: optional
