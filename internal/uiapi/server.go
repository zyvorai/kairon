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
	"crypto/tls"
	"encoding/json"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/metrics"
	"github.com/zyvorai/kairon/internal/ratelimit"
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
	// Users, when non-empty, enables real per-operator username/password
	// login (see auth.go) alongside (not instead of) the legacy Token
	// above -- either credential is accepted. Mutated at runtime by
	// setOwnPassword/resetPassword, so every access goes through usersMu
	// (see auth.go's userCount/findUser helpers) rather than reading the
	// field directly.
	Users   []User
	usersMu sync.RWMutex
	// SessionSecret signs/verifies session tokens issued by
	// POST /api/v1/auth/login. Required whenever Users is non-empty.
	SessionSecret []byte
	// UsersSecretNamespace/UsersSecretName/UsersSecretKey tell
	// persistUsers (auth.go) which Kubernetes Secret to write an updated
	// Users list back into after a password change, so it survives a pod
	// restart. UsersSecretName empty means runtime password changes are
	// refused (see errPersistenceNotConfigured) -- set only when the Helm
	// chart itself owns the kairon-ui-users Secret (not when an operator
	// supplies ui.auth.existingSecret, which some external tool may manage
	// and which kairon-ui must not silently overwrite).
	UsersSecretNamespace string
	UsersSecretName      string
	UsersSecretKey       string
	// SharedStateNamespace/SharedStateConfigMapName point at the ConfigMap
	// kairon-ui uses to make session revocation, login lockout, and
	// console-ticket state visible across replicas -- see
	// internal/uiapi/sharedstate.go and docs/guides/kairon-ui-ha.md. Empty
	// SharedStateConfigMapName (the default) disables cross-replica sync
	// entirely: every mutation below still applies to this process's own
	// in-memory state exactly as before, correct for the single-replica
	// deployment this chart still defaults to. Password changes propagate
	// separately, via UsersSecretName above, independent of this field.
	SharedStateNamespace     string
	SharedStateConfigMapName string
	// OIDC, when set, enables "Sign in with SSO" (see oidc.go) alongside
	// -- not instead of -- Token and Users above. This is the one place
	// in Kairon that breaks the project's Go-stdlib-only design guarantee
	// (see README.md); nil (the default) means every request path
	// behaves exactly as it did before this field existed.
	OIDC *OIDCAuth
	// revoked backs POST /api/v1/auth/logout; zero value (an empty
	// sync.Map) is ready to use.
	revoked sync.Map
	// loginAttempts backs handleLogin's per-username rate limiting; zero
	// value is ready to use.
	loginAttempts sync.Map
	// passwordChangedAt backs resetPassword's forced logout of a reset
	// account's outstanding sessions (see passwordChangedAfter in
	// auth.go); zero value is ready to use.
	passwordChangedAt sync.Map
	// ConsoleToken/ConsolePort configure the VNC console relay (see
	// console.go): the shared bearer token kairon-ui presents to a
	// kairon-node's console listener, and the port that listener runs on.
	// Either empty disables the console feature entirely (handleConsole
	// returns 501).
	ConsoleToken string
	ConsolePort  string
	// ConsoleTLS, when set, dials kairon-node's console relay over
	// wss:// with this TLS config (verifying its server certificate)
	// instead of plaintext ws://. One-way TLS is enough here -- the
	// shared ConsoleToken already authenticates kairon-ui to kairon-node,
	// so a client certificate would be redundant.
	ConsoleTLS *tls.Config
	// RBACConsoleCheck, when true, additionally requires a real
	// Kubernetes SubjectAccessReview (verb "get" on the machines/console
	// subresource) to allow console access -- layered alongside, never
	// instead of, the kairon.zyvor.dev/console-allowed-users annotation
	// allowlist consoleAuthorized already enforces. False (the default)
	// is today's unchanged, annotation-only behavior. Only meaningful for
	// an OIDC-authenticated identity whose claims are also mapped into
	// kube-apiserver's own OIDC config -- a local ui.auth.users[] account
	// has no real Kubernetes User to check RBAC against, so it's denied
	// outright when this is true (fail closed, matching consoleAuthorized's
	// own existing "no real identity" precedent for the empty-username
	// case). See docs/guides/kairon-ui-console-rbac.md.
	RBACConsoleCheck bool
	// consoleTickets backs the console feature's single-use WebSocket
	// tickets; zero value is ready to use.
	consoleTickets sync.Map
	// Metrics, when set, serves /metrics (unauthenticated, same convention
	// as /healthz/readyz) and observes every request's method/status/
	// duration -- see withMetrics. Optional, nil-checked; without it
	// nothing here changes.
	Metrics *metrics.Recorder
	// RateLimit, when set, bounds request volume per remote address across
	// every route this Handler serves -- see withRateLimit. Distinct from,
	// and in addition to, the per-username login lockout above (auth.go):
	// that one only throttles repeated failed passwords against one
	// username; this throttles request volume from one client address
	// against anything, including a successfully-authenticated client
	// hammering an unrelated route. Optional, nil-checked; without it
	// nothing here changes.
	RateLimit *ratelimit.Limiter
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
	// Unauthenticated for the same reason: a Prometheus scrape config
	// never carries the bearer token either, same convention as
	// internal/health.Server's own /metrics route.
	if s.Metrics != nil {
		top.Handle("GET /metrics", s.Metrics.Handler())
	}

	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/overview", s.handleOverview)

	api.HandleFunc("GET /api/v1/machines", s.handleListMachines)
	api.HandleFunc("POST /api/v1/machines", s.handleCreateMachine)
	api.HandleFunc("GET /api/v1/machines/{namespace}/{name}", s.handleGetMachine)
	api.HandleFunc("DELETE /api/v1/machines/{namespace}/{name}", s.handleDeleteMachine)
	api.HandleFunc("POST /api/v1/machines/{namespace}/{name}/start", s.handlePowerMachine("Running"))
	api.HandleFunc("POST /api/v1/machines/{namespace}/{name}/stop", s.handlePowerMachine("Stopped"))
	api.HandleFunc("POST /api/v1/machines/{namespace}/{name}/pause", s.handlePowerMachine("Paused"))
	api.HandleFunc("POST /api/v1/machines/{namespace}/{name}/resume", s.handlePowerMachine("Running"))
	api.HandleFunc("POST /api/v1/machines/{namespace}/{name}/console/ticket", s.handleConsoleTicket)
	api.HandleFunc("POST /api/v1/machines/{namespace}/{name}/exec", s.handleExec)
	api.HandleFunc("POST /api/v1/machines/{namespace}/{name}/agent-file/put", s.handleAgentPutFile)
	api.HandleFunc("POST /api/v1/machines/{namespace}/{name}/agent-file/get", s.handleAgentGetFile)
	api.HandleFunc("GET /api/v1/machines/{namespace}/{name}/qga/fsfreeze-status", s.handleQGAFsfreezeStatus)
	api.HandleFunc("POST /api/v1/machines/{namespace}/{name}/qga/firewall/open", s.handleQGAFirewallOpen)
	api.HandleFunc("POST /api/v1/machines/{namespace}/{name}/qga/firewall/close", s.handleQGAFirewallClose)
	api.HandleFunc("GET /api/v1/machines/{namespace}/{name}/logs", s.handleLogs)

	api.HandleFunc("GET /api/v1/migrations", s.handleListMigrations)
	api.HandleFunc("POST /api/v1/migrations", s.handleCreateMigration)
	api.HandleFunc("GET /api/v1/migrations/{namespace}/{name}", s.handleGetMigration)
	api.HandleFunc("POST /api/v1/migrations/evacuate", s.handleEvacuate)
	api.HandleFunc("POST /api/v1/migrations/{namespace}/{name}/recover", s.handleRecoverMigration)

	api.HandleFunc("GET /api/v1/snapshots", s.handleListSnapshots)
	api.HandleFunc("POST /api/v1/snapshots", s.handleCreateSnapshot)

	api.HandleFunc("GET /api/v1/nodes", s.handleListNodes)
	api.HandleFunc("GET /api/v1/config", s.handleConfig)

	api.HandleFunc("POST /api/v1/auth/password", s.handleSetOwnPassword)
	api.HandleFunc("POST /api/v1/users/{username}/password", s.handleResetPassword)

	top.Handle("/api/v1/", s.withAudit(s.withAuth(api)))

	// Unauthenticated by necessity: login must be reachable without a
	// token to be useful at all; config/logout follow the same
	// unauthenticated convention as /healthz above.
	top.HandleFunc("GET /api/v1/auth/config", s.handleAuthConfig)
	top.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	top.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	// OIDC/SSO (see oidc.go) -- unauthenticated for the same reason as
	// auth/login above: this IS how a browser authenticates.
	top.HandleFunc("GET /api/v1/auth/oidc/login", s.handleOIDCLogin)
	top.HandleFunc("GET /api/v1/auth/oidc/callback", s.handleOIDCCallback)
	// Also unauthenticated at this layer by necessity: a browser's native
	// WebSocket API can't send an Authorization header, so this route is
	// gated by the single-use ticket handleConsoleTicket issues instead
	// (that ticket-issuing call *is* behind the normal auth above).
	top.HandleFunc("GET /api/v1/machines/{namespace}/{name}/console", s.handleConsole)
	// The SPA route is intentionally unauthenticated (same as netra's own
	// serveWeb registration) -- it serves static JS/CSS/HTML, not data;
	// every actual data fetch the page makes goes through the auth-gated
	// /api/v1/... routes above.
	top.HandleFunc("/", s.serveWeb)
	return s.withMetrics(s.withRateLimit(top))
}

