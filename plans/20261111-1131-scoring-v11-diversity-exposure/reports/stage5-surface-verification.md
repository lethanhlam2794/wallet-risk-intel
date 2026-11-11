# Stage 5 — Live Surface Verification (scoring-v1.1)

**Surface classification: API surface — REST endpoint changed.** `POST /api/v1/screenings`
gains `options.policy_version` (validated enum) and v1.1 exposure responses carry
`onchain_computed` items + `coverage.onchain_coverage`/`sources_evaluated`. No UI
route exists in this repo (backend-only monolith) — browser evidence N/A by
surface classification; verification is live `curl` against a running instance.

## Fixture (dev DB `check_wallet` on :5433)

| Entity | Value |
|---|---|
| Candidate W | `0x1111…1111` (ethereum) |
| Source R1 | `0x2222…2222` — label SCAM, `verification_status=reported` |
| Source R2 | `0x3333…3333` — label HACK, `verification_status=verified` |
| Transfers | R1→W 1 ETH confirmed (meaningful); R2→W 1 ETH confirmed; R1→W 0.0001 ETH confirmed (dust) |
| Index state | `fresh`, `indexed_at=now()`, `history_truncated=false` — all 3 wallets |
| API boot | `SCORING_POLICY=v1.1.0 go run ./cmd/api` on :8010 |

## Results (raw bodies in `stage5/*.json`)

| Case | HTTP | Latency | Evidence |
|---|---|---|---|
| `policy_version:"v1.1.0"` | 201 | 7.22 s | `v11-response.json` — score **60**, `blocks.exposure.subtotal=60`, two `EXPOSURE_ONCHAIN_SOURCE` reasons ×30, items carry `basis:"onchain_computed"`, `direction:"INBOUND"`, `meaningful_count:1`, real `tx_hashes`, `coverage:"complete"`, `taint_kind:"direct"`, `onchain_coverage:"complete"`, `sources_evaluated:2`; SCAM reason keeps `verification_status:"reported"` (not inflated); dust tx excluded from count; `PROCEEDS_RECEIVER` informational w0 emitted; `status:"PARTIAL"` honest (sanctions/labels legs UNAVAILABLE w/o provider keys) |
| `policy_version:"v1.0.0"` | 201 | ~5 s | `v10-response.json` — score **0**, `policy_version:"v1.0.0"`, exposure items `[]`, `onchain_coverage` key **absent** (omitempty envelope parity) — divergence vs v1.1 on identical data |
| `policy_version:"v9.9.9"` | 400 | 0.005 s | `v99-response.json` — `BAD_REQUEST: options.policy_version is not a registered scoring policy`, no screening persisted |
| default (no version) | 201 | ~6 s | `default-response.json` — resolves to deployment `v1.1.0`, score 60 |
| `SCORING_POLICY=v9.9.9` boot | exit 1 | — | stderr: `config: SCORING_POLICY: "v9.9.9" is not a registered scoring policy` — fail-closed startup |

## Domain-invariant spot checks on live output

- `reported` SCAM label → reason `verification_status:"reported"` — reported ≠ verified preserved
- `PARTIAL` status on unavailable provider legs — failure ≠ clean
- Dust transfer observed but counted 0 → materiality floor enforced end-to-end
- `hops:1` on both sources; `taint_kind:"direct"` only (no depth-2 propagation — sources had no direct-tainted senders of their own)
