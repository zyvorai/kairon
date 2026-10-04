// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/model"
)

// resolveImageSource downloads m.Spec.Image.Source into a.ImageCacheDir if
// not already cached under its digest, verifies it against
// m.Spec.Image.Digest (required -- see model.ValidateImageSource), and returns
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
	if err := model.ValidateImageSource(m.Spec.Image); err != nil {
		return "", err
	}
	hexDigest := strings.TrimPrefix(m.Spec.Image.Digest, model.ImageDigestPrefix)
	cachePath := filepath.Join(a.ImageCacheDir, "sha256", hexDigest)
	if m.Spec.Image.Source.OCI != "" {
		// Keyed by manifest digest, not disk bytes, so kept apart.
		cachePath = filepath.Join(a.ImageCacheDir, "oci", "sha256", hexDigest)
	}
	if _, err := os.Stat(cachePath); err == nil {
		return cachePath, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat image cache entry %s: %w", cachePath, err)
	}
	if m.Spec.Image.Source.OCI != "" {
		if err := a.pullOCIDisk(ctx, m.Spec.Image, cachePath); err != nil {
			return "", err
		}
		return cachePath, nil
	}
	if err := downloadToTemp(ctx, m.Spec.Image.Source.HTTPURL, cachePath, hexDigest); err != nil {
		return "", err
	}
	return cachePath, nil
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
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %s", source, resp.Status)
	}

	tmp, err := os.CreateTemp(filepath.Dir(destPath), ".import-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", source, err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once the rename below succeeds

	hasher := sha256.New()
	if _, err := io.Copy(tmp, io.TeeReader(resp.Body, hasher)); err != nil {
		_ = tmp.Close()
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

// importedImage is the record kairon-node keeps next to the download cache
// for a source that went through FluxVM's import, so later reconciles and
// other Machines with the same digest reuse the converted disk.
type importedImage struct {
	Image      string   `json:"image"`
	ExtraDisks []string `json:"extraDisks,omitempty"`
	Actions    []string `json:"actions,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

// dataDisks names the import's extra disks model.ImportDiskName(1..N) in
// source order.
func (r importedImage) dataDisks() []fluxvm.DataDisk {
	var out []fluxvm.DataDisk
	for i, p := range r.ExtraDisks {
		out = append(out, fluxvm.DataDisk{Name: model.ImportDiskName(i + 1), Backing: p})
	}
	return out
}

// importName is the FluxVM import name for a digest and repair choice; it
// is also the record's file name under <cache>/imported/.
func importName(img model.ImageSpec) string {
	hexDigest := strings.TrimPrefix(img.Digest, model.ImageDigestPrefix)
	name := "kairon-" + hexDigest[:24]
	if img.Source.Repair {
		name += "-repaired"
	}
	return name
}

// resolveImportedImage converts (and with Repair, fixes) the downloaded
// file at cachedPath through FluxVM's POST /v1/images/import and returns
// the boot disk path. Content-addressed like the download cache: one
// import per digest and repair choice per node.
func (a *Agent) resolveImportedImage(ctx context.Context, m model.Machine, cachedPath string) (importedImage, error) {
	name := importName(m.Spec.Image)
	recordPath := filepath.Join(a.ImageCacheDir, "imported", name+".json")
	if b, err := os.ReadFile(recordPath); err == nil {
		var rec importedImage
		if err := json.Unmarshal(b, &rec); err == nil && rec.Image != "" {
			return rec, nil
		}
	}
	res, err := a.Flux.ImportImage(ctx, cachedPath, name, m.Spec.Image.Source.Repair)
	if err != nil {
		return importedImage{}, fmt.Errorf("import %s (%s): %w", m.Spec.Image.Source.Location(), dash(m.Spec.Image.Source.Format), err)
	}
	rec := importedImage{Image: res.Image, ExtraDisks: res.ExtraDisks}
	if res.Repair != nil {
		rec.Actions, rec.Warnings = res.Repair.Actions, res.Repair.Warnings
		a.Log.Info("image repaired", "machine", m.Metadata.Name, "os", res.Repair.Distro, "actions", len(res.Repair.Actions), "warnings", strings.Join(res.Repair.Warnings, "; "))
	}
	if err := os.MkdirAll(filepath.Dir(recordPath), 0o755); err != nil {
		return importedImage{}, err
	}
	b, _ := json.Marshal(rec)
	tmp := recordPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return importedImage{}, err
	}
	if err := os.Rename(tmp, recordPath); err != nil {
		return importedImage{}, err
	}
	return rec, nil
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
