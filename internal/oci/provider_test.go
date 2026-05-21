package oci

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	sdkplugin "github.com/oakwood-commons/scafctl-plugin-sdk/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRegistry(t *testing.T) (*httptest.Server, *Plugin) {
	t.Helper()
	reg := registry.New()
	srv := httptest.NewServer(reg)
	t.Cleanup(srv.Close)

	p := &Plugin{}
	return srv, p
}

func pushRandomImage(t *testing.T, srv *httptest.Server, repoTag string) {
	t.Helper()
	ref, err := name.ParseReference(fmt.Sprintf("%s/%s", srv.Listener.Addr().String(), repoTag))
	require.NoError(t, err)

	img, err := random.Image(256, 1)
	require.NoError(t, err)

	err = remote.Write(ref, img)
	require.NoError(t, err)
}

func TestGetProviders(t *testing.T) {
	p := &Plugin{}
	providers, err := p.GetProviders(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{ProviderName}, providers)
}

func TestGetProviderDescriptor(t *testing.T) {
	p := &Plugin{}

	t.Run("known provider", func(t *testing.T) {
		desc, err := p.GetProviderDescriptor(context.Background(), ProviderName)
		require.NoError(t, err)
		assert.Equal(t, ProviderName, desc.Name)
		assert.Equal(t, "OCI Image Provider", desc.DisplayName)
		assert.NotEmpty(t, desc.Description)
		assert.NotNil(t, desc.Schema)
		assert.NotEmpty(t, desc.Capabilities)
		assert.NotNil(t, desc.OutputSchemas, "OutputSchemas must be present")
		for _, cap := range desc.Capabilities {
			assert.Contains(t, desc.OutputSchemas, cap, "OutputSchemas must include capability %s", cap)
		}
	})

	t.Run("unknown provider", func(t *testing.T) {
		_, err := p.GetProviderDescriptor(context.Background(), "unknown")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown provider")
	})
}

func TestExecuteProvider_UnknownProvider(t *testing.T) {
	p := &Plugin{}
	_, err := p.ExecuteProvider(context.Background(), "unknown", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider")
}

func TestExecuteProvider_MissingOperation(t *testing.T) {
	p := &Plugin{}
	_, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "operation")
}

func TestExecuteProvider_UnknownOperation(t *testing.T) {
	p := &Plugin{}
	_, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{"operation": "bogus"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown operation")
}

func TestDigest(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/myapp:v1")

	ctx := context.Background()
	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpDigest,
		"ref":       fmt.Sprintf("%s/myorg/myapp:v1", srv.Listener.Addr().String()),
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.Contains(t, data["digest"].(string), "sha256:")
	assert.Greater(t, data["size"].(int64), int64(0))
	assert.NotEmpty(t, data["mediaType"])
}

func TestDigest_MissingRef(t *testing.T) {
	p := &Plugin{}
	_, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{
		"operation": OpDigest,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ref")
}

func TestManifest(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/myapp:v1")

	ctx := context.Background()
	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpManifest,
		"ref":       fmt.Sprintf("%s/myorg/myapp:v1", srv.Listener.Addr().String()),
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.NotEmpty(t, data["manifest"])
	assert.Contains(t, data["manifest"].(string), "layers")
}

func TestLs(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/myapp:v1")
	pushRandomImage(t, srv, "myorg/myapp:v2")

	ctx := context.Background()
	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation":  OpLs,
		"repository": fmt.Sprintf("%s/myorg/myapp", srv.Listener.Addr().String()),
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	tags, ok := data["tags"].([]string)
	require.True(t, ok)
	assert.Contains(t, tags, "v1")
	assert.Contains(t, tags, "v2")
}

func TestLs_MissingRepository(t *testing.T) {
	p := &Plugin{}
	_, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{
		"operation": OpLs,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "repository")
}

func TestCatalog(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "org1/app1:latest")
	pushRandomImage(t, srv, "org2/app2:latest")

	ctx := context.Background()
	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpCatalog,
		"registry":  srv.Listener.Addr().String(),
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	repos, ok := data["repositories"].([]string)
	require.True(t, ok)
	assert.GreaterOrEqual(t, len(repos), 2)
}

func TestCatalog_MissingRegistry(t *testing.T) {
	p := &Plugin{}
	_, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{
		"operation": OpCatalog,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "registry")
}

func TestDescribeWhatIf(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()

	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{
			name:  "digest",
			input: map[string]any{"operation": OpDigest, "ref": "ghcr.io/org/app:v1"},
			want:  "ghcr.io/org/app:v1",
		},
		{
			name:  "ls",
			input: map[string]any{"operation": OpLs, "repository": "ghcr.io/org/app"},
			want:  "ghcr.io/org/app",
		},
		{
			name:  "copy",
			input: map[string]any{"operation": OpCopy, "src": "a.io/x:1", "dst": "b.io/x:1"},
			want:  "a.io/x:1",
		},
		{
			name:  "append without dst",
			input: map[string]any{"operation": OpAppend, "ref": "ghcr.io/org/app:v1"},
			want:  "Would append layer(s) to ghcr.io/org/app:v1",
		},
		{
			name:  "append with dst",
			input: map[string]any{"operation": OpAppend, "ref": "ghcr.io/org/app:v1", "dst": "ghcr.io/org/app:v2"},
			want:  "→",
		},
		{
			name:  "unknown op",
			input: map[string]any{"operation": "foo"},
			want:  "foo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc, err := p.DescribeWhatIf(ctx, ProviderName, tt.input)
			require.NoError(t, err)
			assert.Contains(t, desc, tt.want)
		})
	}
}

func TestDescribeWhatIf_UnknownProvider(t *testing.T) {
	p := &Plugin{}
	_, err := p.DescribeWhatIf(context.Background(), "unknown", nil)
	assert.Error(t, err)
}

