---
name: doc-reporter
description: Final report stage of the explicitly opted-in global planning pipeline. Persist approved plan/structure/review and stop at the implementation handoff; do not start coding or commits.
---

# Skill: doc-reporter

## Activation
Run only within the explicitly requested global planning pipeline, or when the
user explicitly requests this stage with its approved inputs. Merely mentioning
the skill, invoking ck:plan or finding old artifacts does not activate it.

## Purpose
Ask DeepSeek to write the approved Markdown plan report from
`docs/report/plan.json`, `docs/report/structure.json`, and
`docs/report/review.json`. Codex validates the report for traceability, safety,
and handoff completeness. DeepSeek is the preferred report author, but Codex may
patch or author the report when DeepSeek is unavailable or wrong. This is the
final mandatory step of the global workflow. After this report is written, stop
and hand the report path to the user/implementation team.

## How to run
Choose the timestamped output path, then ask DeepSeek to write the Markdown
report file:

```bash
python3 /home/lethanhlam/.codex/skills/_shared/deepseek_workflow.py report \
  --plan docs/report/plan.json \
  --structure docs/report/structure.json \
  --review docs/report/review.json --allow-external \
  --out docs/plan/<YYYYMMDD-HHmm>-<slug>-plan.md
```

Before invoking, minimize ALL three input artifacts for external transport and
persistence. Never send raw secrets, customer dumps, logs or repository snapshots.
If safe minimization or authorization is absent, author the report locally and
record the fallback. Preserve requirements, not sensitive verbatim raw_input.

After DeepSeek writes the file, Codex validates that it follows the template,
contains no secrets/raw API responses, traces requirements to acceptance
criteria, and clearly states the implementation handoff. Codex may request one
DeepSeek rewrite if the report is incomplete or unsafe. If it still fails or
DeepSeek is unavailable, Codex writes or patches the final report and includes a
brief review note that Codex fallback/correction was used.

## Output location
```text
docs/plan/<YYYYMMDD-HHmm>-<slug>-plan.md
```

Create this under the actual project root, as a sibling of folders such as
`src/`, `tests/`, and package/config files. Do not create an extra
top-level folder named `project`.

`<slug>` is a kebab-case summary of `plan.goal` with about six words max.

## Report template
```markdown
# Plan: <goal>

- **Date**: <ISO timestamp>
- **Requested by**: <user, if known>
- **Stack**: <language / framework / runtime>
- **Status**: Approved by senior-reviewer at <timestamp>
- **Risk**: <review.risk_rating>
- **Previous report**: <filename, only for re-approval iterations>

## Goal
<plan.goal>

## Scope
**In scope:**
- ...

**Out of scope:**
- ...

## Requirements
| ID | Description | Priority |
|---|---|---|
| R1 | ... | must |

## Acceptance Criteria
| ID | Description | Covers |
|---|---|---|
| AC1 | ... | R1 |

## Structure
```
<structure.tree>
```

## Config Strategy
- Env file: `.env`
- Example file: `.env.example`
- Config module: <structure.config_strategy.config_module>
- Validation: <structure.config_strategy.validation>

## Test Strategy
- ...

## Risks / Rollback
- ...

## Review Notes
- Resolved comments and decisions from senior-reviewer

## Deferred Follow-ups
- Items explicitly accepted as out of scope

## Implementation Handoff
- Owner: external implementation team
- Start point: this approved report
- Codex global workflow status: complete after this file is written
```

## Rules
1. Never include secrets, API keys, tokens, raw `.env` contents, credentials,
   or unredacted third-party responses.
2. Minimize sensitive source inputs BEFORE transport and persistence, not merely
   when reviewing the returned report. Do not log rejected raw model output.
3. Reports are append-only. Never overwrite a previous report.
4. If the plan changes after approval, write a new report and reference the
   previous report filename.
5. Include enough detail for the external implementation, test, review, and
   commit teams to trace work back to requirements without reopening hidden
   context.
6. Stop after writing the report. Do not implement, test, review code, commit,
   or prepare a PR as part of this workflow.
