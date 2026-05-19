---
description: "Run Go code review on recent changes. Checks idiomatic Go, security, error handling, concurrency, tests, and plugin conventions."
agent: "go-reviewer"
---
Review the current Go code changes thoroughly. You MUST complete all phases below.

## Phase 1: Automated checks

1. Run `go vet ./...` and `task lint`
2. Run `git diff --stat HEAD -- '*.go'` and `git status --short` to identify all changed and new files
3. Read the full diff for all changed files
4. Read the full contents of all new Go files
5. Run `go test -coverprofile=cover.out` on every changed package
6. Run `go test -race` on changed packages

## Phase 2: Systematic review

For each changed or new file, check all of these categories.

### Security
- [ ] Command injection
- [ ] Path traversal
- [ ] Hardcoded credentials or tokens
- [ ] Unsafe deserialization of untrusted input

### Error handling
- [ ] Ignored errors
- [ ] Missing error wrapping
- [ ] Panics for recoverable failures
- [ ] Error messages leaking sensitive information

### Concurrency
- [ ] Goroutine leaks
- [ ] Race conditions
- [ ] Deadlock potential

### Code quality
- [ ] Functions over 60 lines
- [ ] Nesting depth over 4 levels
- [ ] Non-idiomatic Go patterns

### Plugin conventions
- [ ] Business logic stays out of `cmd/.../main.go`
- [ ] Exported structs have JSON/YAML tags where appropriate
- [ ] Interfaces are small and used for seams, not speculation
- [ ] Secrets are loaded from config or env, not hardcoded
- [ ] Context is threaded through blocking or I/O-heavy paths
- [ ] No magic values when constants or config should be used

### Correctness
- [ ] Delegation forwards all required fields and options
- [ ] No mutation of shared or input structs without intent
- [ ] Nil, empty, and zero-value cases are handled
- [ ] Defaults match docs and examples
- [ ] Deterministic ordering where map output is surfaced
- [ ] `defer cancel()` appears immediately after context creation

### Dead code
- [ ] New exported functions have real callers outside tests
- [ ] New struct fields are actually read and written
- [ ] No orphaned imports or unused configuration

## Phase 3: Adversarial analysis

For each behavioral change, try to break it:
- What happens with nil, empty, or zero inputs?
- What happens when a dependency fails?
- What happens under concurrent access?
- What happens when required configuration is missing?
- Can the change regress existing plugin behavior?

## Phase 4: Cross-file consistency

- [ ] Interface changes are reflected in implementations
- [ ] Signature changes are reflected in call sites
- [ ] New configuration fields are reflected in docs and examples
- [ ] Tests cover the changed behavior, not just compile paths

## Phase 5: Coverage analysis

1. Run `go tool cover -func=cover.out` for each changed package
2. For every changed file:
   - Flag changed functions below 70% coverage
   - Flag any new file below 70% overall coverage
   - Flag any new file with 0% effective coverage as HIGH severity
3. Estimate whether patch coverage is at least 70%
4. Recommend specific missing test cases for each gap

## Phase 6: Self-review

1. Re-read the full diff one more time
2. Ask what you did not check
3. Distinguish root causes from symptoms
4. Look for repeated bug patterns across files
5. Report any new findings separately

## Output format

Use severity levels: CRITICAL > HIGH > MEDIUM > LOW > INFO.

For each finding include:
- file
- line
- severity
- description
- suggested fix

End with a short summary table: files reviewed, findings by severity, and coverage status.
