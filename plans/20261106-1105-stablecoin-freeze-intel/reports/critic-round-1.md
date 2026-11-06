VERDICT: REJECTED

Two blocker-class defects (a factually wrong event→label mapping, and a read-path invalidation hole that makes published freezes invisible to API consumers for up to an hour) plus four majors. The core architecture — SnapshotSource fetcher + delta publish inside the service-managed tx — is sound and every load-bearing code claim checks out. Fix the blockers and this is a good plan.

---

## Verified-Claims Audit

| # | Claim | Result |
|---|---|---|
| 1 | `pipeline_labels.go` Publish calls `PublishRevokeAbsentWalletLabels` (full-replace; absence=revoke) | **TRUE** — `apps/api/internal/ingestion/pipeline_labels.go:165`; the revoke is `last_seen_at < NOW()`-based, source-scoped (`queries/labels_publish.sql:93-103`). Delta justification is real. |
| 2 | `SnapshotSource.Download(ctx) (Snapshot, error)` + `Snapshot.Files`; zero changes to `Service.execute()` | **TRUE** — `internal/provider/provider.go:83-85,31`; `service.go:261-266` downloads, temp-cleans, checksum-no-ops, stages, publishes with no source-keyed code. Caveat in finding 14. |
| 3 | `stagingPurge`/`stagingDelete` maps are the housekeeping hooks | **TRUE** — `internal/ingestion/pipeline.go:47,71`; consumed by `service.go:404,416`. See finding 15. |
| 4 | `walletattribution.Fingerprint(net,addr,category,label)` pinned identity | **TRUE with a catch** — `internal/provider/walletattribution/mapper.go:20` — the formula hardcodes a `"wallet_attribution\|"` namespace prefix. See finding 4. |
| 5 | `verification_status` CHECK `('unverified','reported','verified','disputed')`; FROZEN seeded in 000005 | **TRUE** — `migrations/000005_labels.up.sql:58` and `:27` (`'FROZEN'`, `MALICIOUS_ACTIVITY`). FROZEN already carries scoring weight 60 (`internal/scoring/policy.go:44`) — freeze labels immediately affect `risk_score`; the plan never mentions this consequence. |
| 6 | `data_sources.source_type` has no CHECK; `'chain_scan'` admissible | **TRUE** — `migrations/000001_init.up.sql:33` (`VARCHAR(30) NOT NULL`, no CHECK). `'snapshot'`/`'api'` in use. `'chain_scan'` endorsed — it honestly signals delta semantics vs `'snapshot'`'s full-replace. |
| 7 | `providerconfig.ResolveAll` + killswitch = right RPC resolution path | **TRUE** — `internal/providerconfig/resolver.go:106`; `cmd/worker/main.go:169-177` (`ResolveAll` + `NewStatusCache`). Caveat in finding 7. |
| 8 | `HasPipeline` gating gives admin trigger "for free" | **Gating yes, working trigger NO** — `service.go:92`, `handler.go:48` will accept the source, but the API's fetchers map won't run it. See finding 3. |

---

## Findings

### 1. [BLOCKER] `DestroyedBlackFunds` → revoke is factually wrong — the address stays blacklisted

Phase-03 (`phase-03-freeze-pipeline.md:36-38`) specifies: *"Unfreeze/destroy events → `revoked_at = now` on matching active labels"*. The plan's own Open Question #2 asks revoke-vs-terminal-marker and phase-03 already picked the wrong side.

`TetherToken.destroyBlackFunds(_blackListedUser)` does `require(isBlackListed[_blackListedUser])` and burns the balance — **it does not clear `isBlackListed`**. After `DestroyedBlackFunds`, the wallet is still blacklisted: it still cannot receive, move, or transact USDT. Revoking the FROZEN label on this event asserts "unfrozen" for a still-frozen address — a manufactured false-negative on the exact question this feature exists to answer, and a worse violation than "unknown ≠ safe": it asserts *known-unsafe as safe* with `verified` confidence.

Evidence anchor: `phase-03-freeze-pipeline.md:36-38`; `plan.md:83-84` (open question); contract semantics — `destroyBlackFunds` has no blacklist-clearing path; only `RemovedBlackList` does.

