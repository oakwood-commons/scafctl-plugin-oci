package oci

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	sdkprovider "github.com/oakwood-commons/scafctl-plugin-sdk/provider"
)

// ociEmptyConfigMediaType is the OCI 1.1 empty config media type. It is not
// defined by the pinned go-containerregistry, so it lives here.
const ociEmptyConfigMediaType = types.MediaType("application/vnd.oci.empty.v1+json")

// artifactImage serves a hand-built OCI 1.1 artifact manifest to
// partial.CompressedToImage, which extends it into a full v1.Image.
//
// remote.Write consumes exactly the four methods below: blob uploads flow
// from RawConfigFile() (config blob) and Layers() (walks the manifest layer
// digests through LayerByDigest), and the manifest PUT sends RawManifest()
// verbatim — preserving the top-level artifactType that the mutate pipeline
// cannot produce.
type artifactImage struct {
	manifest v1.Manifest
	config   []byte
	layers   map[v1.Hash]v1.Layer
}

// MediaType implements partial.ImageCore.
func (a *artifactImage) MediaType() (types.MediaType, error) {
	return a.manifest.MediaType, nil
}

// RawConfigFile implements partial.ImageCore.
func (a *artifactImage) RawConfigFile() ([]byte, error) {
	return a.config, nil
}

// RawManifest implements partial.CompressedImageCore.
func (a *artifactImage) RawManifest() ([]byte, error) {
	return json.Marshal(a.manifest)
}

// LayerByDigest implements partial.CompressedImageCore.
func (a *artifactImage) LayerByDigest(h v1.Hash) (partial.CompressedLayer, error) {
	l, ok := a.layers[h]
	if !ok {
		return nil, fmt.Errorf("layer %s not found in artifact", h)
	}
	return l, nil
}

// artifactLayerSpec is one parsed artifact_layers entry: the raw file layer
// plus its descriptor annotations.
type artifactLayerSpec struct {
	path        string
	layer       v1.Layer
	annotations map[string]string
}

// executePushArtifact assembles an OCI 1.1 artifact entirely in memory — a
// custom top-level artifactType, a config blob with a caller-chosen media
// type, and raw-file layers each with their own descriptor media type and
// optional annotations — and pushes it via remote.Write. Layer blobs are the
// input files verbatim (no tar, no gzip), so each descriptor digest equals
// the file's own sha256.
func (p *Plugin) executePushArtifact(ctx context.Context, input map[string]any) (*sdkprovider.Output, error) {
	ref, err := requireString(input, "ref")
	if err != nil {
		return nil, err
	}

	artifactType, err := requireString(input, "artifact_type")
	if err != nil {
		return nil, err
	}

	imgRef, err := p.parseReference(ref)
	if err != nil {
		return nil, err
	}

	configMediaType, configBytes, err := resolveArtifactConfig(input)
	if err != nil {
		return nil, err
	}

	layerSpecs, err := parseArtifactLayers(input)
	if err != nil {
		return nil, err
	}

	configDesc, err := partial.Descriptor(static.NewLayer(configBytes, configMediaType))
	if err != nil {
		return nil, fmt.Errorf("describing config blob: %w", err)
	}

	layerMap := make(map[v1.Hash]v1.Layer, len(layerSpecs))
	layerDescs := make([]v1.Descriptor, 0, len(layerSpecs))
	layerDigests := make([]string, 0, len(layerSpecs))
	for _, spec := range layerSpecs {
		desc, err := partial.Descriptor(spec.layer)
		if err != nil {
			return nil, fmt.Errorf("describing layer %q: %w", spec.path, err)
		}
		desc.Annotations = spec.annotations
		layerDescs = append(layerDescs, *desc)
		layerMap[desc.Digest] = spec.layer
		layerDigests = append(layerDigests, desc.Digest.String())
	}

	manifest := v1.Manifest{
		SchemaVersion: 2,
		MediaType:     types.OCIManifestSchema1,
		ArtifactType:  artifactType,
		Config:        *configDesc,
		Layers:        layerDescs,
	}
	if rawAnns, has := input["annotations"]; has {
		anns, err := coerceStringMap(rawAnns)
		if err != nil {
			return nil, fmt.Errorf("field \"annotations\": %w", err)
		}
		manifest.Annotations = anns
	}

	img, err := partial.CompressedToImage(&artifactImage{
		manifest: manifest,
		config:   configBytes,
		layers:   layerMap,
	})
	if err != nil {
		return nil, fmt.Errorf("building artifact image: %w", err)
	}

	retryOpts, err := parseRetry(input)
	if err != nil {
		return nil, err
	}

	if err := remote.Write(imgRef, img, p.remoteOptions(ctx, retryOpts...)...); err != nil {
		return nil, mapRegistryError(fmt.Sprintf("push-artifact: writing artifact to %q", ref), err)
	}

	digest, err := img.Digest()
	if err != nil {
		return nil, fmt.Errorf("getting digest: %w", err)
	}

	totalSize, err := totalImageSize(img)
	if err != nil {
		return nil, fmt.Errorf("getting size: %w", err)
	}

	return &sdkprovider.Output{
		Data: map[string]any{
			"success":      true,
			"ref":          ref,
			"digest":       digest.String(),
			"size":         totalSize,
			"mediaType":    string(types.OCIManifestSchema1),
			"artifactType": artifactType,
			"layerDigests": layerDigests,
		},
	}, nil
}

