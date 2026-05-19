package oci

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
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

func TestMutate_MissingConfig(t *testing.T) {
	srv, p := setupRegistry(t)
	pushRandomImage(t, srv, "myorg/app:v1")

	ctx := context.Background()
	ref := fmt.Sprintf("%s/myorg/app:v1", srv.Listener.Addr().String())

	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpMutate,
		"ref":       ref,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "config")
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