**Required change**: `event_type='destroy'` must NOT revoke. Publish mapping becomes: `freeze` → upsert FROZEN; `unfreeze` (`RemovedBlackList`/`UnBlacklisted`) → `revoked_at=now`; `destroy` → label stays active + evidence append `{event:'DestroyedBlackFunds', tx_hash, block}` as a terminal lifecycle marker. If Tether later unblacklists the address, the `RemovedBlackList` event performs the revoke on its own.

### 2. [BLOCKER] Freeze publishes never invalidate the labels read path — FROZEN labels invisible for up to 1h (labels + screening)

`label.Service.ListLabels` cache-keys on `wallet:labels:{net}:{addr}:{version}` where `version` comes from `LatestLabelSnapshotPublished` — which is **hard-scoped to `code='wallet_attribution'`** (`queries/labels_read.sql:20-24`). TTL is 1 hour (`internal/label/service.go:25`). Nothing in any phase touches this.

Consequence: a freeze publish inserts a `data_source_snapshots` row under `stablecoin_freeze` — the wattr snapshot version is unchanged, so the cache key is unchanged, and the cached `labelSet` (without the new FROZEN label, or with a just-revoked one still showing active) is served for up to an hour. `GET /wallets/:net/:addr/labels` misses the freeze; **`/screenings` misses it too** — `screening/service.go:360` calls the same `Labels.ListLabels` path.

The feature's entire sales pitch in `plan.md:20-22` is *speed* (frozen ~41 min after the drain, "faster than any structured feed we ingest"). A 1-hour staleness window on both consumer surfaces destroys the value proposition and produces stale `COMPLETED` screening results that claim freshness they don't have. This is squarely the AGENTS.md cache rule: *"invalidated on publish; never serve stale data as current."*

**Required change**: the plan must specify an invalidation story. Options: (a) broaden `LatestLabelSnapshotPublished` to "latest publish across all label-producing sources" so every freeze publish bumps the key version — but then `data_status`/`lastSynced` freshness (`service.go:119,283-291`) starts reporting freeze publish times, blurring the wattr freshness meaning; (b) targeted key deletion for the staged wallet set in/after the publish tx (small set — dozens of `wallet:labels:{net}:{addr}:*` keys); (c) composite version (`wattrVersion|freezeVersion`). Any of these works; "nothing" does not. Freshness-metadata semantics (`lastSynced` across two sources) must be decided explicitly.

### 3. [MAJOR] Admin trigger will 202-accept then FAIL: `cmd/api` fetchers map lacks the scanner

`ingestion.NewService` (shared by both binaries) registers the new pipeline in `s.pipelines` (`service.go:109-112`) — phase-03 requires this. So `HasPipeline("stablecoin_freeze")` is `true` **inside the API process too** (`handler.go:48`), and `POST /admin/ingestion-runs` Enqueues a run... which then dies in `execute()`: `s.fetchers["stablecoin_freeze"]` is nil because phase-04 wires the scanner only into `cmd/worker` — the API's own fetchers map at `cmd/api/main.go:137-140` contains only `ofac_sdn` and `wallet_attribution`. Result: 202 → FAILED run, `"no downloader configured for source"`. Phase-04's own criterion *"HasPipeline must accept stablecoin_freeze — verify in handler test or live call"* passes the letter while the run is broken — a worse UX than rejecting, since the admin API has no FilePath escape hatch (`triggerBody` carries only `source`, `handler.go:29-31`).

**Required change**: either wire the scanner into `cmd/api`'s fetchers map (feasible — `pcRes`/`pcKeys` already resolved at `cmd/api/main.go:117-125`) or explicitly scope the source to worker-only and make the handler reject it with a clear error. Pick one and write it into phase-04.

### 4. [MAJOR] Reusing `Fingerprint()` embeds a `wallet_attribution|` namespace — cross-source collision re-opens absence-as-revoke

`Fingerprint` is `sha256("wallet_attribution|" + net + "|" + addr + "|" + cat + "|" + label)` (`internal/provider/walletattribution/mapper.go:20-24`) — the source code is baked into the digest. Freeze labels would carry fingerprints namespaced to a different source. That is wrong on its face for provenance, and it is load-bearing-wrong: `wallet_labels.fingerprint` is `UNIQUE` globally (`000005:46`), and `PublishUpsertWalletLabels` does `ON CONFLICT (fingerprint) DO UPDATE` *without* touching `source_id` (`queries/labels_publish.sql:73-80`).

