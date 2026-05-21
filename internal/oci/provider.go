// Package oci implements the OCI image provider plugin.
package oci

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/google/go-containerregistry/pkg/v1/validate"
	"github.com/google/jsonschema-go/jsonschema"
	sdkplugin "github.com/oakwood-commons/scafctl-plugin-sdk/plugin"
	sdkprovider "github.com/oakwood-commons/scafctl-plugin-sdk/provider"
	sdkhelper "github.com/oakwood-commons/scafctl-plugin-sdk/provider/schemahelper"
)

const (
	// ProviderName is the unique identifier for this provider.
	ProviderName = "oci"
)

// version is the provider version, settable via ldflags.
var version = "0.1.0"

// Operations supported by the OCI provider.
const (
	OpDigest   = "digest"
	OpManifest = "manifest"
	OpLs       = "ls"
	OpCatalog  = "catalog"
	OpPull     = "pull"
	OpPush     = "push"
	OpCopy     = "copy"
	OpAppend   = "append"
	OpMutate   = "mutate"
	OpConfig   = "config"
	OpTag      = "tag"
	OpIndex    = "index"
	OpValidate = "validate"
	OpBlob     = "blob"
	OpExport   = "export"
	OpFlatten  = "flatten"
	OpRebase   = "rebase"
	OpDelete   = "delete"
)

// Plugin implements the scafctl ProviderPlugin interface.
type Plugin struct {
	// Static credentials from ConfigureProvider settings.
	registry    string
	username    string
	password    string
	authHandler string
	scope       string
	insecure    bool
}

// Ensure Plugin implements the required interface.
var _ sdkplugin.ProviderPlugin = (*Plugin)(nil)

// GetProviders returns the list of providers exposed by this plugin.
//
//nolint:revive // ctx required by interface
func (p *Plugin) GetProviders(_ context.Context) ([]string, error) {
	return []string{ProviderName}, nil
}

// GetProviderDescriptor returns the descriptor for the named provider.
//
//nolint:revive // ctx required by interface
func (p *Plugin) GetProviderDescriptor(_ context.Context, providerName string) (*sdkprovider.Descriptor, error) {
	if providerName != ProviderName {
		return nil, fmt.Errorf("unknown provider: %s", providerName)
	}

	parsedVersion, err := semver.NewVersion(version)
	if err != nil {
		return nil, fmt.Errorf("invalid provider version %q: %w", version, err)
	}

	return &sdkprovider.Descriptor{
		Name:        ProviderName,
		DisplayName: "OCI Image Provider",
		Description: "Daemonless OCI image operations using go-containerregistry. Replaces crane CLI for registry interactions.",
		APIVersion:  "v1",
		Version:     parsedVersion,
		Category:    "container",
		Capabilities: []sdkprovider.Capability{
			sdkprovider.CapabilityFrom,
			sdkprovider.CapabilityAction,
		},
		Schema:        buildInputSchema(),
		OutputSchemas: buildOutputSchemas(),
	}, nil
}

// ExecuteProvider executes the named provider with the given input.
func (p *Plugin) ExecuteProvider(ctx context.Context, providerName string, input map[string]any) (*sdkprovider.Output, error) {
	if providerName != ProviderName {
		return nil, fmt.Errorf("unknown provider: %s", providerName)
	}

	if input == nil {
		return nil, fmt.Errorf("input is required")
	}

	op, _ := input["operation"].(string)
	if op == "" {
		return nil, fmt.Errorf("required field 'operation' is missing")
	}

	switch op {
	case OpDigest:
		return p.executeDigest(ctx, input)
	case OpManifest:
		return p.executeManifest(ctx, input)
	case OpLs:
		return p.executeLs(ctx, input)
	case OpCatalog:
		return p.executeCatalog(ctx, input)
	case OpPull:
		return p.executePull(ctx, input)
	case OpPush:
		return p.executePush(ctx, input)
	case OpCopy:
		return p.executeCopy(ctx, input)
	case OpConfig:
		return p.executeConfig(ctx, input)
	case OpTag:
		return p.executeTag(ctx, input)
	case OpAppend:
		return p.executeAppend(ctx, input)
	case OpMutate:
		return p.executeMutate(ctx, input)
	case OpIndex:
		return p.executeIndex(ctx, input)
	case OpValidate:
		return p.executeValidate(ctx, input)
	case OpBlob:
		return p.executeBlob(ctx, input)
	case OpExport:
		return p.executeExport(ctx, input)
	case OpFlatten:
		return p.executeFlatten(ctx, input)
	case OpRebase:
		return p.executeRebase(ctx, input)
	case OpDelete:
		return p.executeDelete(ctx, input)
	default:
		return nil, fmt.Errorf("unknown operation: %s", op)
	}
}

// DescribeWhatIf returns a description of what the provider would do.
//
//nolint:revive // ctx required by interface
func (p *Plugin) DescribeWhatIf(_ context.Context, providerName string, input map[string]any) (string, error) {
	if providerName != ProviderName {
		return "", fmt.Errorf("unknown provider: %s", providerName)
	}

	if input == nil {
		return "Would perform no operation (nil input)", nil
	}

	op, _ := input["operation"].(string)
	ref, _ := input["ref"].(string)
	src, _ := input["src"].(string)
	dst, _ := input["dst"].(string)
	repository, _ := input["repository"].(string)
	registry, _ := input["registry"].(string)
	path, _ := input["path"].(string)

	switch op {
	case OpDigest:
		return fmt.Sprintf("Would get digest for %s", ref), nil
	case OpManifest:
		return fmt.Sprintf("Would get manifest for %s", ref), nil
	case OpLs:
		return fmt.Sprintf("Would list tags in %s", repository), nil
	case OpCatalog:
		return fmt.Sprintf("Would list repositories in %s", registry), nil
	case OpPull:
		return fmt.Sprintf("Would pull %s to %s", ref, path), nil
	case OpPush:
		return fmt.Sprintf("Would push %s from %s", ref, path), nil
	case OpCopy:
		return fmt.Sprintf("Would copy %s to %s", src, dst), nil
	case OpAppend:
		msg := fmt.Sprintf("Would append layer(s) to %s", ref)
		if dst != "" && dst != ref {
			msg = fmt.Sprintf("Would append layer(s) to %s → %s", ref, dst)
		}
		return msg, nil
	case OpMutate:
		parts := []string{"Would mutate"}
		if n := countLayers(input); n > 0 {
			parts = append(parts, fmt.Sprintf("(append %d layer(s))", n))
		}
		parts = append(parts, ref)
		if dst != "" && dst != ref {
			parts = append(parts, "→", dst)
		}
		return strings.Join(parts, " "), nil
	case OpIndex:
		n := countManifests(input)
		return fmt.Sprintf("Would create multi-arch index %s with %d manifest(s)", ref, n), nil
	case OpConfig:
		return fmt.Sprintf("Would get config for %s", ref), nil
	case OpTag:
		newTag, _ := input["tag"].(string)
		return fmt.Sprintf("Would tag %s as %s", ref, newTag), nil
	case OpValidate:
		return fmt.Sprintf("Would validate image integrity for %s", ref), nil
	case OpBlob:
		digest, _ := input["digest"].(string)
		return fmt.Sprintf("Would read blob %s from %s", digest, ref), nil
	case OpExport:
		return fmt.Sprintf("Would export filesystem of %s to %s", ref, path), nil
	case OpFlatten:
		return fmt.Sprintf("Would flatten %s into a single layer", ref), nil
	case OpRebase:
		oldBase, _ := input["old_base"].(string)
		newBase, _ := input["new_base"].(string)
		return fmt.Sprintf("Would rebase %s from %s onto %s", ref, oldBase, newBase), nil
	case OpDelete:
		return fmt.Sprintf("Would delete %s from registry", ref), nil
	default:
		return fmt.Sprintf("Would perform unknown operation %q", op), nil
	}
}