// withRateLimit rejects a request with 429 (Retry-After set, same
// convention handleLogin's own per-username lockout already uses) once
// its remote address has exceeded RateLimit's configured rate --
// deliberately outermost (ahead of withAuth, even ahead of routing to
// /healthz/readyz), the same reasoning withAudit/withMetrics already
// document: request volume from one address against an unauthenticated or
// rejected route is exactly what this needs to bound, not just successful
// authenticated calls. No-op wrapper when RateLimit isn't configured, so
// this changes nothing about behavior or performance without it.
func (s *Server) withRateLimit(next http.Handler) http.Handler {
	if s.RateLimit == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := remoteIP(r.RemoteAddr)
		if allowed, retryAfter := s.RateLimit.Allow(key); !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			if s.Log != nil {
				s.Log.Warn("uiapi rate limited", "remoteAddr", r.RemoteAddr, "method", r.Method, "path", r.URL.Path)
			}
			writeError(w, http.StatusTooManyRequests, "too many requests; try again later")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// remoteIP strips the port from r.RemoteAddr (host:port) so the rate
// limiter's per-key bucket is keyed by address alone -- falls back to the
// raw value unchanged if it doesn't parse as host:port (defensive; Go's
// own net/http always sets RemoteAddr in that shape for a real
// connection).
func remoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// withMetrics wraps every route (auth-gated or not, including /healthz/
// readyz/metrics themselves) with request count/latency observation --
// deliberately outermost, the same reasoning withAudit already documents
// for wrapping outside withAuth: request volume/latency on a rejected or
// unauthenticated call (a failed login, a probe) is real signal too, not
// just successful API calls. No-op wrapper when Metrics isn't configured,
// so this changes nothing about behavior or performance without it.
func (s *Server) withMetrics(next http.Handler) http.Handler {
	if s.Metrics == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.Metrics.ObserveHTTPRequest(r.Method, rec.status, time.Since(start))
	})
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

