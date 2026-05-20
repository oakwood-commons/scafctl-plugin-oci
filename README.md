# scafctl-plugin-oci

A scafctl provider plugin for daemonless OCI image operations. Inspect, copy, build, mutate, and publish container images directly from scafctl solutions without requiring Docker, Podman, or any container runtime.

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
| `config` | Get image config JSON |
| `ls` | List tags in a repository |
| `catalog` | List repositories in a registry |
| `pull` | Pull image to local tarball |
| `push` | Push local tarball to registry |
| `copy` | Copy image between registries |
| `append` | Append layer(s) to a base image (supports scratch) |
| `mutate` | Modify image config with optional layer append, annotations, and destination override |
| `tag` | Tag a remote image without re-pushing |
| `validate` | Validate image integrity (manifest, config, layers) |
| `blob` | Read individual blob (layer) by digest |
| `export` | Export image filesystem as tarball |
| `flatten` | Flatten all layers into a single layer |
| `rebase` | Rebase image onto a new base image |
| `index` | Create a multi-arch image index from per-platform images |
| `delete` | Delete image from registry |

## Installation

```bash
# Install from catalog
scafctl plugins install oci

# Or build and install locally
task release:local VERSION=0.2.0
```

## Authentication

The plugin uses a layered credential chain. No container runtime required.

### Credential priority (highest → lowest)

| # | Source | When it applies |
|---|--------|-----------------|
| 1 | Explicit settings | `username`/`password` in solution config |
| 2 | Host broker | Solution context with `scafctl auth login` |
| 3 | scafctl credential store | After `scafctl catalog login <registry>` |
| 4 | Docker/Podman config | `~/.docker/config.json` fallback |
| 5 | Anonymous | Public registries |

For registries not in the auto-detected list, the plugin queries all available host auth handlers and tries each one. This means any registry with a configured auth handler works automatically — no manual credential bridging required.

### Auto-detected registries

| Registry | Auth handler |
|----------|-------------|
| `ghcr.io` | github |
| `gcr.io`, `*.pkg.dev` | gcp |
| `*.azurecr.io` | entra |

Other registries fall back to trying all available host auth handlers.

### Settings

| Setting | Type | Description |
|---------|------|-------------|
| `registry` | string | Registry hostname for credential scoping |
| `username` | string | Explicit username |
| `password` | string | Explicit password or token |
| `auth_handler` | string | Force a specific auth handler (e.g., `ford-quay`) |
| `scope` | string | OAuth scope override |
| `insecure` | bool | Allow HTTP (non-TLS) registries for dev/localhost/air-gapped environments |

```yaml
resolvers:
  private-image:
    provider: oci
    settings:
      registry: ghcr.io
      username: "{{env.REGISTRY_USER}}"
      password: "{{env.REGISTRY_TOKEN}}"
    inputs:
      operation: digest
      ref: ghcr.io/myorg/private-app:v1
```

## Operation Reference

### `digest`

Get the digest, size, and media type of a remote image.

| Input | Required | Description |
|-------|----------|-------------|
| `ref` | yes | Image reference |
| `platform` | no | Target platform (`os/arch`) for multi-arch images |

**Output**: `success`, `digest`, `size`, `mediaType`

### `manifest`

Get the raw manifest JSON for a remote image.

| Input | Required | Description |
|-------|----------|-------------|
| `ref` | yes | Image reference |

**Output**: `success`, `digest`, `size`, `mediaType`, `manifest`

### `config`

Get the image config JSON (OCI config containing env, entrypoint, cmd, labels, etc.).

| Input | Required | Description |
|-------|----------|-------------|
| `ref` | yes | Image reference |
| `platform` | no | Target platform for multi-arch images |

**Output**: `success`, `ref`, `digest`, `mediaType`, `config`

### `ls`

List tags in a repository.

| Input | Required | Description |
|-------|----------|-------------|
| `repository` | yes | Repository path (e.g., `ghcr.io/org/repo`) |

**Output**: `success`, `repository`, `tags`

### `catalog`

List repositories in a registry.

