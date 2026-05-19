---
description: "Implement or refine Go provider changes while keeping descriptor, execution, WhatIf, docs, and tests aligned."
---
# Provider Implementation

Use this prompt when implementing provider changes in a generated plugin repository.

## Goal

Change provider behavior without creating drift between descriptor, runtime behavior, WhatIf output, docs, and tests.

## Required workflow

1. Summarize the intended provider behavior before editing code.
2. Update `GetProviderDescriptor` if the public contract changes.
3. Update `ExecuteProvider` to match that contract.
4. Update `DescribeWhatIf` so it describes the same behavior without side effects.
5. Update tests in the same change.
6. Update docs or examples if inputs, outputs, or defaults changed.

## Checks

- Capabilities match the implemented behavior.
- `OutputSchemas` includes an entry for every declared capability.
- Required schema fields match runtime expectations.
- Unknown provider handling is consistent.
- Nil, empty, and zero-value inputs are handled deliberately.
- Output field names are stable.
- `DescribeWhatIf` remains trustworthy.

## Deliverable

Report:
- the behavior change
- the files changed
- the tests added or updated
- any remaining edge cases