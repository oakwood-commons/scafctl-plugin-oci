package oci

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	sdkplugin "github.com/oakwood-commons/scafctl-plugin-sdk/plugin"
	"gopkg.in/yaml.v3"
)

// registryAuthHandlers maps well-known registries to the scafctl auth handler
// that can produce tokens for them.
var registryAuthHandlers = map[string]string{
	"ghcr.io":              "github",
	"gcr.io":               "gcp",
	"pkg.dev":              "gcp",
	"azurecr.io":           "entra",
	"docker.io":            "",
	"registry-1.docker.io": "",
	"index.docker.io":      "",
}

// --- staticKeychain: explicit credentials from settings ---

type staticKeychain struct {
	registry string
	auth     authn.Authenticator
}

func (k *staticKeychain) Resolve(resource authn.Resource) (authn.Authenticator, error) {
	if k.registry == "" {
		return authn.Anonymous, nil
	}
	target := resource.RegistryStr()
	if target == k.registry || (target == name.DefaultRegistry && k.registry == "docker.io") {
		return k.auth, nil
	}
	return authn.Anonymous, nil
}

func newStaticKeychain(registry, username, password string) authn.Keychain {
	return &staticKeychain{
		registry: registry,
		auth:     &authn.Basic{Username: username, Password: password},
	}
}

// --- hostKeychain: calls back to scafctl host broker for tokens ---

type hostKeychain struct {
	ctx      context.Context
	handler  string
	scope    string
	username string
}

func (k *hostKeychain) Resolve(resource authn.Resource) (authn.Authenticator, error) {
	hc := sdkplugin.HostClientFromContext(k.ctx)
	if hc == nil {
		return authn.Anonymous, nil
	}

	handler := k.handler
	if handler == "" {
		detected, known := detectAuthHandler(resource.RegistryStr())
		if known {
			if detected == "" {
				// Registry is explicitly mapped as "no handler needed" (e.g., docker.io).
				return authn.Anonymous, nil
			}
			handler = detected
		}
	}

	// For unknown registries, look up the handler from the host's config.
	// customOAuth2 entries in config.yaml map registries to auth handlers
	// (e.g., fcr.ford.com → ford-quay).
	registryHost := resource.RegistryStr()
	var mappedUsername string
	if handler == "" {
		if mapping := lookupRegistryHandler(registryHost); mapping != nil {
			handler = mapping.Handler
			mappedUsername = mapping.Username
		}
	}

	// Last resort: try registry hostname itself as the handler.
	if handler == "" {
		handler = registryHost
	}

	return k.tryHandler(hc, handler, registryHost, mappedUsername)
}

// tryHandler attempts to get a token from a specific auth handler.
func (k *hostKeychain) tryHandler(hc *sdkplugin.HostServiceClient, handler, registryHost, mappedUsername string) (authn.Authenticator, error) {
	scope := k.scope
	if scope == "" {
		scope = defaultScopeForHandler(handler)
	}

	resp, err := hc.GetAuthToken(k.ctx, handler, scope, 60, false)
	if err != nil {
		// If the handler or scope were explicitly configured, surface the error
		// so callers can distinguish broker failures from anonymous fallback.
		if k.handler != "" || k.scope != "" {
			return nil, fmt.Errorf("host auth broker (%s): %w", handler, err)
		}
		return authn.Anonymous, nil
	}

	username := k.username
	if username == "" {
		// Prefer username from customOAuth2 config.yaml mapping (e.g., $oauthtoken).
		if mappedUsername != "" {
			username = mappedUsername
		}
	}
	if username == "" {
		// Fall back to registries.json for a username hint.
		// Look up by registry hostname first (keys in registries.json are
		// registry hostnames), then fall back to handler name.
		if cfg, err := loadRegistriesConfig(); err == nil {
			if entry, ok := cfg.Registries[registryHost]; ok && entry.Username != "" {
				username = entry.Username
			} else if entry, ok := cfg.Registries[handler]; ok && entry.Username != "" {
				username = entry.Username
			}
		}
	}
	if username == "" {
		username = "x-access-token"
	}

	return &authn.Basic{Username: username, Password: resp.AccessToken}, nil
}

// --- scafctlKeychain: reads from scafctl's own credential store ---