Attack path: if prettydeath upstream ever emits `(ethereum, 0x…, FROZEN, usdt_blacklist)` — upstream label vocabulary is uncontrolled — the freeze upsert hits the wattr row, rewrites its evidence, leaves `source_id = wallet_attribution`, and the **next wattr publish's `PublishRevokeAbsent` revokes it** (absent from wattr staging, `last_seen_at` stale). The exact absence-as-revoke defect this plan exists to escape re-enters through the identity formula. Reverse direction is equally confused (freeze-owned row mutated by wattr upserts).

Open Question #3's literal answer: reusing the *function* is fine only with a source-namespaced digest. **Required change**: parametrize the prefix (e.g. `Fingerprint(source, net, addr, cat, label)` or a `stablefreeze.Fingerprint` twin pinning `"stablecoin_freeze|"`), keeping the rest of the formula identical. Cost: ~5 lines + a collision test.

### 5. [MAJOR] Degenerate window loops FAILED: `head − conf ≤ cursor` → `toBlock < fromBlock` → Validate hard-fails

Windows are `(cursor, min(head−conf, cursor+window)]` with Validate requiring `toBlock ≥ fromBlock` (`phase-03:23`). When head hasn't advanced `FREEZE_SCAN_CONFIRMATIONS` blocks since the last run (12 blocks ≈ 2.4 min — entirely plausible for back-to-back admin triggers, retries, or a quiet chain), `toBlock < fromBlock`, Validate fails, run FAILED, cursor untouched — forever, until head moves. This is a self-inflicted failure state on a *correct* scan of an empty window.

**Required change**: the scanner must either (a) short-circuit to a COMPLETED no-op outcome (e.g. return an error/sentinel the service treats as skip — but `execute()` has no such path today, so realistically (b)), or (b) emit a header carrying an explicit `empty:true` / `toBlock = fromBlock−1` marker that Import/Validate accepts and Publish treats as "advance nothing" — which keeps the existing stage→validate→publish shape intact. Specify which; today neither exists.

### 6. [MAJOR] `block_ts` is unobtainable from `eth_getLogs` — schema and `observed_at` depend on an unspecced second RPC

`stg_freeze_events.block_ts` (phase-01) and `observed_at = block_ts` (phase-03:31) assume a field `eth_getLogs` **does not return**. Log entries carry `blockNumber`/`blockHash`, never a timestamp. Getting `block_ts` requires `eth_getBlockByNumber` per distinct block (freeze events are sparse — tens of blocks per window — so this is cheap and batchable) or dropping the column/falling back to publish-time `NOW()`. No phase budgets the extra calls, so phase-01's schema and phase-02's file format are specified against a field that doesn't exist in the scan output.

**Required change**: spell out the timestamp strategy in phase-02 — batch `eth_getBlockByNumber` over distinct `block_number`s post-scan (bounded by staged event count), or make `block_ts` nullable with `observed_at` = scan time and the block number kept as the canonical evidence. Either is defensible; silence is not.

### 7. [MINOR] Resolved ankr `BaseURL` is the *multichain* Advanced-API base — `eth_getLogs` needs the node base

`AnkrBaseURL` defaults to `https://rpc.ankr.com/multichain` (`internal/config/config.go:209`); `pcRes.Get("ankr").BaseURL` returns that. Standard `eth_*` JSON-RPC on Ankr lives at `rpc.ankr.com/{chain}/{key}` — the multichain endpoint serves only `ankr_*` methods. The ankr adapter already performs this exact derivation in `HeadBlock` (`internal/provider/ankr/adapter.go:216`): `strings.TrimSuffix(baseURL, "/multichain") + "/" + chain`. Phase-02 says "compatible with ankr node-API base `rpc.ankr.com/eth`" but never states the derivation — an implementer using `res.BaseURL` verbatim gets a method-not-found wall on the first call. Spell it out (or better, extract the shared helper).

### 8. [MINOR] `freeze_scan_state` PK on `contract_address` alone — wrong shape for multi-chain

