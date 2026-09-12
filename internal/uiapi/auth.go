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
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// User is one operator account: a username plus a bcrypt hash of their
// password, never the password itself. Server.Users is populated once at
// startup (see cmd/kairon-ui/main.go) from $KAIRON_UI_USERS_JSON and,
// optionally, one bcrypt-hashed-at-startup entry for a Helm-generated
// default admin account (see main.go's $KAIRON_UI_DEFAULT_ADMIN_PASSWORD).
type User struct {
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
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

// sessionPayload is the signed, base64url-encoded JSON body of a session
// token. It carries no secret material -- forging one requires the HMAC
// key, not just reading a token, so the payload itself doesn't need to be
// hidden, only the signature needs to be unforgeable.
type sessionPayload struct {
	Subject string `json:"sub"`
	Expires int64  `json:"exp"`
}

const sessionTTL = 12 * time.Hour

// signSession builds "base64url(payload).base64url(HMAC-SHA256(payload,key))"
// -- a minimal hand-rolled equivalent of a JWT HS256 token, without adding a
// JWT library dependency for a single subject+expiry claim.
func signSession(key []byte, username string, ttl time.Duration) (token string, expires time.Time, err error) {
	expires = time.Now().Add(ttl)
	payload, err := json.Marshal(sessionPayload{Subject: username, Expires: expires.Unix()})
	if err != nil {
		return "", time.Time{}, err
	}
	encPayload := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(encPayload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encPayload + "." + sig, expires, nil
}

// verifySession checks the signature and expiry and returns the subject.
func verifySession(key []byte, token string) (username string, expires time.Time, err error) {
	encPayload, sig, ok := strings.Cut(token, ".")
	if !ok {
		return "", time.Time{}, errors.New("malformed session token")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(encPayload))
	wantSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if len(sig) != len(wantSig) || !hmac.Equal([]byte(sig), []byte(wantSig)) {
		return "", time.Time{}, errors.New("invalid session signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encPayload)
	if err != nil {
		return "", time.Time{}, err
	}
	var p sessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", time.Time{}, err
	}
	expires = time.Unix(p.Expires, 0)
	if time.Now().After(expires) {
		return "", time.Time{}, errors.New("session expired")
	}
	return p.Subject, expires, nil
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

func (s *Server) findUser(username string) (User, bool) {
	for _, u := range s.Users {
		if u.Username == username {
			return u, true
		}
	}
	return User{}, false
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
		"loginEnabled": len(s.Users) > 0,
		"tokenEnabled": s.Token != "",
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if len(s.Users) == 0 {
		writeError(w, http.StatusNotImplemented, "username/password login is not configured on this server")
		return
	}
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
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
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "username": user.Username, "expiresAt": expires})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if tok := bearerToken(r); tok != "" {
		if _, expires, err := verifySession(s.SessionSecret, tok); err == nil {
			s.revokeSession(tok, expires)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
