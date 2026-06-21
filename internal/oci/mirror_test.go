package oci

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDestinationRegistry spins up a second, independent registry. Unlike the
// shared-storage same-registry case, blobs are NOT globally visible here, so a
// manifest-only copy leaves referenced blobs missing -- the strict-registry
// behavior that issue #17 reproduces.
func newDestinationRegistry(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	return srv
}

// pushMultiArchIndex writes a two-platform (linux/amd64 + linux/arm64) index to
// the test registry and returns its reference string.
func pushMultiArchIndex(t *testing.T, srv *httptest.Server, repoTag string) string {
	t.Helper()
	addr := srv.Listener.Addr().String()
	refStr := fmt.Sprintf("%s/%s", addr, repoTag)
	ref, err := name.ParseReference(refStr)
	require.NoError(t, err)

	idx := v1.ImageIndex(empty.Index)
	for _, plat := range []struct{ os, arch string }{{"linux", "amd64"}, {"linux", "arm64"}} {
		img, imgErr := random.Image(256, 1)
		require.NoError(t, imgErr)

		cfg, cfgErr := img.ConfigFile()
		require.NoError(t, cfgErr)
		cfg.OS = plat.os
		cfg.Architecture = plat.arch

		img, imgErr = mutateConfigFile(img, cfg)
		require.NoError(t, imgErr)

		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{
			Add: img,
			Descriptor: v1.Descriptor{
				Platform: &v1.Platform{OS: plat.os, Architecture: plat.arch},
			},
		})
	}

	require.NoError(t, remote.WriteIndex(ref, idx))
	return refStr
}

// TestCopy_ReplicatesBlobs is the regression test for issue #17: copy must
// upload the config + layer blobs (not just the manifest) so the destination
// image is fully resolvable. validate.Image fetches every referenced blob.
func TestCopy_ReplicatesBlobs(t *testing.T) {
	srv, p := setupRegistry(t)
	srcAddr := srv.Listener.Addr().String()
	pushRandomImage(t, srv, "myorg/src:v1")

	dstSrv := newDestinationRegistry(t)
	dstAddr := dstSrv.Listener.Addr().String()

	ctx := context.Background()
	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpCopy,
		"src":       fmt.Sprintf("%s/myorg/src:v1", srcAddr),
		"dst":       fmt.Sprintf("%s/myorg/dst:v1", dstAddr),
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])
	assert.Equal(t, false, data["skipped"])
	assert.NotEmpty(t, data["mediaType"])

	dstRef, err := name.ParseReference(fmt.Sprintf("%s/myorg/dst:v1", dstAddr))
	require.NoError(t, err)
	desc, err := remote.Get(dstRef)
	require.NoError(t, err)
	img, err := desc.Image()
	require.NoError(t, err)
	// Fails if blobs were not replicated to the destination (the pre-fix behavior).
	require.NoError(t, validate.Image(img))
}

// TestCopy_MultiArchIndexReplicatesChildren is the regression test for the
// multi-arch half of issue #17: copy must recurse into index children and
// upload each child's blobs.
func TestCopy_MultiArchIndexReplicatesChildren(t *testing.T) {
	srv, p := setupRegistry(t)
	srcAddr := srv.Listener.Addr().String()
	pushMultiArchIndex(t, srv, "myorg/src:v1")

	dstSrv := newDestinationRegistry(t)
	dstAddr := dstSrv.Listener.Addr().String()

	ctx := context.Background()
	out, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpCopy,
		"src":       fmt.Sprintf("%s/myorg/src:v1", srcAddr),
		"dst":       fmt.Sprintf("%s/myorg/dst:v1", dstAddr),
	})
	require.NoError(t, err)

	data, ok := out.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["success"])

	dstRef, err := name.ParseReference(fmt.Sprintf("%s/myorg/dst:v1", dstAddr))
	require.NoError(t, err)
	desc, err := remote.Get(dstRef)
	require.NoError(t, err)
	require.True(t, desc.MediaType.IsIndex())

	idx, err := desc.ImageIndex()
	require.NoError(t, err)
	im, err := idx.IndexManifest()
	require.NoError(t, err)
	require.Len(t, im.Manifests, 2)

	for _, m := range im.Manifests {
		child, cerr := idx.Image(m.Digest)
		require.NoError(t, cerr)
		require.NoError(t, validate.Image(child))
	}
}