func TestMissingInputValidation(t *testing.T) {
	// Verifies all operations fail gracefully when required inputs are missing.
	p := &Plugin{}
	ctx := context.Background()

	tests := []struct {
		op      string
		wantErr string
	}{
		{OpPull, "ref"},
		{OpPush, "ref"},
		{OpCopy, "src"},
		{OpAppend, "ref"},
		{OpMutate, "ref"},
		{OpDelete, "ref"},
	}
	for _, tt := range tests {
		t.Run(tt.op, func(t *testing.T) {
			_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{"operation": tt.op})
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestPull(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/myapp:v1")

	ctx := context.Background()
	path := t.TempDir() + "/pulled.tar"

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpPull,
		"ref":       fmt.Sprintf("%s/myorg/myapp:v1", srv.Listener.Addr().String()),
		"path":      path,
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.Contains(t, data["digest"].(string), "sha256:")
	assert.Equal(t, path, data["path"])

	// Verify file was written.
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))
}

func TestPush(t *testing.T) {
	srv, p := setupRegistry(t)

	// First pull an image to get a tarball, then push it to a new tag.
	pushRandomImage(t, srv, "myorg/myapp:src")

	ctx := context.Background()
	path := t.TempDir() + "/image.tar"

	// Pull to tarball.
	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpPull,
		"ref":       fmt.Sprintf("%s/myorg/myapp:src", srv.Listener.Addr().String()),
		"path":      path,
	})
	require.NoError(t, err)

	// Push the tarball to a new tag.
	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpPush,
		"ref":       fmt.Sprintf("%s/myorg/myapp:pushed", srv.Listener.Addr().String()),
		"path":      path,
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.Contains(t, data["digest"].(string), "sha256:")
}

func TestCopy(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/src:v1")

	ctx := context.Background()
	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpCopy,
		"src":       fmt.Sprintf("%s/myorg/src:v1", srv.Listener.Addr().String()),
		"dst":       fmt.Sprintf("%s/myorg/dst:v1", srv.Listener.Addr().String()),
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.Contains(t, data["digest"].(string), "sha256:")

	// Verify destination exists.
	digestOut, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpDigest,
		"ref":       fmt.Sprintf("%s/myorg/dst:v1", srv.Listener.Addr().String()),
	})
	require.NoError(t, err)
	dstData, _ := digestOut.Data.(map[string]any)
	assert.Equal(t, data["digest"], dstData["digest"])
}

func TestAppend(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/base:v1")

	// Create a temporary file to use as a layer.
	layerDir := t.TempDir()
	layerFile := layerDir + "/data.txt"
	require.NoError(t, os.WriteFile(layerFile, []byte("layer content"), 0o600))

	// Create a tarball layer.
	tarPath := t.TempDir() + "/layer.tar"
	createTestTar(t, tarPath, layerFile)

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/base:v1", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpAppend,
		"ref":       ref,
		"layers":    []any{tarPath},
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.Contains(t, data["digest"].(string), "sha256:")
}

func TestMutate(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpMutate,
		"ref":       ref,
		"config": map[string]any{
			"env":        []any{"FOO=bar", "BAZ=qux"},
			"user":       "nobody",
			"workdir":    "/app",
			"labels":     map[string]any{"version": "1.0"},
			"entrypoint": []any{"/bin/sh"},
			"cmd":        []any{"-c", "echo hello"},
		},
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.Contains(t, data["digest"].(string), "sha256:")
}

func TestMutate_MissingConfigAndInputs(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpMutate,
		"ref":       ref,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mutate requires at least one of")
}

func TestDelete(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpDelete,
		"ref":       ref,
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
}

// createTestTar creates a simple tar file containing the given file.
func createTestTar(t *testing.T, tarPath, filePath string) {
	t.Helper()
	f, err := os.Create(tarPath) //nolint:gosec // test helper with controlled paths
	require.NoError(t, err)

	tw := tar.NewWriter(f)

	content, err := os.ReadFile(filePath) //nolint:gosec // test helper with controlled paths
	require.NoError(t, err)

	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "data.txt",
		Mode: 0o644,
		Size: int64(len(content)),
	}))
	_, err = tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, f.Close())
}

func BenchmarkExecuteDigest(b *testing.B) {
	reg := registry.New()
	srv := httptest.NewServer(reg)
	b.Cleanup(srv.Close)

	p := &Plugin{}
	ref, err := name.ParseReference(fmt.Sprintf("%s/bench/app:v1", srv.Listener.Addr().String()))
	require.NoError(b, err)
	img, err := random.Image(256, 1)
	require.NoError(b, err)
	require.NoError(b, remote.Write(ref, img))

	ctx := context.Background()
	input := map[string]any{
		"operation": OpDigest,
		"ref":       fmt.Sprintf("%s/bench/app:v1", srv.Listener.Addr().String()),
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		_, err := p.ExecuteProvider(ctx, ProviderName, input)
		require.NoError(b, err)
	}
}

func TestAppend_DirectoryLayer(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/base:v1")

	// Create a directory with files to append as a layer.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/hello.txt", []byte("hello"), 0o600))
	require.NoError(t, os.MkdirAll(dir+"/sub", 0o750))
	require.NoError(t, os.WriteFile(dir+"/sub/world.txt", []byte("world"), 0o600))

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/base:v1", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpAppend,
		"ref":       ref,
		"layers":    []any{dir},
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.Contains(t, data["digest"].(string), "sha256:")
}

func TestAppend_MissingLayers(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpAppend,
		"ref":       "localhost/foo:bar",
		"layers":    []any{},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "at least one layer")
}

