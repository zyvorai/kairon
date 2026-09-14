// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package uiapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// User is one operator account: a username plus a bcrypt hash of their
// password, never the password itself. Server.Users is seeded at startup
// (see cmd/kairon-ui/main.go) from $KAIRON_UI_USERS_JSON and, optionally,
// one bcrypt-hashed-at-startup entry for a Helm-generated default admin
// account (see main.go's $KAIRON_UI_DEFAULT_ADMIN_PASSWORD) -- and can be
// mutated afterward via POST /api/v1/auth/password and
// POST /api/v1/users/{username}/password (see setOwnPassword/resetPassword
// below), which is why every read of Server.Users goes through usersMu.
type User struct {
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
	// IsAdmin gates POST /api/v1/users/{username}/password -- only an admin
	// account may reset another operator's password. The Helm-seeded
	// default admin account is always IsAdmin (see loadUsers in
	// cmd/kairon-ui/main.go); everyone else defaults to false unless
	// ui.auth.users[].admin is set.
	IsAdmin bool `json:"admin,omitempty"`
}

// dummyHash is compared against on an unknown username so a "no such user"
// response takes the same time as a "wrong password" one -- otherwise
// handleLogin would leak which usernames exist via response timing. It is
// never a real credential.
var dummyHash = mustBcryptHash("kairon-dummy-password-for-constant-time-compare")

func mustBcryptHash(pw string) []byte {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	return h
}

type contextKey string

// usernameHolderKey holds a *string set by withAudit before calling
// withAuth, so a successful session-token auth (deep inside withAuth) can
// report the authenticated username back out to the audit log -- without
// this indirection, withAudit (which must wrap *outside* withAuth so a
// rejected auth attempt is itself logged, see withAudit's own doc comment)
// would have no way to see what withAuth learned about the caller.
const usernameHolderKey contextKey = "kairon-ui-username-holder"

func withUsernameHolder(r *http.Request) (*http.Request, *string) {
	holder := new(string)
	return r.WithContext(context.WithValue(r.Context(), usernameHolderKey, holder)), holder
}

func setContextUsername(ctx context.Context, username string) {
	if holder, ok := ctx.Value(usernameHolderKey).(*string); ok {
		*holder = username
	}
}

// usernameFromContext reads back what withAuth (or a handler downstream of
// it) recorded via setContextUsername -- e.g. handleConsoleTicket uses
// this to bind an issued ticket to the operator who requested it, so the
// console's own audit log can attribute a session to a person, not just
// "someone with a valid ticket."
func usernameFromContext(ctx context.Context) string {
	if holder, ok := ctx.Value(usernameHolderKey).(*string); ok {
		return *holder
	}
	return ""
}

// sessionPayload is the signed, base64url-encoded JSON body of a session
// token. It carries no secret material -- forging one requires the HMAC
// key, not just reading a token, so the payload itself doesn't need to be
// hidden, only the signature needs to be unforgeable.
type sessionPayload struct {
	Subject string `json:"sub"`
	Expires int64  `json:"exp"`
	// IssuedAt lets withAuth reject a session issued before the subject's
	// most recent password reset (see Server.passwordChangedAt /
	// resetPassword below), without needing a server-side list of every
	// outstanding token.
	IssuedAt int64 `json:"iat"`
}

const sessionTTL = 12 * time.Hour