// getKeychain returns the configured keychain, defaulting to the default keychain.
func (p *Plugin) getKeychain(ctx context.Context) authn.Keychain {
	return buildKeychain(ctx, p.registry, p.username, p.password, p.authHandler, p.scope)
}

// remoteOptions returns the standard remote options for registry interactions.
func (p *Plugin) remoteOptions(ctx context.Context, opts ...remote.Option) []remote.Option {
	base := []remote.Option{
		remote.WithAuthFromKeychain(p.getKeychain(ctx)),
		remote.WithContext(ctx),
	}
	return append(base, opts...)
}

// parseReference wraps name.ParseReference with insecure support and
// friendlier tag validation errors.
func (p *Plugin) parseReference(ref string) (name.Reference, error) {
	var opts []name.Option
	if p.insecure {
		opts = append(opts, name.Insecure)
	}
	parsed, err := name.ParseReference(ref, opts...)
	if err != nil {
		if strings.Contains(ref, "+") {
			return nil, fmt.Errorf("parsing reference %q: tags cannot contain '+' (try replacing with '-'): %w", ref, err)
		}
		return nil, fmt.Errorf("parsing reference %q: %w", ref, err)
	}
	return parsed, nil
}

// isUnsupported checks if an error indicates the OCI registry operation is unsupported.
// Used to detect when a registry doesn't support tag-based manifest deletion.
func isUnsupported(err error) bool {
	if err == nil {
		return false
	}
	// Check for a structured transport.Error with the UNSUPPORTED OCI error code.
	var terr *transport.Error
	if errors.As(err, &terr) {
		for _, diag := range terr.Errors {
			if diag.Code == transport.UnsupportedErrorCode {
				return true
			}
		}
	}
	// Fall back to string matching for non-standard registry responses.
	return strings.Contains(err.Error(), "UNSUPPORTED")
}

// parsePlatform parses an os/arch[/variant] string into a v1.Platform.
func parsePlatform(s string) (*v1.Platform, error) {
	parts := strings.SplitN(s, "/", 3)
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid platform %q: expected os/arch", s)
	}
	if parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid platform %q: os and arch must be non-empty", s)
	}
	platform := &v1.Platform{
		OS:           parts[0],
		Architecture: parts[1],
	}
	if len(parts) == 3 {
		if parts[2] == "" {
			return nil, fmt.Errorf("invalid platform %q: variant must be non-empty when provided", s)
		}
		platform.Variant = parts[2]
	}
	return platform, nil
}

// platformOption parses the optional "platform" input and returns a remote.Option slice.
func platformOption(input map[string]any) ([]remote.Option, error) {
	platformStr, _ := input["platform"].(string)
	if platformStr == "" {
		return nil, nil
	}
	plat, err := parsePlatform(platformStr)
	if err != nil {
		return nil, err
	}
	return []remote.Option{remote.WithPlatform(*plat)}, nil
}

// isScratch returns true if the ref indicates a scratch (empty) base image.
func isScratch(ref string) bool {
	return ref == "scratch"
}

// newScratchImage returns a new empty OCI image.
func newScratchImage() (v1.Image, error) {
	return mutate.MediaType(empty.Image, types.OCIManifestSchema1), nil
}

// resolveBaseImage returns the base image for append/mutate operations.
// If ref is "scratch", returns an empty image. Otherwise fetches from the registry.
func (p *Plugin) resolveBaseImage(ctx context.Context, input map[string]any, ref string) (v1.Image, error) {
	if isScratch(ref) {
		return newScratchImage()
	}

	imgRef, err := p.parseReference(ref)
	if err != nil {
		return nil, err
	}

	platOpts, err := platformOption(input)
	if err != nil {
		return nil, err
	}

	desc, err := remote.Get(imgRef, p.remoteOptions(ctx, platOpts...)...)
	if err != nil {
		return nil, fmt.Errorf("fetching image %q: %w", ref, err)
	}

	img, err := desc.Image()
	if err != nil {
		return nil, fmt.Errorf("resolving image %q: %w", ref, err)
	}

	return img, nil
}

// resolveDestination determines the destination reference for push operations.
// When ref is "scratch", a dst input is required. Otherwise dst defaults to ref.
func (p *Plugin) resolveDestination(ref string, input map[string]any) (string, name.Reference, error) {
	dstStr := ref
	if rawDst, hasDst := input["dst"]; hasDst {
		dst, ok := rawDst.(string)
		if !ok {
			return "", nil, fmt.Errorf("field \"dst\": expected string, got %T", rawDst)
		}
		if dst == "" {
			return "", nil, fmt.Errorf("field \"dst\": must be non-empty")
		}
		dstStr = dst
	}

	if isScratch(ref) && dstStr == ref {
		return "", nil, fmt.Errorf("\"dst\" is required when ref is \"scratch\"")
	}

	dstRef, err := p.parseReference(dstStr)
	if err != nil {
		return "", nil, err
	}

	return dstStr, dstRef, nil
}

// requireString extracts a required string field from input or returns an error.
func requireString(input map[string]any, field string) (string, error) {
	v, _ := input[field].(string)
	if v == "" {
		return "", fmt.Errorf("required field %q is missing or empty", field)
	}
	return v, nil
}

// executeDigest gets the digest for a remote image reference.
func (p *Plugin) executeDigest(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	imgRef, err := p.parseReference(ref)
	if err != nil {
		return nil, err
	}

	desc, err := remote.Get(imgRef, p.remoteOptions(ctx)...)
	if err != nil {
		return nil, fmt.Errorf("fetching descriptor for %q: %w", ref, err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success":   true,
			"digest":    desc.Digest.String(),
			"size":      desc.Size,
			"mediaType": string(desc.MediaType),
		},
	}, nil
}

// executeManifest gets the manifest JSON for a remote image reference.
func (p *Plugin) executeManifest(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	imgRef, err := p.parseReference(ref)
	if err != nil {
		return nil, err
	}

	desc, err := remote.Get(imgRef, p.remoteOptions(ctx)...)
	if err != nil {
		return nil, fmt.Errorf("fetching manifest for %q: %w", ref, err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success":   true,
			"digest":    desc.Digest.String(),
			"size":      desc.Size,
			"mediaType": string(desc.MediaType),
			"manifest":  string(desc.Manifest),
		},
	}, nil
}

// executeLs lists tags in a repository.
func (p *Plugin) executeLs(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	repository, err := requireString(input, "repository")
	if err != nil {
		return nil, err
	}

	repo, err := name.NewRepository(repository)
	if err != nil {
		return nil, fmt.Errorf("parsing repository %q: %w", repository, err)
	}

	tags, err := remote.List(repo, p.remoteOptions(ctx)...)
	if err != nil {
		return nil, fmt.Errorf("listing tags for %q: %w", repository, err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success":    true,
			"repository": repository,
			"tags":       tags,
		},
	}, nil
}

// executeCatalog lists repositories in a registry.
func (p *Plugin) executeCatalog(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	registryStr, err := requireString(input, "registry")
	if err != nil {
		return nil, err
	}

	reg, err := name.NewRegistry(registryStr)
	if err != nil {
		return nil, fmt.Errorf("parsing registry %q: %w", registryStr, err)
	}

	repos, err := remote.Catalog(ctx, reg, p.remoteOptions(ctx)...)
	if err != nil {
		return nil, fmt.Errorf("listing catalog for %q: %w", registryStr, err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success":      true,
			"registry":     registryStr,
			"repositories": repos,
		},
	}, nil
}

