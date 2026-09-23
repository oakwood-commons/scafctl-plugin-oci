package oci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPushArtifact is the acceptance round-trip for push-artifact:
//
//   - AC#1: a manifest with a custom artifactType, config media type,
//     per-layer media types, and annotations lands in the registry and the
//     manifest digest is returned.
//   - AC#2: layer blobs equal the input files byte-for-byte and the
//     descriptor digests equal the files' own sha256 (no tar/gzip).
//   - AC#3: the whole push is in-process. This test talks to an in-memory
//     httptest registry with no Docker daemon and no oras binary — the
//     daemonless guarantee is inherent in the test's own operation.
func TestPushArtifact(t *testing.T) {
	srv, p := setupRegistry(t)
	ctx := context.Background()

	dir := t.TempDir()
	yamlBytes := []byte("key: value\n")
	htmlBytes := []byte("<h1>hello</h1>")
	yamlPath := filepath.Join(dir, "config.yaml")
	htmlPath := filepath.Join(dir, "index.html")
	require.NoError(t, os.WriteFile(yamlPath, yamlBytes, 0o600))
	require.NoError(t, os.WriteFile(htmlPath, htmlBytes, 0o600))
	configInline := `{"kind":"thing"}`

	ref := fmt.Sprintf("%s/myorg/artifact:v1", srv.Listener.Addr().String())
	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation":         OpPushArtifact,
		"ref":               ref,
		"artifact_type":     "application/vnd.example.thing.v1",
		"config_media_type": "application/vnd.example.config.v1+json",
		"config_inline":     configInline,
		"annotations":       map[string]any{"org.opencontainers.image.created": "2026-09-23"},
		"artifact_layers": []any{
			map[string]any{
				"path":        yamlPath,
				"media_type":  "application/vnd.example.payload.v1+yaml",
				"annotations": map[string]any{"org.opencontainers.image.title": "config.yaml"},
			},
			map[string]any{
				"path":       htmlPath,
				"media_type": "text/html",
			},
		},
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.Equal(t, ref, data["ref"])
	assert.Equal(t, "application/vnd.example.thing.v1", data["artifactType"])
	assert.Equal(t, "application/vnd.oci.image.manifest.v1+json", data["mediaType"])
	assert.Contains(t, data["digest"].(string), "sha256:")
	assert.Equal(t, int64(len(configInline)+len(yamlBytes)+len(htmlBytes)), data["size"],
		"size should be config + raw layer bytes")

	// AC#1: pull the manifest back and verify the artifact fields survived.
	imgRef, err := name.ParseReference(ref)
	require.NoError(t, err)
	desc, err := remote.Get(imgRef)
	require.NoError(t, err)
	assert.Equal(t, data["digest"], desc.Digest.String(), "output digest should match the pushed manifest")

	m, err := v1.ParseManifest(bytes.NewReader(desc.Manifest))
	require.NoError(t, err)
	assert.Equal(t, int64(2), m.SchemaVersion)
	assert.Equal(t, "application/vnd.oci.image.manifest.v1+json", string(m.MediaType))
	assert.Equal(t, "application/vnd.example.thing.v1", m.ArtifactType, "manifest must carry a top-level artifactType")
	assert.Equal(t, "application/vnd.example.config.v1+json", string(m.Config.MediaType))
	assert.Equal(t, "2026-09-23", m.Annotations["org.opencontainers.image.created"])
	require.Len(t, m.Layers, 2)
	assert.Equal(t, "application/vnd.example.payload.v1+yaml", string(m.Layers[0].MediaType))
	assert.Equal(t, "config.yaml", m.Layers[0].Annotations["org.opencontainers.image.title"])
	assert.Equal(t, "text/html", string(m.Layers[1].MediaType))
	assert.Nil(t, m.Layers[1].Annotations)

	// AC#2: descriptor digests equal the files' own sha256 — no tar/gzip.
	digests, ok := data["layerDigests"].([]string)
	require.True(t, ok)
	require.Len(t, digests, 2)
	assert.Equal(t, fmt.Sprintf("sha256:%x", sha256.Sum256(yamlBytes)), digests[0])
	assert.Equal(t, digests[0], m.Layers[0].Digest.String())
	assert.Equal(t, fmt.Sprintf("sha256:%x", sha256.Sum256(htmlBytes)), digests[1])
	assert.Equal(t, digests[1], m.Layers[1].Digest.String())

	// AC#2: blobs round-trip byte-for-byte.
	assertBlobEqual(t, imgRef.Context().Digest(digests[0]), yamlBytes)
	assertBlobEqual(t, imgRef.Context().Digest(digests[1]), htmlBytes)
	assertBlobEqual(t, imgRef.Context().Digest(m.Config.Digest.String()), []byte(configInline))
}

