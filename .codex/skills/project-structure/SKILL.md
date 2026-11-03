---
name: project-structure
description: Within the opted-in global planning pipeline, consume a ready plan.json and select the smallest stack-appropriate layout and config strategy before senior review. Not an automatic trigger for every new module.
---

# Skill: project-structure

## Activation
Run only within the explicitly requested global planning pipeline, or when the
user explicitly requests this stage and its ready plan input exists. Merely
mentioning the skill, invoking ck:plan or finding artifacts is not activation.

## Purpose
Ask DeepSeek to write one authoritative folder/file layout artifact for the
planned work. Codex supplies detected repository conventions as advisor context,
validates the result, and may patch or author `docs/report/structure.json` when
DeepSeek is unavailable or wrong.

## How to run
Codex inspects the repository just enough to summarize existing conventions,
then asks DeepSeek to write the structure artifact:

```bash
python3 /home/lethanhlam/.codex/skills/_shared/deepseek_workflow.py structure \
  --plan docs/report/plan.json \
  --repo-context-file /tmp/reviewed-safe-context.txt --allow-external \
  --out docs/report/structure.json
```

Review and minimize the plan artifact AND context before external transport.
Do not send secrets, private logs, customer dumps or raw repository snapshots.
If authorization or safe minimization is absent, author locally and record the
fallback in notes. The flag is an accidental-call guard, not a PII detector.

After DeepSeek writes the file, Codex validates JSON/schema, verifies the rules
below, and routes layout/config issues to senior-reviewer. Codex may request one
DeepSeek rewrite if the output is malformed or unsafe. If it still fails or
DeepSeek is unavailable, Codex writes or patches this artifact and records the
fallback/correction in `notes` or `review_flags`.

## When to trigger
After `planner` produces docs/report/plan.json with `status: ready_for_structure`, before
`senior-reviewer`, only within the activation boundary above. Adding a module
does not independently activate this pipeline.

## Supported stacks

The trees below illustrate larger applications, not mandatory scaffolding.
Include only boundaries justified by current contracts or real complexity;
a one-file CLI need not acquire services, repositories, a config module or .env.

### Python
```
<project-root>/
|-- src/<package_name>/
|   |-- __init__.py
|   |-- main.py
|   |-- config.py        # typed settings, env loading, startup validation
|   |-- core/            # business logic
|   |-- services/        # external integrations
|   |-- models/          # data models / schemas
|   |-- constants.py     # stable domain constants only, not env config
|   `-- utils/
|-- tests/
|   |-- unit/
|   `-- integration/
|-- docs/report/          # workflow JSON artifacts land here
|-- docs/plan/            # plan reports land here
|-- pyproject.toml
|-- README.md
`-- .env.example
```

### Node / TypeScript
```
<project-root>/
|-- src/
|   |-- index.ts
|   |-- modules/<feature>/
|   |   |-- controller.ts
|   |   |-- service.ts
|   |   |-- repository.ts
|   |   `-- types.ts
|   |-- config/
|   |   |-- env.ts       # env parsing and validation
|   |   `-- index.ts     # exported typed app config
|   |-- constants/
|   `-- lib/
|-- tests/
|   |-- unit/
|   `-- integration/
|-- docs/report/
|-- docs/plan/
|-- package.json
|-- tsconfig.json
`-- .env.example
```

### Go
```
<project-root>/
|-- cmd/<app_name>/main.go
|-- internal/
|   |-- config/
|   |-- domain/
|   |-- service/
|   |-- repository/
|   `-- handler/
|-- pkg/                  # public reusable packages, if any
|-- test/
|-- docs/report/
|-- docs/plan/
|-- go.mod
`-- .env.example
```

## Rules
1. `docs/report/` is mandatory for workflow JSON artifacts, and `docs/plan/` is mandatory for final Markdown plan reports at the actual project root, as a sibling of
   folders such as `src/`, `tests/`, `cmd/`, `internal/`, package files, and
   config files. Do not create an extra folder named `project`.
2. Respect existing boundaries and reuse the responsibility owner. Add a layer
   only for a current contract, incompatible external semantics or demonstrated
   complexity; do not mechanically split Controller/Service/Repository/Mapper.
3. If the project already exists, detect and respect its current layout
   instead of forcing this template wholesale; propose the minimal diff to
   align it, and flag major deviations to `senior-reviewer` instead of
   silently restructuring an existing repo.
4. Follow existing test layout; mirror source when useful, not as a mandatory new tree.
5. Never hard-code secrets, API keys, tokens, passwords, production URLs,
   ports, database URLs, absolute machine paths, feature flags, or
   environment-specific values in source code. Put runtime configuration in
   `.env` plus a stack-appropriate config module, and commit only
   `.env.example`.
6. Validate required configuration at startup. Fail fast with a clear error
   when required config is missing, but never print secret values.
7. Keep environment config, domain constants, and test fixtures separate:
   values that change by environment belong in config; stable business
   vocabulary belongs in constants/enums/types; test-only values belong in
   fixtures or test helpers.
8. Create dependencies at the entrypoint/composition root and pass them into
   services. Business logic must not construct database clients, HTTP clients,
   SDK clients, loggers, or config readers directly.
9. External integrations belong behind clients/adapters/services so tests can
   replace them with fakes or mocks. Unit tests must not require live network,
   real secrets, or production services.
10. Logging and error handling must avoid secret leakage. Do not log raw
    tokens, `.env` contents, credentials, or full third-party responses that
    may contain sensitive data.

## Boundary expectations
These describe responsibilities where such components already exist or are
justified by the current requirements; they do not require creating each file.
- Controller/handler/route files parse input, call a use-case/service, and
  map the result to a response. They must not contain business workflows.
- Service/use-case files own application flow and business decisions. They
  depend on interfaces/ports where practical, not concrete infrastructure.
- Repository/storage files own persistence details only.
- Client/integration files own third-party API calls, retries, timeouts, and
  response mapping.
- Domain/core files should avoid importing web frameworks, database drivers,
  or vendor SDKs unless the existing framework convention makes that
  unavoidable.

## Existing project adaptation
- Detect the repository's current framework conventions before proposing new
  folders. Prefer established conventions for frameworks such as Django,
  FastAPI, Flask, Next.js, NestJS, Express, Vite, CLIs, and Go services.
- Do not introduce a second config system if one already exists. Extend the
  current config loader and `.env.example` instead.
- Propose the smallest layout diff that satisfies the plan. Major
  reorganizations must be listed in `review_flags` for `senior-reviewer`.
- If the stack is unsupported or ambiguous, set `stack` to the closest
  detected value, document the uncertainty in `notes`, and add a
  `review_flags` item instead of inventing a new standard silently.

## Output schema (docs/report/structure.json)
```json
{
  "stack": "python | node | typescript | go",
  "tree": "```\n...ascii tree...\n```",
  "config_strategy": {
    "env_file": ".env",
    "example_file": ".env.example",
    "config_module": "path/to/config module",
    "validation": "how required config is validated at startup"
  },
  "anti_hardcode_rules": [
    "no secrets, URLs, ports, absolute paths, or environment-specific values in business logic"
  ],
  "boundaries": [
    "controller/handler",
    "service/use-case",
    "repository/storage",
    "external clients/integrations"
  ],
  "notes": ["deviations from the default template and why"],
  "review_flags": ["layout/config/security concerns senior-reviewer must inspect"]
}
```
