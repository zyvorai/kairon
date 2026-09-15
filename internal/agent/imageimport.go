// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/zyvorai/kairon/internal/model"
)

// digestPrefix is the only digest algorithm spec.image.digest supports for
// a spec.image.source Machine -- matches the sha256:<hex> convention this
// project already uses for CSI volume/OCI-style digests elsewhere.
const digestPrefix = "sha256:"

// resolveImageSource downloads m.Spec.Image.Source into a.ImageCacheDir if
// not already cached under its digest, verifies it against
// m.Spec.Image.Digest (required -- see validateImageSource), and returns
// the cached file's absolute path. Idempotent: a second Machine (or a
// later reconcile tick of the same Machine) naming the same digest never
// re-downloads, it just stats the existing cache file -- this is the
// whole answer to "50 Machines booting the same golden image" without a
// separate cache/refcount data structure, since the cache path is itself
// content-addressed and immutable once written.
func (a *Agent) resolveImageSource(ctx context.Context, m model.Machine) (string, error) {
	if a.ImageCacheDir == "" {
		return "", fmt.Errorf("this node has no image cache configured (-image-cache-dir / $KAIRON_IMAGE_CACHE_DIR) -- spec.image.source cannot be used without it; see docs/guides/machine-image-import.md")
	}
	if err := validateImageSource(m.Spec.Image); err != nil {
		return "", err
	}
	hexDigest := strings.TrimPrefix(m.Spec.Image.Digest, digestPrefix)
	cachePath := filepath.Join(a.ImageCacheDir, "sha256", hexDigest)
	if _, err := os.Stat(cachePath); err == nil {
		return cachePath, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat image cache entry %s: %w", cachePath, err)
	}
	if err := downloadToTemp(ctx, m.Spec.Image.Source.HTTPURL, cachePath, hexDigest); err != nil {
		return "", err
	}
	return cachePath, nil
}

// validateImageSource enforces that Digest is set (and sha256-shaped)
// whenever Source is -- it's the cache key resolveImageSource keys on, so
// without it two Machines naming the same (mutable) HTTPURL would have no
// way to know whether they mean the same bytes. Exported for
// internal/controller's admission webhook to call too (defense in depth,
// same convention other cross-package validation in this project follows).
func validateImageSource(img model.ImageSpec) error {
	if img.Source == nil {
		return nil
	}
	if strings.TrimSpace(img.Source.HTTPURL) == "" {
		return fmt.Errorf("spec.image.source.httpURL is required when spec.image.source is set")
	}
	if !strings.HasPrefix(img.Source.HTTPURL, "http://") && !strings.HasPrefix(img.Source.HTTPURL, "https://") {
		return fmt.Errorf("spec.image.source.httpURL %q must be an http:// or https:// URL", img.Source.HTTPURL)
	}
	hexDigest, ok := strings.CutPrefix(img.Digest, digestPrefix)
	if !ok || len(hexDigest) != sha256.Size*2 {
		return fmt.Errorf("spec.image.digest must be set as %q plus a 64-character hex digest when spec.image.source is set (it's the cache key -- see docs/guides/machine-image-import.md)", digestPrefix)
	}
	return nil
}

// downloadToTemp streams source into a temp file in the same directory as
// destPath (so the final os.Rename is same-filesystem and atomic -- no
// partial file is ever visible at destPath), hashing as it streams, and
// verifies the result against wantHexDigest before the rename. No format
// conversion: FluxVM already consumes qcow2/raw directly (see
// docs/guides/machine-storage.md), so bytes are trusted via digest, not
// inspected.
func downloadToTemp(ctx context.Context, source, destPath, wantHexDigest string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o750); err != nil {
		return fmt.Errorf("create image cache directory: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", source, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", source, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %s", source, resp.Status)
	}

	tmp, err := os.CreateTemp(filepath.Dir(destPath), ".import-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", source, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	hasher := sha256.New()
	if _, err := io.Copy(tmp, io.TeeReader(resp.Body, hasher)); err != nil {
		tmp.Close()
		return fmt.Errorf("download %s: %w", source, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("finalize download of %s: %w", source, err)
	}

	gotHexDigest := hex.EncodeToString(hasher.Sum(nil))
	if gotHexDigest != wantHexDigest {
		return fmt.Errorf("downloaded %s: digest mismatch, got sha256:%s, want sha256:%s", source, gotHexDigest, wantHexDigest)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("finalize image cache entry for %s: %w", source, err)
	}
	return nil
}