func TestGetStringSlice_InvalidTypes(t *testing.T) {
	_, err := getStringSlice(map[string]any{"x": 42}, "x")
	assert.Error(t, err)

	_, err = getStringSlice(map[string]any{"x": []any{1}}, "x")
	assert.Error(t, err)

	_, err = getStringSlice(map[string]any{}, "x")
	assert.Error(t, err)
}

func TestGetStringSlice_ValidTypes(t *testing.T) {
	result, err := getStringSlice(map[string]any{"x": []string{"a", "b"}}, "x")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, result)

	result, err = getStringSlice(map[string]any{"x": []any{"c", "d"}}, "x")
	require.NoError(t, err)
	assert.Equal(t, []string{"c", "d"}, result)
}

func TestPull_MissingPath(t *testing.T) {
	p := &Plugin{}
	_, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{
		"operation": OpPull,
		"ref":       "localhost/foo:bar",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "path")
}

func TestPush_MissingPath(t *testing.T) {
	p := &Plugin{}
	_, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{
		"operation": OpPush,
		"ref":       "localhost/foo:bar",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "path")
}

func TestCopy_MissingDst(t *testing.T) {
	p := &Plugin{}
	_, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{
		"operation": OpCopy,
		"src":       "localhost/foo:bar",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dst")
}

func TestConfigureProvider_AuthIntegration(t *testing.T) {
	// Verifies that ConfigureProvider stores credentials and the resulting
	// keychain is used for subsequent operations. The test registry does not
	// enforce auth server-side; this test validates client-side keychain
	// wiring, not registry-side access control.
	reg := registry.New()
	srv := httptest.NewServer(reg)
	t.Cleanup(srv.Close)

	p := &Plugin{}
	ctx := context.Background()

	// Push an image directly (registry doesn't enforce auth server-side,
	// but the keychain will be used by the client for all requests).
	addr := srv.Listener.Addr().String()
	ref, err := name.ParseReference(fmt.Sprintf("%s/auth-test/app:v1", addr))
	require.NoError(t, err)
	img, err := random.Image(256, 1)
	require.NoError(t, err)
	require.NoError(t, remote.Write(ref, img))

	// Configure with credentials targeting this registry.
	settings := map[string]json.RawMessage{
		"registry": json.RawMessage(`"` + addr + `"`),
		"username": json.RawMessage(`"testuser"`),
		"password": json.RawMessage(`"testpass"`),
	}
	err = p.ConfigureProvider(ctx, "oci", sdkplugin.ProviderConfig{Settings: settings})
	require.NoError(t, err)

	// Verify the keychain was set (plugin can resolve).
	assert.NotEmpty(t, p.username)

	// Execute a digest operation — proves the configured keychain is used.
	out, err := p.ExecuteProvider(ctx, "oci", map[string]any{
		"operation": "digest",
		"ref":       fmt.Sprintf("%s/auth-test/app:v1", addr),
	})
	require.NoError(t, err)
	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.Contains(t, data["digest"].(string), "sha256:")
}

func TestConfigureProvider_NilSettings(t *testing.T) {
	p := &Plugin{}
	err := p.ConfigureProvider(context.Background(), "oci", sdkplugin.ProviderConfig{})
	require.NoError(t, err)
	assert.Empty(t, p.username)
}

func TestLifecycleMethods_UnknownProvider(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()

	t.Run("ConfigureProvider", func(t *testing.T) {
		err := p.ConfigureProvider(ctx, "unknown", sdkplugin.ProviderConfig{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown provider")
	})

	t.Run("ExecuteProviderStream", func(t *testing.T) {
		err := p.ExecuteProviderStream(ctx, "unknown", nil, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown provider")
	})

	t.Run("ExtractDependencies", func(t *testing.T) {
		_, err := p.ExtractDependencies(ctx, "unknown", nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown provider")
	})

	t.Run("StopProvider", func(t *testing.T) {
		err := p.StopProvider(ctx, "unknown")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown provider")
	})
}

func TestMutate_InvalidConfigType(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpMutate,
		"ref":       ref,
		"config": map[string]any{
			"env": 42, // wrong type
		},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "env")
}

func TestParsePlatform(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantOS   string
		wantArch string
		wantVar  string
		wantErr  bool
	}{
		{"os/arch", "linux/amd64", "linux", "amd64", "", false},
		{"os/arch/variant", "linux/arm/v7", "linux", "arm", "v7", false},
		{"missing arch", "linux", "", "", "", true},
		{"empty string", "", "", "", "", true},
		{"empty os", "/amd64", "", "", "", true},
		{"empty arch", "linux/", "", "", "", true},
		{"empty variant", "linux/arm/", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plat, err := parsePlatform(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOS, plat.OS)
			assert.Equal(t, tt.wantArch, plat.Architecture)
			assert.Equal(t, tt.wantVar, plat.Variant)
		})
	}
}

func TestPull_InvalidPlatform(t *testing.T) {
	p := &Plugin{}
	_, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{
		"operation": OpPull,
		"ref":       "localhost/foo:bar",
		"path":      "/tmp/test.tar",
		"platform":  "invalid",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid platform")
}

func TestMutate_ConvenienceInputs(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation":  OpMutate,
		"ref":        ref,
		"entrypoint": []any{"/app"},
		"user":       "1001",
		"workdir":    "/home/app",
		"env":        []any{"HOME=/home/app"},
		"labels":     map[string]any{"version": "2.0"},
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.Contains(t, data["digest"].(string), "sha256:")
	assert.NotNil(t, data["size"])
}

func TestMutate_ConvenienceOverridesConfig(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpMutate,
		"ref":       ref,
		"config": map[string]any{
			"user": "old-user",
		},
		"user": "new-user", // convenience should win
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])

	// Verify the new user was applied by fetching the image config.
	imgRef, err := name.ParseReference(ref)
	require.NoError(t, err)
	desc, err := remote.Get(imgRef)
	require.NoError(t, err)
	img, err := desc.Image()
	require.NoError(t, err)
	cfgFile, err := img.ConfigFile()
	require.NoError(t, err)
	assert.Equal(t, "new-user", cfgFile.Config.User)
}

func TestMutate_WithLayers(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/binary", []byte("app binary"), 0o600))

	tarPath := t.TempDir() + "/layer.tar"
	createTestTar(t, tarPath, dir+"/binary")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation":  OpMutate,
		"ref":        ref,
		"layers":     []any{tarPath},
		"entrypoint": []any{"/binary"},
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.Contains(t, data["digest"].(string), "sha256:")
}

func TestMutate_LayersOnly(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/data.txt", []byte("data"), 0o600))

	tarPath := t.TempDir() + "/layer.tar"
	createTestTar(t, tarPath, dir+"/data.txt")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpMutate,
		"ref":       ref,
		"layers":    []any{tarPath},
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
}

func TestMutate_WithDst(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	srcRef := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())
	dstRef := fmt.Sprintf("%s/myorg/app:v2", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation":  OpMutate,
		"ref":        srcRef,
		"dst":        dstRef,
		"entrypoint": []any{"/app"},
		"labels":     map[string]any{"version": "2.0"},
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.Equal(t, dstRef, data["ref"])

	// Verify destination image exists and has correct config.
	imgRef, err := name.ParseReference(dstRef)
	require.NoError(t, err)
	desc, err := remote.Get(imgRef)
	require.NoError(t, err)
	img, err := desc.Image()
	require.NoError(t, err)
	cfgFile, err := img.ConfigFile()
	require.NoError(t, err)
	assert.Equal(t, []string{"/app"}, cfgFile.Config.Entrypoint)
	assert.Equal(t, "2.0", cfgFile.Config.Labels["version"])
}

func TestMutate_CombinedAppendMutateDst(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/base:latest")

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/app", []byte("binary"), 0o600))

	tarPath := t.TempDir() + "/layer.tar"
	createTestTar(t, tarPath, dir+"/app")

	ctx := context.Background()
	srcRef := fmt.Sprintf("%s/myorg/base:latest", srv.Listener.Addr().String())
	dstRef := fmt.Sprintf("%s/myorg/myapp:v1", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation":  OpMutate,
		"ref":        srcRef,
		"dst":        dstRef,
		"layers":     []any{tarPath},
		"entrypoint": []any{"/app"},
		"user":       "1001",
		"labels":     map[string]any{"built-by": "scafctl"},
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.Equal(t, dstRef, data["ref"])
	assert.Contains(t, data["digest"].(string), "sha256:")
}

func TestMutate_InvalidLayersType(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpMutate,
		"ref":       ref,
		"layers":    42,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "layers")
}

func TestMutate_InvalidDstType(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation":  OpMutate,
		"ref":        ref,
		"entrypoint": []any{"/app"},
		"dst":        42,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dst")
}

func TestMutate_EmptyDst(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation":  OpMutate,
		"ref":        ref,
		"entrypoint": []any{"/app"},
		"dst":        "",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "non-empty")
}

