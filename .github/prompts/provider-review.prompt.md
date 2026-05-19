---
description: "Review Go provider changes for scafctl-specific method semantics, schema correctness, WhatIf parity, and tests."
agent: "go-reviewer"
---
# Provider Review

Review the current provider-related Go changes with extra focus on scafctl provider semantics.

## Phase 1: Normal Go review

Complete the standard Go review workflow first.

## Phase 2: Provider contract review

For each changed provider file, verify all of the following:

- `GetProviderDescriptor` accurately describes the implementation.
- Capabilities match the behavior the provider actually supports.
- `OutputSchemas` includes an entry for every declared capability. Missing output schemas cause silent host registration failure.
- Schema fields, required fields, and examples match runtime expectations.
- Output shape is stable and consistent with docs and tests.

## Phase 3: Lifecycle review

- Unknown provider names are rejected consistently in all relevant methods.
- `DescribeWhatIf` is side-effect free and matches execution intent.
- `ConfigureProvider` is used for host configuration rather than hidden globals.
- `ExecuteProviderStream` is either correctly implemented or explicitly unsupported.
- `ExtractDependencies` only reports real dependencies.
- `StopProvider` is safe to call repeatedly.

## Phase 4: Test review

- Tests cover happy path and unknown provider behavior.
- Tests cover nil, empty, or invalid input when relevant.
- Tests cover any new configuration-dependent logic.
- Tests would catch a mismatch between descriptor and implementation.
- Tests would catch a mismatch between `DescribeWhatIf` and `ExecuteProvider`.

## Output format

Use severity levels: CRITICAL > HIGH > MEDIUM > LOW > INFO.

For each finding include:
- file
- line
- severity
- description
- suggested fix

End with a short summary stating whether provider behavior is coherent across descriptor, execution, WhatIf, and tests.