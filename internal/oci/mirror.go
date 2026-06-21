package oci

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// platformAll is the sentinel value selecting the full multi-arch index.
const platformAll = "all"

// parseCopyPlatform parses the optional "platform" input for copy operations.
//
// It returns:
//   - a concrete *v1.Platform when a specific os/arch is requested (narrow copy);
//   - all=true when "all" is requested or the field is empty (copy the full index);
//   - an error when the value is malformed.
func parseCopyPlatform(input map[string]any) (plat *v1.Platform, all bool, err error) {
	raw, _ := input["platform"].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, platformAll) {
		return nil, true, nil
	}
	p, perr := parsePlatform(raw)
	if perr != nil {
		return nil, false, perr
	}
	return p, false, nil
}

// wantSkip reports whether the "skipIfExists" (alias "force"=false) input asks
// the operation to skip when the destination already holds the same content.
func wantSkip(input map[string]any) (bool, error) {
	rawSkip, hasSkip := input["skipIfExists"]
	rawForce, hasForce := input["force"]
	if hasSkip && hasForce {
		return false, fmt.Errorf("set only one of skipIfExists or force, not both")
	}
	if hasSkip {
		b, err := toBool(rawSkip)
		if err != nil {
			return false, fmt.Errorf("invalid skipIfExists: %w", err)
		}
		return b, nil
	}
	// "force: false" is the inverse opt-in.
	if hasForce {
		b, err := toBool(rawForce)
		if err != nil {
			return false, fmt.Errorf("invalid force: %w", err)
		}
		return !b, nil
	}
	return false, nil
}

// wantPreserveReferrers reports whether referrers (cosign signatures and
// attestations) should be mirrored alongside the subject. Defaults to true.
func wantPreserveReferrers(input map[string]any) (bool, error) {
	if raw, ok := input["preserveReferrers"]; ok {
		b, err := toBool(raw)
		if err != nil {
			return true, fmt.Errorf("invalid preserveReferrers: %w", err)
		}
		return b, nil
	}
	return true, nil
}

// parseRetry builds retry options from the optional "retry" input.
//
// Accepted shapes:
//   - integer / numeric string: maximum attempts (e.g. 5)
//   - bool true: enable with a sensible default attempt count
//   - map: {attempts: int, backoff: "1s", maxBackoff: "30s"}
//
// When retry is unset the go-containerregistry defaults apply (3 attempts,
// retrying 408/429/5xx), so this only overrides tuning when requested.
func parseRetry(input map[string]any) ([]remote.Option, error) {
	raw, ok := input["retry"]
	if !ok || raw == nil {
		return nil, nil
	}

	attempts := 0
	baseBackoff := time.Second
	var maxBackoff time.Duration

	switch v := raw.(type) {
	case bool:
		if !v {
			return nil, nil
		}
		attempts = 3
	case map[string]any:
		if a, has := v["attempts"]; has {
			n, err := toInt(a)
			if err != nil {
				return nil, fmt.Errorf("invalid retry.attempts: %w", err)
			}
			attempts = n
		} else {
			attempts = 3
		}
		if b, has := v["backoff"]; has {
			d, err := toDuration(b)
			if err != nil {
				return nil, fmt.Errorf("invalid retry.backoff: %w", err)
			}
			baseBackoff = d
		}
		if mb, has := v["maxBackoff"]; has {
			d, err := toDuration(mb)
			if err != nil {
				return nil, fmt.Errorf("invalid retry.maxBackoff: %w", err)
			}
			maxBackoff = d
		}
	default:
		n, err := toInt(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid retry: expected an attempt count or a {attempts, backoff} map, got %T", raw)
		}
		attempts = n
	}

	if attempts <= 0 {
		return nil, nil
	}

	backoff := remote.Backoff{
		Duration: baseBackoff,
		Factor:   2.0,
		Jitter:   0.1,
		Steps:    attempts,
		Cap:      maxBackoff,
	}
	return []remote.Option{remote.WithRetryBackoff(backoff)}, nil
}