// executePull pulls an image to a local tarball.
func (p *Plugin) executePull(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	path, err := requireString(input, "path")
	if err != nil {
		return nil, err
	}

	imgRef, err := p.parseReference(ref)
	if err != nil {
		return nil, err
	}

	platOpts, err := platformOption(input)
	if err != nil {
		return nil, err
	}

	desc, err := remote.Get(imgRef, p.remoteOptions(ctx, platOpts...)...)
	if err != nil {
		return nil, fmt.Errorf("fetching image %q: %w", ref, err)
	}

	img, err := desc.Image()
	if err != nil {
		return nil, fmt.Errorf("resolving image %q: %w", ref, err)
	}

	tag, ok := imgRef.(name.Tag)
	if !ok {
		// For digest references, use a fixed tag so `docker load`
		// produces a meaningful tag instead of "latest".
		tag = imgRef.Context().Tag("pulled")
	}

	if err := tarball.WriteToFile(path, tag, img); err != nil {
		return nil, fmt.Errorf("writing tarball to %q: %w", path, err)
	}

	digest, err := img.Digest()
	if err != nil {
		return nil, fmt.Errorf("getting digest: %w", err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success": true,
			"ref":     ref,
			"digest":  digest.String(),
			"path":    path,
		},
	}, nil
}

// executePush pushes a local tarball to a registry.
func (p *Plugin) executePush(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	path, err := requireString(input, "path")
	if err != nil {
		return nil, err
	}

	imgRef, err := p.parseReference(ref)
	if err != nil {
		return nil, err
	}

	// Load the image from tarball. Pass nil tag to load whatever image is in the tarball.
	img, err := tarball.ImageFromPath(path, nil)
	if err != nil {
		return nil, fmt.Errorf("reading tarball from %q: %w", path, err)
	}

	if err := remote.Write(imgRef, img, p.remoteOptions(ctx)...); err != nil {
		return nil, fmt.Errorf("pushing image to %q: %w", ref, err)
	}

	digest, err := img.Digest()
	if err != nil {
		return nil, fmt.Errorf("getting digest: %w", err)
	}

	size, err := img.Size()
	if err != nil {
		return nil, fmt.Errorf("getting size: %w", err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success": true,
			"ref":     ref,
			"digest":  digest.String(),
			"size":    size,
		},
	}, nil
}

// executeCopy copies an image between registries.
func (p *Plugin) executeCopy(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	src, err := requireString(input, "src")
	if err != nil {
		return nil, err
	}

	dst, err := requireString(input, "dst")
	if err != nil {
		return nil, err
	}

	srcRef, err := p.parseReference(src)
	if err != nil {
		return nil, err
	}

	dstRef, err := p.parseReference(dst)
	if err != nil {
		return nil, err
	}

	desc, err := remote.Get(srcRef, p.remoteOptions(ctx)...)
	if err != nil {
		return nil, fmt.Errorf("fetching source %q: %w", src, err)
	}

	// Copy the full descriptor (handles both images and indexes).
	if err := remote.Put(dstRef, desc, p.remoteOptions(ctx)...); err != nil {
		return nil, fmt.Errorf("pushing to destination %q: %w", dst, err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success":   true,
			"src":       src,
			"dst":       dst,
			"digest":    desc.Digest.String(),
			"size":      desc.Size,
			"mediaType": string(desc.MediaType),
		},
	}, nil
}

// executeConfig gets the image config JSON for a remote image reference.
func (p *Plugin) executeConfig(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	imgRef, err := p.parseReference(ref)
	if err != nil {
		return nil, err
	}

	// Fetch the descriptor first.
	desc, err := remote.Get(imgRef, p.remoteOptions(ctx)...)
	if err != nil {
		return nil, fmt.Errorf("fetching descriptor for %q: %w", ref, err)
	}

	// For multi-arch (index), just return the index manifest.
	// For single image, return the image config.
	var digest v1.Hash
	var mediaType types.MediaType
	var config []byte

	if desc.MediaType.IsIndex() {
		// Index (multi-arch): return the index manifest as config.
		idx, err := desc.ImageIndex()
		if err != nil {
			return nil, fmt.Errorf("reading index %q: %w", ref, err)
		}
		digest, err = idx.Digest()
		if err != nil {
			return nil, fmt.Errorf("getting index digest: %w", err)
		}
		mediaType, err = idx.MediaType()
		if err != nil {
			return nil, fmt.Errorf("getting index mediaType: %w", err)
		}
		config, err = idx.RawManifest()
		if err != nil {
			return nil, fmt.Errorf("getting index manifest: %w", err)
		}
	} else {
		// Single image: return the image config.
		img, err := desc.Image()
		if err != nil {
			return nil, fmt.Errorf("reading image %q: %w", ref, err)
		}
		digest, err = img.Digest()
		if err != nil {
			return nil, fmt.Errorf("getting image digest: %w", err)
		}
		mediaType, err = img.MediaType()
		if err != nil {
			return nil, fmt.Errorf("getting image mediaType: %w", err)
		}
		config, err = img.RawConfigFile()
		if err != nil {
			return nil, fmt.Errorf("getting image config: %w", err)
		}
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success":   true,
			"ref":       ref,
			"digest":    digest.String(),
			"mediaType": string(mediaType),
			"config":    string(config),
		},
	}, nil
}

// executeTag tags a remote image with a new tag without re-pushing.
func (p *Plugin) executeTag(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	newTag, err := requireString(input, "tag")
	if err != nil {
		return nil, err
	}

	srcRef, err := p.parseReference(ref)
	if err != nil {
		return nil, err
	}

	desc, err := remote.Get(srcRef, p.remoteOptions(ctx)...)
	if err != nil {
		return nil, fmt.Errorf("fetching image %q: %w", ref, err)
	}

	// Validate the new tag by parsing a full reference — this catches
	// invalid characters (e.g., '+') with a clear error before any RPC.
	if _, err := p.parseReference(srcRef.Context().Tag(newTag).String()); err != nil {
		return nil, fmt.Errorf("invalid tag %q: %w", newTag, err)
	}

	// Build the destination tag in the same repository.
	dstTag := srcRef.Context().Tag(newTag)

	if err := remote.Tag(dstTag, desc, p.remoteOptions(ctx)...); err != nil {
		return nil, fmt.Errorf("tagging %q as %q: %w", ref, newTag, err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success": true,
			"ref":     ref,
			"tag":     dstTag.String(),
			"digest":  desc.Digest.String(),
		},
	}, nil
}

// executeAppend appends layers to a base image.
func (p *Plugin) executeAppend(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	layerPaths, err := getStringSlice(input, "layers")
	if err != nil {
		return nil, err
	}
	if len(layerPaths) == 0 {
		return nil, fmt.Errorf("at least one layer path is required")
	}

	dstStr, dstRef, err := p.resolveDestination(ref, input)
	if err != nil {
		return nil, err
	}

	img, err := p.resolveBaseImage(ctx, input, ref)
	if err != nil {
		return nil, err
	}

	var layers []v1.Layer
	for _, lp := range layerPaths {
		layer, layerErr := layerFromPath(lp)
		if layerErr != nil {
			return nil, fmt.Errorf("creating layer from %q: %w", lp, layerErr)
		}
		layers = append(layers, layer)
	}

	newImg, err := mutate.AppendLayers(img, layers...)
	if err != nil {
		return nil, fmt.Errorf("appending layers: %w", err)
	}

	if err := remote.Write(dstRef, newImg, p.remoteOptions(ctx)...); err != nil {
		return nil, fmt.Errorf("pushing appended image to %q: %w", dstStr, err)
	}

	newDigest, err := newImg.Digest()
	if err != nil {
		return nil, fmt.Errorf("getting new digest: %w", err)
	}

	newSize, err := newImg.Size()
	if err != nil {
		return nil, fmt.Errorf("getting new size: %w", err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success": true,
			"ref":     dstStr,
			"digest":  newDigest.String(),
			"size":    newSize,
		},
	}, nil
}

