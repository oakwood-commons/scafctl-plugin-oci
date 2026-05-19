package oci_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/oakwood-commons/scafctl-plugin-oci/internal/oci"
	sdkprovider "github.com/oakwood-commons/scafctl-plugin-sdk/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_FullWorkflow exercises the entire provider lifecycle:
//
//	push → ls → digest → manifest → copy → mutate → append → pull → delete → catalog
//
// This proves the plugin works end-to-end against a real (in-memory) OCI registry.
func TestIntegration_FullWorkflow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Start in-memory OCI registry.
	reg := registry.New()
	srv := httptest.NewServer(reg)
	t.Cleanup(srv.Close)

	p := &oci.Plugin{}
	ctx := context.Background()
	addr := srv.Listener.Addr().String()

	// --- Step 0: Create a source image and pull to tarball ---
	srcRef := fmt.Sprintf("%s/integration/source:v1", addr)
	seedImage(t, srcRef)

	tarPath := filepath.Join(t.TempDir(), "source.tar")
	out := execute(t, ctx, p, map[string]any{
		"operation": "pull",
		"ref":       srcRef,
		"path":      tarPath,
	})
	t.Logf("pull: digest=%s path=%s", field(out, "digest"), tarPath)

	// --- Step 1: Push tarball to a new tag ---
	pushRef := fmt.Sprintf("%s/integration/app:v1", addr)
	out = execute(t, ctx, p, map[string]any{
		"operation": "push",
		"ref":       pushRef,
		"path":      tarPath,
	})
	pushDigest := field(out, "digest")
	t.Logf("push: ref=%s digest=%s", pushRef, pushDigest)

	// --- Step 2: List tags ---
	out = execute(t, ctx, p, map[string]any{
		"operation":  "ls",
		"repository": fmt.Sprintf("%s/integration/app", addr),
	})
	tags := data(out)["tags"].([]string)
	assert.Contains(t, tags, "v1")
	t.Logf("ls: tags=%v", tags)

	// --- Step 3: Get digest ---
	out = execute(t, ctx, p, map[string]any{
		"operation": "digest",
		"ref":       pushRef,
	})
	assert.Equal(t, pushDigest, field(out, "digest"))
	t.Logf("digest: %s", field(out, "digest"))

	// --- Step 4: Get manifest ---
	out = execute(t, ctx, p, map[string]any{
		"operation": "manifest",
		"ref":       pushRef,
	})
	assert.Contains(t, field(out, "manifest"), "layers")
	t.Logf("manifest: mediaType=%s", field(out, "mediaType"))

	// --- Step 5: Copy to another repo ---
	copyDst := fmt.Sprintf("%s/integration/copy:v1", addr)
	out = execute(t, ctx, p, map[string]any{
		"operation": "copy",
		"src":       pushRef,
		"dst":       copyDst,
	})
	assert.Equal(t, pushDigest, field(out, "digest"))
	t.Logf("copy: %s → %s", pushRef, copyDst)

	// --- Step 6: Mutate image config ---
	mutateRef := fmt.Sprintf("%s/integration/app:v2-mutated", addr)
	execute(t, ctx, p, map[string]any{
		"operation": "copy",
		"src":       pushRef,
		"dst":       mutateRef,
	})

	out = execute(t, ctx, p, map[string]any{
		"operation": "mutate",
		"ref":       mutateRef,
		"config": map[string]any{
			"env":        []any{"APP_ENV=production", "VERSION=2.0"},
			"user":       "appuser",
			"workdir":    "/opt/app",
			"labels":     map[string]any{"maintainer": "test@example.com"},
			"entrypoint": []any{"/bin/app"},
			"cmd":        []any{"serve", "--port=8080"},
		},
	})
	mutateDigest := field(out, "digest")
	assert.NotEqual(t, pushDigest, mutateDigest, "mutated image should have different digest")
	t.Logf("mutate: new digest=%s", mutateDigest)

	// --- Step 7: Append a layer (directory) ---
	appendRef := fmt.Sprintf("%s/integration/app:v3-appended", addr)
	execute(t, ctx, p, map[string]any{
		"operation": "copy",
		"src":       pushRef,
		"dst":       appendRef,
	})

	layerDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(layerDir, "config.yaml"), []byte("key: value\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(layerDir, "static"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(layerDir, "static", "index.html"), []byte("<h1>hello</h1>"), 0o600))

	out = execute(t, ctx, p, map[string]any{
		"operation": "append",
		"ref":       appendRef,
		"layers":    []any{layerDir},
	})
	appendDigest := field(out, "digest")
	assert.NotEqual(t, pushDigest, appendDigest, "appended image should have different digest")
	t.Logf("append: new digest=%s", appendDigest)

	// --- Step 8: Pull the mutated image to verify round-trip ---
	pullPath := filepath.Join(t.TempDir(), "mutated.tar")
	execute(t, ctx, p, map[string]any{
		"operation": "pull",
		"ref":       mutateRef,
		"path":      pullPath,
	})
	info, err := os.Stat(pullPath)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))
	t.Logf("pull: mutated image saved (%d bytes)", info.Size())

	// --- Step 9: Delete the original ---
	execute(t, ctx, p, map[string]any{
		"operation": "delete",
		"ref":       pushRef,
	})
	t.Logf("delete: removed %s", pushRef)

	// Verify deleted — digest should fail.
	_, err = p.ExecuteProvider(ctx, "oci", map[string]any{
		"operation": "digest",
		"ref":       pushRef,
	})
	assert.Error(t, err, "digest should fail after delete")
	t.Logf("verified: digest after delete returns error")

	// --- Step 10: Catalog ---
	out = execute(t, ctx, p, map[string]any{
		"operation": "catalog",
		"registry":  addr,
	})
	repos := data(out)["repositories"].([]string)
	assert.GreaterOrEqual(t, len(repos), 2)
	t.Logf("catalog: %v", repos)

	t.Log("✓ Full integration workflow completed successfully")
}

