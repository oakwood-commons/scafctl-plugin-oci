---
description: "Fetch and triage PR review comments and CI failures for the current branch. Presents findings for approval before handing off to fixes."
agent: "pr-reviewer"
argument-hint: "Optional: PR number or leave blank to use current branch"
---
Triage unresolved PR review comments and CI failures. Use `gh` CLI and the **GitHub GraphQL API (v4)** to fetch review threads -- the REST API does not expose the `isResolved` field.

Follow these phases **in order** -- do not skip ahead:

1. **Fetch**: Fetch all review threads via GraphQL; **skip comments that are already resolved**. Include **outdated but unresolved** threads -- these still need a response and resolution even if the code has moved
2. **Pipeline check**: Run `gh pr checks <PR_NUMBER>` to see CI status. If any checks are failing, run `gh run view <RUN_ID> --log-failed` to get failure logs. Include pipeline failures in the triage summary alongside review comments -- they may overlap with reviewer feedback or reveal additional issues
3. **Early exit**: If there are **zero unresolved threads** and **all checks are passing**, report that and stop
4. **Triage**: For each unresolved comment and pipeline failure, assess whether it's a legit problem with the code. Present the triage summary with recommendations and **wait for user approval** before proceeding to fix actionable items

Include thread IDs in the triage output so fixes can be linked to resolved threads afterward.