// signSession builds "base64url(payload).base64url(HMAC-SHA256(payload,key))"
// -- a minimal hand-rolled equivalent of a JWT HS256 token, without adding a
// JWT library dependency for a single subject+expiry claim.
func signSession(key []byte, username string, ttl time.Duration) (token string, expires time.Time, err error) {
	now := time.Now()
	expires = now.Add(ttl)
	payload, err := json.Marshal(sessionPayload{Subject: username, Expires: expires.Unix(), IssuedAt: now.Unix()})
	if err != nil {
		return "", time.Time{}, err
	}
	encPayload := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(encPayload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encPayload + "." + sig, expires, nil
}

// verifySession checks the signature and expiry and returns the subject
// plus when the token was issued (see sessionPayload.IssuedAt).
func verifySession(key []byte, token string) (username string, expires time.Time, issuedAt time.Time, err error) {
	encPayload, sig, ok := strings.Cut(token, ".")
	if !ok {
		return "", time.Time{}, time.Time{}, errors.New("malformed session token")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(encPayload))
	wantSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if len(sig) != len(wantSig) || !hmac.Equal([]byte(sig), []byte(wantSig)) {
		return "", time.Time{}, time.Time{}, errors.New("invalid session signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encPayload)
	if err != nil {
		return "", time.Time{}, time.Time{}, err
	}
	var p sessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", time.Time{}, time.Time{}, err
	}
	expires = time.Unix(p.Expires, 0)
	if time.Now().After(expires) {
		return "", time.Time{}, time.Time{}, errors.New("session expired")
	}
	return p.Subject, expires, time.Unix(p.IssuedAt, 0), nil
}

// revokeSession/isSessionRevoked back POST /api/v1/auth/logout with a
// small in-memory, best-effort revocation list: sessions are otherwise
// stateless (no server-side session store), so logout needs *something* to
// actually invalidate the token rather than merely asking the client to
// forget it. Only effective on the replica that served the logout --
// kairon-ui runs as a single replica today (charts/kairon/values.yaml), so
// this covers the common case, not a distributed session store. Entries
// are lazily dropped once their own expiry passes, so this never grows
// without bound.
func (s *Server) revokeSession(token string, expires time.Time) {
	s.revoked.Store(token, expires)
}

func (s *Server) isSessionRevoked(token string) bool {
	v, ok := s.revoked.Load(token)
	if !ok {
		return false
	}
	expires, _ := v.(time.Time)
	if time.Now().After(expires) {
		s.revoked.Delete(token)
		return false
	}
	return true
}

// passwordChangedAfter reports whether username's password was reset (via
// resetPassword) at or after sessionIssuedAt -- used by withAuth to reject
// a session token issued before that reset, since there's no server-side
// list of every outstanding token to individually revoke. Per-replica,
// same documented limitation as revokeSession/recordLoginResult.
func (s *Server) passwordChangedAfter(username string, sessionIssuedAt time.Time) bool {
	v, ok := s.passwordChangedAt.Load(username)
	if !ok {
		return false
	}
	changedAt, _ := v.(time.Time)
	return !sessionIssuedAt.After(changedAt)
}

func (s *Server) userCount() int {
	s.usersMu.RLock()
	defer s.usersMu.RUnlock()
	return len(s.Users)
}

func (s *Server) findUser(username string) (User, bool) {
	s.usersMu.RLock()
	defer s.usersMu.RUnlock()
	for _, u := range s.Users {
		if u.Username == username {
			return u, true
		}
	}
	return User{}, false
}

// persistUsers writes the current in-memory Users list back to the
// Kubernetes Secret backing it (see cmd/kairon-ui/main.go's
// UsersSecretNamespace/UsersSecretName wiring), so a password change
// survives a pod restart instead of silently reverting to whatever
// $KAIRON_UI_USERS_JSON was at startup. Returns an error the caller should
// treat as fatal to the request: an in-memory-only change that can't be
// persisted would silently vanish on the next restart, which is worse than
// refusing the request.
func (s *Server) persistUsers(ctx context.Context) error {
	if s.UsersSecretName == "" {
		return errPersistenceNotConfigured
	}
	s.usersMu.RLock()
	usersJSON, err := json.Marshal(s.Users)
	s.usersMu.RUnlock()
	if err != nil {
		return err
	}
	key := s.UsersSecretKey
	if key == "" {
		key = "users.json"
	}
	return s.Kube.PatchSecretStringData(ctx, s.UsersSecretNamespace, s.UsersSecretName, map[string]string{key: string(usersJSON)})
}

var errPersistenceNotConfigured = errors.New("runtime password changes are not persisted on this deployment (ui.auth.users is set via an externally-managed existingSecret)")

// loginAttemptState tracks failed logins for one requested username.
// Keyed on the raw username the caller supplied, not whether it resolves
// to a real account -- otherwise a nonexistent username never locking out
// would itself leak which usernames exist, the same anti-enumeration
// concern handleLogin's constant-time dummy-hash compare already guards.
type loginAttemptState struct {
	mu          sync.Mutex
	count       int
	lockedUntil time.Time
}

const (
	maxLoginAttempts = 5
	loginLockoutFor  = 5 * time.Minute
)

func (s *Server) loginState(username string) *loginAttemptState {
	v, _ := s.loginAttempts.LoadOrStore(username, &loginAttemptState{})
	return v.(*loginAttemptState)
}

// loginLockedFor returns how much longer username is locked out, or 0 if
// it may attempt a login now.
func (s *Server) loginLockedFor(username string) time.Duration {
	st := s.loginState(username)
	st.mu.Lock()
	defer st.mu.Unlock()
	if remaining := time.Until(st.lockedUntil); remaining > 0 {
		return remaining
	}
	return 0
}

// recordLoginResult clears a username's failure count on success, or
// increments it on failure -- locking the username out for
// loginLockoutFor once maxLoginAttempts consecutive failures accumulate.
// This is in-memory and per-replica, the same documented limitation as
// session revocation (see revokeSession) -- kairon-ui runs one replica by
// default.
func (s *Server) recordLoginResult(username string, success bool) {
	st := s.loginState(username)
	st.mu.Lock()
	defer st.mu.Unlock()
	if success {
		st.count = 0
		st.lockedUntil = time.Time{}
		return
	}
	st.count++
	if st.count >= maxLoginAttempts {
		st.lockedUntil = time.Now().Add(loginLockoutFor)
		st.count = 0
	}
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, prefix) {
		return strings.TrimPrefix(h, prefix)
	}
	return ""
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleAuthConfig tells the frontend which login UI to render, without it
// having to guess from a 401: the two-step username/password form when
// Users is configured, otherwise the legacy raw-token box.
func (s *Server) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"loginEnabled": s.userCount() > 0,
		"tokenEnabled": s.Token != "",
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.userCount() == 0 {
		writeError(w, http.StatusNotImplemented, "username/password login is not configured on this server")
		return
	}
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if remaining := s.loginLockedFor(req.Username); remaining > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(remaining.Seconds())+1))
		if s.Log != nil {
			s.Log.Warn("uiapi login rate-limited", "username", req.Username, "remoteAddr", r.RemoteAddr, "retryAfter", remaining.Round(time.Second).String())
		}
		writeError(w, http.StatusTooManyRequests, "too many failed attempts; try again later")
		return
	}
	user, found := s.findUser(req.Username)
	hash := dummyHash
	if found {
		hash = []byte(user.PasswordHash)
	}
	// Always run the bcrypt compare, even for an unknown username, so a
	// "no such user" response takes the same time as a "wrong password"
	// one -- avoids leaking valid usernames via response timing.
	credentialsOK := bcrypt.CompareHashAndPassword(hash, []byte(req.Password)) == nil
	s.recordLoginResult(req.Username, found && credentialsOK)
	if !found || !credentialsOK {
		if s.Log != nil {
			s.Log.Warn("uiapi login failed", "username", req.Username, "remoteAddr", r.RemoteAddr)
		}
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	token, expires, err := signSession(s.SessionSecret, user.Username, sessionTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue session")
		return
	}
	if s.Log != nil {
		s.Log.Info("uiapi login", "username", user.Username, "remoteAddr", r.RemoteAddr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "username": user.Username, "isAdmin": user.IsAdmin, "expiresAt": expires})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if tok := bearerToken(r); tok != "" {
		if _, expires, _, err := verifySession(s.SessionSecret, tok); err == nil {
			s.revokeSession(tok, expires)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

const minPasswordLength = 8

type setPasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// handleSetOwnPassword implements POST /api/v1/auth/password: an
// authenticated operator changes their own password, re-proving their
// current one first (a "confirm password" step for a sensitive action,
// same idea as GitHub/GitLab's own account settings). Only meaningful for
// session-token auth -- the legacy shared token has no per-user identity
// to attach a new password to.
func (s *Server) handleSetOwnPassword(w http.ResponseWriter, r *http.Request) {
	username := usernameFromContext(r.Context())
	if username == "" {
		writeError(w, http.StatusBadRequest, "changing a password requires a per-operator session login, not the shared token")
		return
	}
	if s.UsersSecretName == "" {
		writeError(w, http.StatusNotImplemented, errPersistenceNotConfigured.Error())
		return
	}
	var req setPasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if len(req.NewPassword) < minPasswordLength {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("new password must be at least %d characters", minPasswordLength))
		return
	}
	user, found := s.findUser(username)
	if !found || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.CurrentPassword)) != nil {
		writeError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	if err := s.setPasswordHash(r.Context(), username, req.NewPassword); err != nil {
		if s.Log != nil {
			s.Log.Error("uiapi password change failed", "username", username, "error", err)
		}
		writeError(w, http.StatusInternalServerError, "failed to change password: "+err.Error())
		return
	}
	if s.Log != nil {
		s.Log.Info("uiapi password changed", "username", username, "remoteAddr", r.RemoteAddr)
	}
	w.WriteHeader(http.StatusNoContent)
}

type resetPasswordRequest struct {
	NewPassword string `json:"newPassword"`
}

// handleResetPassword implements POST /api/v1/users/{username}/password:
// an admin sets another operator's password without needing to hand-craft
// a bcrypt hash and redeploy. Immediately invalidates that operator's
// outstanding sessions (see Server.passwordChangedAt) -- resetting
// someone's password is often exactly because their account may be
// compromised, so the reset should also end any session they (or an
// attacker) currently hold.
func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	caller := usernameFromContext(r.Context())
	if caller != "" {
		// Session-token caller: must be a real admin account. A legacy
		// shared-token caller (caller == "") falls through -- that token
		// is already root-equivalent for every other route (see
		// withAuth), so it's treated as admin here too rather than
		// introducing a second, inconsistent authorization tier.
		callerUser, found := s.findUser(caller)
		if !found || !callerUser.IsAdmin {
			writeError(w, http.StatusForbidden, "only an admin account may reset another operator's password")
			return
		}
	}
	if s.UsersSecretName == "" {
		writeError(w, http.StatusNotImplemented, errPersistenceNotConfigured.Error())
		return
	}
	target := r.PathValue("username")
	if _, found := s.findUser(target); !found {
		writeError(w, http.StatusNotFound, "no such user")
		return
	}
	var req resetPasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if len(req.NewPassword) < minPasswordLength {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("new password must be at least %d characters", minPasswordLength))
		return
	}
	if err := s.setPasswordHash(r.Context(), target, req.NewPassword); err != nil {
		if s.Log != nil {
			s.Log.Error("uiapi password reset failed", "username", target, "error", err)
		}
		writeError(w, http.StatusInternalServerError, "failed to reset password: "+err.Error())
		return
	}
	s.passwordChangedAt.Store(target, time.Now())
	if s.Log != nil {
		s.Log.Info("uiapi password reset", "username", target, "resetBy", caller, "remoteAddr", r.RemoteAddr)
	}
	w.WriteHeader(http.StatusNoContent)
}

// setPasswordHash bcrypt-hashes newPassword, updates the in-memory Users
// entry for username under usersMu, and persists the whole list back to
// the backing Secret (persistUsers) -- rolling the in-memory change back
// if persistence fails, so a restart can never silently lose it.
func (s *Server) setPasswordHash(ctx context.Context, username, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	s.usersMu.Lock()
	var previous string
	idx := -1
	for i, u := range s.Users {
		if u.Username == username {
			idx = i
			previous = u.PasswordHash
			break
		}
	}
	if idx < 0 {
		s.usersMu.Unlock()
		return fmt.Errorf("user %q not found", username)
	}
	s.Users[idx].PasswordHash = string(hash)
	s.usersMu.Unlock()

	if err := s.persistUsers(ctx); err != nil {
		s.usersMu.Lock()
		s.Users[idx].PasswordHash = previous
		s.usersMu.Unlock()
		return err
	}
	return nil
}
