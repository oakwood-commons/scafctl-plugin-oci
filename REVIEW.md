# Copilot code review: how to review this repo

You are reviewing as an **independent final reviewer**, not a re-run of this
repo's own development process. Your job is to find correctness, security,
compatibility, concurrency, and test-coverage gaps that local checks missed --
not to restate style preferences.

## Sources of truth

Read the repository's own guidance directly rather than inventing parallel rules:

- `.github/copilot-instructions.md` -- repository architecture, build/test/lint commands, and coding conventions.
- `.github/instructions/` -- path- and role-specific implementation constraints.

Where this file's guidance and a source above conflict on a repo-specific
convention, the source above wins; this file governs *how* to conduct the
review.

## Method: review the whole change before writing any findings

1. **Read the full diff and the PR description together** before judging any
   single hunk so cross-file interactions and design intent are clear.
2. **Inventory every changed interface**: exported functions/types, RPC/HTTP
   methods, schemas, config keys, CLI flags, or documented behaviors. Check
   callers, sibling code paths, and existing tests outside the touched lines --
   a hunk that looks fine in isolation can still break caller assumptions.
3. **Check test reality against the actual change**: verify new and updated
   tests exercise the real runtime code path, boundary conditions, and failure
   modes -- not a mock that bypasses the bug or only the happy path.

## What to weigh heavily

- **Regressions and boundary cases**: empty/zero/nil inputs, capacity/eviction
  limits, overwrite file permissions (`0600` tightening on existing files), and
  the error path of every external call (network, filesystem, subprocess, gRPC).
- **Concurrency, synchronization, and resource cleanup**: data races, holding
  locks across network or interactive I/O, closing shared transports while
  requests are in flight, goroutine leaks, and `context.Context` cancellation.
- **Trust boundaries and secret redaction**: trace untrusted inputs to sinks
  (filesystem, exec, HTTP/SSRFs, HTML/CSP, template/expression evaluation) and
  verify secrets/tokens never leak into logs, error strings, dry-run/WhatIf
  descriptions, or snapshots.
- **Comment hygiene**: verify touched comments match actual code behavior, and
  flag development/review narration in code or test comments (PR numbers, review
  rounds, or ticket scaffolding).

Repo-specific (`scafctl-plugin-oci` -- provider plugin):

- **Provider contract coherence**: `GetProviders`, `GetProviderDescriptor`,
  `ExecuteProvider`, `DescribeWhatIf`, and `ExtractDependencies` must agree on
  provider name, schema, and capabilities. Every declared capability MUST have
  a matching entry in `OutputSchemas` (missing output schemas fail host
  registration).
- **Side-effect-free & redacted `DescribeWhatIf`**: `DescribeWhatIf` must never
  perform network/filesystem/process mutations and must never echo raw secrets
  or tokens into dry-run messages.
- **Trust boundaries & resource cleanup**: trace inputs reaching shell/process
  execution, filesystem paths (traversal/symlink/0600 modes), HTTP requests,
  OCI registries, or templates; verify context cancellation, timeouts, and
  response/file handle cleanup.
- **Coverage & race safety**: require `-race`-clean tests and 70%+ patch
  coverage covering both happy and failure paths.
- **Publish hygiene**: this repo is public -- flag any **non-public** employer-
  or organization-specific reference (internal tool names, private
  hostnames/domains, internal codenames, employee/user IDs, or real
  credentials) in the diff, including in docs, examples, and test fixtures.
  Legitimate public project/dependency identifiers (such as
  `oakwood-commons/*` module paths and release URLs) and clearly fictional
  placeholders (`example.com`, `myorg`) are expected and must not be flagged.
- **PR title/body accuracy**: the repo squash-merges pull requests using the PR
  title and body -- the PR title must be a valid conventional-commit subject
  and the body must describe the change as it stands at `HEAD`, not an earlier
  commit. Flag a stale body or non-conventional title.

## What not to spend the review on

Purely stylistic comments, subjective refactoring suggestions, or low-value
formatting nitpicks should not be raised unless they violate an explicit
repository convention. If you have nothing substantive to say about a hunk, say
nothing about it.

Concretely, do not flag:

- **Style nits** that `gofmt`/`gofumpt`/`goimports` or the configured linter
  already govern.
- **"Consider ..." suggestions with no behavioral impact** -- speculative
  abstractions, renames of private helpers, or alternate phrasings of docs.
- **Defensive guards for non-existent callers** -- input validation on internal
  paths with no external input, or nil checks where the value provably cannot
  be nil.

## Follow the implication

If one finding suggests a class of mistake (a missing boundary check, an
unhandled error path, a repeated copy-paste defect), **search the rest of the
diff for other occurrences of the same class before finishing**. Report it as
one consolidated finding covering every location, not one comment per
occurrence.

## Before finishing: a completeness pass

Before submitting the review, reconsider the whole diff once more against:
the interface inventory from Method step 2, any patterns surfaced by your
findings above (did you check every occurrence?), and whether the tests in
Method step 3 would actually catch a regression in what you found. The goal is
one comprehensive Round 1 review, not serial discovery across rounds.

## Re-reviews (after a fix is pushed)

Stick to what changed since your last review (`<last_reviewed_commit>..HEAD`)
rather than re-reviewing the entire pull request from scratch:

1. **Verify prior findings and their fixes**: confirm each previously reported
   issue is resolved, and check the immediate implications of that fix (e.g.
   whether the fix introduced a regression or missed a sibling occurrence of
   the same defect in adjacent paths).
2. **Scope review to changed files + relevant context**: audit only the files
   modified since `<last_reviewed_commit>`, reading callers, consumers, tests,
   or contract definitions in unchanged files only as needed to verify the
   delta. **Do not re-audit unrelated, untouched files** that already passed an
   earlier review round.
3. **Check PR body sync**: confirm the PR title and body still match `HEAD`.