func TestMutate_StringEntrypoint(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation":  OpMutate,
		"ref":        ref,
		"entrypoint": "/app",
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])

	// Verify the entrypoint was normalized to a slice.
	imgRef, err := name.ParseReference(ref)
	require.NoError(t, err)
	desc, err := remote.Get(imgRef)
	require.NoError(t, err)
	img, err := desc.Image()
	require.NoError(t, err)
	cfgFile, err := img.ConfigFile()
	require.NoError(t, err)
	assert.Equal(t, []string{"/app"}, cfgFile.Config.Entrypoint)
}

func TestMutate_MapEnv(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpMutate,
		"ref":       ref,
		"env":       map[string]any{"HOME": "/app", "PORT": "8080"},
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])

	// Verify the env was normalized.
	imgRef, err := name.ParseReference(ref)
	require.NoError(t, err)
	desc, err := remote.Get(imgRef)
	require.NoError(t, err)
	img, err := desc.Image()
	require.NoError(t, err)
	cfgFile, err := img.ConfigFile()
	require.NoError(t, err)
	assert.Len(t, cfgFile.Config.Env, 2)
	assert.Contains(t, cfgFile.Config.Env, "HOME=/app")
	assert.Contains(t, cfgFile.Config.Env, "PORT=8080")
}

// --- Scratch image tests ---

func TestAppend_Scratch(t *testing.T) {
	srv, p := setupRegistry(t)
	addr := srv.Listener.Addr().String()
	dstRef := fmt.Sprintf("%s/myorg/scratch-app:v1", addr)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0o600))

	ctx := context.Background()
	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpAppend,
		"ref":       "scratch",
		"dst":       dstRef,
		"layers":    []any{dir},
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.Equal(t, dstRef, data["ref"])
	assert.Contains(t, data["digest"].(string), "sha256:")
}

func TestMutate_Scratch(t *testing.T) {
	srv, p := setupRegistry(t)
	addr := srv.Listener.Addr().String()
	dstRef := fmt.Sprintf("%s/myorg/scratch-configured:v1", addr)

	ctx := context.Background()
	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpMutate,
		"ref":       "scratch",
		"dst":       dstRef,
		"config": map[string]any{
			"entrypoint": []any{"/bin/sh"},
			"user":       "1001",
			"labels": map[string]any{
				"maintainer": "test",
			},
		},
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.Equal(t, dstRef, data["ref"])

	ref, err := name.ParseReference(dstRef)
	require.NoError(t, err)
	desc, err := remote.Get(ref)
	require.NoError(t, err)
	img, err := desc.Image()
	require.NoError(t, err)
	cfgFile, err := img.ConfigFile()
	require.NoError(t, err)
	assert.Equal(t, []string{"/bin/sh"}, cfgFile.Config.Entrypoint)
	assert.Equal(t, "1001", cfgFile.Config.User)
	assert.Equal(t, "test", cfgFile.Config.Labels["maintainer"])
}