// scafctlKeychain reads credentials from scafctl's registries.json and
// the associated container auth files. This allows the plugin to use
// credentials stored by `scafctl catalog login` without requiring Docker
// or `--write-registry-auth`.
type scafctlKeychain struct{}

// registriesConfig represents the structure of ~/.config/scafctl/registries.json.
type registriesConfig struct {
	Registries map[string]registryEntry `json:"registries"`
}

type registryEntry struct {
	Username          string `json:"username"`
	ContainerAuthFile string `json:"containerAuthFile"`
}

// dockerConfig represents a container auth file (Docker/Podman format).
type dockerConfig struct {
	Auths map[string]dockerAuth `json:"auths"`
}

type dockerAuth struct {
	Auth string `json:"auth"` // base64(username:password)
}

func (k *scafctlKeychain) Resolve(resource authn.Resource) (authn.Authenticator, error) {
	registry := resource.RegistryStr()

	cfg, err := loadRegistriesConfig()
	if err == nil {
		// Look up registry in scafctl's config.
		entry, ok := cfg.Registries[registry]
		if !ok {
			// Try canonical name mapping (index.docker.io ↔ docker.io).
			if registry == name.DefaultRegistry {
				entry, ok = cfg.Registries["docker.io"]
			}
		}

		if ok {
			// If there's a containerAuthFile, read creds from it.
			if entry.ContainerAuthFile != "" {
				auth, err := readContainerAuth(entry.ContainerAuthFile, registry)
				if err == nil && auth != nil {
					return auth, nil
				}
			}
		}
	}

	// Always try the default container auth locations, even if the registry
	// wasn't in registries.json. Credentials stored by `cldctl catalog login`
	// or similar tools may be in these files.
	for _, path := range scafctlAuthFilePaths() {
		auth, err := readContainerAuth(path, registry)
		if err == nil && auth != nil {
			return auth, nil
		}
	}

	return authn.Anonymous, nil
}

// loadRegistriesConfig reads scafctl's registries.json.
// Checks both scafctl and cldctl config directories.
func loadRegistriesConfig() (*registriesConfig, error) {
	for _, app := range []string{"scafctl", "cldctl"} {
		path := configPath(app, "registries.json")
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path) //nolint:gosec // path from config, not user input
		if err != nil {
			continue
		}
		var cfg registriesConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}
		if len(cfg.Registries) > 0 {
			return &cfg, nil
		}
	}
	return nil, fmt.Errorf("no registries config found")
}

// readContainerAuth reads a Docker/Podman-format auth file and extracts
// credentials for the given registry.
func readContainerAuth(path, registry string) (authn.Authenticator, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path from scafctl config, not user input
	if err != nil {
		return nil, err
	}
	var dc dockerConfig
	if err := json.Unmarshal(data, &dc); err != nil {
		return nil, err
	}

	// Try exact match, then with/without scheme.
	auth, ok := dc.Auths[registry]
	if !ok {
		auth, ok = dc.Auths["https://"+registry]
	}
	if !ok && registry == name.DefaultRegistry {
		auth, ok = dc.Auths["docker.io"]
		if !ok {
			auth, ok = dc.Auths["https://index.docker.io/v1/"]
		}
	}
	if !ok || auth.Auth == "" {
		return nil, nil
	}

	decoded, err := base64.StdEncoding.DecodeString(auth.Auth)
	if err != nil {
		return nil, err
	}

	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return nil, nil
	}

	return &authn.Basic{Username: parts[0], Password: parts[1]}, nil
}

// configPath returns the path to a file in an application's config directory.
func configPath(app, filename string) string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, app, filename)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if runtime.GOOS == "windows" {
		if appdata := os.Getenv("LOCALAPPDATA"); appdata != "" {
			return filepath.Join(appdata, app, filename)
		}
	}
	return filepath.Join(home, ".config", app, filename)
}

// scafctlConfigPath returns the path to a file in scafctl's config directory.
func scafctlConfigPath(filename string) string {
	return configPath("scafctl", filename)
}

// scafctlAuthFilePaths returns candidate paths for scafctl's container auth files.
func scafctlAuthFilePaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	paths := []string{
		filepath.Join(home, ".config", "containers", "auth.json"),
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		paths = append(paths, filepath.Join(xdg, "containers", "auth.json"))
	}
	return paths
}

// --- host config: resolve registry → auth handler from config.yaml ---

