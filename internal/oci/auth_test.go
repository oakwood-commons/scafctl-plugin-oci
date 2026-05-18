package oci

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticKeychain_Resolve(t *testing.T) {
	kc := newStaticKeychain("ghcr.io", "user", "pass")

	t.Run("matching registry", func(t *testing.T) {
		reg, err := name.NewRegistry("ghcr.io")
		require.NoError(t, err)
		auth, err := kc.Resolve(reg)
		require.NoError(t, err)
		assert.NotEqual(t, authn.Anonymous, auth)
	})

	t.Run("non-matching registry", func(t *testing.T) {
		reg, err := name.NewRegistry("ecr.io")
		require.NoError(t, err)
		auth, err := kc.Resolve(reg)
		require.NoError(t, err)
		assert.Equal(t, authn.Anonymous, auth)
	})
}

func TestStaticKeychain_EmptyRegistry(t *testing.T) {
	kc := &staticKeychain{registry: "", auth: &authn.Basic{Username: "x", Password: "y"}}
	reg, err := name.NewRegistry("anything.io")
	require.NoError(t, err)
	auth, err := kc.Resolve(reg)
	require.NoError(t, err)
	assert.Equal(t, authn.Anonymous, auth)
}

func TestBuildKeychain_WithCredentials(t *testing.T) {
	kc := buildKeychain(context.Background(), "ghcr.io", "user", "token", "", "")
	assert.NotNil(t, kc)
}

func TestBuildKeychain_WithoutCredentials(t *testing.T) {
	kc := buildKeychain(context.Background(), "", "", "", "", "")
	assert.NotNil(t, kc)
}

func TestScafctlKeychain_Resolve(t *testing.T) {
	// Create a temp directory simulating scafctl config.
	tmpDir := t.TempDir()

	// Write a container auth file.
	authFile := filepath.Join(tmpDir, "auth.json")
	creds := base64.StdEncoding.EncodeToString([]byte("testuser:testtoken"))
	dc := dockerConfig{
		Auths: map[string]dockerAuth{
			"ghcr.io": {Auth: creds},
		},
	}
	data, err := json.Marshal(dc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(authFile, data, 0o600))

	// Write registries.json pointing to it.
	regFile := filepath.Join(tmpDir, "registries.json")
	rc := registriesConfig{
		Registries: map[string]registryEntry{
			"ghcr.io": {
				Username:          "testuser",
				ContainerAuthFile: authFile,
			},
		},
	}
	data, err = json.Marshal(rc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(regFile, data, 0o600))

	// Override config path via XDG_CONFIG_HOME.
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Rename "scafctl" subdir structure — scafctlConfigPath expects <xdg>/scafctl/registries.json.
	scafctlDir := filepath.Join(tmpDir, "scafctl")
	require.NoError(t, os.MkdirAll(scafctlDir, 0o750))
	require.NoError(t, os.Rename(regFile, filepath.Join(scafctlDir, "registries.json")))

	kc := &scafctlKeychain{}

	t.Run("known registry returns credentials", func(t *testing.T) {
		reg, err := name.NewRegistry("ghcr.io")
		require.NoError(t, err)
		auth, err := kc.Resolve(reg)
		require.NoError(t, err)
		assert.NotEqual(t, authn.Anonymous, auth)

		// Verify it's the right creds.
		authCfg, err := auth.Authorization()
		require.NoError(t, err)
		assert.Equal(t, "testuser", authCfg.Username)
		assert.Equal(t, "testtoken", authCfg.Password)
	})

	t.Run("unknown registry returns anonymous", func(t *testing.T) {
		reg, err := name.NewRegistry("registry.redhat.io")
		require.NoError(t, err)
		auth, err := kc.Resolve(reg)
		require.NoError(t, err)
		assert.Equal(t, authn.Anonymous, auth)
	})
}

func TestScafctlKeychain_NoConfig(t *testing.T) {
	// Point to a non-existent config dir.
	t.Setenv("XDG_CONFIG_HOME", "/nonexistent/path")

	kc := &scafctlKeychain{}
	reg, err := name.NewRegistry("ghcr.io")
	require.NoError(t, err)
	auth, err := kc.Resolve(reg)
	require.NoError(t, err)
	assert.Equal(t, authn.Anonymous, auth)
}

func TestDetectAuthHandler(t *testing.T) {
	tests := []struct {
		registry string
		handler  string
	}{
		{"ghcr.io", "github"},
		{"gcr.io", "gcp"},
		{"us-docker.pkg.dev", "gcp"},
		{"myacr.azurecr.io", "entra"},
		{"registry.redhat.io", ""},
		{"quay.io", ""},
		{"docker.io", ""},
	}
	for _, tt := range tests {
		t.Run(tt.registry, func(t *testing.T) {
			assert.Equal(t, tt.handler, detectAuthHandler(tt.registry))
		})
	}
}

func TestReadContainerAuth_InvalidBase64(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "auth.json")
	dc := dockerConfig{
		Auths: map[string]dockerAuth{
			"ghcr.io": {Auth: "not-valid-base64!!!"},
		},
	}
	data, err := json.Marshal(dc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(tmpFile, data, 0o600))

	auth, err := readContainerAuth(tmpFile, "ghcr.io")
	assert.Error(t, err)
	assert.Nil(t, auth)
}

func TestHostKeychain_NoHostClient(t *testing.T) {
	// When no host client is in context, hostKeychain should return anonymous.
	kc := &hostKeychain{
		ctx:     context.Background(),
		handler: "github",
		scope:   "read:packages",
	}
	reg, err := name.NewRegistry("ghcr.io")
	require.NoError(t, err)
	auth, err := kc.Resolve(reg)
	require.NoError(t, err)
	assert.Equal(t, authn.Anonymous, auth)
}

func TestHostKeychain_AutoDetectHandler(t *testing.T) {
	// When handler is empty, it should detect from registry and still return anonymous (no host client).
	kc := &hostKeychain{
		ctx: context.Background(),
	}
	reg, err := name.NewRegistry("ghcr.io")
	require.NoError(t, err)
	auth, err := kc.Resolve(reg)
	require.NoError(t, err)
	assert.Equal(t, authn.Anonymous, auth)
}

func TestDefaultScopeForHandler(t *testing.T) {
	assert.Equal(t, "read:packages,write:packages", defaultScopeForHandler("github"))
	assert.Equal(t, "https://www.googleapis.com/auth/cloud-platform", defaultScopeForHandler("gcp"))
	assert.Empty(t, defaultScopeForHandler("unknown"))
}

func TestScafctlConfigPath_XDGOverride(t *testing.T) {
	// When XDG_CONFIG_HOME is set, it takes priority over HOME.
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	path := scafctlConfigPath("registries.json")
	assert.Equal(t, "/custom/config/scafctl/registries.json", path)
}
