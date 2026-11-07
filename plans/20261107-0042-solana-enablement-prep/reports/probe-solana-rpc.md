# Phase 2 — Live keyless probes, Solana public RPC

Date: 2026-11-07 · Executor: Devin Cook · Method: `curl` against
`https://api.mainnet-beta.solana.com` (keyless) and
`https://solana-rpc.publicnode.com` (keyless). Outputs verbatim.
Addresses used: `9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM` (Binance
hot wallet — high-activity), `TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA`
(SPL Token program — multi-tx-per-slot feed),
`FkJxFK13Z8vEWpumhHwGZkKNCdum4yA9BT7F3mo8YPTp` + `3hNzYSQs2ziFhnXYzxNqQ1HGnW8pQs9Gf4KfoRErfw9i`
(Binance ATAs — SPL-discovery-gap probe).

## 1. Head + ledger-floor probes

```
$ getSlot {"commitment":"finalized"}
{"jsonrpc":"2.0","result":466200957,"id":1}

$ getFirstAvailableBlock        (api.mainnet-beta.solana.com)
{"jsonrpc":"2.0","result":0,"id":2}

$ minimumLedgerSlot             (api.mainnet-beta.solana.com)
{"jsonrpc":"2.0","result":466111843,"id":3}

$ getFirstAvailableBlock        (solana-rpc.publicnode.com)
{"jsonrpc":"2.0","result":465597729,"id":2}
```

- `getSlot@finalized` = 466200957 — typed head endpoint ✓
- `getFirstAvailableBlock` on mainnet-beta = **0** (archival-backed — see
  §2; the public endpoint serves BigTable history, not just local ledger).
- `minimumLedgerSlot` = 466111843 — local RocksDB floor (~89K slots ≈ ~14h
  behind head), NOT the queryable-history floor on this endpoint.
- **publicnode reports a real floor** 465597729 — ~605K slots ≈ ~3 days
  behind its head. Two keyless endpoints with opposite truncation
  behavior: mainnet-beta archival, publicnode truncated.

## 2. Archival + blockTime nullability evidence

```
$ getBlock(1000, {encoding:"json", transactionDetails:"signatures",
   rewards:false, commitment:"finalized"})
{"jsonrpc":"2.0","result":{"blockHeight":null,"blockTime":null,
 "blockhash":"2ea54v6yrYGomzv7ekcQoDXuje5auFuCCZ9XLmpjzbiY",
 "parentSlot":999,
 "previousBlockhash":"5A94tKgqrweY8JRGj6TPgtCjoQiqD11EcTJUjBnu1gv4",
 "signatures":["5KDvkjdpGdKvTHJ8Y1gzmasQN535mC6dfZZY6zNYxKD57Z9gPVrpQRAa3Wjb3mDtRmAhL89ZXz1XSVgcdmFYuhjR","5rkQmgtfLbvmAB9rC7f7mrQH6GFNd13wDBHY9ZNv2iYqFLJ1PtXAgy35DU1go4LpxPi4catgmjxHaUfEVN9VaGbH","2YdzFFjyMdrLi5TJJSXTn1BdkSDZmhxJrJagHvNQyepzjMCVaQCuGRJ5BSXFes7Kojwhzp2T2tCieo2CEjZZk7ir","7ppabEeQQDpDULJfMdLAiNhchu6FEawmSAjSCV1ycNETLxxGj3DgzWvRwNoWeVRU1EVSeGENvPC5u9HxzPSJqt4"]},"id":1}

$ getBlock(200000000, same params)
{"jsonrpc":"2.0","result":{"blockHeight":182664994,
 "blockTime":1686964563,"blockhash":"bYJHNf3HevTfeX2EDCoHNCs3zedpdsTYAF8g3s1yuxZ",
 "parentSlot":199999999, "signatures":[...]}}

$ getTransaction(<sig from slot 1000>, {encoding:"jsonParsed",
   commitment:"finalized", maxSupportedTransactionVersion:0})
→ slot 1000, blockTime null, version null, meta null
```

- `getBlock` at slot 1000 returns data → archival history reachable
  (`getFirstAvailableBlock=0` is truthful on this endpoint).
- **`blockTime: null` on a finalized block + finalized tx** — the
  no-fabricated-blockTime rule is REQUIRED (early-era blocks lack
  timestamps). `blockHeight` also nullable — confirms slot ≠ height.