// registryHandlerMapping holds a resolved registry-to-handler mapping from config.yaml.
type registryHandlerMapping struct {
	Handler  string
	Username string
}

// hostConfig represents the auth-relevant portion of config.yaml.
type hostConfig struct {
	Auth struct {
		CustomOAuth2 []customOAuth2Entry `yaml:"customOAuth2"`
	} `yaml:"auth"`
}

type customOAuth2Entry struct {
	Name             string `yaml:"name"`
	Registry         string `yaml:"registry"`
	RegistryUsername string `yaml:"registryUsername"`
}

// lookupRegistryHandler checks the host's config.yaml for a customOAuth2 entry
// whose registry field matches the target. This maps enterprise registries
// (e.g., fcr.ford.com) to their auth handler (e.g., ford-quay).
func lookupRegistryHandler(registry string) *registryHandlerMapping {
	for _, app := range []string{"cldctl", "scafctl"} {
		cfgPath := configPath(app, "config.yaml")
		if cfgPath == "" {
			continue
		}
		data, err := os.ReadFile(cfgPath) //nolint:gosec // path from config dir, not user input
		if err != nil {
			continue
		}
		var cfg hostConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			continue
		}
		for _, entry := range cfg.Auth.CustomOAuth2 {
			if entry.Registry == registry && entry.Name != "" {
				return &registryHandlerMapping{
					Handler:  entry.Name,
					Username: entry.RegistryUsername,
				}
			}
		}
	}
	return nil
}

// --- helpers ---

// detectAuthHandler returns the auth handler for a known registry.
// Returns (handler, true) if the registry is in the well-known map
// (handler may be "" meaning "no handler needed, use anonymous").
// Returns ("", false) if the registry is unknown.
func detectAuthHandler(registry string) (string, bool) {
	if h, ok := registryAuthHandlers[registry]; ok {
		return h, true
	}
	for suffix, handler := range registryAuthHandlers {
		if handler != "" && strings.HasSuffix(registry, "."+suffix) {
			return handler, true
		}
	}
	return "", false
}

func defaultScopeForHandler(handler string) string {
	switch handler {
	case "github":
		return "read:packages,write:packages"
	case "gcp":
		return "https://www.googleapis.com/auth/cloud-platform"
	default:
		return ""
	}
}

// buildKeychain constructs a multi-keychain with priority:
//  1. Explicit credentials from settings (username/password)
//  2. scafctl/cldctl credential store (per-registry creds from catalog login)
//  3. Default Docker/Podman keychain (~/.docker/config.json)
//  4. Host auth token via broker (scafctl/cldctl solution context)
//
// The host broker is last because it returns handler-scoped tokens (e.g.,
// a GitHub token) that may not match the target registry's account. Per-
// registry credentials from the credential store or Docker config are more
// specific and should take precedence.
//
// Anonymous access works naturally: if none of the keychains return
// credentials for a registry, go-containerregistry falls through to
// anonymous. Public registries (e.g., registry.redhat.io, quay.io
// public repos) work without any login.
func buildKeychain(ctx context.Context, registryHost, username, password, authHandler, scope string) authn.Keychain {
	var keychains []authn.Keychain

	// Priority 1: Explicit credentials from settings.
	if username != "" && password != "" {
		keychains = append(keychains, newStaticKeychain(registryHost, username, password))
	}

	// Priority 2: scafctl/cldctl's own credential store.
	keychains = append(keychains, &scafctlKeychain{})

	// Priority 3: Docker/Podman config (~/.docker/config.json).
	keychains = append(keychains, authn.DefaultKeychain)

	// Priority 4: Host auth token (from scafctl/cldctl solution context).
	if ctx != nil {
		keychains = append(keychains, &hostKeychain{
			ctx:      ctx,
			handler:  authHandler,
			scope:    scope,
			username: username,
		})
	}

	return authn.NewMultiKeychain(keychains...)
}

// getSettingString extracts a string value from a json.RawMessage settings map.
func getSettingString(settings map[string]json.RawMessage, key string) string {
	raw, ok := settings[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// getSettingBool extracts a boolean value from a json.RawMessage settings map.
func getSettingBool(settings map[string]json.RawMessage, key string) bool {
	raw, ok := settings[key]
	if !ok {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false
	}
	return b
}
