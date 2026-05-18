---
description: "Go coding conventions: struct tags, error handling, design principles, context usage, and formatting."
applyTo: "**/*.go"
---

# Go Conventions

## Struct Tags

Always add JSON/YAML tags on exported structs when the type crosses package or process boundaries.

- Add tags consistently on request, response, config, and persisted types.
- Omit unnecessary tags on purely internal structs only when they are clearly internal.

## Error Handling

Always wrap errors with context:

~~~go
if err != nil {
    return fmt.Errorf("failed to create user: %w", err)
}
~~~

- Return errors with `fmt.Errorf("context: %w", err)`.
- Never panic in library code.
- Check all error returns.
- Keep error messages actionable and specific about the failed operation.

## Design Principles

- Prefer composition over inheritance.
- Keep interfaces small (1-3 methods).
- Accept interfaces, return structs.
- Define interfaces where they are used, not where they are implemented.
- Use constructor functions for dependency injection.
- Use functional options (`WithX(value)`) when constructors need optional configuration.
- Avoid package-level mutable state.

## Context and Cancellation

- Always pass `context.Context` as the first parameter for operations that may block, perform I/O, or need cancellation.
- Honor context cancellation and timeouts in downstream calls.
- Call `defer cancel()` immediately after creating a derived context.

## Secrets and Configuration

- Read secrets from environment variables or injected configuration -- never hardcode them.
- Do not scatter magic strings or numbers through provider logic; use constants or configuration.
- Keep business rules in the provider implementation, not in `main.go` or thin wiring layers.

## Formatting

- `gofmt` and `goimports` are mandatory.
- Keep imports clean and grouped by `goimports`.
- Prefer clear names over abbreviations unless they are standard Go or domain terms.

## Testing-Friendly Design

- Keep side effects behind small interfaces so provider logic is easy to unit test.
- Extract complex branching into small helper functions instead of burying it inside command or provider entrypoints.
- Favor deterministic behavior: sort map-derived output when order matters.