- `getBlock(200000000)` works: `transactionDetails:"signatures"` is the
  before-seed source for bounding desc collection (§5).

## 3. `getSignaturesForAddress` — page shape, cap, ordering

```
$ getSignaturesForAddress(9WzD…, {limit:5, commitment:"finalized"})
[{"blockTime":1793982818,"confirmationStatus":"finalized","err":null,
  "memo":null,
  "signature":"ZC5EsCedgdVoim3oP9YHhyNhQWCsJeknPrthiodZTAwtNEv9jaZjkU3H959eZHwrQkMSLoRBAqM5sDEHvj6AW4L",
  "slot":466178148,"transactionIndex":532},
 {"blockTime":1793982812,…,"slot":466178119,"transactionIndex":445},
 {"blockTime":1793982641,…,"slot":466177329,"transactionIndex":98},
 {"blockTime":1793857863,…,"slot":465593628,"transactionIndex":139},
 {"blockTime":1793837900,…,"slot":465500173,"transactionIndex":227}]

$ getSignaturesForAddress(9WzD…, {limit:2000,…})
{"jsonrpc":"2.0","error":{"code":-32602,
 "message":"Invalid limit; max 1000"},"id":1}
```

Fields per row: `blockTime, confirmationStatus, err, memo, signature,
slot, transactionIndex`. `transactionIndex` present on ALL rows probed —
the canonical intra-slot position.

`limit=1000` full page (9WzD…): 1000 rows, slots 466178148→438561539,
strictly desc, 0 NULL blockTime, 0 NULL transactionIndex, **20 rows with
`err != null`** (failed txs DO appear on the feed), all
`confirmationStatus:"finalized"`.

Same-slot ordering (Token program, 1000 rows over 5 slots
466201212–466201216): every multi-tx slot has `transactionIndex` strictly
descending within the slot; the global sequence is strictly desc by
`(slot, transactionIndex)` — i.e., **canonical block order, reversed**.

## 4. `before`/`until` cursor semantics

```
$ before=SevwtEmRwMRZ… (was index 10 of the 1000-page, slot 465194095)
→ 8 rows, slots 465194023…465167388 — boundary sig ABSENT (exclusive)

$ until=5NwHJpHZnAvW… (was index 40, slot 463282433)
→ exactly 40 rows, last at slot 463445389 — boundary sig ABSENT (exclusive)
```

Both bounds EXCLUSIVE. `until` halts the desc walk when passed.

**`before` accepts foreign sigs as global positional seeds:**

```
$ before=<sig@slot466201348 not on 9WzD… feed> → returns the unbounded
  head page (466178148…) — positions above own-head = no-op
$ before=4UsiwZTYEBqNpUnYAteKAKd2dC1Qia… (slot 342771184, foreign — an
  ATA feed sig, NOT involving 9WzD…)
→ [(342769379,…),(342769246,…),(342766633,…),(342766328,…),(342766261,…)]
  — collection anchored just below the seed's (slot,txIndex) position
```

⇒ `before` anchors the desc walk at the seed's GLOBAL `(slot,
transactionIndex)` position — a `getBlock(T).signatures` seed can bound
collection below `fetchTo` when `fetchTo < head`. Confirmed viable.

## 5. `getTransaction` — version flag, jsonParsed, tokenBalances

```
$ getTransaction(dH5qj32…, {encoding:"jsonParsed",commitment:"finalized"})
{"jsonrpc":"2.0","error":{"code":-32015,"message":"Transaction version
 (0) is not supported by the requesting client. Please try the request
 again with the following configuration parameter:
 \"maxSupportedTransactionVersion\": 0"},"id":1}

$ + "maxSupportedTransactionVersion":0
→ slot 466201348, blockTime 1793987809, version 0, meta.err null,
  fee 5380, innerInstructions 0, preTokenBalances 18, postTokenBalances 18
  postTokenBalance[0]: {"accountIndex":2,
    "mint":"Fp4JkqEcKYKBKhb9iMu9NSDeo71HWgq2q9QtXaaKpump",
    "owner":"DDxmWQ3MpwQRPAsBcy8C5v6NBLeQXxD795cxjPELNFDZ",
    "programId":"TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb",
    "uiTokenAmount":{"amount":"0","decimals":6,"uiAmount":null,
      "uiAmountString":"0"}}