| Input | Required | Description |
|-------|----------|-------------|
| `registry` | yes | Registry hostname |

**Output**: `success`, `registry`, `repositories`

### `pull`

Pull an image to a local Docker-style tarball.

| Input | Required | Description |
|-------|----------|-------------|
| `ref` | yes | Image reference |
| `path` | yes | Local file path for the tarball |
| `platform` | no | Target platform for multi-arch images |

**Output**: `success`, `ref`, `digest`, `path`

### `push`

Push a local tarball to a registry.

| Input | Required | Description |
|-------|----------|-------------|
| `ref` | yes | Destination image reference |
| `path` | yes | Local tarball path |

**Output**: `success`, `ref`, `digest`, `size`

### `copy`

Copy an image between registries without pulling locally.

| Input | Required | Description |
|-------|----------|-------------|
| `src` | yes | Source image reference |
| `dst` | yes | Destination image reference |

**Output**: `success`, `src`, `dst`, `digest`, `size`, `mediaType`

### `append`

Append layer(s) to a base image. Supports `ref: scratch` for building from-scratch images.

| Input | Required | Description |
|-------|----------|-------------|
| `ref` | yes | Base image reference (or `scratch`) |
| `layers` | yes | Array of file/directory paths to add as layers |
| `dst` | no | Destination reference (required when `ref` is `scratch`) |
| `platform` | no | Target platform for multi-arch base images |

**Output**: `success`, `ref`, `digest`, `size`

### `mutate`

Modify image config fields, optionally append layers and apply OCI annotations. Can write to a different destination.

| Input | Required | Description |
|-------|----------|-------------|
| `ref` | yes | Image reference (or `scratch`) |
| `dst` | no | Destination reference (defaults to `ref`) |
| `entrypoint` | no | Container entrypoint (string or array) |
| `cmd` | no | Container command (string or array) |
| `user` | no | User to run as |
| `workdir` | no | Working directory |
| `env` | no | Environment variables (array of `VAR=value` or map) |
| `labels` | no | Docker labels (key-value map) |
| `annotations` | no | OCI manifest annotations (key-value map, distinct from labels) |
| `exposed_ports` | no | Ports to expose (array of `port/proto`, e.g., `["8080/tcp"]`) |
| `set_platform` | no | Set platform `os/arch` on the image config |
| `layers` | no | Layer paths to append |
| `config` | no | Nested config map (alternative to convenience inputs above) |
| `output` | no | Write mutated image to local tarball instead of pushing to registry |
| `platform` | no | Target platform for multi-arch base images |

Convenience inputs (`entrypoint`, `cmd`, etc.) override nested `config` fields. At least one mutation (config field, layer, or annotation) is required.

When `output` is set, the mutated image is written to a local tarball file instead of being pushed to a registry.

**Output**: `success`, `ref`, `digest`, `size` (or `success`, `path`, `digest`, `size` when `output` is set)

### `tag`

Tag a remote image with a new tag without re-pushing. Common CI pattern for promoting images.

| Input | Required | Description |
|-------|----------|-------------|
| `ref` | yes | Source image reference |
| `tag` | yes | New tag to apply |

**Output**: `success`, `ref`, `tag`, `digest`

### `validate`

Validate image integrity by checking manifest, config, and layer digests.

| Input | Required | Description |
|-------|----------|-------------|
| `ref` | yes | Image reference |
| `platform` | no | Target platform for multi-arch images |

**Output**: `success`, `ref`, `digest`, `layerCount` (or `success: false`, `error` on invalid image)

### `blob`

Read an individual blob (layer) by digest. Optionally write to a file.

| Input | Required | Description |
|-------|----------|-------------|
| `ref` | yes | Image reference (repository context) |
| `digest` | yes | Blob digest (`sha256:...`) |
| `path` | no | Local file path to write the blob to |

**Output**: `success`, `ref`, `digest`, `size`, `mediaType`

### `export`

Export the filesystem of an image as a flat tarball (like `docker export`).