func TestAppend_ScratchNoDst(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpAppend,
		"ref":       "scratch",
		"layers":    []any{"/tmp/some-layer"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dst")
}

func TestMutate_ScratchNoDst(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpMutate,
		"ref":       "scratch",
		"config": map[string]any{
			"entrypoint": []any{"/app"},
		},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dst")
}

// --- Index operation tests ---

func mutateConfigFile(img v1.Image, cfgFile *v1.ConfigFile) (v1.Image, error) {
	return mutate.ConfigFile(img, cfgFile)
}

func pushRandomImageWithPlatform(t *testing.T, srv *httptest.Server, repoTag, os, arch string) {
	t.Helper()
	ref, err := name.ParseReference(fmt.Sprintf("%s/%s", srv.Listener.Addr().String(), repoTag))
	require.NoError(t, err)

	img, err := random.Image(256, 1)
	require.NoError(t, err)

	cfgFile, err := img.ConfigFile()
	require.NoError(t, err)
	cfgFile.OS = os
	cfgFile.Architecture = arch

	img, err = mutateConfigFile(img, cfgFile)
	require.NoError(t, err)

	err = remote.Write(ref, img)
	require.NoError(t, err)
}

func TestIndex_HappyPath(t *testing.T) {
	srv, p := setupRegistry(t)
	addr := srv.Listener.Addr().String()

	pushRandomImageWithPlatform(t, srv, "myorg/app:v1-amd64", "linux", "amd64")
	pushRandomImageWithPlatform(t, srv, "myorg/app:v1-arm64", "linux", "arm64")

	ctx := context.Background()
	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpIndex,
		"ref":       fmt.Sprintf("%s/myorg/app:v1", addr),
		"manifests": []any{
			map[string]any{
				"ref":      fmt.Sprintf("%s/myorg/app:v1-amd64", addr),
				"platform": "linux/amd64",
			},
			map[string]any{
				"ref":      fmt.Sprintf("%s/myorg/app:v1-arm64", addr),
				"platform": "linux/arm64",
			},
		},
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.Contains(t, data["digest"].(string), "sha256:")
	assert.NotEmpty(t, data["mediaType"])

	idxRef, err := name.ParseReference(fmt.Sprintf("%s/myorg/app:v1", addr))
	require.NoError(t, err)
	desc, err := remote.Get(idxRef)
	require.NoError(t, err)
	idx, err := desc.ImageIndex()
	require.NoError(t, err)
	im, err := idx.IndexManifest()
	require.NoError(t, err)
	assert.Len(t, im.Manifests, 2)
}

func TestIndex_SingleManifest(t *testing.T) {
	srv, p := setupRegistry(t)
	addr := srv.Listener.Addr().String()

	pushRandomImageWithPlatform(t, srv, "myorg/app:v1-amd64", "linux", "amd64")

	ctx := context.Background()
	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpIndex,
		"ref":       fmt.Sprintf("%s/myorg/app:v1", addr),
		"manifests": []any{
			map[string]any{
				"ref":      fmt.Sprintf("%s/myorg/app:v1-amd64", addr),
				"platform": "linux/amd64",
			},
		},
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
}

func TestIndex_AutoDetectPlatform(t *testing.T) {
	srv, p := setupRegistry(t)
	addr := srv.Listener.Addr().String()

	pushRandomImageWithPlatform(t, srv, "myorg/app:v1-amd64", "linux", "amd64")
	pushRandomImageWithPlatform(t, srv, "myorg/app:v1-arm64", "linux", "arm64")

	ctx := context.Background()
	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpIndex,
		"ref":       fmt.Sprintf("%s/myorg/app:v1", addr),
		"manifests": []any{
			map[string]any{"ref": fmt.Sprintf("%s/myorg/app:v1-amd64", addr)},
			map[string]any{"ref": fmt.Sprintf("%s/myorg/app:v1-arm64", addr)},
		},
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])

	idxRef, err := name.ParseReference(fmt.Sprintf("%s/myorg/app:v1", addr))
	require.NoError(t, err)
	desc, err := remote.Get(idxRef)
	require.NoError(t, err)
	idx, err := desc.ImageIndex()
	require.NoError(t, err)
	im, err := idx.IndexManifest()
	require.NoError(t, err)
	require.Len(t, im.Manifests, 2)

	platforms := make([]string, 0, 2)
	for _, m := range im.Manifests {
		require.NotNil(t, m.Platform)
		platforms = append(platforms, m.Platform.OS+"/"+m.Platform.Architecture)
	}
	assert.Contains(t, platforms, "linux/amd64")
	assert.Contains(t, platforms, "linux/arm64")
}

func TestIndex_MissingManifests(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpIndex,
		"ref":       "localhost/foo:bar",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "manifests")
}

func TestIndex_EmptyManifests(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpIndex,
		"ref":       "localhost/foo:bar",
		"manifests": []any{},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "at least one")
}

