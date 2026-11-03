# Workflow Routing

Choose the owner from the user's intended action; load its SKILL.md before
executing. Explicit invocation takes precedence, but discussing a skill is not
an invocation. Compound requests may authorize multiple stages in order.

| Intent | Owner | Helpers when needed | Boundary |
|---|---|---|---|
| Explain or compare existing behavior | ck:ask | ck:scout | No implementation |
| Diagnose only | ck:debug | Domain skill | Stop at evidence and root cause |
| Fix a known failure | ck:fix | ck:test, qa-automation | No implicit shipping |
| Implement a known feature | ck:cook | Domain skill | No implicit named pipeline |
| Plan only | ck:plan | ck:scout, ck:research | No implementation |
| Run tests only | ck:test | qa-automation, ck:web-testing | No automatic fixes or test weakening |
| Explicit global planning pipeline | request-normalizer -> planner -> project-structure -> senior-reviewer -> doc-reporter | Approved writers | Stop at report |
| Review a local diff | code-reviewer | Domain reviewer | Findings do not authorize commits |
| Review a PR | ck:review-pr | Domain reviewer | Honor review-only or explicit fix scope |
| Commit, push, or deploy | Installed skill for the requested action | Review/test | Separate authorization required |
| Update documentation | Direct documentation-management work | Domain skill | Update existing document owner |

An implementation may use a brief local plan; architecture work may need
ck:plan. Do not force plan -> cook for every feature or diagnosis -> brainstorm
-> plan for an explanation. A plan path alone does not authorize execution.

## Host Capabilities

Use only tools actually exposed by the current host. Map capabilities, not
tool names: read/search, edit, run command, ask user, delegate, track progress.
When no task tracker exists, use the persistent plan checklist. When no
delegation tool is available or delegation is not authorized, work locally.
Label local review self-review; leave required independent sign-off pending.
Missing optional hydration does not make completed, verified work incomplete.
Never assume TodoWrite, TaskCreate, Task, or AskUserQuestion is available.

Task and delegation examples are conditional host examples. Check availability
before invocation, not by calling an absent tool. Named agent roles describe
work, not installed tools or the opted-in planner skill. Perform that work
locally in dependency order when delegation is unavailable. Ask through an
exposed supported input tool or direct text when a real answer is needed.
Existing session authorization remains valid; mode flags and review scores
cannot grant commit, push, publication, deployment, or ledger approval.

## Paths And Validators

Resolve references relative to the referring skill file, project artifacts
from the project root, and configured global artifacts from their global root.
Preserve valid Claude-specific paths; do not replace .claude indiscriminately.
Use injected naming/active-plan metadata only when present. Otherwise use the
project's plans/<timestamp>-<slug>/ and return its path for continuation.
For explicit global planning use the configured global plans root; absent one,
use ~/.codex/plans on Codex or ~/.claude/plans on Claude.

Run optional hydration, ck CLI, or set-active-plan scripts only if available
and supported by the host. Otherwise edit the persistent checklist directly,
preserving its schema and recording dependencies. No hook pointer is required.

Load the [review artifact contract](../../_shared/references/workflow-artifacts.md).
For a configured validator, resolve its real installed path before running it.
Do not assume claude/hooks/workflow-artifact-gate.cjs exists in a project.
If unavailable, inspect the same required artifacts and checks locally, label
the result self-review, and report automated validation unavailable. Required
independent or automated sign-off stays pending; never invent a PASS. This
fallback does not waive tests, risk review, or an explicit user approval gate.

## Completion

Judge completion by deliverables and actual test/review evidence, not counts of
tool calls. Report unrun checks and pending sign-off honestly. Update docs when
behavior, setup, commands, architecture, security, public contracts, or future
maintenance decisions change. Never commit merely because review finished.