Phase-01 declares `PK (contract_address)` while the table also carries `network_id`. EVM contract addresses collide across chains routinely (same deployer+nonce, deterministic deploys) — the moment TRON-another-EVM or a second network joins the registry, the PK is a live collision and every row lookup is ambiguous. **Fix now**: `PRIMARY KEY (network_id, contract_address)`. Costs nothing today; painful to migrate later.

### 9. [MINOR] Down-migration precedent doesn't transfer

Phase-01 says "follow wallet_attribution down-migration precedent" (`phase-01:30-31`). That precedent (`migrations/000005_labels.down.sql`) `DROP`s the entire `wallet_labels` table — inapplicable now that the table is shared. The freeze down-migration must instead: `DELETE FROM wallet_labels WHERE source_id = (…'stablecoin_freeze')` (FK order — the labels reference `data_sources`), `DELETE FROM ingestion_runs WHERE source_id = …`, drop `stg_freeze_events` + `freeze_scan_state`, then `DELETE FROM data_sources`. Anything less fails FK or orphans provenance.

### 10. [MINOR] Unfreeze revoke discards the revoking tx — provenance gap

Phase-03 appends destroy info to evidence but for `unfreeze` only sets `revoked_at` (`phase-03:36-38`). The unfreeze transaction hash is exactly the evidence an auditor needs to answer "who unfroze this and when" — the AGENTS.md rule "do not delete old evidence" extends to *recording* the revoking event. **Fix**: the revoke statement should merge `{unfreeze_event, tx_hash, block_number, block_ts}` into the label's evidence JSONB alongside `revoked_at`.

### 11. [MINOR] Reorg bound: 12 confirmations is adequate but `finalized` is strictly cleaner

12 conf ≈ 2.4 min — defensible (post-merge deep reorgs require slashable equivocation), and issuer admin txs are rare. But `eth_getLogs` accepts the `"finalized"` block tag on most providers — scanning to finalized removes `FREEZE_SCAN_CONFIRMATIONS` as a knob and makes the bound reorg-proof *by definition* rather than by parameter. Consider it; 12-conf is acceptable if the tag proves unsupported on ankr free tier.

### 12. [MINOR] Backfill needs ~11 *separate* triggers — no convergence mechanism in the plan

`-mode ingest` executes one `RunSync` and exits (`cmd/worker/main.go:62-129`). The per-run window cap (endorsed — bounded blast radius, resumable via cursor, beats a one-shot resumable scan on simplicity) means ~11 sequential manual invocations to reach head. Phase-04 only says "document convergence". Either add a `-until-caught-up` loop flag to the worker or document the shell loop (`until … ; do worker -source stablecoin_freeze; done`) explicitly. Ops will otherwise assume one run finishes backfill.

### 13. [NIT] Snapshot version `freeze-<head>-<unix>` can collide

`data_source_snapshots UNIQUE(source_id, version)` (`000005:94`): two runs landing on the same head within one second → duplicate version → publish fails on the constraint. Precedent (`wattr-<checksum>-<unix>`) shares the flaw, but append the run-id prefix to make it a non-issue.

### 14. [NIT] "the service already knows how to checksum" is imprecise — the fetcher owns checksums, and `f.Checksum[:12]` can panic

`execute()` never checksums fetcher artifacts — it reads `snap.Checksum` verbatim (`service.go:289`); only the `-file` path computes checksums (`service.go:424-454`). The scanner must populate `File.Checksum` + `provider.CombinedChecksum` itself (as `Downloader.Download` does, `downloader.go:168-172`). Worse: `service.go:281` logs `f.Checksum[:12]` whenever `FinalURL != ""` — a scanner that sets `FinalURL` but a short/empty checksum panics inside `execute` (RunSync has no recover). Pin both requirements in phase-02.

### 15. [NIT] `stagingPurge`/`stagingDelete` nil-call → panic at the next run's start

`purgeStagingExcept` calls `stagingPurge[name](…)` bare (`service.go:404`) — register `StagingNames() = ["freeze_events"]` without the map entries and the *next ingestion run of any source* panics mid-`begin()` (nil func call). The plan does wire both maps, so this is a fragility note, not a defect: add a `NewService` assertion that every registered `StagingNames` resolves in both maps — turns a silent panic into a boot-fatal config error.