```

- v0 txs REQUIRE `maxSupportedTransactionVersion:0` (typed `-32015`).
- `postTokenBalances`/`preTokenBalances` entries carry `accountIndex,
  mint, owner, programId, uiTokenAmount.amount` (raw u64 decimal) —
  ATA→owner attribution path exists. `programId` distinguishes Token vs
  Token-2022 (`TokenzQdB…`).

Parsed inner/outer ixs (tx `4gbxxvRXW8ymjua5qUD4Fqdt…`, slot 466177329):
outer `spl-associated-token-account` `create` carries
`{account, mint, source, systemProgram, tokenProgram, wallet}`; inner
`spl-token`/`system` ixs parsed `{info:{…}, type}`; a `transferChecked`
carries `{authority, destination, mint, source, tokenAmount:{amount:
"95000000000", decimals, …}}` — `source`/`destination` are TOKEN
ACCOUNTS, `mint` is the asset id. A `transfer` (non-Checked) carries
`{amount, authority, destination, source}` — NO mint field (needs
tokenBalances attribution).

## 6. JSON-RPC batching

```
$ POST [6× getTransaction jsonParsed] → array of 6 results, order
  shuffled (ids 1,0,2,5,4,3) — keyed by id
```

Batch works: N logical calls in 1 HTTP request.

## 7. SPL DISCOVERY GAP — decision-determining proof (problem h)

`getTokenAccountsByOwner(9WzD…, {programId: Tokenkeg…}, jsonParsed)`:
**2834 token accounts, 2814 distinct mints, 15 USDC ATAs** — per-ATA
auxiliary feeds are unbounded on a busy wallet.

`getSignaturesForAddress(3hNzYSQs…USDC-ATA)` → 2 sigs (slots 359509488,
342771184). `getTransaction` on both: **owner `9WzD…` absent from
`accountKeys` in BOTH txs**. Slot 359509488 is a plain SPL `transfer`:

```
{"transfer": {"amount":"1000000",
  "authority":"GFJfRAsqyryttEsxK9qfG1ojWDF5vP8BJfACx8awbng9",
  "destination":"3hNzYSQs2ziFhnXYzxNqQ1HGnW8pQs9Gf4KfoRErfw9i",
  "source":"CGuBD2QetEQs7wq8qPfdP7icTdFwBiCvCCeFsdNio7pB"}}
```

⇒ an incoming USDC deposit to a wallet's ATA is present on the ATA feed
but **unindexable on the owner feed** (feed indexes `accountKeys`).
Closed ATAs are unenumerable in `getTokenAccountsByOwner` (returns only
open accounts by construction) — the blind spot is permanent, not
enumerable-with-effort. PROOF COMPLETE.

## 8. Commitment + disabled methods + rate limit

```
$ getSignaturesForAddress(…, {commitment:"processed"})
{"jsonrpc":"2.0","error":{"code":-32602,"message":"Method does not
 support commitment below `confirmed`"},"id":1}

$ {commitment:"confirmed"} → all rows confirmationStatus:"finalized"
$ (no commitment param) → all rows "finalized" (default)

$ getBlockSignatures(466200900, {commitment:"finalized"})
{"jsonrpc":"2.0","error":{"code":-32601,"message":"Method not found"},
 "id":1}

$ (after ~15-20 requests in ~2 min)
{"jsonrpc":"2.0","error":{"code":429,"message":"Connection rate limits
 exceeded"},"id":1}
```

- `processed` rejected outright; `confirmed` returns finalized rows in
  practice; adapter pins `finalized` explicitly.
- `getBlockSignatures` disabled on public RPC — `getBlock` with
  `transactionDetails:"signatures"` is the working before-seed source.
- 429 JSON-RPC error (not HTTP-429 page) — connection-rate throttling.

## 9. PublicNode secondary endpoint (keyless)

```
$ getSlot@finalized → 466203081  ✓ same methods, keyless
$ getSignaturesForAddress(9WzD…, {limit:3}) → identical shape incl.
  transactionIndex ✓
$ getFirstAvailableBlock → 465597729  (≈600K slots ≈ ~3 days retained —
  TRUNCATED history)
```

PublicNode = viable keyless mirror BUT with a real ledger floor ~3 days
behind head — usable only inside its retained window; `history_truncated`
honesty applies below its floor.