// TestCopy_PlatformSelector verifies issue #18A: copy can narrow a multi-arch
// index to a single platform image.
func TestCopy_PlatformSelector(t *testing.T) {
	srv, p := setupRegistry(t)
	srcAddr := srv.Listener.Addr().String()
	pushMultiArchIndex(t, srv, "myorg/src:v1")

	dstSrv := newDestinationRegistry(t)
	dstAddr := dstSrv.Listener.Addr().String()

	ctx := context.Background()
	_, err := p.ExecuteProvider(ctx, ProviderName, map[string]any{
		"operation": OpCopy,
		"src":       fmt.Sprintf("%s/myorg/src:v1", srcAddr),
		"dst":       fmt.Sprintf("%s/myorg/dst:amd64", dstAddr),
		"platform":  "linux/amd64",
	})
	require.NoError(t, err)

	dstRef, err := name.ParseReference(fmt.Sprintf("%s/myorg/dst:amd64", dstAddr))
	require.NoError(t, err)
	desc, err := remote.Get(dstRef)
	require.NoError(t, err)
	require.True(t, desc.MediaType.IsImage())

	img, err := desc.Image()
	require.NoError(t, err)
	cfg, err := img.ConfigFile()
	require.NoError(t, err)
	assert.Equal(t, "amd64", cfg.Architecture)
	require.NoError(t, validate.Image(img))
}

// TestCopy_SkipIfExists verifies issue #18B: a second copy with skipIfExists is
// a no-op once the destination digest matches.
func TestCopy_SkipIfExists(t *testing.T) {
	srv, p := setupRegistry(t)
	addr := srv.Listener.Addr().String()
	pushRandomImage(t, srv, "myorg/src:v1")

	ctx := context.Background()
	args := map[string]any{
		"operation":    OpCopy,
		"src":          fmt.Sprintf("%s/myorg/src:v1", addr),
		"dst":          fmt.Sprintf("%s/myorg/dst:v1", addr),
		"skipIfExists": true,
	}

	out1, err := p.ExecuteProvider(ctx, ProviderName, args)
	require.NoError(t, err)
	assert.Equal(t, false, out1.Data.(map[string]any)["skipped"])

	out2, err := p.ExecuteProvider(ctx, ProviderName, args)
	require.NoError(t, err)
	assert.Equal(t, true, out2.Data.(map[string]any)["skipped"])
}

// TestCopy_ForceFalseSkips verifies the "force: false" alias for skipIfExists.
func TestCopy_ForceFalseSkips(t *testing.T) {
	srv, p := setupRegistry(t)
	addr := srv.Listener.Addr().String()
	pushRandomImage(t, srv, "myorg/src:v1")

	ctx := context.Background()
	args := map[string]any{
		"operation": OpCopy,
		"src":       fmt.Sprintf("%s/myorg/src:v1", addr),
		"dst":       fmt.Sprintf("%s/myorg/dst:v1", addr),
		"force":     false,
	}

	_, err := p.ExecuteProvider(ctx, ProviderName, args)
	require.NoError(t, err)

	out2, err := p.ExecuteProvider(ctx, ProviderName, args)
	require.NoError(t, err)
	assert.Equal(t, true, out2.Data.(map[string]any)["skipped"])
}