| Input | Required | Description |
|-------|----------|-------------|
| `ref` | yes | Image reference |
| `path` | yes | Local file path for the exported tarball |
| `platform` | no | Target platform for multi-arch images |

**Output**: `success`, `ref`, `path`, `size`

### `flatten`

Flatten all image layers into a single layer. Useful for reducing image size and layer count.

| Input | Required | Description |
|-------|----------|-------------|
| `ref` | yes | Image reference |
| `dst` | no | Destination reference (defaults to `ref`) |
| `platform` | no | Target platform for multi-arch images |

**Output**: `success`, `ref`, `digest`, `size`

### `rebase`

Rebase an image onto a new base image (swap base layers).

| Input | Required | Description |
|-------|----------|-------------|
| `ref` | yes | Image reference |
| `old_base` | yes | Old base image reference |
| `new_base` | yes | New base image reference |
| `dst` | no | Destination reference (defaults to `ref`) |

**Output**: `success`, `ref`, `digest`, `size`

### `index`

Create a multi-arch OCI image index from per-platform images.

| Input | Required | Description |
|-------|----------|-------------|
| `ref` | yes | Destination index reference |
| `manifests` | yes | Array of `{ref, platform?}` entries |

Each manifest entry:
| Field | Required | Description |
|-------|----------|-------------|
| `ref` | yes | Per-platform image reference |
| `platform` | no | Platform in `os/arch[/variant]` format (auto-detected from image config if omitted) |

**Output**: `success`, `ref`, `digest`, `mediaType`

### `delete`

Delete an image from a registry.

| Input | Required | Description |
|-------|----------|-------------|
| `ref` | yes | Image reference to delete |

**Output**: `success`, `ref`

## Usage

### CLI

```bash
# Get digest
scafctl run provider oci operation=digest ref=ghcr.io/myorg/myapp:latest

# Get image config
scafctl run provider oci operation=config ref=ghcr.io/myorg/myapp:latest

# List tags
scafctl run provider oci operation=ls repository=ghcr.io/myorg/myapp

# Copy between registries
scafctl run provider oci operation=copy src=ghcr.io/myorg/app:v1 dst=ecr.io/myorg/app:v1

# Tag without re-pushing
scafctl run provider oci operation=tag ref=ghcr.io/myorg/app:v1 tag=latest

# Build from scratch
scafctl run provider oci operation=append ref=scratch dst=ghcr.io/myorg/data:v1 layers='["./data"]'

# Mutate with annotations
scafctl run provider oci operation=mutate ref=ghcr.io/myorg/app:v1 entrypoint=/app annotations='{"org.opencontainers.image.source":"https://github.com/myorg/app"}'

# Mutate to local tarball
scafctl run provider oci operation=mutate ref=ghcr.io/myorg/app:v1 user=nobody output=./mutated.tar

# Validate image integrity
scafctl run provider oci operation=validate ref=ghcr.io/myorg/app:v1

# Export image filesystem
scafctl run provider oci operation=export ref=ghcr.io/myorg/app:v1 path=./rootfs.tar

# Flatten image layers
scafctl run provider oci operation=flatten ref=ghcr.io/myorg/app:v1 dst=ghcr.io/myorg/app:flat
```

### Solution

```yaml
spec:
  resolvers:
    image-digest:
      resolve:
        with:
          - provider: oci
            inputs:
              operation: digest
              ref: ghcr.io/myorg/myapp:latest

  workflow:
    actions:
      push-image:
        provider: oci
        inputs:
          operation: push
          ref: "expr: _.registry + '/myapp:' + _.version"
          path: ./dist/image.tar
```

See `examples/` for complete solution files.

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
# Build and install as a local catalog artifact
task release:local VERSION=0.2.0

# Run a sample solution
scafctl run solution -f examples/digest.yaml
```

## Release

```bash
task release:tag VERSION=0.2.0
```

This creates a signed git tag and pushes it. The CI release workflow runs tests, builds binaries for all platforms, publishes the plugin artifact to the catalog, and refreshes the catalog index.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

Apache-2.0 — see [LICENSE](LICENSE) for details.