### 16. [NIT] Loose ends to pin in phases

- `upstream_category` on freeze labels is unspecified — suggest `'FROZEN'` (matches `risk_categories` join convention) or the raw event name.
- `entity = null` is conservative-correct; attaching a seeded issuer entity ("Tether"/"Circle") is *evidenced* (the emitting contract identifies the issuer) and improves screening output — optional, decide explicitly.
- `ImportResult.Labels` reused as the staged-event counter — fine, note the semantic stretch for run-row counters.
- `GetSourceByCode` does not filter `is_active` (`queries/ingestion_runs.sql` generated SQL: `WHERE code = $1`) — disabling the `data_sources` row won't stop runs; killswitch only via `provider_configs.status`. Acceptable, but say so.
- `ingestion_runs.cursor_value TEXT` (`000001:147`) sits unused — a `contract:last_block` summary there is free observability.

---

## Domain attack-surface answers (for the record)

- **Event completeness**: correct and complete. USDT emits `AddedBlackList`, `RemovedBlackList`, `DestroyedBlackFunds` (all non-indexed → `data` tail); USDC emits `Blacklisted`/`UnBlacklisted` (`indexed` → `topics[1]`). No `UnBlacklisted` exists on USDT — the plan's "handle BOTH" indexing stance is right.
- **Cursor gaps**: none. Events in `(toBlock, head]` are deferred, not skipped — the next window covers them. Cursor-in-publish-tx is the correct atomicity shape.
- **Re-freeze**: `revoked_at=NULL` on upsert matches existing precedent (`labels_publish.sql:80`) and is spec'd with a test — good.
- **`verification_status='verified'`**: defensible — endorse. The issuer's own contract event *is* the freeze act, not a report awaiting corroboration; there is no higher authority to defer to. The "never auto-verify" invariant targets community/provider *reports*; this is primary chain evidence. `reason` staying `NOT_AVAILABLE` is required (events carry none) and the plan holds that line.
- **`'chain_scan'` source_type**: admissible (no CHECK) and endorsed — communicates delta semantics vs `'snapshot'`.
- **Checksum no-op trap**: plan's header-unique solution works (`service.go:288-295` skips only on byte-identical artifacts). Wasteful publish on quiet days is a few ms of tx — acceptable; a "cheap advance" alternative would break the atomic publish shape. No change needed.
- **Genesis constants**: hardcode + test. Verify each deploy block via `eth_getCode` presence diff at `genesis−1`/`genesis` (or the known deploy tx receipt) in a unit/integration test — dynamic per-run resolution is wasted complexity for immutable history.
- **BSC/TRON path**: verified — BSC pegged USDT/USDC expose no blacklist functions. The contract registry is the right extension point for other EVM issuers; TRON needs a new scanner behind the same `SnapshotSource` interface (base58, different events API) — acceptable, but fix the `freeze_scan_state` PK first (finding 8).

## What the plan got right

Delta-not-snapshot is the correct core call, and the evidence it cites is real: `PublishRevokeAbsentWalletLabels` is genuinely full-replace (`pipeline_labels.go:165`), `Service.execute()` genuinely needs zero changes (`service.go:256-277`), and reusing run rows/locks/staging/publish-tx avoids a parallel ingestion stack. The fail-closed posture on RPC errors, the staging-PK idempotency story, and keeping `reason`/`entity` null are all correct instincts — the defects are in the event semantics, the read path, and the wiring edges, not the skeleton.

---

Status: DONE
Summary: Plan rejected — all 8 claimed code facts verified true (with caveats), but found 2 blockers: `DestroyedBlackFunds` wrongly mapped to revoke (address stays blacklisted per TetherToken semantics), and freeze publishes never invalidate the wattr-scoped labels cache (1h staleness on `/wallets/labels` and `/screenings`). Plus 4 majors: broken admin-trigger path in cmd/api, fingerprint namespace collision, degenerate-window FAILED loop, and `block_ts` requiring an unspecced `eth_getBlockByNumber` call.
Concerns/Blockers: Report file could not be written (read-only mode) — full report emitted above per instruction.


Exit code: 0

