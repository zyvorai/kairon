// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/zyvorai/kairon/internal/model"
)

func digestOf(payload []byte) string {
	sum := sha256.Sum256(payload)
	return digestPrefix + hex.EncodeToString(sum[:])
}

func machineWithImageSource(url, digest string) model.Machine {
	return model.Machine{
		Spec: model.MachineSpec{
			Image: model.ImageSpec{
				Source: &model.ImageSource{HTTPURL: url},
				Digest: digest,
			},
		},
	}
}

func TestResolveImageSourceRequiresCacheDir(t *testing.T) {
	a := &Agent{}
	m := machineWithImageSource("http://example.invalid/x.qcow2", digestOf([]byte("x")))
	if _, err := a.resolveImageSource(context.Background(), m); err == nil {
		t.Fatal("expected an error when ImageCacheDir is unset")
	}
}

func TestValidateImageSourceRequiresURLAndDigest(t *testing.T) {
	validDigest := digestOf([]byte("payload"))
	cases := []struct {
		name string
		img  model.ImageSpec
		ok   bool
	}{
		{"no source", model.ImageSpec{Path: "/x"}, true},
		{"missing url", model.ImageSpec{Source: &model.ImageSource{}, Digest: validDigest}, false},
		{"non-http url", model.ImageSpec{Source: &model.ImageSource{HTTPURL: "ftp://x/y"}, Digest: validDigest}, false},
		{"missing digest", model.ImageSpec{Source: &model.ImageSource{HTTPURL: "http://x/y"}}, false},
		{"malformed digest", model.ImageSpec{Source: &model.ImageSource{HTTPURL: "http://x/y"}, Digest: "sha256:short"}, false},
		{"valid", model.ImageSpec{Source: &model.ImageSource{HTTPURL: "http://x/y"}, Digest: validDigest}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateImageSource(c.img)
			if c.ok && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestResolveImageSourceDownloadsAndCaches(t *testing.T) {
	payload := []byte("fake qcow2 bytes")
	wantDigest := digestOf(payload)
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Write(payload)
	}))
	defer srv.Close()

	a := &Agent{ImageCacheDir: t.TempDir()}
	m := machineWithImageSource(srv.URL, wantDigest)

	path, err := a.resolveImageSource(context.Background(), m)
	if err != nil {
		t.Fatalf("resolveImageSource: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cached file: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("cached file contents mismatch: got %q want %q", got, payload)
	}
	if requests != 1 {
		t.Fatalf("expected 1 request, got %d", requests)
	}

	// Second call with the same digest must not re-download.
	path2, err := a.resolveImageSource(context.Background(), m)
	if err != nil {
		t.Fatalf("resolveImageSource (cached): %v", err)
	}
	if path2 != path {
		t.Fatalf("expected the same cache path, got %q and %q", path, path2)
	}
	if requests != 1 {
		t.Fatalf("expected still 1 request after a cache hit, got %d", requests)
	}
}

func TestResolveImageSourceRejectsDigestMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("actual bytes"))
	}))
	defer srv.Close()

	wrongDigest := digestOf([]byte("some other bytes"))
	a := &Agent{ImageCacheDir: t.TempDir()}
	m := machineWithImageSource(srv.URL, wrongDigest)

	if _, err := a.resolveImageSource(context.Background(), m); err == nil {
		t.Fatal("expected a digest mismatch error")
	}
	hexDigest := wrongDigest[len(digestPrefix):]
	if _, err := os.Stat(filepath.Join(a.ImageCacheDir, "sha256", hexDigest)); !os.IsNotExist(err) {
		t.Fatalf("expected no file left behind after a digest mismatch, stat error: %v", err)
	}
}

func TestResolveImageSourcePropagatesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	a := &Agent{ImageCacheDir: t.TempDir()}
	m := machineWithImageSource(srv.URL, digestOf([]byte("x")))
	if _, err := a.resolveImageSource(context.Background(), m); err == nil {
		t.Fatal("expected an error for a 404 response")
	}
}

func TestResolveImageSourceRejectsMalformedSpec(t *testing.T) {
	a := &Agent{ImageCacheDir: t.TempDir()}
	m := machineWithImageSource("not-a-url", "sha256:bad")
	if _, err := a.resolveImageSource(context.Background(), m); err == nil {
		t.Fatal("expected validation to reject a non-http URL and malformed digest")
	}
}