// withAudit logs every mutating request (anything but GET) that reaches
// /api/v1/... -- method, path, remote address, and the resulting status
// code, including auth failures (it wraps outside withAuth deliberately,
// so a rejected token attempt against a destructive route is itself part
// of the trail). This is a partial fix for kairon-ui having no audit
// trail at all: it does not attribute an action to an individual operator
// (auth today is one shared bearer token -- see cmd/kairon-ui/main.go),
// but "some trail" is a real improvement over "none," and doing better
// than that needs a real per-operator auth model, a separate decision.
func (s *Server) withAudit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// withUsernameHolder lets withAuth, deep inside next, report back
		// which operator authenticated (session-token auth only -- the
		// legacy shared token has no identity to report). Always attached
		// -- not just when this handler goes on to log -- since other
		// handlers downstream (e.g. handleConsoleTicket, binding a ticket
		// to its issuer) rely on reading it back via usernameFromContext
		// regardless of method or whether a logger is configured.
		r, holder := withUsernameHolder(r)
		if r.Method == http.MethodGet || s.Log == nil {
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		fields := []any{"method", r.Method, "path", r.URL.Path, "remoteAddr", r.RemoteAddr, "status", rec.status}
		if *holder != "" {
			fields = append(fields, "user", *holder)
		}
		s.Log.Info("uiapi request", fields...)
	})
}

// statusRecorder captures the status code a handler actually wrote, since
// http.ResponseWriter itself doesn't expose it after the fact.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Flush lets statusRecorder satisfy http.Flusher by delegating to the
// ResponseWriter it wraps, when that one supports it -- without this, a
// streaming handler behind withMetrics (e.g. handleLogs' follow=true
// tail) would silently lose incremental flushing: a `w.(http.Flusher)`
// type assertion on a bare *statusRecorder never succeeds on its own,
// since embedding the http.ResponseWriter *interface* only promotes that
// interface's own methods, not Flush (a separate, optional interface),
// regardless of what the concrete value underneath actually implements.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// withAuth accepts either credential: the legacy shared static token
// (constant-time compared, as before) or a signed, unexpired, unrevoked
// session token from POST /api/v1/auth/login -- whichever Server has
// configured (Token, Users, or both). Neither configured means
// unauthenticated dev mode, unchanged from before.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token == "" && s.userCount() == 0 && s.OIDC == nil {
			next.ServeHTTP(w, r)
			return
		}
		tok := bearerToken(r)
		if tok != "" {
			if s.Token != "" && len(tok) == len(s.Token) && subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
			// Gated on SessionSecret being configured, not on userCount()
			// -- a session token verifies the same way regardless of
			// whether it came from POST /api/v1/auth/login (Users) or the
			// OIDC callback (OIDC); both require SessionSecret to be set
			// at startup (see cmd/kairon-ui/main.go), and an OIDC-only
			// deployment (no ui.auth.users at all) must still verify its
			// own sessions here, not fall through to the "wide open"
			// branch above.
			if len(s.SessionSecret) > 0 {
				if username, _, issuedAt, err := verifySession(s.SessionSecret, tok); err == nil && !s.isSessionRevoked(tok) && !s.passwordChangedAfter(username, issuedAt) {
					setContextUsername(r.Context(), username)
					next.ServeHTTP(w, r)
					return
				}
			}
		}
		writeError(w, http.StatusUnauthorized, "invalid or missing bearer token")
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
