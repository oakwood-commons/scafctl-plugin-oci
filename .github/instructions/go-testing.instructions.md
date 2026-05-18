---
description: "Go testing conventions: table-driven tests, race detection, coverage, and benchmarks."
applyTo: "**/*_test.go"
---

# Go Testing Conventions

## Framework

- Use standard `go test` with **table-driven tests**.
- Use `testify/assert` for assertions.
- Place reusable test doubles in `mock.go` files when needed.

## Race Detection

Always run with the `-race` flag:

~~~bash
go test -race ./...
~~~

## Coverage

~~~bash
go test -cover ./...
~~~

## Coverage Targets

| Code Type | Package Target | Patch Target |
|-----------|---------------|-------------|
| Domain packages | 80%+ | 80%+ |
| CLI and wiring packages | 65%+ | 70%+ |
| Critical business logic | 90%+ | 100% |

### Patch Coverage

Every PR should keep patch coverage high enough that new logic is actually exercised.

- Write tests in the same change as the new code.
- Do not submit a new file with 0% coverage.
- At minimum, cover the happy path and one error path.
- If an entrypoint is hard to test directly, extract the core logic into a helper and test that helper.

## Benchmarks

Add benchmark tests for performance-sensitive code, provider hot paths, or parsing/rendering logic:

~~~go
func BenchmarkMyFeature(b *testing.B) {
    b.ReportAllocs()
    b.ResetTimer()

    for b.Loop() {
        // benchmark code
    }
}
~~~

## Practical Guidance

- Prefer targeted package tests while iterating locally.
- Add integration-style tests only when they validate real provider behavior that unit tests cannot cover.
- Keep tests deterministic: avoid time, network, and filesystem coupling unless the test is explicitly about those behaviors.