// executeMutate modifies image config fields, optionally appending layers and
// writing to a destination reference.
func (p *Plugin) executeMutate(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	img, err := p.resolveBaseImage(ctx, input, ref)
	if err != nil {
		return nil, err
	}

	// Append layers if provided.
	var layerPaths []string
	if _, hasLayers := input["layers"]; hasLayers {
		var layersErr error
		layerPaths, layersErr = getStringSlice(input, "layers")
		if layersErr != nil {
			return nil, layersErr
		}
	}
	if len(layerPaths) > 0 {
		var layers []v1.Layer
		for _, lp := range layerPaths {
			layer, layerErr := layerFromPath(lp)
			if layerErr != nil {
				return nil, fmt.Errorf("creating layer from %q: %w", lp, layerErr)
			}
			layers = append(layers, layer)
		}
		img, err = mutate.AppendLayers(img, layers...)
		if err != nil {
			return nil, fmt.Errorf("appending layers: %w", err)
		}
	}

	// Build config from convenience inputs merged with nested config map.
	cfg := mergeConvenienceConfig(input)

	_, hasAnnotations := input["annotations"]
	if len(cfg) == 0 && len(layerPaths) == 0 && !hasAnnotations {
		return nil, fmt.Errorf("mutate requires at least one of: config fields, convenience inputs (entrypoint, cmd, user, workdir, env, labels, exposed_ports, set_platform), layers, or annotations")
	}

	if len(cfg) > 0 {
		cfgFile, cfgErr := img.ConfigFile()
		if cfgErr != nil {
			return nil, fmt.Errorf("reading image config: %w", cfgErr)
		}

		if cfgErr := applyConfigMutations(cfgFile, cfg); cfgErr != nil {
			return nil, fmt.Errorf("invalid config mutation: %w", cfgErr)
		}

		img, err = mutate.ConfigFile(img, cfgFile)
		if err != nil {
			return nil, fmt.Errorf("applying config mutations: %w", err)
		}
	}

	// Apply OCI annotations (manifest-level, distinct from Docker labels).
	if rawAnns, hasAnns := input["annotations"]; hasAnns {
		var annErr error
		img, annErr = applyAnnotations(img, rawAnns)
		if annErr != nil {
			return nil, annErr
		}
	}

	// If output path is set, write to tarball instead of pushing to registry.
	if outputPath, _ := input["output"].(string); outputPath != "" {
		return p.writeOutputTarball(img, ref, outputPath)
	}

	// Determine destination: dst overrides ref.
	dstStr, dstRef, err := p.resolveDestination(ref, input)
	if err != nil {
		return nil, err
	}

	if err := remote.Write(dstRef, img, p.remoteOptions(ctx)...); err != nil {
		return nil, fmt.Errorf("pushing mutated image to %q: %w", dstStr, err)
	}

	newDigest, err := img.Digest()
	if err != nil {
		return nil, fmt.Errorf("getting new digest: %w", err)
	}

	newSize, err := img.Size()
	if err != nil {
		return nil, fmt.Errorf("getting new size: %w", err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success": true,
			"ref":     dstStr,
			"digest":  newDigest.String(),
			"size":    newSize,
		},
	}, nil
}

// applyAnnotations applies OCI manifest-level annotations to an image.
func applyAnnotations(img v1.Image, rawAnns any) (v1.Image, error) {
	anns, ok := rawAnns.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("field \"annotations\": expected object, got %T", rawAnns)
	}
	annMap := make(map[string]string, len(anns))
	for k, v := range anns {
		annMap[k] = fmt.Sprintf("%v", v)
	}
	annotated := mutate.Annotations(img, annMap)
	annotatedImg, ok := annotated.(v1.Image)
	if !ok {
		return nil, fmt.Errorf("applying annotations: unexpected result type")
	}
	return annotatedImg, nil
}

// writeOutputTarball writes a mutated image to a local tarball file.
func (p *Plugin) writeOutputTarball(img v1.Image, ref, outputPath string) (*sdkprovider.Output, error) {
	newDigest, err := img.Digest()
	if err != nil {
		return nil, fmt.Errorf("getting digest: %w", err)
	}

	parsed, parseErr := p.parseReference(ref)
	if parseErr != nil {
		parsed, _ = name.ParseReference("localhost/mutated:latest")
	}
	tagRef, ok := parsed.(name.Tag)
	if !ok {
		tagRef = parsed.Context().Tag("mutated")
	}

	if err := tarball.WriteToFile(filepath.Clean(outputPath), tagRef, img); err != nil {
		return nil, fmt.Errorf("writing mutated image to %q: %w", outputPath, err)
	}

	newSize, err := img.Size()
	if err != nil {
		return nil, fmt.Errorf("getting size: %w", err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success": true,
			"path":    outputPath,
			"digest":  newDigest.String(),
			"size":    newSize,
		},
	}, nil
}

// executeValidate validates image integrity by checking manifest, config, and layers.
func (p *Plugin) executeValidate(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	imgRef, err := p.parseReference(ref)
	if err != nil {
		return nil, err
	}

	// Fetch the descriptor first.
	desc, err := remote.Get(imgRef, p.remoteOptions(ctx)...)
	if err != nil {
		return nil, fmt.Errorf("fetching descriptor for %q: %w", ref, err)
	}

	// For multi-arch (index), validate the index structure.
	// For single image, validate the image.
	var allValid = true
	var validationErr string
	var digest v1.Hash
	var mediaType types.MediaType
	var layerCount int

	if desc.MediaType.IsIndex() {
		// Index (multi-arch): validate the index structure (not individual platform manifests).
		idx, err := desc.ImageIndex()
		if err != nil {
			return nil, fmt.Errorf("reading index %q: %w", ref, err)
		}
		digest, err = idx.Digest()
		if err != nil {
			allValid = false
			validationErr = fmt.Sprintf("getting index digest: %v", err)
		}
		mediaType, err = idx.MediaType()
		if err != nil {
			allValid = false
			if validationErr == "" {
				validationErr = fmt.Sprintf("getting index media type: %v", err)
			}
		}
		// Index doesn't have layers like an image does, so layer count is 0.
		layerCount = 0

		// Validate the index structure itself.
		if _, err := idx.IndexManifest(); err != nil {
			allValid = false
			if validationErr == "" {
				validationErr = fmt.Sprintf("reading index manifest: %v", err)
			}
		}
	} else {
		// Single image: validate the image.
		img, err := desc.Image()
		if err != nil {
			return nil, fmt.Errorf("reading image %q: %w", ref, err)
		}

		if err := validateImage(img); err != nil {
			return &sdkprovider.Output{
				Data: map[string]any{
					"success": false,
					"ref":     ref,
					"error":   err.Error(),
				},
			}, nil
		}

		digest, err = img.Digest()
		if err != nil {
			allValid = false
			validationErr = fmt.Sprintf("getting image digest: %v", err)
		}
		mediaType, err = img.MediaType()
		if err != nil {
			allValid = false
			if validationErr == "" {
				validationErr = fmt.Sprintf("getting image media type: %v", err)
			}
		}
		layers, err := img.Layers()
		if err != nil {
			allValid = false
			if validationErr == "" {
				validationErr = fmt.Sprintf("getting image layers: %v", err)
			}
		}
		layerCount = len(layers)
	}

	result := map[string]any{
		"success":    allValid,
		"ref":        ref,
		"digest":     digest.String(),
		"mediaType":  string(mediaType),
		"layerCount": layerCount,
	}
	if validationErr != "" {
		result["error"] = validationErr
	}

	return &sdkprovider.Output{Data: result}, nil
}

