# Parallel Review Workflow

**Ultrathink** to exhaustively list ALL potential edge cases, then dispatch parallel `code-reviewer` agents to verify: <scope>$ARGUMENTS</scope>

**IMPORTANT:** Activate needed skills. Ensure token efficiency. Sacrifice grammar for concision.

## Workflow

### 1. Ultrathink Edge Cases

Main agent deeply analyzes the scope to LIST all potential edge cases FIRST:
- Read `codebase-summary.md` for context
- Use `/ck:scout` to find relevant files
- **Think exhaustively** about what could go wrong:
  - Null/undefined scenarios
  - Boundary conditions (off-by-one, empty, max values)
  - Error handling gaps
  - Race conditions, async edge cases
  - Input validation holes
  - Trust-boundary defects
  - Resource leaks
  - Untested code paths

**Output format:**
```markdown
## Edge Cases Identified

### Category: [scope-area]
1. [edge case description] → files: [file1, file2]
```

### 2. Categorize & Assign

Group edge cases by similar scope for parallel verification:
- Each category → one `code-reviewer` agent
- Max 6 categories (merge small ones)
- Each reviewer gets specific edge cases to VERIFY, not discover

### 3. Parallel Verification

Launch N `code-reviewer` subagents simultaneously:
- Pass: category name, list of edge cases, relevant files
- Task: **VERIFY** if each edge case is properly handled in code
- Report candidates using the severity, confidence, status, and finding format from
  `references/review-calibration.md`
- Include a fingerprint tuple: `(root cause, violated invariant, runtime sink, impact)`

### 4. Aggregate, Deduplicate, and Resolve Conflicts

The main reviewer owns the merged verdict; subagent output is candidate evidence.

1. Normalize locations and terminology, then group identical fingerprint tuples.
2. Merge duplicate evidence into one finding. Do not deduplicate findings merely because
   they mention the same file or symptom when their root causes can occur independently.
3. When reviewers disagree, re-open the source/contract and compare evidence quality.
   Direct runtime evidence and authoritative contracts outrank inference and mocks.
4. Mark unresolved contradictions DISPUTED with both evidence paths and the targeted check
   needed to decide. Do not select the harsher verdict by default.
5. Run the false-positive adjudication procedure in `references/review-calibration.md` on
   every candidate, with a second controller check for every BLOCKER/MAJOR.
6. Count only ACCEPTED actionable findings; report rejected candidates separately without
   inflating the issue total.

```markdown
## Edge Case Verification Report

### Summary
- Candidate findings: X
- Accepted unique: Y
- Rejected false positives: Z
- Disputed / needs verification: W

### Unhandled Edge Cases (Need Fix)
| # | Edge Case | File | Status |
|---|-----------|------|--------|
```

### 5. Verification Review

After aggregation, verify accepted findings against the full scope:
- Re-check aggregated findings and unhandled edge cases
- Confirm each blocking issue has a concrete file/line and reproduction path
- Classify findings as ACCEPTED / REJECTED / DISPUTED / NEEDS_VERIFICATION
- Assign severity and confidence independently

### 6. Auto-Fix Pipeline

**IF** unhandled/partial edge cases found:
- Ask: "Found N unhandled edge cases. Fix with /ck:fix --parallel? [Y/n]"

### 7. Final Report
- Summary of verification
- Ask: "Commit? [Y/n]" → use `git-manager`
