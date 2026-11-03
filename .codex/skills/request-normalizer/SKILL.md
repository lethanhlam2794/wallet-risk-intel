---
name: request-normalizer
description: Normalize a minimized request as the first stage of an explicitly requested global planning pipeline. Ordinary questions, fixes and ck:plan do not activate this pipeline.
---

# Skill: request-normalizer

## Activation
Run only within the explicitly requested global planning pipeline, or when the
user explicitly requests this stage with its documented inputs. Mentioning a
skill, invoking ck:plan or finding old artifacts is not pipeline activation.
Never start implementation/commit stages from the report handoff.

## Purpose
Ask DeepSeek to convert a reviewed, minimized user request into strict JSON that downstream
skills can trust. Codex validates the schema and writes a fallback artifact if DeepSeek is unavailable. This is always the first workflow step.

## How to run
Run the shared DeepSeek artifact writer from the project root:

```bash
python3 /home/lethanhlam/.codex/skills/_shared/deepseek_workflow.py structured-request \
  --input-file /tmp/reviewed-safe-request.txt --allow-external \
  --out docs/report/structured_request.json
```

The script loads `DEEPSEEK_API_KEY` from
`/home/lethanhlam/.ai-management/.env` first and writes the artifact file
itself. Codex must not print the key, raw `.env`, raw API response, or any
unredacted credentials.

Before invocation, minimize input without losing explicit requirements. Never
send secrets, customer dumps, private logs or raw repository snapshots. The
input file above must contain only the reviewed summary. Without authorization
or safe minimization, author locally; do not call the external writer. Its
`--allow-external` flag prevents accidental calls, not arbitrary sensitive-data
leakage. Existing `--raw` accepts sanitized input only; prefer files over argv.
The legacy normalize.py uses the same consent boundary; without consent it
returns a degraded fallback without retaining the unreviewed original input.

Fallback behavior: if DeepSeek cannot write valid JSON after one retry, Codex writes a minimal valid `docs/report/structured_request.json` with `normalization_degraded: true` and records the reason in `ambiguities`.

## Output schema (docs/report/structured_request.json)
```json
{
  "language": "vi | en | other",
  "task_type": "feature | bugfix | refactor | docs | review | setup | question | other",
  "intent": "short verb phrase",
  "goal": "1-2 sentence restatement of the end goal",
  "entities": ["key nouns, files, apps, APIs, modules"],
  "tech_hint": "python | node | typescript | go | frontend | mobile | unspecified",
  "constraints": ["explicit user constraints"],
  "ambiguities": ["unclear or underspecified points"],
  "urgency": "low | normal | high | unspecified",
  "risk_level": "low | medium | high | unknown",
  "requires_code_change": true,
  "raw_input": "minimized request used for this workflow",
  "normalization_degraded": false
}
```

## Validation rules
- Return JSON only. No markdown, commentary, or code fences.
- Preserve the user's language and explicit requirements in the minimized
  `raw_input`; never preserve sensitive original values for verbatim fidelity.
- Put explicitly stated constraints in `constraints`; when applicable also
  record `Input minimized for external processing` as a processing note.
- Put inferred uncertainty in `ambiguities`; do not resolve it here.
- Set `requires_code_change` false for explanation-only, review-only, or
  planning-only requests unless the user asks to modify files.
- If `task_type`, `tech_hint`, or `risk_level` is uncertain, use the closest
  allowed value and explain the uncertainty in `ambiguities`.

## Failure handling
- Retry transient transport or malformed/schema-invalid output at most once;
  do not retry permanent authentication/client errors or expose raw responses.
- If it fails again, Codex writes a rule-based minimal JSON with
  `"normalization_degraded": true`.
- Never block the pipeline silently. Always return some valid JSON.

## Non-goals
Do not decide whether the request is complete enough to execute. That gate
belongs to `planner`.