// assertBlobEqual pulls the blob for ref and asserts its bytes equal want.
func assertBlobEqual(t *testing.T, ref name.Digest, want []byte) {
	t.Helper()
	l, err := remote.Layer(ref)
	require.NoError(t, err)
	rc, err := l.Compressed()
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, want, got, "blob should round-trip byte-for-byte")
}

// TestPushArtifact_DefaultConfig verifies that omitting all config fields
// pushes the OCI 1.1 empty config sentinel: a {} blob with the empty config
// media type (whose digest is the well-known sha256 of "{}").
func TestPushArtifact_DefaultConfig(t *testing.T) {
	srv, p := setupRegistry(t)
	ctx := context.Background()

	layerPath := filepath.Join(t.TempDir(), "payload.bin")
	payload := []byte("raw bytes")
	require.NoError(t, os.WriteFile(layerPath, payload, 0o600))

	ref := fmt.Sprintf("%s/myorg/empty-config:v1", srv.Listener.Addr().String())
	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation":     OpPushArtifact,
		"ref":           ref,
		"artifact_type": "application/vnd.example.thing.v1",
		"artifact_layers": []any{
			map[string]any{"path": layerPath, "media_type": "application/octet-stream"},
		},
	})
	require.NoError(t, err)
	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])

	imgRef, err := name.ParseReference(ref)
	require.NoError(t, err)
	desc, err := remote.Get(imgRef)
	require.NoError(t, err)
	m, err := v1.ParseManifest(bytes.NewReader(desc.Manifest))
	require.NoError(t, err)

	assert.Equal(t, string(ociEmptyConfigMediaType), string(m.Config.MediaType))
	assert.Equal(t, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("{}"))), m.Config.Digest.String(),
		"empty config digest should be the well-known sha256({})")
	assertBlobEqual(t, imgRef.Context().Digest(m.Config.Digest.String()), []byte("{}"))
}

// TestPushArtifact_ConfigPath verifies config_path loads the blob from a file.
func TestPushArtifact_ConfigPath(t *testing.T) {
	srv, p := setupRegistry(t)
	ctx := context.Background()

	dir := t.TempDir()
	configBytes := []byte(`{"from":"file"}`)
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, configBytes, 0o600))
	layerPath := filepath.Join(dir, "payload.bin")
	require.NoError(t, os.WriteFile(layerPath, []byte("payload"), 0o600))

	ref := fmt.Sprintf("%s/myorg/config-file:v1", srv.Listener.Addr().String())
	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation":         OpPushArtifact,
		"ref":               ref,
		"artifact_type":     "application/vnd.example.thing.v1",
		"config_media_type": "application/vnd.example.config.v1+json",
		"config_path":       configPath,
		"artifact_layers": []any{
			map[string]any{"path": layerPath, "media_type": "application/octet-stream"},
		},
	})
	require.NoError(t, err)

	imgRef, err := name.ParseReference(ref)
	require.NoError(t, err)
	desc, err := remote.Get(imgRef)
	require.NoError(t, err)
	m, err := v1.ParseManifest(bytes.NewReader(desc.Manifest))
	require.NoError(t, err)
	assert.Equal(t, "application/vnd.example.config.v1+json", string(m.Config.MediaType))
	assert.Equal(t, fmt.Sprintf("sha256:%x", sha256.Sum256(configBytes)), m.Config.Digest.String())
	assertBlobEqual(t, imgRef.Context().Digest(m.Config.Digest.String()), configBytes)
}