// TestCopy_PreservesReferrers verifies issue #18D: cosign signatures and other
// referrers attached to the source are mirrored to the destination.
func TestCopy_PreservesReferrers(t *testing.T) {
	srv, p := setupRegistry(t)
	srcAddr := srv.Listener.Addr().String()

	subjectRefStr := fmt.Sprintf("%s/myorg/src:v1", srcAddr)
	subjectRef, err := name.ParseReference(subjectRefStr)
	require.NoError(t, err)
	subjectImg, err := random.Image(256, 1)
	require.NoError(t, err)
	require.NoError(t, remote.Write(subjectRef, subjectImg))

	subjectDigest, err := subjectImg.Digest()
	require.NoError(t, err)
	subjectMT, err := subjectImg.MediaType()
	require.NoError(t, err)
	subjectSize, err := subjectImg.Size()
	require.NoError(t, err)

	// Build a referrer (signature-like) pointing at the subject, push by digest.
	referrer, err := random.Image(128, 1)
	require.NoError(t, err)
	referrer = mutate.Subject(referrer, v1.Descriptor{
		MediaType: subjectMT,
		Size:      subjectSize,
		Digest:    subjectDigest,
	}).(v1.Image)
	referrerDigest, err := referrer.Digest()
	require.NoError(t, err)
	require.NoError(t, remote.Write(subjectRef.Context().Digest(referrerDigest.String()), referrer))

	// Sanity: the source exposes exactly one referrer.
	srcReferrers, err := remote.Referrers(subjectRef.Context().Digest(subjectDigest.String()))
	require.NoError(t, err)
	srcManifest, err := srcReferrers.IndexManifest()
	require.NoError(t, err)
	require.Len(t, srcManifest.Manifests, 1)

	// Copy to a separate destination registry.
	dstSrv := newDestinationRegistry(t)
	dstAddr := dstSrv.Listener.Addr().String()
	out, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{
		"operation": OpCopy,
		"src":       subjectRefStr,
		"dst":       fmt.Sprintf("%s/myorg/dst:v1", dstAddr),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, out.Data.(map[string]any)["referrersCopied"])

	// The destination must now expose the referrer for the same subject digest.
	dstRef, err := name.ParseReference(fmt.Sprintf("%s/myorg/dst:v1", dstAddr))
	require.NoError(t, err)
	dstReferrers, err := remote.Referrers(dstRef.Context().Digest(subjectDigest.String()))
	require.NoError(t, err)
	dstManifest, err := dstReferrers.IndexManifest()
	require.NoError(t, err)
	require.Len(t, dstManifest.Manifests, 1)
}

// TestCopy_SkipIfExistsWithPlatform verifies that idempotency works when a
// narrowed (single-platform) copy is combined with skipIfExists. The written
// manifest is the platform image, not the source index, so the skip comparison
// must use the resolved image digest rather than the index digest.
func TestCopy_SkipIfExistsWithPlatform(t *testing.T) {
	srv, p := setupRegistry(t)
	srcAddr := srv.Listener.Addr().String()
	pushMultiArchIndex(t, srv, "myorg/src:v1")

	dstSrv := newDestinationRegistry(t)
	dstAddr := dstSrv.Listener.Addr().String()

	ctx := context.Background()
	args := map[string]any{
		"operation":    OpCopy,
		"src":          fmt.Sprintf("%s/myorg/src:v1", srcAddr),
		"dst":          fmt.Sprintf("%s/myorg/dst:amd64", dstAddr),
		"platform":     "linux/amd64",
		"skipIfExists": true,
	}

	out1, err := p.ExecuteProvider(ctx, ProviderName, args)
	require.NoError(t, err)
	assert.Equal(t, false, out1.Data.(map[string]any)["skipped"])

	out2, err := p.ExecuteProvider(ctx, ProviderName, args)
	require.NoError(t, err)
	assert.Equal(t, true, out2.Data.(map[string]any)["skipped"], "narrowed re-copy must be skipped once the platform image digest matches")
}

func TestParseCopyPlatform(t *testing.T) {
	tests := []struct {
		name      string
		input     map[string]any
		wantAll   bool
		wantPlat  string
		wantError bool
	}{
		{name: "empty means all", input: map[string]any{}, wantAll: true},
		{name: "all keyword", input: map[string]any{"platform": "all"}, wantAll: true},
		{name: "all keyword case-insensitive", input: map[string]any{"platform": "ALL"}, wantAll: true},
		{name: "specific platform", input: map[string]any{"platform": "linux/amd64"}, wantPlat: "linux/amd64"},
		{name: "invalid platform", input: map[string]any{"platform": "garbage"}, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plat, all, err := parseCopyPlatform(tt.input)
			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantAll, all)
			if tt.wantPlat == "" {
				assert.Nil(t, plat)
			} else {
				require.NotNil(t, plat)
				assert.Equal(t, tt.wantPlat, plat.OS+"/"+plat.Architecture)
			}
		})
	}
}