func TestIndex_InvalidManifestEntry(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpIndex,
		"ref":       "localhost/foo:bar",
		"manifests": []any{"not-an-object"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expected object")
}

func TestIndex_MissingManifestRef(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpIndex,
		"ref":       "localhost/foo:bar",
		"manifests": []any{
			map[string]any{"platform": "linux/amd64"},
		},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ref")
}

func TestIndex_InvalidPlatform(t *testing.T) {
	srv, p := setupRegistry(t)
	addr := srv.Listener.Addr().String()
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpIndex,
		"ref":       fmt.Sprintf("%s/myorg/app:idx", addr),
		"manifests": []any{
			map[string]any{
				"ref":      fmt.Sprintf("%s/myorg/app:v1", addr),
				"platform": "bad",
			},
		},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid platform")
}

func TestIndex_WhatIf(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()

	desc, err := p.DescribeWhatIf(ctx, ProviderName, map[string]any{
		"operation": OpIndex,
		"ref":       "ghcr.io/myorg/app:v1",
		"manifests": []any{
			map[string]any{"ref": "a", "platform": "linux/amd64"},
			map[string]any{"ref": "b", "platform": "linux/arm64"},
		},
	})
	require.NoError(t, err)
	assert.Contains(t, desc, "multi-arch index")
	assert.Contains(t, desc, "2 manifest(s)")
	assert.Contains(t, desc, "ghcr.io/myorg/app:v1")
}

func TestIndex_InvalidManifestsType(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpIndex,
		"ref":       "localhost/foo:bar",
		"manifests": 42,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "manifests")
}

// --- Config operation tests ---

func TestConfig_HappyPath(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpConfig,
		"ref":       ref,
	})
	require.NoError(t, err)
	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.True(t, data["success"].(bool))
	assert.NotEmpty(t, data["config"])
	assert.NotEmpty(t, data["digest"])
	// Config should be valid JSON
	assert.Contains(t, data["config"].(string), "config")
}

func TestConfig_MissingRef(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpConfig,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ref")
}

// --- Tag operation tests ---

func TestTag_HappyPath(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpTag,
		"ref":       ref,
		"tag":       "latest",
	})
	require.NoError(t, err)
	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.True(t, data["success"].(bool))
	assert.Contains(t, data["tag"].(string), "latest")
	assert.NotEmpty(t, data["digest"])

	// Verify the new tag exists
	newRef := fmt.Sprintf("%s/myorg/app:latest", srv.Listener.Addr().String())
	tagRef, err := name.ParseReference(newRef)
	require.NoError(t, err)
	_, err = remote.Get(tagRef)
	assert.NoError(t, err)
}

func TestTag_MissingTag(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpTag,
		"ref":       ref,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tag")
}

// --- Annotations tests ---

func TestMutate_Annotations(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())
	dstRef := fmt.Sprintf("%s/myorg/app:annotated", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpMutate,
		"ref":       ref,
		"dst":       dstRef,
		"user":      "nobody",
		"annotations": map[string]any{
			"org.opencontainers.image.source": "https://github.com/myorg/app",
			"org.opencontainers.image.title":  "myapp",
		},
	})
	require.NoError(t, err)
	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.True(t, data["success"].(bool))
	assert.NotEmpty(t, data["digest"])

	// Verify annotations in manifest
	imgRef, err := name.ParseReference(dstRef)
	require.NoError(t, err)
	desc, err := remote.Get(imgRef)
	require.NoError(t, err)
	assert.Contains(t, string(desc.Manifest), "org.opencontainers.image.source")
}

func TestMutate_AnnotationsInvalidType(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation":   OpMutate,
		"ref":         ref,
		"user":        "nobody",
		"annotations": "invalid",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "annotations")
}

// --- Tag validation tests ---

func TestTagValidation_PlusChar(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpDigest,
		"ref":       "ghcr.io/myorg/app:0.1.0+dirty",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "+")
}

// --- WhatIf tests for new operations ---

func TestDescribeWhatIf_Config(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()

	desc, err := p.DescribeWhatIf(ctx, ProviderName, map[string]any{
		"operation": OpConfig,
		"ref":       "ghcr.io/org/app:v1",
	})
	require.NoError(t, err)
	assert.Contains(t, desc, "config")
	assert.Contains(t, desc, "ghcr.io/org/app:v1")
}

func TestDescribeWhatIf_Tag(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()

	desc, err := p.DescribeWhatIf(ctx, ProviderName, map[string]any{
		"operation": OpTag,
		"ref":       "ghcr.io/org/app:v1",
		"tag":       "latest",
	})
	require.NoError(t, err)
	assert.Contains(t, desc, "tag")
	assert.Contains(t, desc, "ghcr.io/org/app:v1")
	assert.Contains(t, desc, "latest")
}

// --- Insecure setting test ---

func TestInsecureSetting(t *testing.T) {
	p := &Plugin{}
	err := p.ConfigureProvider(context.Background(), ProviderName, sdkplugin.ProviderConfig{
		Settings: map[string]json.RawMessage{
			"insecure": json.RawMessage(`true`),
		},
	})
	require.NoError(t, err)
	assert.True(t, p.insecure)
}

// --- Validate operation tests ---

func TestValidate_HappyPath(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpValidate,
		"ref":       ref,
	})
	require.NoError(t, err)
	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.True(t, data["success"].(bool))
	assert.NotEmpty(t, data["digest"])
	assert.Greater(t, data["layerCount"], 0)
}

func TestValidate_MissingRef(t *testing.T) {
	p := &Plugin{}
	_, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{
		"operation": OpValidate,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ref")
}

// --- Blob operation tests ---

func TestBlob_HappyPath(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	// First get the digest of a layer
	imgRef, err := name.ParseReference(ref)
	require.NoError(t, err)
	desc, err := remote.Get(imgRef)
	require.NoError(t, err)
	img, err := desc.Image()
	require.NoError(t, err)
	layers, err := img.Layers()
	require.NoError(t, err)
	require.NotEmpty(t, layers)
	layerDigest, err := layers[0].Digest()
	require.NoError(t, err)

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpBlob,
		"ref":       ref,
		"digest":    layerDigest.String(),
	})
	require.NoError(t, err)
	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.True(t, data["success"].(bool))
	assert.Equal(t, layerDigest.String(), data["digest"])
}

