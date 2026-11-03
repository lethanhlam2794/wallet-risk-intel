---
name: planner
description: Within the opted-in global planning pipeline, consume validated structured_request.json and produce plan.json with a missing-info gate. Not a general planning or ck:plan trigger.
---

# Skill: planner

## Activation
Run only within the explicitly requested global planning pipeline, or when the
user explicitly requests this stage and validated inputs exist. Mentioning the
skill, invoking ck:plan or finding old artifacts does not activate the pipeline.

## Purpose
Ask DeepSeek to create an actionable engineering plan, then have Codex review it
for completeness, safety, schema validity, and missing information. Codex actively
participates: it may request a rewrite, patch the plan, or author the fallback
`docs/report/plan.json` when DeepSeek is unavailable or wrong.

## How to run
Codex first gathers a short repo-context summary when useful, then asks DeepSeek
to write the plan file:

```bash
python3 /home/lethanhlam/.codex/skills/_shared/deepseek_workflow.py plan \
  --structured-request docs/report/structured_request.json \
  --repo-context-file /tmp/reviewed-safe-context.txt --allow-external \
  --out docs/report/plan.json
```

Before this call, review and minimize BOTH the input artifact and context file.
Prior artifacts are not automatically safe for external transport. Never send
credentials, customer dumps, private logs or repository snapshots. If safe
minimization/authorization is absent, author locally and record the fallback.
`raw_input` in the upstream artifact is the minimized request, not sensitive
original text. The flag is an accidental-call guard, not proof of consent.

After DeepSeek writes the file, Codex validates JSON/schema, checks the required
checklist below, and enforces the hard stop rule. If the output is malformed,
incomplete, unsafe, or misses important repo context, Codex may request one
DeepSeek rewrite. If it still fails or DeepSeek is unavailable, Codex authors or
patches `docs/report/plan.json` directly and records the fallback/correction in
`decision_log`.

## Required checklist
Resolve these before `status` can be `ready_for_structure`:

- Clear goal and definition of done
- Target stack: language, framework, runtime, deployment context when relevant
- Scope boundaries: what is in and out
- Inputs and outputs of the feature/fix
- Non-functional constraints: security, performance, compatibility, UX, data
- Existing codebase context for non-greenfield work
- Acceptance criteria and verification method
- Config/secrets needs and no-hard-code expectations
- Risk level and rollback or fallback plan for high-risk changes

Ask only questions that change implementation or review outcomes. Do not
block on nice-to-have details.

## Output schema (docs/report/plan.json)
```json
{
  "goal": "string",
  "task_type": "feature | bugfix | refactor | docs | review | setup | other",
  "scope_in": ["..."],
  "scope_out": ["..."],
  "tech_stack": {"language": "", "framework": "", "runtime": ""},
  "affected_files_or_areas": ["paths, modules, services, or unknown"],
  "requirements": [
    {"id": "R1", "description": "...", "priority": "must | should | nice"}
  ],
  "inputs_outputs": {"inputs": ["..."], "outputs": ["..."]},
  "constraints": ["..."],
  "config_and_secrets": ["required env/config keys or none"],
  "risks": [{"risk": "...", "impact": "low | medium | high", "mitigation": "..."}],
  "rollback_plan": "how to revert or recover, or why not applicable",
  "acceptance_criteria": [
    {"id": "AC1", "description": "...", "covers": ["R1"]}
  ],
  "test_strategy": ["unit, integration, e2e, manual checks"],
  "assumptions": ["safe assumptions that do not change scope"],
  "decision_log": ["important planning decisions and why"],
  "missing_info": ["exact question to ask the user"],
  "status": "ready_for_structure | blocked_on_user"
}
```

## Hard stop rule
If `missing_info` is non-empty:
- Set `status` to `blocked_on_user`.
- Stop the pipeline. Do not run `project-structure`.
- Return the questions to the user in the same language as the request.
- Resume only after the user answers, then re-run planning with merged context.

## Anti-patterns
- Do not fill hard requirements with plausible defaults.
- Do not plan a larger solution than the user requested.
- Do not hide uncertainty in `assumptions`; use `missing_info` when it can
  change architecture, data handling, security, or acceptance criteria.