func TestWantSkip(t *testing.T) {
	tests := []struct {
		name    string
		input   map[string]any
		want    bool
		wantErr bool
	}{
		{name: "unset", input: map[string]any{}, want: false},
		{name: "skipIfExists true", input: map[string]any{"skipIfExists": true}, want: true},
		{name: "skipIfExists string", input: map[string]any{"skipIfExists": "true"}, want: true},
		{name: "force false skips", input: map[string]any{"force": false}, want: true},
		{name: "force true does not skip", input: map[string]any{"force": true}, want: false},
		{name: "invalid", input: map[string]any{"skipIfExists": 7}, wantErr: true},
		{name: "both present is ambiguous", input: map[string]any{"skipIfExists": true, "force": false}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := wantSkip(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWantPreserveReferrers(t *testing.T) {
	got, err := wantPreserveReferrers(map[string]any{})
	require.NoError(t, err)
	assert.True(t, got, "defaults to true")

	got, err = wantPreserveReferrers(map[string]any{"preserveReferrers": false})
	require.NoError(t, err)
	assert.False(t, got)

	_, err = wantPreserveReferrers(map[string]any{"preserveReferrers": 3})
	require.Error(t, err)
}

func TestParseRetry(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		wantOpts int
		wantErr  bool
	}{
		{name: "unset", input: map[string]any{}, wantOpts: 0},
		{name: "bool false", input: map[string]any{"retry": false}, wantOpts: 0},
		{name: "bool true", input: map[string]any{"retry": true}, wantOpts: 1},
		{name: "integer", input: map[string]any{"retry": 5}, wantOpts: 1},
		{name: "numeric string", input: map[string]any{"retry": "4"}, wantOpts: 1},
		{name: "zero disables", input: map[string]any{"retry": 0}, wantOpts: 0},
		{name: "map full", input: map[string]any{"retry": map[string]any{"attempts": 4, "backoff": "2s", "maxBackoff": "10s"}}, wantOpts: 1},
		{name: "map default attempts", input: map[string]any{"retry": map[string]any{"backoff": "1s"}}, wantOpts: 1},
		{name: "bad backoff", input: map[string]any{"retry": map[string]any{"backoff": "nope"}}, wantErr: true},
		{name: "bad type", input: map[string]any{"retry": []any{1, 2}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseRetry(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, opts, tt.wantOpts)
		})
	}
}

func TestMapRegistryError(t *testing.T) {
	t.Run("passes through non-transport errors", func(t *testing.T) {
		err := mapRegistryError("copy", errors.New("boom"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "copy: boom")
	})

	t.Run("manifest invalid yields blob-ordering hint", func(t *testing.T) {
		terr := &transport.Error{
			StatusCode: 400,
			Errors:     []transport.Diagnostic{{Code: transport.ManifestInvalidErrorCode, Message: "bad"}},
		}
		err := mapRegistryError("copy", terr)
		assert.Contains(t, err.Error(), "blob-before-manifest")
		assert.ErrorIs(t, err, terr)
	})

	t.Run("unauthorized yields auth hint", func(t *testing.T) {
		terr := &transport.Error{
			StatusCode: 401,
			Errors:     []transport.Diagnostic{{Code: transport.UnauthorizedErrorCode, Message: "no"}},
		}
		err := mapRegistryError("push", terr)
		assert.Contains(t, err.Error(), "auth login")
	})

	t.Run("rate limit yields retry hint", func(t *testing.T) {
		terr := &transport.Error{
			StatusCode: 429,
			Errors:     []transport.Diagnostic{{Code: transport.TooManyRequestsErrorCode, Message: "slow down"}},
		}
		err := mapRegistryError("pull", terr)
		assert.Contains(t, err.Error(), "retry")
	})

	t.Run("status-only fallback", func(t *testing.T) {
		terr := &transport.Error{StatusCode: 403}
		err := mapRegistryError("push", terr)
		assert.Contains(t, err.Error(), "access denied")
	})

	t.Run("manifest unknown yields existence hint", func(t *testing.T) {
		terr := &transport.Error{
			StatusCode: 404,
			Errors:     []transport.Diagnostic{{Code: transport.ManifestUnknownErrorCode, Message: "missing"}},
		}
		err := mapRegistryError("pull", terr)
		assert.Contains(t, err.Error(), "does not exist")
	})

	t.Run("denied yields scope hint", func(t *testing.T) {
		terr := &transport.Error{
			StatusCode: 403,
			Errors:     []transport.Diagnostic{{Code: transport.DeniedErrorCode, Message: "denied"}},
		}
		err := mapRegistryError("push", terr)
		assert.Contains(t, err.Error(), "access denied")
	})

	t.Run("invalid tag yields naming hint", func(t *testing.T) {
		terr := &transport.Error{
			StatusCode: 400,
			Errors:     []transport.Diagnostic{{Code: transport.TagInvalidErrorCode, Message: "bad tag"}},
		}
		err := mapRegistryError("copy", terr)
		assert.Contains(t, err.Error(), "reference is invalid")
	})

	t.Run("unauthorized status fallback", func(t *testing.T) {
		terr := &transport.Error{StatusCode: 401}
		err := mapRegistryError("pull", terr)
		assert.Contains(t, err.Error(), "authentication required")
	})

	t.Run("rate-limit status fallback", func(t *testing.T) {
		terr := &transport.Error{StatusCode: 429}
		err := mapRegistryError("copy", terr)
		assert.Contains(t, err.Error(), "rate limited")
	})

	t.Run("not-found status fallback", func(t *testing.T) {
		terr := &transport.Error{StatusCode: 404}
		err := mapRegistryError("pull", terr)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("unmapped status passes through", func(t *testing.T) {
		terr := &transport.Error{StatusCode: 500}
		err := mapRegistryError("copy", terr)
		assert.Contains(t, err.Error(), "copy:")
		assert.ErrorIs(t, err, terr)
	})

	t.Run("nil error", func(t *testing.T) {
		assert.NoError(t, mapRegistryError("copy", nil))
	})
}

func TestToDuration(t *testing.T) {
	d, err := toDuration("1500ms")
	require.NoError(t, err)
	assert.Equal(t, 1500*time.Millisecond, d)

	d, err = toDuration(2)
	require.NoError(t, err)
	assert.Equal(t, 2*time.Second, d)

	d, err = toDuration(int64(3))
	require.NoError(t, err)
	assert.Equal(t, 3*time.Second, d)

	d, err = toDuration(1.5)
	require.NoError(t, err)
	assert.Equal(t, 1500*time.Millisecond, d)

	_, err = toDuration([]int{1})
	require.Error(t, err)

	_, err = toDuration("not-a-duration")
	require.Error(t, err)
}

func TestToInt(t *testing.T) {
	tests := []struct {
		name    string
		in      any
		want    int
		wantErr bool
	}{
		{name: "int", in: 5, want: 5},
		{name: "int64", in: int64(6), want: 6},
		{name: "float64", in: 7.0, want: 7},
		{name: "string", in: "8", want: 8},
		{name: "non-integer float", in: 1.9, wantErr: true},
		{name: "bad string", in: "nope", wantErr: true},
		{name: "bad type", in: []int{1}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toInt(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// writeReferrer attaches a single image referrer to subject and returns nothing.
func writeReferrer(t *testing.T, subjectRef name.Reference, subject v1.Descriptor) {
	t.Helper()
	referrer, err := random.Image(128, 1)
	require.NoError(t, err)
	referrer = mutate.Subject(referrer, subject).(v1.Image)
	rDigest, err := referrer.Digest()
	require.NoError(t, err)
	require.NoError(t, remote.Write(subjectRef.Context().Digest(rDigest.String()), referrer))
}

// subjectDescriptor returns the OCI descriptor for an image subject.
func subjectDescriptor(t *testing.T, img v1.Image) v1.Descriptor {
	t.Helper()
	digest, err := img.Digest()
	require.NoError(t, err)
	mt, err := img.MediaType()
	require.NoError(t, err)
	size, err := img.Size()
	require.NoError(t, err)
	return v1.Descriptor{MediaType: mt, Size: size, Digest: digest}
}

// TestCopy_SkipReconcilesNewReferrers verifies that a skipped copy still
// mirrors referrers attached to the source after the subject was first copied.
func TestCopy_SkipReconcilesNewReferrers(t *testing.T) {
	srv, p := setupRegistry(t)
	srcAddr := srv.Listener.Addr().String()

	subjectRefStr := fmt.Sprintf("%s/myorg/src:v1", srcAddr)
	subjectRef, err := name.ParseReference(subjectRefStr)
	require.NoError(t, err)
	subjectImg, err := random.Image(256, 1)
	require.NoError(t, err)
	require.NoError(t, remote.Write(subjectRef, subjectImg))

	dstSrv := newDestinationRegistry(t)
	dstAddr := dstSrv.Listener.Addr().String()
	args := map[string]any{
		"operation":    OpCopy,
		"src":          subjectRefStr,
		"dst":          fmt.Sprintf("%s/myorg/dst:v1", dstAddr),
		"skipIfExists": true,
	}

	ctx := context.Background()
	out1, err := p.ExecuteProvider(ctx, ProviderName, args)
	require.NoError(t, err)
	assert.Equal(t, false, out1.Data.(map[string]any)["skipped"])
	assert.Equal(t, 0, out1.Data.(map[string]any)["referrersCopied"])

	// Attach a referrer to the (unchanged) source subject after the first copy.
	writeReferrer(t, subjectRef, subjectDescriptor(t, subjectImg))

	out2, err := p.ExecuteProvider(ctx, ProviderName, args)
	require.NoError(t, err)
	assert.Equal(t, true, out2.Data.(map[string]any)["skipped"], "subject digest unchanged, so the manifest write is skipped")
	assert.Equal(t, 1, out2.Data.(map[string]any)["referrersCopied"], "the new referrer must still be mirrored on a skipped copy")

	subjectDigest, err := subjectImg.Digest()
	require.NoError(t, err)
	dstRef, err := name.ParseReference(fmt.Sprintf("%s/myorg/dst:v1", dstAddr))
	require.NoError(t, err)
	dstReferrers, err := remote.Referrers(dstRef.Context().Digest(subjectDigest.String()))
	require.NoError(t, err)
	dstManifest, err := dstReferrers.IndexManifest()
	require.NoError(t, err)
	require.Len(t, dstManifest.Manifests, 1)
}

// TestCopy_PreservesIndexReferrer covers an index-typed referrer, exercising
// the WriteIndex branch of copyReferrers.
func TestCopy_PreservesIndexReferrer(t *testing.T) {
	srv, p := setupRegistry(t)
	srcAddr := srv.Listener.Addr().String()

	subjectRefStr := fmt.Sprintf("%s/myorg/src:v1", srcAddr)
	subjectRef, err := name.ParseReference(subjectRefStr)
	require.NoError(t, err)
	subjectImg, err := random.Image(256, 1)
	require.NoError(t, err)
	require.NoError(t, remote.Write(subjectRef, subjectImg))

	idxReferrer, err := random.Index(128, 1, 1)
	require.NoError(t, err)
	idxReferrer = mutate.Subject(idxReferrer, subjectDescriptor(t, subjectImg)).(v1.ImageIndex)
	rDigest, err := idxReferrer.Digest()
	require.NoError(t, err)
	require.NoError(t, remote.WriteIndex(subjectRef.Context().Digest(rDigest.String()), idxReferrer))

	dstSrv := newDestinationRegistry(t)
	dstAddr := dstSrv.Listener.Addr().String()
	out, err := p.ExecuteProvider(context.Background(), ProviderName, map[string]any{
		"operation": OpCopy,
		"src":       subjectRefStr,
		"dst":       fmt.Sprintf("%s/myorg/dst:v1", dstAddr),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, out.Data.(map[string]any)["referrersCopied"])

	subjectDigest, err := subjectImg.Digest()
	require.NoError(t, err)
	dstRef, err := name.ParseReference(fmt.Sprintf("%s/myorg/dst:v1", dstAddr))
	require.NoError(t, err)
	dstReferrers, err := remote.Referrers(dstRef.Context().Digest(subjectDigest.String()))
	require.NoError(t, err)
	dstManifest, err := dstReferrers.IndexManifest()
	require.NoError(t, err)
	require.Len(t, dstManifest.Manifests, 1)
}

// TestDestinationComplete verifies the skip-verification helpers: honoring
// skipIfExists is only safe when every referenced blob already exists on the
// destination. This guards the issue #17 case where a manifest is present but
// its blobs are missing, which a digest-only check would wrongly treat as done.
func TestDestinationComplete(t *testing.T) {
	srv, _ := setupRegistry(t)
	addr := srv.Listener.Addr().String()

	img, err := random.Image(256, 2)
	require.NoError(t, err)

	pushedRef, err := name.ParseReference(fmt.Sprintf("%s/myorg/present:v1", addr))
	require.NoError(t, err)
	require.NoError(t, remote.Write(pushedRef, img))

	t.Run("all blobs present", func(t *testing.T) {
		ok, err := imageBlobsPresent(img, pushedRef.Context(), nil)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("blobs absent in a different registry", func(t *testing.T) {
		// A separate registry instance has independent blob storage, so the
		// pushed image's blobs are genuinely missing here.
		otherSrv := newDestinationRegistry(t)
		emptyRepo, err := name.NewRepository(fmt.Sprintf("%s/myorg/empty", otherSrv.Listener.Addr().String()))
		require.NoError(t, err)
		ok, err := imageBlobsPresent(img, emptyRepo, nil)
		require.NoError(t, err)
		assert.False(t, ok, "blobs are not in this registry, so the copy must not be skipped")
	})

	t.Run("index children present", func(t *testing.T) {
		idxRefStr := pushMultiArchIndex(t, srv, "myorg/idx:v1")
		idxRef, err := name.ParseReference(idxRefStr)
		require.NoError(t, err)
		desc, err := remote.Get(idxRef)
		require.NoError(t, err)
		idx, err := desc.ImageIndex()
		require.NoError(t, err)

		ok, err := indexContentsPresent(context.Background(), idx, idxRef.Context(), nil)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("index children absent in a different registry", func(t *testing.T) {
		idxRefStr := pushMultiArchIndex(t, srv, "myorg/idx2:v1")
		idxRef, err := name.ParseReference(idxRefStr)
		require.NoError(t, err)
		desc, err := remote.Get(idxRef)
		require.NoError(t, err)
		idx, err := desc.ImageIndex()
		require.NoError(t, err)

		otherSrv := newDestinationRegistry(t)
		emptyRepo, err := name.NewRepository(fmt.Sprintf("%s/myorg/idx2", otherSrv.Listener.Addr().String()))
		require.NoError(t, err)
		ok, err := indexContentsPresent(context.Background(), idx, emptyRepo, nil)
		require.NoError(t, err)
		assert.False(t, ok, "child manifests are missing, so the copy must not be skipped")
	})
}

// TestIsNotFoundErr confirms only registry 404s are treated as absence.
func TestIsNotFoundErr(t *testing.T) {
	assert.True(t, isNotFoundErr(&transport.Error{StatusCode: 404}))
	assert.False(t, isNotFoundErr(&transport.Error{StatusCode: 500}))
	assert.False(t, isNotFoundErr(errors.New("boom")))
	assert.False(t, isNotFoundErr(nil))
}