// copyReferrers mirrors the referrers (cosign signatures, attestations, SBOMs)
// attached to subjectDigest from the source repository to the destination
// repository. It returns the number of referrer manifests copied.
//
// Writing each referrer with remote.Write/WriteIndex preserves the subject
// field and, on registries without the referrers API, updates the
// referrers fallback tag automatically.
func (p *Plugin) copyReferrers(ctx context.Context, srcRef, dstRef name.Reference, subjectDigest string, opts []remote.Option) (int, error) {
	srcSubject := srcRef.Context().Digest(subjectDigest)

	idx, err := remote.Referrers(srcSubject, opts...)
	if err != nil {
		return 0, fmt.Errorf("listing referrers: %w", err)
	}
	manifest, err := idx.IndexManifest()
	if err != nil {
		return 0, fmt.Errorf("reading referrers index: %w", err)
	}

	count := 0
	var errs []error
	for _, m := range manifest.Manifests {
		select {
		case <-ctx.Done():
			return count, ctx.Err()
		default:
		}

		digestStr := m.Digest.String()
		srcChild := srcRef.Context().Digest(digestStr)
		dstChild := dstRef.Context().Digest(digestStr)

		if err := p.copyReferrer(srcChild, dstChild, opts); err != nil {
			// A single problematic referrer (for example an artifact manifest
			// type a registry cannot model as an image) must not abort
			// mirroring of the remaining signatures, attestations, and SBOMs.
			errs = append(errs, fmt.Errorf("referrer %s: %w", digestStr, err))
			continue
		}
		count++
	}
	if len(errs) > 0 {
		return count, errors.Join(errs...)
	}
	return count, nil
}

// copyReferrer mirrors a single referrer manifest from src to dst, selecting the
// write strategy from its media type. Image and OCI artifact manifests are both
// single manifests with a config and layers, so remote.Write replicates their
// blobs and preserves the subject field; only image indexes need WriteIndex.
func (p *Plugin) copyReferrer(src, dst name.Digest, opts []remote.Option) error {
	rdesc, err := remote.Get(src, opts...)
	if err != nil {
		return mapRegistryError("fetching referrer", err)
	}

	if rdesc.MediaType.IsIndex() {
		ri, e := rdesc.ImageIndex()
		if e != nil {
			return fmt.Errorf("resolving referrer index: %w", e)
		}
		if e := remote.WriteIndex(dst, ri, opts...); e != nil {
			return mapRegistryError("writing referrer index", e)
		}
		return nil
	}

	ri, e := rdesc.Image()
	if e != nil {
		return fmt.Errorf("resolving referrer manifest: %w", e)
	}
	if e := remote.Write(dst, ri, opts...); e != nil {
		return mapRegistryError("writing referrer manifest", e)
	}
	return nil
}

// destinationComplete reports whether the destination already holds every blob
// and child manifest referenced by the content being copied. A matching
// manifest digest alone is not proof of completeness: an interrupted prior copy
// can leave the manifest present while referenced blobs are missing (the exact
// failure this provider repairs), so honoring skipIfExists on a digest match
// alone would make a broken image impossible to repair.
func (p *Plugin) destinationComplete(ctx context.Context, dst name.Reference, plat *v1.Platform, narrowedImg v1.Image, desc *remote.Descriptor, opts []remote.Option) (bool, error) {
	repo := dst.Context()
	switch {
	case plat != nil:
		return imageBlobsPresent(narrowedImg, repo, opts)
	case desc.MediaType.IsIndex():
		idx, err := desc.ImageIndex()
		if err != nil {
			return false, fmt.Errorf("resolving index: %w", err)
		}
		return indexContentsPresent(ctx, idx, repo, opts)
	case desc.MediaType.IsImage():
		img, err := desc.Image()
		if err != nil {
			return false, fmt.Errorf("resolving image: %w", err)
		}
		return imageBlobsPresent(img, repo, opts)
	default:
		// Non-standard manifests carry no separately addressable blobs we can
		// verify here, so the matching manifest digest is the best signal.
		return true, nil
	}
}

