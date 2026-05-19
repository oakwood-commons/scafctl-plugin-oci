// Package oci implements the OCI image provider plugin.
package oci

import (
	"context"
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
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
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
	OpIndex    = "index"
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
	case OpAppend:
		return p.executeAppend(ctx, input)
	case OpMutate:
		return p.executeMutate(ctx, input)
	case OpIndex:
		return p.executeIndex(ctx, input)
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

	imgRef, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("parsing reference %q: %w", ref, err)
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
func resolveDestination(ref string, input map[string]any) (string, name.Reference, error) {
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

	dstRef, err := name.ParseReference(dstStr)
	if err != nil {
		return "", nil, fmt.Errorf("parsing destination reference %q: %w", dstStr, err)
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

	imgRef, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("parsing reference %q: %w", ref, err)
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

	imgRef, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("parsing reference %q: %w", ref, err)
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

	imgRef, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("parsing reference %q: %w", ref, err)
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

	imgRef, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("parsing reference %q: %w", ref, err)
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

	srcRef, err := name.ParseReference(src)
	if err != nil {
		return nil, fmt.Errorf("parsing source reference %q: %w", src, err)
	}

	dstRef, err := name.ParseReference(dst)
	if err != nil {
		return nil, fmt.Errorf("parsing destination reference %q: %w", dst, err)
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

	dstStr, dstRef, err := resolveDestination(ref, input)
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

	if len(cfg) == 0 && len(layerPaths) == 0 {
		return nil, fmt.Errorf("mutate requires at least one of: config fields, convenience inputs (entrypoint, cmd, user, workdir, env, labels), or layers")
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

	// Determine destination: dst overrides ref.
	dstStr, dstRef, err := resolveDestination(ref, input)
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

// executeDelete deletes an image from a registry.
func (p *Plugin) executeDelete(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	imgRef, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("parsing reference %q: %w", ref, err)
	}

	if err := remote.Delete(imgRef, p.remoteOptions(ctx)...); err != nil {
		return nil, fmt.Errorf("deleting %q: %w", ref, err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success": true,
			"ref":     ref,
		},
	}, nil
}

// executeIndex creates a multi-arch OCI image index from a list of per-platform manifests.
func (p *Plugin) executeIndex(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	idxRef, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("parsing reference %q: %w", ref, err)
	}

	manifests, err := getManifestEntries(input)
	if err != nil {
		return nil, err
	}

	idx := v1.ImageIndex(empty.Index)

	for _, entry := range manifests {
		entryRef, err := name.ParseReference(entry.ref)
		if err != nil {
			return nil, fmt.Errorf("parsing manifest reference %q: %w", entry.ref, err)
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
	for _, key := range []string{"entrypoint", "cmd", "user", "workdir", "env", "labels"} {
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
					OpDigest, OpManifest, OpLs, OpCatalog,
					OpPull, OpPush, OpCopy, OpAppend, OpMutate, OpIndex, OpDelete,
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