// executeBlob reads a blob by digest from a repository.
func (p *Plugin) executeBlob(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	digestStr, err := requireString(input, "digest")
	if err != nil {
		return nil, err
	}

	imgRef, err := p.parseReference(ref)
	if err != nil {
		return nil, err
	}

	blobRef := imgRef.Context().Digest(digestStr)

	layer, err := remote.Layer(blobRef, p.remoteOptions(ctx)...)
	if err != nil {
		return nil, fmt.Errorf("fetching blob %q from %q: %w", digestStr, ref, err)
	}

	size, err := layer.Size()
	if err != nil {
		return nil, fmt.Errorf("getting blob size: %w", err)
	}

	mediaType, err := layer.MediaType()
	if err != nil {
		return nil, fmt.Errorf("getting blob media type: %w", err)
	}

	// If a path is provided, write the blob to disk.
	if path, _ := input["path"].(string); path != "" {
		rc, err := layer.Compressed()
		if err != nil {
			return nil, fmt.Errorf("reading blob: %w", err)
		}
		defer func() { _ = rc.Close() }()

		f, err := os.Create(filepath.Clean(path))
		if err != nil {
			return nil, fmt.Errorf("creating blob file %q: %w", path, err)
		}
		defer func() { _ = f.Close() }()

		if _, err := io.Copy(f, rc); err != nil {
			return nil, fmt.Errorf("writing blob to %q: %w", path, err)
		}
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success":   true,
			"ref":       ref,
			"digest":    digestStr,
			"size":      size,
			"mediaType": string(mediaType),
		},
	}, nil
}

// executeExport exports the filesystem of an image as a tarball.
func (p *Plugin) executeExport(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	path, err := requireString(input, "path")
	if err != nil {
		return nil, err
	}

	imgRef, err := p.parseReference(ref)
	if err != nil {
		return nil, err
	}

	platOpts, err := platformOption(input)
	if err != nil {
		return nil, err
	}

	desc, err := remote.Get(imgRef, p.remoteOptions(ctx, platOpts...)...)
	if err != nil {
		return nil, fmt.Errorf("fetching image %q: %w", ref, err)
	}

	img, err := desc.Image()
	if err != nil {
		return nil, fmt.Errorf("resolving image %q: %w", ref, err)
	}

	rc := mutate.Extract(img)
	defer func() { _ = rc.Close() }()

	f, err := os.Create(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("creating output file %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	written, err := io.Copy(f, rc)
	if err != nil {
		return nil, fmt.Errorf("exporting filesystem to %q: %w", path, err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success": true,
			"ref":     ref,
			"path":    path,
			"size":    written,
		},
	}, nil
}

// executeFlatten flattens an image into a single layer.
func (p *Plugin) executeFlatten(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	imgRef, err := p.parseReference(ref)
	if err != nil {
		return nil, err
	}

	platOpts, err := platformOption(input)
	if err != nil {
		return nil, err
	}

	desc, err := remote.Get(imgRef, p.remoteOptions(ctx, platOpts...)...)
	if err != nil {
		return nil, fmt.Errorf("fetching image %q: %w", ref, err)
	}

	img, err := desc.Image()
	if err != nil {
		return nil, fmt.Errorf("resolving image %q: %w", ref, err)
	}

	// Extract the flattened filesystem and create a new single-layer image.
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return mutate.Extract(img), nil
	})
	if err != nil {
		return nil, fmt.Errorf("creating flattened layer: %w", err)
	}

	base, err := newScratchImage()
	if err != nil {
		return nil, fmt.Errorf("creating scratch image: %w", err)
	}

	flatImg, err := mutate.AppendLayers(base, layer)
	if err != nil {
		return nil, fmt.Errorf("appending flattened layer: %w", err)
	}

	// Preserve runtime config fields from the original image, but do NOT copy
	// RootFS.DiffIDs or History — those describe the old layers, not the new
	// single flattened layer. Copying them would produce invalid OCI metadata.
	srcCfg, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("reading original config: %w", err)
	}
	flatCfg, err := flatImg.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("reading flattened config: %w", err)
	}
	flatCfg.Config = srcCfg.Config
	flatCfg.OS = srcCfg.OS
	flatCfg.Architecture = srcCfg.Architecture
	flatCfg.Variant = srcCfg.Variant
	flatCfg.OSVersion = srcCfg.OSVersion
	flatCfg.OSFeatures = srcCfg.OSFeatures
	flatCfg.Author = srcCfg.Author
	flatImg, err = mutate.ConfigFile(flatImg, flatCfg)
	if err != nil {
		return nil, fmt.Errorf("applying original config: %w", err)
	}

	dstStr, dstRef, err := p.resolveDestination(ref, input)
	if err != nil {
		return nil, err
	}

	if err := remote.Write(dstRef, flatImg, p.remoteOptions(ctx)...); err != nil {
		return nil, fmt.Errorf("pushing flattened image to %q: %w", dstStr, err)
	}

	newDigest, err := flatImg.Digest()
	if err != nil {
		return nil, fmt.Errorf("getting digest: %w", err)
	}

	newSize, err := flatImg.Size()
	if err != nil {
		return nil, fmt.Errorf("getting size: %w", err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success": true,
			"ref":     dstStr,
			"digest":  newDigest.String(),
			"size":    newSize,
		},
	}, nil
}

// executeRebase rebases an image onto a new base image.
func (p *Plugin) executeRebase(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	oldBaseRef, err := requireString(input, "old_base")
	if err != nil {
		return nil, err
	}

	newBaseRef, err := requireString(input, "new_base")
	if err != nil {
		return nil, err
	}

	origRef, err := p.parseReference(ref)
	if err != nil {
		return nil, err
	}
	oldRef, err := p.parseReference(oldBaseRef)
	if err != nil {
		return nil, err
	}
	newRef, err := p.parseReference(newBaseRef)
	if err != nil {
		return nil, err
	}

	opts := p.remoteOptions(ctx)

	origDesc, err := remote.Get(origRef, opts...)
	if err != nil {
		return nil, fmt.Errorf("fetching original image %q: %w", ref, err)
	}
	origImg, err := origDesc.Image()
	if err != nil {
		return nil, fmt.Errorf("resolving original image %q: %w", ref, err)
	}

	oldDesc, err := remote.Get(oldRef, opts...)
	if err != nil {
		return nil, fmt.Errorf("fetching old base %q: %w", oldBaseRef, err)
	}
	oldImg, err := oldDesc.Image()
	if err != nil {
		return nil, fmt.Errorf("resolving old base %q: %w", oldBaseRef, err)
	}

	newDesc, err := remote.Get(newRef, opts...)
	if err != nil {
		return nil, fmt.Errorf("fetching new base %q: %w", newBaseRef, err)
	}
	newImg, err := newDesc.Image()
	if err != nil {
		return nil, fmt.Errorf("resolving new base %q: %w", newBaseRef, err)
	}

	rebasedImg, err := mutate.Rebase(origImg, oldImg, newImg)
	if err != nil {
		return nil, fmt.Errorf("rebasing image: %w", err)
	}

	dstStr, dstRef, err := p.resolveDestination(ref, input)
	if err != nil {
		return nil, err
	}

	if err := remote.Write(dstRef, rebasedImg, p.remoteOptions(ctx)...); err != nil {
		return nil, fmt.Errorf("pushing rebased image to %q: %w", dstStr, err)
	}

	newDigest, err := rebasedImg.Digest()
	if err != nil {
		return nil, fmt.Errorf("getting digest: %w", err)
	}

	newSize, err := rebasedImg.Size()
	if err != nil {
		return nil, fmt.Errorf("getting size: %w", err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success": true,
			"ref":     dstStr,
			"digest":  newDigest.String(),
			"size":    newSize,
		},
	}, nil
}