// resolveArtifactConfig determines the config blob's media type and bytes.
// config_path and config_inline are mutually exclusive; when neither is set,
// the OCI 1.1 empty config sentinel ({} with the empty config media type) is
// used. When content IS supplied, config_media_type is required — the
// empty-config media type is reserved for the empty blob and must not label
// arbitrary content.
func resolveArtifactConfig(input map[string]any) (types.MediaType, []byte, error) {
	configPath, _ := input["config_path"].(string)
	configInline, _ := input["config_inline"].(string)
	if configPath != "" && configInline != "" {
		return "", nil, fmt.Errorf("fields \"config_path\" and \"config_inline\" are mutually exclusive")
	}

	mediaType, _ := input["config_media_type"].(string)
	if mediaType == "" {
		if configPath != "" || configInline != "" {
			return "", nil, fmt.Errorf(
				"field \"config_media_type\" is required when config_path or config_inline is set")
		}
		mediaType = string(ociEmptyConfigMediaType)
	}

	switch {
	case configPath != "":
		b, err := os.ReadFile(configPath) //nolint:gosec // caller-supplied config path; reading it is the operation
		if err != nil {
			return "", nil, fmt.Errorf("reading config_path %q: %w", configPath, err)
		}
		return types.MediaType(mediaType), b, nil
	case configInline != "":
		return types.MediaType(mediaType), []byte(configInline), nil
	default:
		return types.MediaType(mediaType), []byte("{}"), nil
	}
}

// parseArtifactLayers reads the artifact_layers array. Each entry is a file
// whose raw bytes become the layer blob (no tar, no gzip) plus the
// descriptor's media type and optional annotations.
func parseArtifactLayers(input map[string]any) ([]artifactLayerSpec, error) {
	raw, ok := input["artifact_layers"]
	if !ok {
		return nil, fmt.Errorf("required field \"artifact_layers\" is missing")
	}

	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("field \"artifact_layers\": expected array, got %T", raw)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("field \"artifact_layers\": at least one layer is required")
	}

	specs := make([]artifactLayerSpec, 0, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("artifact_layers[%d]: expected object, got %T", i, item)
		}

		path, _ := m["path"].(string)
		if path == "" {
			return nil, fmt.Errorf("artifact_layers[%d]: required field \"path\" is missing or empty", i)
		}
		mediaType, _ := m["media_type"].(string)
		if mediaType == "" {
			return nil, fmt.Errorf("artifact_layers[%d]: required field \"media_type\" is missing or empty", i)
		}

		content, err := os.ReadFile(path) //nolint:gosec // caller-supplied layer path; reading it is the operation
		if err != nil {
			return nil, fmt.Errorf("artifact_layers[%d]: reading %q: %w", i, path, err)
		}

		spec := artifactLayerSpec{
			path:  path,
			layer: static.NewLayer(content, types.MediaType(mediaType)),
		}
		if rawAnns, has := m["annotations"]; has {
			anns, err := coerceStringMap(rawAnns)
			if err != nil {
				return nil, fmt.Errorf("artifact_layers[%d].annotations: %w", i, err)
			}
			spec.annotations = anns
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// countArtifactLayers returns the number of artifact_layers entries in the input.
func countArtifactLayers(input map[string]any) int {
	raw, ok := input["artifact_layers"]
	if !ok {
		return 0
	}
	items, ok := raw.([]any)
	if !ok {
		return 0
	}
	return len(items)
}