// TestIntegration_WhatIfParity verifies DescribeWhatIf covers all operations.
func TestIntegration_WhatIfParity(t *testing.T) {
	p := &oci.Plugin{}
	ctx := context.Background()

	operations := []map[string]any{
		{"operation": "digest", "ref": "ghcr.io/test/app:v1"},
		{"operation": "manifest", "ref": "ghcr.io/test/app:v1"},
		{"operation": "ls", "repository": "ghcr.io/test/app"},
		{"operation": "catalog", "registry": "ghcr.io"},
		{"operation": "pull", "ref": "ghcr.io/test/app:v1", "path": "/tmp/x.tar"},
		{"operation": "push", "ref": "ghcr.io/test/app:v1", "path": "/tmp/x.tar"},
		{"operation": "copy", "src": "a.io/x:1", "dst": "b.io/x:1"},
		{"operation": "append", "ref": "ghcr.io/test/app:v1"},
		{"operation": "mutate", "ref": "ghcr.io/test/app:v1"},
		{"operation": "delete", "ref": "ghcr.io/test/app:v1"},
	}

	for _, input := range operations {
		t.Run(input["operation"].(string), func(t *testing.T) {
			desc, err := p.DescribeWhatIf(ctx, "oci", input)
			require.NoError(t, err)
			assert.NotEmpty(t, desc)
			t.Logf("WhatIf[%s]: %s", input["operation"], desc)
		})
	}
}

// --- helpers ---

func seedImage(t *testing.T, ref string) {
	t.Helper()
	imgRef, err := name.ParseReference(ref)
	require.NoError(t, err)

	img, err := random.Image(512, 2)
	require.NoError(t, err)

	err = remote.Write(imgRef, img, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	require.NoError(t, err)
}

func execute(t *testing.T, ctx context.Context, p *oci.Plugin, input map[string]any) *sdkprovider.Output { //nolint:revive // test helper: t must be first
	t.Helper()
	out, err := p.ExecuteProvider(ctx, "oci", input)
	require.NoError(t, err, "operation=%s", input["operation"])
	d, ok := out.Data.(map[string]any)
	require.True(t, ok, "expected map[string]any data")
	assert.Equal(t, true, d["success"], "expected success=true")
	return out
}

func data(out *sdkprovider.Output) map[string]any {
	d, _ := out.Data.(map[string]any)
	return d
}

func field(out *sdkprovider.Output, key string) string {
	d := data(out)
	if d == nil {
		return ""
	}
	s, _ := d[key].(string)
	return s
}