// executeDelete deletes an image from a registry.
func (p *Plugin) executeDelete(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	imgRef, err := p.parseReference(ref)
	if err != nil {
		return nil, err
	}

	// First try tag-based DELETE (works on most registries and the test registry).
	// If the registry returns UNSUPPORTED, fall back to digest-based DELETE as a
	// spec-aligned fallback for registries that implement manifest deletion by digest.
	deleteErr := remote.Delete(imgRef, p.remoteOptions(ctx)...)
	if deleteErr == nil {
		return &sdkprovider.Output{
			Data: map[string]any{
				"success": true,
				"ref":     ref,
			},
		}, nil
	}

	// Check if the registry returned UNSUPPORTED — if so, retry with digest.
	if !isUnsupported(deleteErr) {
		return nil, fmt.Errorf("deleting %q: %w", ref, deleteErr)
	}

	// Resolve to digest and retry.
	desc, err := remote.Get(imgRef, p.remoteOptions(ctx)...)
	if err != nil {
		return nil, fmt.Errorf("resolving %q for digest-based delete: %w", ref, err)
	}

	digestRef := imgRef.Context().Digest(desc.Digest.String())
	if err := remote.Delete(digestRef, p.remoteOptions(ctx)...); err != nil {
		return nil, fmt.Errorf("deleting %q by digest: %w", ref, err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success": true,
			"ref":     ref,
			"digest":  desc.Digest.String(),
		},
	}, nil
}

// executeIndex creates a multi-arch OCI image index from a list of per-platform manifests.
func (p *Plugin) executeIndex(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	idxRef, err := p.parseReference(ref)
	if err != nil {
		return nil, err
	}

	manifests, err := getManifestEntries(input)
	if err != nil {
		return nil, err
	}

	idx := v1.ImageIndex(empty.Index)

	for _, entry := range manifests {
		entryRef, err := p.parseReference(entry.ref)
		if err != nil {
			return nil, err
		}

		desc, err := remote.Get(entryRef, p.remoteOptions(ctx)...)
		if err != nil {
			return nil, fmt.Errorf("fetching manifest %q: %w", entry.ref, err)
		}

		img, err := desc.Image()
		if err != nil {
			return nil, fmt.Errorf("resolving image %q: %w", entry.ref, err)
		}

		platform, err := resolveEntryPlatform(entry, img)
		if err != nil {
			return nil, err
		}

		add := mutate.IndexAddendum{
			Add: img,
			Descriptor: v1.Descriptor{
				Platform: platform,
			},
		}

		idx = mutate.AppendManifests(idx, add)
	}

	if err := remote.WriteIndex(idxRef, idx, p.remoteOptions(ctx)...); err != nil {
		return nil, fmt.Errorf("pushing index to %q: %w", ref, err)
	}

	digest, err := idx.Digest()
	if err != nil {
		return nil, fmt.Errorf("getting index digest: %w", err)
	}

	mediaType, err := idx.MediaType()
	if err != nil {
		return nil, fmt.Errorf("getting index media type: %w", err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success":   true,
			"ref":       ref,
			"digest":    digest.String(),
			"mediaType": string(mediaType),
		},
	}, nil
}

// manifestEntry represents one image in an index.
type manifestEntry struct {
	ref      string
	platform string
}

// getManifestEntries extracts and validates the manifests array from input.
func getManifestEntries(input map[string]any) ([]manifestEntry, error) {
	raw, ok := input["manifests"]
	if !ok {
		return nil, fmt.Errorf("required field \"manifests\" is missing")
	}

	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("field \"manifests\": expected array, got %T", raw)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("field \"manifests\": at least one manifest entry is required")
	}

	entries := make([]manifestEntry, 0, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("manifests[%d]: expected object, got %T", i, item)
		}

		ref, _ := m["ref"].(string)
		if ref == "" {
			return nil, fmt.Errorf("manifests[%d]: required field \"ref\" is missing or empty", i)
		}

		platform, _ := m["platform"].(string)
		entries = append(entries, manifestEntry{ref: ref, platform: platform})
	}

	return entries, nil
}

// resolveEntryPlatform determines the platform for an index entry. If an explicit
// platform string is provided, it is parsed and used. Otherwise, the platform is
// read from the image's config file.
func resolveEntryPlatform(entry manifestEntry, img v1.Image) (*v1.Platform, error) {
	if entry.platform != "" {
		p, err := parsePlatform(entry.platform)
		if err != nil {
			return nil, fmt.Errorf("manifest %q: %w", entry.ref, err)
		}
		return p, nil
	}

	cfgFile, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("manifest %q: reading config to detect platform: %w", entry.ref, err)
	}

	if cfgFile.OS == "" || cfgFile.Architecture == "" {
		return nil, fmt.Errorf("manifest %q: platform not specified and image config lacks os/architecture", entry.ref)
	}

	return &v1.Platform{
		OS:           cfgFile.OS,
		Architecture: cfgFile.Architecture,
		Variant:      cfgFile.Variant,
	}, nil
}

// countManifests returns the number of manifest entries in the input.
func countManifests(input map[string]any) int {
	raw, ok := input["manifests"]
	if !ok {
		return 0
	}
	items, ok := raw.([]any)
	if !ok {
		return 0
	}
	return len(items)
}

// getStringSlice extracts a []string from input, handling []any from JSON deserialization.
func getStringSlice(input map[string]any, field string) ([]string, error) {
	raw, ok := input[field]
	if !ok {
		return nil, fmt.Errorf("required field %q is missing", field)
	}

	switch v := raw.(type) {
	case []string:
		return v, nil
	case []any:
		result := make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("field %q[%d]: expected string, got %T", field, i, item)
			}
			result = append(result, s)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("field %q: expected array, got %T", field, raw)
	}
}

// layerFromPath creates a layer from a file path (tar.gz or directory).
// For directories, the tar is built lazily via an opener to avoid goroutine leaks.
func layerFromPath(path string) (v1.Layer, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving path %q: %w", path, err)
	}

	fi, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", absPath, err)
	}

	if fi.IsDir() {
		opener := func() (io.ReadCloser, error) {
			pr, pw := io.Pipe()
			go func() {
				pw.CloseWithError(writeTarDir(pw, absPath))
			}()
			return pr, nil
		}
		return tarball.LayerFromOpener(opener)
	}

	// Assume tarball file.
	return tarball.LayerFromFile(absPath)
}

// mergeConvenienceConfig builds a config map from top-level convenience inputs
// merged with the nested "config" map. Convenience inputs take precedence.
func mergeConvenienceConfig(input map[string]any) map[string]any {
	cfg, _ := input["config"].(map[string]any)

	// Collect convenience inputs.
	convenience := map[string]any{}
	for _, key := range []string{"entrypoint", "cmd", "user", "workdir", "env", "labels", "exposed_ports", "set_platform"} {
		if v, exists := input[key]; exists {
			convenience[key] = v
		}
	}

	if cfg == nil && len(convenience) == 0 {
		return nil
	}

	merged := make(map[string]any)
	for k, v := range cfg {
		merged[k] = v
	}
	// Convenience inputs override config fields.
	for k, v := range convenience {
		merged[k] = v
	}
	return merged
}