// TestPushArtifact_Errors covers input validation.
func TestPushArtifact_Errors(t *testing.T) {
	dir := t.TempDir()
	layerPath := filepath.Join(dir, "payload.bin")
	require.NoError(t, os.WriteFile(layerPath, []byte("payload"), 0o600))
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0o600))

	tests := []struct {
		name        string
		input       map[string]any
		errContains string
	}{
		{
			name:        "missing ref",
			input:       map[string]any{"operation": OpPushArtifact, "artifact_type": "application/vnd.example.thing.v1"},
			errContains: `"ref"`,
		},
		{
			name:        "missing artifact_type",
			input:       map[string]any{"operation": OpPushArtifact, "ref": "localhost/a:b"},
			errContains: `"artifact_type"`,
		},
		{
			name: "invalid ref",
			input: map[string]any{
				"operation": OpPushArtifact, "ref": "localhost/a:b+1",
				"artifact_type": "application/vnd.example.thing.v1",
			},
			errContains: "tags cannot contain '+'",
		},
		{
			name: "config_path and config_inline are mutually exclusive",
			input: map[string]any{
				"operation": OpPushArtifact, "ref": "localhost/a:b",
				"artifact_type": "application/vnd.example.thing.v1",
				"config_path":   configPath, "config_inline": "{}",
			},
			errContains: "mutually exclusive",
		},
		{
			name: "config_path unreadable",
			input: map[string]any{
				"operation": OpPushArtifact, "ref": "localhost/a:b",
				"artifact_type": "application/vnd.example.thing.v1",
				"config_path":   filepath.Join(dir, "missing.json"),
			},
			errContains: "reading config_path",
		},
		{
			name: "missing artifact_layers",
			input: map[string]any{
				"operation": OpPushArtifact, "ref": "localhost/a:b",
				"artifact_type": "application/vnd.example.thing.v1",
			},
			errContains: `"artifact_layers" is missing`,
		},
		{
			name: "artifact_layers not an array",
			input: map[string]any{
				"operation": OpPushArtifact, "ref": "localhost/a:b",
				"artifact_type":   "application/vnd.example.thing.v1",
				"artifact_layers": "not-an-array",
			},
			errContains: "expected array",
		},
		{
			name: "artifact_layers empty",
			input: map[string]any{
				"operation": OpPushArtifact, "ref": "localhost/a:b",
				"artifact_type":   "application/vnd.example.thing.v1",
				"artifact_layers": []any{},
			},
			errContains: "at least one layer",
		},
		{
			name: "layer entry not an object",
			input: map[string]any{
				"operation": OpPushArtifact, "ref": "localhost/a:b",
				"artifact_type":   "application/vnd.example.thing.v1",
				"artifact_layers": []any{"a-string"},
			},
			errContains: "expected object",
		},
		{
			name: "layer missing path",
			input: map[string]any{
				"operation": OpPushArtifact, "ref": "localhost/a:b",
				"artifact_type":   "application/vnd.example.thing.v1",
				"artifact_layers": []any{map[string]any{"media_type": "application/octet-stream"}},
			},
			errContains: `"path" is missing or empty`,
		},
		{
			name: "layer missing media_type",
			input: map[string]any{
				"operation": OpPushArtifact, "ref": "localhost/a:b",
				"artifact_type":   "application/vnd.example.thing.v1",
				"artifact_layers": []any{map[string]any{"path": layerPath}},
			},
			errContains: `"media_type" is missing or empty`,
		},
		{
			name: "layer file missing",
			input: map[string]any{
				"operation": OpPushArtifact, "ref": "localhost/a:b",
				"artifact_type": "application/vnd.example.thing.v1",
				"artifact_layers": []any{map[string]any{
					"path": filepath.Join(dir, "missing.bin"), "media_type": "application/octet-stream",
				}},
			},
			errContains: "no such file",
		},
		{
			name: "layer annotations wrong type",
			input: map[string]any{
				"operation": OpPushArtifact, "ref": "localhost/a:b",
				"artifact_type": "application/vnd.example.thing.v1",
				"artifact_layers": []any{map[string]any{
					"path": layerPath, "media_type": "application/octet-stream", "annotations": 42,
				}},
			},
			errContains: "expected object or comma-separated key=value string",
		},
		{
			name: "manifest annotations wrong type",
			input: map[string]any{
				"operation": OpPushArtifact, "ref": "localhost/a:b",
				"artifact_type": "application/vnd.example.thing.v1",
				"annotations":   42,
				"artifact_layers": []any{map[string]any{
					"path": layerPath, "media_type": "application/octet-stream",
				}},
			},
			errContains: `field "annotations"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (&Plugin{}).ExecuteProvider(context.Background(), ProviderName, tt.input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

// TestPushArtifact_LayerDigestNoRegistry locks AC#2 without any registry:
// a static layer built from artifact_layers digests to the file's sha256.
func TestPushArtifact_LayerDigestNoRegistry(t *testing.T) {
	content := []byte("identical bytes, not a tar, not gzip")
	tmp := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(tmp, content, 0o600))

	specs, err := parseArtifactLayers(map[string]any{
		"artifact_layers": []any{map[string]any{"path": tmp, "media_type": "application/vnd.test.raw"}},
	})
	require.NoError(t, err)
	require.Len(t, specs, 1)

	digest, err := specs[0].layer.Digest()
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("sha256:%x", sha256.Sum256(content)), digest.String())

	rc, err := specs[0].layer.Compressed()
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, content, got, "compressed layer content must be the raw file bytes")
}

func TestWhatIf_PushArtifact(t *testing.T) {
	p := &Plugin{}
	desc, err := p.DescribeWhatIf(context.Background(), ProviderName, map[string]any{
		"operation":       OpPushArtifact,
		"ref":             "ghcr.io/myorg/artifact:v1",
		"artifact_type":   "application/vnd.example.thing.v1",
		"artifact_layers": []any{map[string]any{"path": "a.yaml", "media_type": "text/yaml"}},
	})
	require.NoError(t, err)
	assert.Contains(t, desc, "artifact")
	assert.Contains(t, desc, "ghcr.io/myorg/artifact:v1")
	assert.Contains(t, desc, "1 raw layer")
}

func BenchmarkPushArtifact(b *testing.B) {
	reg := registry.New()
	srv := httptest.NewServer(reg)
	b.Cleanup(srv.Close)

	dir := b.TempDir()
	payload := bytes.Repeat([]byte("benchmark payload\n"), 64)
	layerPath := filepath.Join(dir, "payload.bin")
	if err := os.WriteFile(layerPath, payload, 0o600); err != nil {
		b.Fatal(err)
	}

	p := &Plugin{}
	ctx := context.Background()
	addr := srv.Listener.Addr().String()

	b.ReportAllocs()
	b.ResetTimer()

	i := 0
	for b.Loop() {
		input := map[string]any{
			"operation":     OpPushArtifact,
			"ref":           fmt.Sprintf("%s/bench/artifact:v%d", addr, i),
			"artifact_type": "application/vnd.example.bench.v1",
			"artifact_layers": []any{map[string]any{
				"path": layerPath, "media_type": "application/octet-stream",
			}},
		}
		i++
		if _, err := p.ExecuteProvider(ctx, ProviderName, input); err != nil {
			b.Fatal(err)
		}
	}
}
