# scafctl-plugin-oci

Daemonless OCI image operations using go-containerregistry. A crane CLI replacement as a scafctl provider plugin.

## Names

| Surface | Value |
|---------|-------|
| Repository | `scafctl-plugin-oci` |
| Go module | `github.com/oakwood-commons/scafctl-plugin-oci` |
| Binary | `scafctl-plugin-oci` |
| Provider name | `oci` |
| Catalog artifact | `oci` |

The **provider name** is what users reference in solutions (`provider: oci`).

## Operations

| Operation | Description |
|-----------|-------------|
| `digest` | Get digest of a remote image |
| `manifest` | Get manifest JSON for a remote image |
| `ls` | List tags in a repository |
| `catalog` | List repositories in a registry |
| `pull` | Pull image to local tarball |
| `push` | Push local tarball to registry |
| `copy` | Copy image between registries |
| `append` | Append layer(s) to a base image |
| `mutate` | Modify image config (env, labels, entrypoint, cmd, user, workdir) |
| `delete` | Delete image from registry |

## Installation

```bash
# Build and install locally
task release:local VERSION=0.1.0

# Or build from source
task build
```

## Authentication

The plugin uses a layered credential chain. No Docker or Podman installation required.

### Credential priority (highest → lowest)

| # | Source | When it applies |
|---|--------|-----------------|
| 1 | Explicit settings | `username`/`password` in solution config |
| 2 | Host broker | Solution context with `scafctl auth login` |
| 3 | scafctl credential store | After `scafctl catalog login <registry>` |
| 4 | Docker/Podman config | `~/.docker/config.json` fallback |
| 5 | Anonymous | Public registries (Red Hat, Docker Hub public, etc.) |

### Setup (scafctl only — no Docker needed)

```bash
# Login to a registry via scafctl
scafctl catalog login ghcr.io

# That's it. The plugin reads scafctl's credential store directly.
scafctl run provider oci --plugin-dir ./dist operation=ls repository=ghcr.io/myorg/myapp
```

### Auto-detected registries

The plugin auto-detects auth handlers for known registries when running in solution context:

| Registry | Auth handler |
|----------|-------------|
| `ghcr.io` | github |
| `gcr.io`, `*.pkg.dev` | gcp |
| `*.azurecr.io` | entra |

### Explicit settings (solution config)

```yaml
resolvers:
  private-image:
    provider: oci
    settings:
      registry: ghcr.io
      username: "{{env.REGISTRY_USER}}"
      password: "{{env.REGISTRY_TOKEN}}"
      # auth_handler: github   # optional — auto-detected for known registries
      # scope: read:packages   # optional — sensible defaults used
    inputs:
      operation: digest
      ref: ghcr.io/myorg/private-app:v1
```

### Anonymous access

Public registries work without any login:

```bash
scafctl run provider oci --plugin-dir ./dist operation=digest ref=registry.access.redhat.com/ubi9/ubi-minimal:latest
```

## CLI Usage (crane replacement)

Use `scafctl run provider oci` for ad-hoc crane-like operations:

```bash
# List tags (like crane ls)
scafctl run provider oci operation=ls repository=ghcr.io/myorg/myapp

# Get digest (like crane digest)
scafctl run provider oci operation=digest ref=ghcr.io/myorg/myapp:latest

# Get manifest (like crane manifest)
scafctl run provider oci operation=manifest ref=ghcr.io/myorg/myapp:latest

# Copy between registries (like crane copy)
scafctl run provider oci operation=copy src=ghcr.io/myorg/app:v1 dst=ecr.io/myorg/app:v1

# Pull image to tarball (like crane pull)
scafctl run provider oci operation=pull ref=ghcr.io/myorg/app:v1 path=./image.tar

# Push tarball to registry (like crane push)
scafctl run provider oci operation=push ref=ghcr.io/myorg/app:v2 path=./image.tar

# Append a layer (like crane append)
scafctl run provider oci operation=append ref=ghcr.io/myorg/app:v1 layers='["./layer.tar.gz"]'

# Mutate image config (like crane mutate)
scafctl run provider oci operation=mutate ref=ghcr.io/myorg/app:v1 config='{"env":["FOO=bar"],"user":"nobody"}'

# Delete image (like crane delete)
scafctl run provider oci operation=delete ref=ghcr.io/myorg/app:v1
```

When using `--plugin-dir`, ensure you've run `task build` first.

## Solution Usage

Reference the **oci** provider in solutions:

```yaml
resolvers:
  image-digest:
    resolve:
      with:
        - provider: oci
          inputs:
            operation: digest
            ref: ghcr.io/myorg/myapp:latest

spec:
  workflow:
    actions:
      push-image:
        provider: oci
        inputs:
          operation: push
          ref: "expr: _.registry + '/myapp:' + _.version"
          path: ./dist/image.tar
```

## Development

```bash
task test        # Run tests
task lint        # Run linter
task build       # Build binary
task ci          # Full CI pipeline (lint + test + build)
task bench       # Run benchmarks
```

## Local Testing

```bash
# 1. Build the binary
task build

# 2. Test against a public registry (no auth needed)
scafctl run provider oci --plugin-dir ./dist operation=digest ref=docker.io/library/alpine:latest

# 3. Login to a private registry via scafctl
scafctl catalog login ghcr.io

# 4. Test against a private registry
scafctl run provider oci --plugin-dir ./dist operation=ls repository=ghcr.io/myorg/myapp

# 5. Package as a local catalog artifact
task release:local VERSION=0.1.0
```

## Release

### Tagging a release

```bash
task release:tag VERSION=0.1.0
```

This creates a signed git tag and pushes it. The CI release workflow handles the rest.

### What the release workflow does

1. **Verify** — runs tests with race detector
2. **Release** — GoReleaser builds binaries for all platforms, creates GitHub release
3. **Publish** — pushes the plugin artifact to the catalog and refreshes the index

### Required secrets

| Secret | Scopes | Purpose |
|--------|--------|---------|
| `GITHUB_TOKEN` | Default | Build, test, create release, GHCR push |
| `CATALOG_PUSH_TOKEN` | `repo`, `read:packages`, `write:packages` | Refresh catalog index |

### Token strategy

For official providers, use a machine account or GitHub App for the publishing
token rather than a personal account.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

Apache-2.0 — see [LICENSE](LICENSE) for details.