// imageBlobsPresent reports whether the config blob and every layer blob of img
// already exist in repo.
func imageBlobsPresent(img v1.Image, repo name.Repository, opts []remote.Option) (bool, error) {
	cfgName, err := img.ConfigName()
	if err != nil {
		return false, fmt.Errorf("reading config digest: %w", err)
	}
	if ok, err := blobPresent(repo, cfgName, opts); err != nil || !ok {
		return ok, err
	}

	layers, err := img.Layers()
	if err != nil {
		return false, fmt.Errorf("reading layers: %w", err)
	}
	for _, layer := range layers {
		h, err := layer.Digest()
		if err != nil {
			return false, fmt.Errorf("reading layer digest: %w", err)
		}
		if ok, err := blobPresent(repo, h, opts); err != nil || !ok {
			return ok, err
		}
	}
	return true, nil
}

// indexContentsPresent reports whether every child manifest of idx, and the
// blobs of each child image, already exist in repo.
func indexContentsPresent(ctx context.Context, idx v1.ImageIndex, repo name.Repository, opts []remote.Option) (bool, error) {
	manifest, err := idx.IndexManifest()
	if err != nil {
		return false, fmt.Errorf("reading index manifest: %w", err)
	}

	for _, child := range manifest.Manifests {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}

		// The child manifest itself must be present on the destination.
		if _, err := remote.Head(repo.Digest(child.Digest.String()), opts...); err != nil {
			if isNotFoundErr(err) {
				return false, nil
			}
			return false, fmt.Errorf("checking child %s: %w", child.Digest, err)
		}

		switch {
		case child.MediaType.IsIndex():
			sub, err := idx.ImageIndex(child.Digest)
			if err != nil {
				return false, fmt.Errorf("resolving child index %s: %w", child.Digest, err)
			}
			if ok, err := indexContentsPresent(ctx, sub, repo, opts); err != nil || !ok {
				return ok, err
			}
		case child.MediaType.IsImage():
			img, err := idx.Image(child.Digest)
			if err != nil {
				return false, fmt.Errorf("resolving child image %s: %w", child.Digest, err)
			}
			if ok, err := imageBlobsPresent(img, repo, opts); err != nil || !ok {
				return ok, err
			}
		}
	}
	return true, nil
}

// blobPresent reports whether a blob with the given digest exists in repo.
func blobPresent(repo name.Repository, h v1.Hash, opts []remote.Option) (bool, error) {
	layer, err := remote.Layer(repo.Digest(h.String()), opts...)
	if err != nil {
		return false, fmt.Errorf("addressing blob %s: %w", h, err)
	}
	if _, err := layer.Size(); err != nil {
		if isNotFoundErr(err) {
			return false, nil
		}
		return false, fmt.Errorf("checking blob %s: %w", h, err)
	}
	return true, nil
}

// isNotFoundErr reports whether err is a registry 404 (manifest or blob absent).
func isNotFoundErr(err error) bool {
	var terr *transport.Error
	if errors.As(err, &terr) {
		return terr.StatusCode == http.StatusNotFound
	}
	return false
}

// mapRegistryError augments cryptic registry/transport errors with actionable
// guidance while preserving the original error via %w.
func mapRegistryError(context string, err error) error {
	if err == nil {
		return nil
	}

	var terr *transport.Error
	if !errors.As(err, &terr) {
		return fmt.Errorf("%s: %w", context, err)
	}

	if hint := hintForDiagnostics(terr); hint != "" {
		return fmt.Errorf("%s: %s: %w", context, hint, err)
	}
	if hint := hintForStatus(terr.StatusCode); hint != "" {
		return fmt.Errorf("%s: %s: %w", context, hint, err)
	}
	return fmt.Errorf("%s: %w", context, err)
}