// applyConfigMutations applies config map values to a config file.
func applyConfigMutations(cfgFile *v1.ConfigFile, cfg map[string]any) error {
	if v, exists := cfg["env"]; exists {
		env, err := coerceEnv(v)
		if err != nil {
			return fmt.Errorf("field \"env\": %w", err)
		}
		cfgFile.Config.Env = env
	}
	if v, exists := cfg["entrypoint"]; exists {
		entrypoint, ok := coerceStringOrSlice(v)
		if !ok {
			return fmt.Errorf("field \"entrypoint\": expected string or array of strings")
		}
		cfgFile.Config.Entrypoint = entrypoint
	}
	if v, exists := cfg["cmd"]; exists {
		cmd, ok := coerceStringOrSlice(v)
		if !ok {
			return fmt.Errorf("field \"cmd\": expected string or array of strings")
		}
		cfgFile.Config.Cmd = cmd
	}
	if user, ok := cfg["user"].(string); ok && user != "" {
		cfgFile.Config.User = user
	}
	if workdir, ok := cfg["workdir"].(string); ok && workdir != "" {
		cfgFile.Config.WorkingDir = workdir
	}
	if labels, ok := cfg["labels"].(map[string]any); ok {
		if cfgFile.Config.Labels == nil {
			cfgFile.Config.Labels = make(map[string]string)
		}
		for k, v := range labels {
			cfgFile.Config.Labels[k] = fmt.Sprintf("%v", v)
		}
	}
	if v, exists := cfg["exposed_ports"]; exists {
		ports, ok := getStringSliceFromAny(v)
		if !ok {
			return fmt.Errorf("field \"exposed_ports\": expected array of strings")
		}
		if cfgFile.Config.ExposedPorts == nil {
			cfgFile.Config.ExposedPorts = make(map[string]struct{})
		}
		for _, port := range ports {
			cfgFile.Config.ExposedPorts[port] = struct{}{}
		}
	}
	if v, exists := cfg["set_platform"]; exists {
		platStr, ok := v.(string)
		if !ok {
			return fmt.Errorf("field \"set_platform\": expected string")
		}
		plat, err := parsePlatform(platStr)
		if err != nil {
			return fmt.Errorf("field \"set_platform\": %w", err)
		}
		cfgFile.OS = plat.OS
		cfgFile.Architecture = plat.Architecture
		cfgFile.Variant = plat.Variant
	}
	return nil
}

// getStringSliceFromAny converts an any value to []string if possible.
func getStringSliceFromAny(v any) ([]string, bool) {
	if v == nil {
		return nil, false
	}
	switch s := v.(type) {
	case []string:
		return s, true
	case []any:
		result := make([]string, 0, len(s))
		for _, item := range s {
			str, ok := item.(string)
			if !ok {
				return nil, false
			}
			result = append(result, str)
		}
		return result, true
	default:
		return nil, false
	}
}

// coerceStringOrSlice normalizes a value to []string, accepting a single string
// or an array of strings.
func coerceStringOrSlice(v any) ([]string, bool) {
	if s, ok := v.(string); ok {
		return []string{s}, true
	}
	return getStringSliceFromAny(v)
}

// coerceEnv normalizes env values to []string{"KEY=VALUE", ...}.
// Accepts []string, []any (of strings), or map[string]any.
func coerceEnv(v any) ([]string, error) {
	if m, ok := v.(map[string]any); ok {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		result := make([]string, 0, len(m))
		for _, k := range keys {
			result = append(result, fmt.Sprintf("%s=%v", k, m[k]))
		}
		return result, nil
	}
	env, ok := getStringSliceFromAny(v)
	if !ok {
		return nil, fmt.Errorf("expected array of strings or map of key-value pairs")
	}
	return env, nil
}

// countLayers returns the number of layers in the input, handling both []any and []string.
func countLayers(input map[string]any) int {
	raw, ok := input["layers"]
	if !ok {
		return 0
	}
	switch v := raw.(type) {
	case []any:
		return len(v)
	case []string:
		return len(v)
	default:
		return 0
	}
}

// validateImage runs integrity checks on an image.
func validateImage(img v1.Image) error {
	return validate.Image(img)
}

// stringOrArraySchema returns a JSON Schema that accepts either a string or an array of strings.
func stringOrArraySchema(description string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Description: description,
		OneOf: []*jsonschema.Schema{
			sdkhelper.StringProp("single value"),
			sdkhelper.ArrayProp("list of values", sdkhelper.WithItems(sdkhelper.StringProp("element"))),
		},
	}
}

// stringArrayOrMapSchema returns a JSON Schema that accepts either an array of strings or an object map.
func stringArrayOrMapSchema(description string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Description: description,
		OneOf: []*jsonschema.Schema{
			sdkhelper.ArrayProp("array of VAR=value strings", sdkhelper.WithItems(sdkhelper.StringProp("VAR=value"))),
			sdkhelper.ObjectProp("map of key-value pairs", nil, nil),
		},
	}
}

