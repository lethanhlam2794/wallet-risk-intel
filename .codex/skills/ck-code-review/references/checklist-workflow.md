# Checklist-Based Review Workflow

How to apply structured review checklists during code review.

## When to Use

- Pre-landing review (from `/ck:ship` pipeline)
- Explicit request for checklist review
- Security audit before release
- Code-reviewer agent when reviewing significant changes (10+ files or security-sensitive)

## Workflow

### 1. Auto-Detect Project Type

```bash
# Check for web app frameworks
if grep -qE '"(react|vue|svelte|next|nuxt|angular)"' package.json 2>/dev/null; then
  echo "web-app"
# Check for API patterns
elif ls src/routes/ src/api/ src/controllers/ app/controllers/ 2>/dev/null | head -1; then
  echo "api"
else
  echo "base-only"
fi
```

### 2. Load Checklists

Always load: `checklists/base.md`

Overlay based on detection:
- `web-app` → also load `checklists/web-app.md`
- `api` → also load `checklists/api.md`
- Both detected → load both overlays

### 3. Get the Diff

```bash
git fetch origin main --quiet
git diff origin/main
```

**CRITICAL:** Read the FULL diff before flagging anything. Checklist suppressions require full context.

### 4. Two-Pass Discovery, Then Triage

**Pass 1 (SAFETY / INTEGRITY) — Run first:**
- Scan security, data integrity, irreversible side effects, and primary runtime paths
- A category is not a severity. Classify each candidate from reachable impact and evidence
  using `review-calibration.md`

**Pass 2 (CORRECTNESS / QUALITY) — Run second:**
- Scan applicable compatibility, business, resource, test, and maintainability categories
- Apply the same severity, confidence, and false-positive adjudication rules

### 5. Check Suppressions

Before reporting any finding, verify it's NOT in the suppressions list (bottom of `base.md`).

Key suppressions:
- Already addressed in the diff
- Readability-aiding redundancy
- Style/formatting issues
- "Consider using X" when Y works fine

### 6. Output

```
Pre-Landing Review: N accepted issues (B blocker, M major, m minor, n nit)

**BLOCKER**:
- [src/auth/login.ts:42] User input is interpolated directly into a query
  Fix: Use the project's parameterized query helper before passing user input

**MINOR**:
- [src/api/users.ts:88] Magic number 30 for pagination limit
  Fix: Extract to constant `DEFAULT_PAGE_SIZE = 30`
```

### 7. Finding Resolution

For each BLOCKER or MAJOR issue:
- Problem with `file:line`
- Confidence and evidence status
- Recommended fix
- If the user has not already authorized fixes, ask whether to fix now, accept the risk,
  or dispute the finding. BLOCKER cannot be accepted for merge without correction.

If fixing is authorized: apply fixes, then re-run tests before continuing. Do not commit
unless the user requested a commit.

## Integration with /ck:ship

The ship pipeline calls this workflow at Step 4. BLOCKER findings block the pipeline;
MAJOR findings require a fix or explicit owner risk acceptance.

## Integration with /ck:code-review

When invoked as part of standard code review, the checklist augments (not replaces) the existing scout → review → fix → verify pipeline. Checklist findings are merged with code-reviewer's own findings.
