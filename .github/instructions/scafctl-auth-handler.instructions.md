---
description: "scafctl auth-handler guidance: handler metadata, login semantics, token behavior, status reporting, and tests."
applyTo: "**/auth_handler*.go"
---

# scafctl Auth-Handler Guidance

Use this guidance when editing auth-handler plugin code.

## Method Responsibilities

- `GetAuthHandlers` describes the handler contract: name, display name, flows, and capabilities.
- `ConfigureAuthHandler` stores host configuration for later use.
- `Login` performs the authentication flow and returns claims plus expiry data.
- `GetStatus` reports current authentication state without side effects.
- `GetToken` returns a usable token or a clear error.
- `Logout`, `ListCachedTokens`, `PurgeExpiredTokens`, and `StopAuthHandler` should be safe and predictable.

## Contract Rules

- Keep handler name, display name, flows, capabilities, and actual behavior aligned.
- Do not advertise flows or capabilities the implementation does not support.
- Keep claims, expiry, login behavior, token behavior, and status reporting internally consistent.
- Reject unknown handler names consistently across methods.

## Token and Status Rules

- `GetStatus` should not mutate auth state.
- `GetToken` should not pretend the user is authenticated when they are not.
- Cached-token behavior should match the handler's actual login and token model.
- Wrap non-trivial errors with context.

## Test Expectations

Every auth-handler change should consider tests for:

- known handler vs unknown handler
- login behavior
- status behavior
- unauthenticated token access
- logout behavior
- cached-token and purge behavior
- any new flow-specific or configuration-dependent edge case