func TestBlob_WriteToFile(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	imgRef, err := name.ParseReference(ref)
	require.NoError(t, err)
	desc, err := remote.Get(imgRef)
	require.NoError(t, err)
	img, err := desc.Image()
	require.NoError(t, err)
	layers, err := img.Layers()
	require.NoError(t, err)
	layerDigest, err := layers[0].Digest()
	require.NoError(t, err)

	outPath := filepath.Join(t.TempDir(), "blob.bin")

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpBlob,
		"ref":       ref,
		"digest":    layerDigest.String(),
		"path":      outPath,
	})
	require.NoError(t, err)
	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.True(t, data["success"].(bool))

	info, err := os.Stat(outPath)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))
}

func TestBlob_MissingDigest(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())
	_, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{
		"operation": OpBlob,
		"ref":       ref,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "digest")
}

// --- Export operation tests ---

func TestExport_HappyPath(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())
	outPath := filepath.Join(t.TempDir(), "export.tar")

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpExport,
		"ref":       ref,
		"path":      outPath,
	})
	require.NoError(t, err)
	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.True(t, data["success"].(bool))
	assert.Equal(t, outPath, data["path"])

	info, err := os.Stat(outPath)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))
}

func TestExport_MissingPath(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())
	_, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{
		"operation": OpExport,
		"ref":       ref,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "path")
}

// --- Flatten operation tests ---

func TestFlatten_HappyPath(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	addr := srv.Listener.Addr().String()
	ref := fmt.Sprintf("%s/myorg/app:v1", addr)
	dstRef := fmt.Sprintf("%s/myorg/app:flat", addr)

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpFlatten,
		"ref":       ref,
		"dst":       dstRef,
	})
	require.NoError(t, err)
	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.True(t, data["success"].(bool))
	assert.NotEmpty(t, data["digest"])

	// Verify the flattened image has exactly 1 layer
	flatRef, err := name.ParseReference(dstRef)
	require.NoError(t, err)
	desc, err := remote.Get(flatRef)
	require.NoError(t, err)
	img, err := desc.Image()
	require.NoError(t, err)
	layers, err := img.Layers()
	require.NoError(t, err)
	assert.Len(t, layers, 1)
}

// --- Rebase operation tests ---

func TestRebase_MissingOldBase(t *testing.T) {
	p := &Plugin{}
	_, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{
		"operation": OpRebase,
		"ref":       "ghcr.io/org/app:v1",
		"new_base":  "ghcr.io/org/base:v2",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "old_base")
}

func TestRebase_MissingNewBase(t *testing.T) {
	p := &Plugin{}
	_, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{
		"operation": OpRebase,
		"ref":       "ghcr.io/org/app:v1",
		"old_base":  "ghcr.io/org/base:v1",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "new_base")
}

// --- Mutate output (tarball) tests ---

func TestMutate_OutputToTarball(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())
	outPath := filepath.Join(t.TempDir(), "mutated.tar")

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpMutate,
		"ref":       ref,
		"user":      "nobody",
		"output":    outPath,
	})
	require.NoError(t, err)
	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.True(t, data["success"].(bool))
	assert.Equal(t, outPath, data["path"])

	info, err := os.Stat(outPath)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))
}

// --- Mutate exposed_ports tests ---

func TestMutate_ExposedPorts(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())
	dstRef := fmt.Sprintf("%s/myorg/app:ports", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation":     OpMutate,
		"ref":           ref,
		"dst":           dstRef,
		"exposed_ports": []any{"8080/tcp", "443/tcp"},
	})
	require.NoError(t, err)
	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.True(t, data["success"].(bool))

	imgRef, err := name.ParseReference(dstRef)
	require.NoError(t, err)
	desc, err := remote.Get(imgRef)
	require.NoError(t, err)
	img, err := desc.Image()
	require.NoError(t, err)
	cfgFile, err := img.ConfigFile()
	require.NoError(t, err)
	assert.Contains(t, cfgFile.Config.ExposedPorts, "8080/tcp")
	assert.Contains(t, cfgFile.Config.ExposedPorts, "443/tcp")
}

// --- Mutate set_platform tests ---

func TestMutate_SetPlatform(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())
	dstRef := fmt.Sprintf("%s/myorg/app:plat", srv.Listener.Addr().String())

	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation":    OpMutate,
		"ref":          ref,
		"dst":          dstRef,
		"set_platform": "linux/arm64",
	})
	require.NoError(t, err)
	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.True(t, data["success"].(bool))

	imgRef, err := name.ParseReference(dstRef)
	require.NoError(t, err)
	desc, err := remote.Get(imgRef)
	require.NoError(t, err)
	img, err := desc.Image()
	require.NoError(t, err)
	cfgFile, err := img.ConfigFile()
	require.NoError(t, err)
	assert.Equal(t, "linux", cfgFile.OS)
	assert.Equal(t, "arm64", cfgFile.Architecture)
}

// --- WhatIf tests for new operations ---

func TestDescribeWhatIf_NewOps(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()

	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{
			name:  "validate",
			input: map[string]any{"operation": OpValidate, "ref": "ghcr.io/org/app:v1"},
			want:  "validate",
		},
		{
			name:  "blob",
			input: map[string]any{"operation": OpBlob, "ref": "ghcr.io/org/app:v1", "digest": "sha256:abc"},
			want:  "blob",
		},
		{
			name:  "export",
			input: map[string]any{"operation": OpExport, "ref": "ghcr.io/org/app:v1", "path": "/tmp/fs.tar"},
			want:  "export",
		},
		{
			name:  "flatten",
			input: map[string]any{"operation": OpFlatten, "ref": "ghcr.io/org/app:v1"},
			want:  "flatten",
		},
		{
			name:  "rebase",
			input: map[string]any{"operation": OpRebase, "ref": "ghcr.io/org/app:v1", "old_base": "base:v1", "new_base": "base:v2"},
			want:  "rebase",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc, err := p.DescribeWhatIf(ctx, ProviderName, tt.input)
			require.NoError(t, err)
			assert.Contains(t, desc, tt.want)
		})
	}
}

