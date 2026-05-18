---
description: "Review Go auth-handler changes for scafctl login semantics, token behavior, status reporting, and test coverage."
agent: "go-reviewer"
---
# Auth-Handler Review

Review the current auth-handler-related Go changes with extra focus on scafctl authentication semantics.

## Phase 1: Normal Go review

Complete the standard Go review workflow first.

## Phase 2: Auth contract review

For each changed auth-handler file, verify all of the following:

- `GetAuthHandlers` accurately describes flows and capabilities.
- Claims, expiry, login behavior, token behavior, and status reporting stay coherent.
- Output and error behavior match docs and tests.

## Phase 3: Lifecycle review

- Unknown handler names are rejected consistently.
- `Login` returns coherent claims and expiry data.
- `GetStatus` reports state without mutating it.
- `GetToken` returns a valid token or a clear auth error.
- Logout and cache-related methods behave consistently.

## Phase 4: Test review

- Tests cover login, status, token, logout, and unknown handler behavior.
- Tests cover any new configuration-dependent logic.
- Tests would catch drift between declared flows/capabilities and implementation.

## Output format

Use severity levels: CRITICAL > HIGH > MEDIUM > LOW > INFO.

For each finding include:
- file
- line
- severity
- description
- suggested fix

End with a short summary stating whether auth-handler behavior is coherent across metadata, login, token, status, and tests.