---
name: senior-reviewer
description: Review plan.json and structure.json within the opted-in global planning pipeline. Approve or route plan/structure changes; this does not authorize implementation or commits.
---

# Skill: senior-reviewer

## Activation
Run only within the explicitly requested global planning pipeline, or when the
user explicitly requests this stage and its inputs exist. Mentioning the skill,
invoking ck:plan or finding old artifacts does not activate the pipeline.
Approval permits the report stage, not automatic implementation or commits.

## Purpose
Codex acts as the senior sign-off gate for DeepSeek-authored plan and structure
artifacts before implementation. This is the review step where Codex writes
`docs/report/review.json`; `coder` cannot run without an approved review.

## Review checklist
- Plan solves the stated goal without scope creep or missing must-have items.
- Requirements map to acceptance criteria and a realistic test strategy.
- Structure matches stack conventions and existing repository patterns.
- When repository memory exists, read `.memory/file-map.md` and
  `.memory/patterns.md` before judging structure fit; use `.memory/repo-index.json`
  only when exact file metadata or symbols are needed.
- Config strategy prevents hard-coded secrets, URLs, ports, paths, and flags.
- Business logic, I/O, persistence, and external clients have clear boundaries.
- Dependencies are justified and not speculative.
- Security risks are addressed: secrets, input validation, auth, file paths,
  injection, unsafe deserialization, and sensitive logging.
- Risks and rollback plan are realistic for the task size.
- `review_flags` from `docs/report/structure.json` are resolved or explicitly accepted.

## Output schema (docs/report/review.json)
```json
{
  "status": "approved | changes_requested",
  "risk_rating": "low | medium | high",
  "comments": [
    {
      "target": "plan | structure",
      "issue": "...",
      "required_fix": "...",
      "route_to": "planner | project-structure"
    }
  ],
  "approved_at": "ISO timestamp, only if approved"
}
```

## Rules
1. Be specific. Point to the requirement, criterion, section, or structure
   item that must change.
2. If requirements, scope, acceptance criteria, risk, or rollback are wrong,
   route back to `planner`.
3. If folder layout, config module, boundaries, or stack conventions are
   wrong, route back to `project-structure`.
4. If repository memory conflicts with `docs/report/structure.json`, do not
   approve silently; route back to `project-structure` unless the deviation is
   explicitly justified in `structure.notes` or `structure.review_flags`.
5. Never approve `blocked_on_user`.
6. Any plan or structure change after approval requires re-review.