// --- Rebase happy-path integration test ---

func TestRebase_HappyPath(t *testing.T) {
	srv, p := setupRegistry(t)
	addr := srv.Listener.Addr().String()

	// Push base-v1 (the original base image)
	pushRandomImage(t, srv, "myorg/base:v1")

	// Build app image on top of base-v1 by appending a layer
	baseRef := fmt.Sprintf("%s/myorg/base:v1", addr)
	appRef := fmt.Sprintf("%s/myorg/app:v1", addr)

	// Create a temp layer file
	layerDir := t.TempDir()
	layerFile := filepath.Join(layerDir, "app.txt")
	require.NoError(t, os.WriteFile(layerFile, []byte("app-content"), 0o600))

	_, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{
		"operation": OpAppend,
		"ref":       baseRef,
		"dst":       appRef,
		"layers":    []any{layerFile},
	})
	require.NoError(t, err)

	// Push base-v2 (the new base image)
	pushRandomImage(t, srv, "myorg/base:v2")

	// Rebase app from base-v1 onto base-v2
	dstRef := fmt.Sprintf("%s/myorg/app:rebased", addr)
	out, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{
		"operation": OpRebase,
		"ref":       appRef,
		"old_base":  baseRef,
		"new_base":  fmt.Sprintf("%s/myorg/base:v2", addr),
		"dst":       dstRef,
	})
	require.NoError(t, err)
	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.True(t, data["success"].(bool))
	assert.NotEmpty(t, data["digest"])

	// Verify the rebased image exists
	imgRef, err := name.ParseReference(dstRef)
	require.NoError(t, err)
	_, err = remote.Get(imgRef)
	assert.NoError(t, err)
}

// --- isUnsupported helper tests ---

func TestIsUnsupported(t *testing.T) {
	t.Run("transport error with UNSUPPORTED code", func(t *testing.T) {
		err := &transport.Error{
			Errors: []transport.Diagnostic{
				{Code: transport.UnsupportedErrorCode, Message: "operation not supported"},
			},
			StatusCode: 405,
		}
		assert.True(t, isUnsupported(err))
	})

	t.Run("wrapped transport error with UNSUPPORTED code", func(t *testing.T) {
		inner := &transport.Error{
			Errors: []transport.Diagnostic{
				{Code: transport.UnsupportedErrorCode, Message: "not supported"},
			},
			StatusCode: 405,
		}
		err := fmt.Errorf("delete failed: %w", inner)
		assert.True(t, isUnsupported(err))
	})

	t.Run("transport error with different code", func(t *testing.T) {
		err := &transport.Error{
			Errors: []transport.Diagnostic{
				{Code: transport.DeniedErrorCode, Message: "access denied"},
			},
			StatusCode: 403,
		}
		assert.False(t, isUnsupported(err))
	})

	t.Run("string fallback with UNSUPPORTED", func(t *testing.T) {
		err := fmt.Errorf("UNSUPPORTED: operation not supported")
		assert.True(t, isUnsupported(err))
	})

	t.Run("other error", func(t *testing.T) {
		err := fmt.Errorf("INTERNAL: server error")
		assert.False(t, isUnsupported(err))
	})

	t.Run("nil error", func(t *testing.T) {
		assert.False(t, isUnsupported(nil))
	})
}

// --- Config multi-arch tests ---

func TestConfig_MultiArchIndex(t *testing.T) {
	srv, p := setupRegistry(t)

	// Create and push a multi-arch index
	ref := fmt.Sprintf("%s/myorg/app:multi", srv.Listener.Addr().String())
	createAndPushMultiArchIndex(t, ref)

	ctx := context.Background()
	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpConfig,
		"ref":       ref,
	})
	require.NoError(t, err)
	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.True(t, data["success"].(bool))
	assert.NotEmpty(t, data["config"])
	assert.NotEmpty(t, data["digest"])
	// Multi-arch index config should contain "manifests" array
	assert.Contains(t, data["config"].(string), "manifests")
}

// --- Validate multi-arch tests ---

func TestValidate_MultiArchIndex(t *testing.T) {
	srv, p := setupRegistry(t)

	// Create and push a multi-arch index
	ref := fmt.Sprintf("%s/myorg/app:multi", srv.Listener.Addr().String())
	createAndPushMultiArchIndex(t, ref)

	ctx := context.Background()
	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpValidate,
		"ref":       ref,
	})
	require.NoError(t, err)
	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.True(t, data["success"].(bool))
	// Multi-arch indexes don't have layers like images do
	assert.Equal(t, 0, data["layerCount"])
	assert.NotEmpty(t, data["digest"])
}

// createAndPushMultiArchIndex is a helper to create a multi-arch image index
func createAndPushMultiArchIndex(t *testing.T, ref string) {
	t.Helper()

	parsedRef, err := name.ParseReference(ref)
	require.NoError(t, err)

	// Create two random images for different platforms
	img1, err := random.Image(256, 1)
	require.NoError(t, err)
	img2, err := random.Image(256, 1)
	require.NoError(t, err)

	// Create index with both images
	idx := mutate.AppendManifests(
		empty.Index,
		mutate.IndexAddendum{
			Add: img1,
			Descriptor: v1.Descriptor{
				MediaType: types.DockerManifestSchema2,
				Platform: &v1.Platform{
					OS:           "linux",
					Architecture: "amd64",
				},
			},
		},
		mutate.IndexAddendum{
			Add: img2,
			Descriptor: v1.Descriptor{
				MediaType: types.DockerManifestSchema2,
				Platform: &v1.Platform{
					OS:           "linux",
					Architecture: "arm64",
				},
			},
		},
	)

	err = remote.WriteIndex(parsedRef, idx)
	require.NoError(t, err)
}
