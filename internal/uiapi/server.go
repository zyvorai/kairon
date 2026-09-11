// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package uiapi is the HTTP backend for kairon-ui: a thin REST wrapper
// around internal/kube.Client, one method-per-intent handler per route (no
// generic PATCH passthrough), serving the built web/ SPA from a directory
// alongside its own /api/v1/... routes. Every handler here is read-only
// with respect to business logic -- it never encodes a decision
// internal/controller or internal/agent don't already make; it only
// translates HTTP requests into the same kube.Client calls kaironctl's own
// subcommands use.
package uiapi

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zyvorai/kairon/internal/kube"
)

// Server wires a *kube.Client into the /api/v1/... route table and,
// optionally, static file serving for the built SPA.
type Server struct {
	Kube  *kube.Client
	Log   *slog.Logger
	Token string
	// WebDir, when set, serves the built web/dist SPA for any request
	// that doesn't match an /api/v1/... route -- KAIRON_UI_WEB_DIR, not
	// go:embed, so `go build`/the dep-free Go CI job never needs Node.
	WebDir string
}

// Handler returns the full mux: auth-gated /api/v1/... routes plus, if
// WebDir is set, static SPA serving for everything else. Auth is a single
// static bearer token (crypto/subtle.ConstantTimeCompare, constant-time
// against timing attacks) -- Token empty means unauthenticated dev mode,
// which cmd/kairon-ui refuses to start with unless explicitly opted into
// (see main.go's -allow-unauthenticated flag), the same secure-by-default
// posture as every other kairon component's fail-closed validation.
func (s *Server) Handler() http.Handler {
	top := http.NewServeMux()

	// Unauthenticated: Kubernetes readiness/liveness probes never carry
	// the bearer token, same convention as every other kairon component's
	// internal/health.Server.
	top.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	top.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/overview", s.handleOverview)

	api.HandleFunc("GET /api/v1/machines", s.handleListMachines)
	api.HandleFunc("POST /api/v1/machines", s.handleCreateMachine)
	api.HandleFunc("GET /api/v1/machines/{namespace}/{name}", s.handleGetMachine)
	api.HandleFunc("DELETE /api/v1/machines/{namespace}/{name}", s.handleDeleteMachine)
	api.HandleFunc("POST /api/v1/machines/{namespace}/{name}/start", s.handlePowerMachine("Running"))
	api.HandleFunc("POST /api/v1/machines/{namespace}/{name}/stop", s.handlePowerMachine("Stopped"))

	api.HandleFunc("GET /api/v1/migrations", s.handleListMigrations)
	api.HandleFunc("POST /api/v1/migrations", s.handleCreateMigration)
	api.HandleFunc("GET /api/v1/migrations/{namespace}/{name}", s.handleGetMigration)
	api.HandleFunc("POST /api/v1/migrations/evacuate", s.handleEvacuate)
	api.HandleFunc("POST /api/v1/migrations/{namespace}/{name}/recover", s.handleRecoverMigration)

	api.HandleFunc("GET /api/v1/snapshots", s.handleListSnapshots)
	api.HandleFunc("POST /api/v1/snapshots", s.handleCreateSnapshot)

	api.HandleFunc("GET /api/v1/nodes", s.handleListNodes)

	top.Handle("/api/v1/", s.withAuth(api))
	// The SPA route is intentionally unauthenticated (same as netra's own
	// serveWeb registration) -- it serves static JS/CSS/HTML, not data;
	// every actual data fetch the page makes goes through the auth-gated
	// /api/v1/... routes above.
	top.HandleFunc("/", s.serveWeb)
	return top
}

// serveWeb serves the built web/dist SPA from WebDir, mirroring netra's
// own internal/api.Server.serveWeb: path-traversal-safe (Clean + prefix
// containment check), falls back to index.html for any path that isn't a
// real file (SPA client-side routing, and also the plain "no WebDir
// configured" case), and sets Content-Type from the file extension since
// http.ServeFile alone doesn't guess it for every asset type Vite emits.
func (s *Server) serveWeb(w http.ResponseWriter, r *http.Request) {
	if s.WebDir == "" {
		if r.URL.Path == "/" {
			writeJSON(w, http.StatusOK, map[string]any{"name": "kairon-ui", "api": "/api/v1/overview"})
			return
		}
		http.NotFound(w, r)
		return
	}
	clean := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if clean == "." {
		clean = "index.html"
	}
	p := filepath.Join(s.WebDir, clean)
	if !strings.HasPrefix(p, filepath.Clean(s.WebDir)+string(os.PathSeparator)) && p != filepath.Join(s.WebDir, "index.html") {
		http.NotFound(w, r)
		return
	}
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		p = filepath.Join(s.WebDir, "index.html")
	}
	if ext := filepath.Ext(p); ext != "" {
		if ct := mime.TypeByExtension(ext); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
	}
	http.ServeFile(w, r, p)
}

func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" {
			got := r.Header.Get("Authorization")
			want := "Bearer " + s.Token
			if len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				writeError(w, http.StatusUnauthorized, "invalid or missing bearer token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func namespaceParam(r *http.Request) string {
	if ns := r.URL.Query().Get("namespace"); ns != "" {
		return ns
	}
	return "default"
}

func decodeJSON(r *http.Request, out any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// writeUpstreamError maps a kube.Client error to an HTTP status: a real
// Kubernetes 404 becomes a 404 here (not a generic 502), everything else
// is a 502 -- the UI backend has no opinion of its own on Kubernetes
// object state, it only relays what the API server actually said.
func writeUpstreamError(w http.ResponseWriter, err error) {
	if kube.IsNotFound(err) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeError(w, http.StatusBadGateway, err.Error())
}

var invalidResourceName = regexp.MustCompile(`[^a-z0-9-]+`)

// resourceName mirrors kaironctl's own sanitizer (cmd/kaironctl/main.go)
// exactly, so a name auto-generated by this API and one auto-generated by
// the CLI for the same input collide the same way (both are deterministic
// functions of the same inputs, not random).
func resourceName(s string) string {
	s = strings.ToLower(s)
	s = invalidResourceName.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	return s
}
