# Remediation — Review Round 4

Date: 2026-11-03 (during + post `review-round-4.md` — **APPROVED**)

## Note on mid-audit edits

The round-4 reviewer ran while remediation continued; it detected the tree
changing mid-audit (G-001 rule 10) and re-verified the FINAL state. Three
residual edges it proved empirically were fixed during/after its audit:

1. **Uppercase-D surrogate escapes** (`\uD800`) — first sanitizer regexes
   required lowercase `d`; Postgres rejects either case. Fixed by
   case-insensitive hex parsing in `parseHex4` (and, ultimately, by the
   scanner rewrite).
2. **Unsanitized `error` JSONB** — `ErrorInfo.Message` can carry
   upstream-controlled text (publicaml `env.Error` → `Msg`) with literal
   NUL/invalid UTF-8; `json.Marshal` then emits `\u0000` → Postgres
   rejects → row lost. Fixed: `errJSON` marshals a copy with
   `jsonbText`-sanitized Message.
3. **Escaped-backslash fake-pair** (`{"e":"\\ud83d\udc00"}`) — the regex
   pair-shield couldn't see backslash parity; `\ud83d` there is literal
   text and `\udc00` a real lone low surrogate. Fixed by replacing all
   regexes with `jsonbEscapeClean` — a sequential scanner consuming `\\`
   as a unit, so only odd-backslash-run `\u` sequences count as escapes.

Final implementation: `jsonbText` (bytes → legal envelope text) +
`jsonbSafe` = `jsonbEscapeClean(jsonbText(b))` (verbatim-path escape
scrubber preserving valid surrogate pairs) + `auditPayload` single picker
covering `lr.Raw`, `lerr.Response`, and the empty case — plus
`jsonbText` on `ErrorInfo.Message`.

## Test coverage added this round

- `lone-surrogate-uppercase-D`, `escaped-backslash-then-lone-low`,
  `escaped-backslash-literal-surrogate-text` cases in
  `TestEnrich_PersistsJSONBHostileBodies` (10 subtests total, real DB).
- `TestEnrich_PersistsHostileErrorMessage` — hostile `Msg` → sanitized
  `error` JSONB, row persists.
- `TestRedirectPolicy_SchemeDowngradeRefused` — extracted `redirectPolicy`
  (identical logic), direct table coverage incl. multi-hop.
- `TestDo_CallerCancelDoesNotFeedBreaker` — `BreakerFailures=1`, two
  mid-flight cancels, healthy request still served.

## Reviewer verdict

`review-round-4.md` — **APPROVED**. 28-case probe battery + live
`::jsonb` casts all pass; 10/10 HostileBodies subtests; full suite +
`-race` green on final code; sqlc regen identical; `go mod tidy -diff`
empty; tree-hash unchanged (read-only audit). Advisories only:
`redirectPolicy` `len(via)>=3` branch uncovered (trivially deep-tail).