func buildInputSchema() *jsonschema.Schema {
	return sdkhelper.ObjectSchema(
		[]string{"operation"},
		map[string]*jsonschema.Schema{
			"operation": sdkhelper.StringProp(
				"The operation to perform",
				sdkhelper.WithEnum(
					OpDigest, OpManifest, OpConfig, OpLs, OpCatalog,
					OpPull, OpPush, OpCopy, OpAppend, OpMutate, OpTag, OpIndex,
					OpValidate, OpBlob, OpExport, OpFlatten, OpRebase, OpDelete,
				),
			),
			"ref": sdkhelper.StringProp(
				"Image reference (e.g., ghcr.io/org/repo:tag). Use \"scratch\" for an empty base image in append/mutate operations",
				sdkhelper.WithExample("ghcr.io/myorg/myapp:latest"),
			),
			"src": sdkhelper.StringProp(
				"Source image reference for copy operations",
				sdkhelper.WithExample("ghcr.io/myorg/myapp:v1"),
			),
			"dst": sdkhelper.StringProp(
				"Destination image reference for copy and mutate operations",
				sdkhelper.WithExample("ecr.io/myorg/myapp:v1"),
			),
			"repository": sdkhelper.StringProp(
				"Repository path for ls operation (e.g., ghcr.io/org/repo)",
				sdkhelper.WithExample("ghcr.io/myorg/myapp"),
			),
			"registry": sdkhelper.StringProp(
				"Registry hostname for catalog operation",
				sdkhelper.WithExample("ghcr.io"),
			),
			"path": sdkhelper.StringProp(
				"Local file path for pull/push operations (Docker-style image tarball)",
				sdkhelper.WithExample("./image.tar"),
			),
			"platform": sdkhelper.StringProp(
				"Target platform (os/arch) for pull, append, and mutate operations on multi-arch images",
				sdkhelper.WithExample("linux/amd64"),
			),
			"layers": sdkhelper.ArrayProp(
				"Layer paths to append (files or directories); supported in append and mutate operations",
				sdkhelper.WithItems(sdkhelper.StringProp("Layer file path")),
			),
			"manifests": sdkhelper.ArrayProp(
				"List of per-platform images for the index operation",
				sdkhelper.WithItems(sdkhelper.ObjectProp(
					"A manifest entry",
					[]string{"ref"},
					map[string]*jsonschema.Schema{
						"ref":      sdkhelper.StringProp("Image reference to include in the index"),
						"platform": sdkhelper.StringProp("Platform in os/arch[/variant] format (auto-detected from image if omitted)"),
					},
				)),
			),
			"config": sdkhelper.ObjectProp(
				"Image configuration mutations (alternative to convenience inputs)",
				nil,
				map[string]*jsonschema.Schema{
					"env":        sdkhelper.ArrayProp("Environment variables", sdkhelper.WithItems(sdkhelper.StringProp("VAR=value"))),
					"labels":     sdkhelper.ObjectProp("Image labels as key-value map", nil, nil),
					"entrypoint": sdkhelper.ArrayProp("Image entrypoint", sdkhelper.WithItems(sdkhelper.StringProp("entrypoint element"))),
					"cmd":        sdkhelper.ArrayProp("Image command", sdkhelper.WithItems(sdkhelper.StringProp("cmd element"))),
					"user":       sdkhelper.StringProp("User to run as"),
					"workdir":    sdkhelper.StringProp("Working directory"),
				},
			),
			"entrypoint": stringOrArraySchema(
				"Container entrypoint (convenience; overrides config.entrypoint). Accepts a string or array of strings",
			),
			"cmd": stringOrArraySchema(
				"Container command (convenience; overrides config.cmd). Accepts a string or array of strings",
			),
			"user": sdkhelper.StringProp(
				"User to run as (convenience; overrides config.user)",
			),
			"workdir": sdkhelper.StringProp(
				"Working directory (convenience; overrides config.workdir)",
			),
			"env": stringArrayOrMapSchema(
				"Environment variables (convenience; overrides config.env). Accepts array of VAR=value strings or a map of key-value pairs",
			),
			"labels": sdkhelper.ObjectProp(
				"Image labels (convenience; overrides config.labels)",
				nil, nil,
			),
			"annotations": sdkhelper.ObjectProp(
				"OCI annotations (manifest-level metadata, distinct from Docker labels). Used with mutate operation",
				nil, nil,
			),
			"tag": sdkhelper.StringProp(
				"New tag to apply to an image (tag operation)",
				sdkhelper.WithExample("latest"),
			),
			"output": sdkhelper.StringProp(
				"Write result to local tarball instead of pushing (mutate operation)",
				sdkhelper.WithExample("./mutated.tar"),
			),
			"exposed_ports": sdkhelper.ArrayProp(
				"Expose container ports (mutate operation, e.g., [\"8080/tcp\", \"443/tcp\"])",
				sdkhelper.WithItems(sdkhelper.StringProp("port/protocol")),
			),
			"set_platform": sdkhelper.StringProp(
				"Set platform os/arch[/variant] on image config (mutate operation)",
				sdkhelper.WithExample("linux/amd64"),
			),
			"old_base": sdkhelper.StringProp(
				"Old base image reference (rebase operation)",
			),
			"new_base": sdkhelper.StringProp(
				"New base image reference (rebase operation)",
			),
			"digest": sdkhelper.StringProp(
				"Blob digest to read (blob operation)",
				sdkhelper.WithExample("sha256:abc123..."),
			),
		},
	)
}

func buildOutputSchemas() map[sdkprovider.Capability]*jsonschema.Schema {
	return map[sdkprovider.Capability]*jsonschema.Schema{
		sdkprovider.CapabilityAction: sdkhelper.ObjectSchema(nil, map[string]*jsonschema.Schema{
			"success":      sdkhelper.BoolProp("Whether the operation succeeded"),
			"digest":       sdkhelper.StringProp("Image digest (sha256:...)"),
			"size":         sdkhelper.IntProp("Image size in bytes"),
			"mediaType":    sdkhelper.StringProp("Manifest media type"),
			"manifest":     sdkhelper.StringProp("Raw manifest JSON (manifest operation)"),
			"tags":         sdkhelper.ArrayProp("List of tags (ls operation)", sdkhelper.WithItems(sdkhelper.StringProp("tag"))),
			"repository":   sdkhelper.StringProp("Repository name"),
			"registry":     sdkhelper.StringProp("Registry hostname"),
			"repositories": sdkhelper.ArrayProp("List of repositories (catalog operation)", sdkhelper.WithItems(sdkhelper.StringProp("repository"))),
			"ref":          sdkhelper.StringProp("Image reference"),
			"src":          sdkhelper.StringProp("Source image reference (copy operation)"),
			"dst":          sdkhelper.StringProp("Destination image reference (copy operation)"),
			"path":         sdkhelper.StringProp("Local file path (pull operation)"),
			"config":       sdkhelper.StringProp("Raw image config JSON (config operation)"),
			"tag":          sdkhelper.StringProp("Applied tag reference (tag operation)"),
			"layerCount":   sdkhelper.IntProp("Number of layers (validate operation)"),
			"error":        sdkhelper.StringProp("Validation error message (validate operation)"),
		}),
		sdkprovider.CapabilityFrom: sdkhelper.ObjectSchema(nil, map[string]*jsonschema.Schema{
			"success":      sdkhelper.BoolProp("Whether the operation succeeded"),
			"digest":       sdkhelper.StringProp("Image digest (sha256:...)"),
			"size":         sdkhelper.IntProp("Image size in bytes"),
			"mediaType":    sdkhelper.StringProp("Manifest media type"),
			"manifest":     sdkhelper.StringProp("Raw manifest JSON (manifest operation)"),
			"tags":         sdkhelper.ArrayProp("List of tags (ls operation)", sdkhelper.WithItems(sdkhelper.StringProp("tag"))),
			"repository":   sdkhelper.StringProp("Repository name"),
			"registry":     sdkhelper.StringProp("Registry hostname"),
			"repositories": sdkhelper.ArrayProp("List of repositories (catalog operation)", sdkhelper.WithItems(sdkhelper.StringProp("repository"))),
			"ref":          sdkhelper.StringProp("Image reference"),
			"path":         sdkhelper.StringProp("Local file path (pull operation)"),
			"config":       sdkhelper.StringProp("Raw image config JSON (config operation)"),
		}),
	}
}

// ConfigureProvider stores host-side configuration.
//
//nolint:revive // ctx required by interface
func (p *Plugin) ConfigureProvider(_ context.Context, providerName string, cfg sdkplugin.ProviderConfig) error {
	if providerName != ProviderName {
		return fmt.Errorf("unknown provider: %s", providerName)
	}

	if cfg.Settings == nil {
		return nil
	}

	p.registry = getSettingString(cfg.Settings, "registry")
	p.username = getSettingString(cfg.Settings, "username")
	p.password = getSettingString(cfg.Settings, "password")
	p.authHandler = getSettingString(cfg.Settings, "auth_handler")
	p.scope = getSettingString(cfg.Settings, "scope")
	p.insecure = getSettingBool(cfg.Settings, "insecure")

	return nil
}

// ExecuteProviderStream is not supported.
//
//nolint:revive // all params required by interface
func (p *Plugin) ExecuteProviderStream(_ context.Context, providerName string, _ map[string]any, _ func(sdkplugin.StreamChunk)) error {
	if providerName != ProviderName {
		return fmt.Errorf("unknown provider: %s", providerName)
	}
	return sdkplugin.ErrStreamingNotSupported
}

// ExtractDependencies returns resolver keys this input depends on.
//
//nolint:revive // all params required by interface
func (p *Plugin) ExtractDependencies(_ context.Context, providerName string, _ map[string]any) ([]string, error) {
	if providerName != ProviderName {
		return nil, fmt.Errorf("unknown provider: %s", providerName)
	}
	return nil, nil
}

// StopProvider performs cleanup for the named provider.
//
//nolint:revive // all params required by interface
func (p *Plugin) StopProvider(_ context.Context, providerName string) error {
	if providerName != ProviderName {
		return fmt.Errorf("unknown provider: %s", providerName)
	}
	return nil
}