// hintForDiagnostics returns an actionable hint for the first recognized OCI
// error code in the transport error, or "" if none match.
func hintForDiagnostics(terr *transport.Error) string {
	for _, diag := range terr.Errors {
		switch diag.Code {
		case transport.ManifestInvalidErrorCode, transport.ManifestBlobUnknownErrorCode, transport.BlobUnknownErrorCode:
			return "the destination registry rejected the manifest because referenced blobs are missing; " +
				"this usually means a blob-before-manifest ordering issue (the copy operation now uploads blobs first)"
		case transport.ManifestUnknownErrorCode, transport.NameUnknownErrorCode:
			return "the manifest or repository does not exist on the registry; verify the reference and that you have pull access"
		case transport.UnauthorizedErrorCode:
			return "authentication failed (401); run `scafctl auth login` for this registry or set username/password/token"
		case transport.DeniedErrorCode:
			return "access denied (403); the token lacks push/pull scope for this repository"
		case transport.TooManyRequestsErrorCode:
			return "the registry is rate limiting (429); enable retries with the `retry` input or reduce concurrency"
		case transport.TagInvalidErrorCode, transport.NameInvalidErrorCode:
			return "the reference is invalid; tags cannot contain '+' and must match the registry's naming rules"
		}
	}
	return ""
}

// hintForStatus maps a bare HTTP status code to an actionable hint.
func hintForStatus(code int) string {
	switch code {
	case http.StatusUnauthorized:
		return "authentication required (401); run `scafctl auth login` for this registry"
	case http.StatusForbidden:
		return "access denied (403); the token lacks the required scope for this repository"
	case http.StatusTooManyRequests:
		return "rate limited (429); enable retries with the `retry` input or reduce concurrency"
	case http.StatusNotFound:
		return "not found (404); verify the reference exists and you have pull access"
	default:
		return ""
	}
}

// toBool coerces common scalar representations into a bool.
func toBool(v any) (bool, error) {
	switch b := v.(type) {
	case bool:
		return b, nil
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(b))
		if err != nil {
			return false, fmt.Errorf("expected a boolean, got %q", b)
		}
		return parsed, nil
	default:
		return false, fmt.Errorf("expected a boolean, got %T", v)
	}
}

// toInt coerces common numeric representations into an int. Floating-point
// inputs must represent whole numbers within the int range; a fractional value
// such as 1.9 is rejected rather than silently truncated, since these values
// drive retry attempt counts where truncation would be surprising.
func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		if n < math.MinInt || n > math.MaxInt {
			return 0, fmt.Errorf("integer out of range: %v", n)
		}
		return int(n), nil
	case float64:
		if n != math.Trunc(n) {
			return 0, fmt.Errorf("expected a whole number, got %v", n)
		}
		if n < math.MinInt || n > math.MaxInt {
			return 0, fmt.Errorf("integer out of range: %v", n)
		}
		return int(n), nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0, fmt.Errorf("expected an integer, got %q", n)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("expected an integer, got %T", v)
	}
}

// toDuration coerces a duration string ("1s", "500ms") or a number of seconds
// into a time.Duration.
func toDuration(v any) (time.Duration, error) {
	switch d := v.(type) {
	case string:
		parsed, err := time.ParseDuration(strings.TrimSpace(d))
		if err != nil {
			return 0, fmt.Errorf("expected a duration such as \"1s\", got %q", d)
		}
		return parsed, nil
	case int:
		return time.Duration(d) * time.Second, nil
	case int64:
		return time.Duration(d) * time.Second, nil
	case float64:
		return time.Duration(d * float64(time.Second)), nil
	default:
		return 0, fmt.Errorf("expected a duration string or number of seconds, got %T", v)
	}
}
