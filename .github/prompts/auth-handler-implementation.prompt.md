---
description: "Implement or refine Go auth-handler changes while keeping metadata, login, token, status, docs, and tests aligned."
---
# Auth-Handler Implementation

Use this prompt when implementing auth-handler changes in a generated plugin repository.

## Goal

Change auth-handler behavior without creating drift between handler metadata, runtime behavior, docs, and tests.

## Required workflow

1. Summarize the intended authentication behavior before editing code.
2. Update `GetAuthHandlers` if the public handler contract changes.
3. Update `Login`, `GetStatus`, and `GetToken` to match that contract.
4. Update tests in the same change.
5. Update docs or examples if flows, capabilities, claims, tokens, or defaults changed.

## Checks

- Flows and capabilities match the implemented behavior.
- Unknown handler handling is consistent.
- Claims and expiry data are coherent.
- `GetStatus` reports state without mutating it.
- `GetToken` returns a usable token or a clear auth error.
- Unauthenticated and error paths are explicit.

## Deliverable

Report:
- the behavior change
- the files changed
- the tests added or updated
- any remaining edge cases