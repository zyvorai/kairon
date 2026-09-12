// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package uiapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

func newUserTestServer(t *testing.T, users []User, sessionSecret string) *Server {
	t.Helper()
	fk := newFakeKube()
	srv := httptest.NewServer(fk.handler())
	t.Cleanup(srv.Close)
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	return &Server{Kube: kc, Users: users, SessionSecret: []byte(sessionSecret)}
}

func hashFor(t *testing.T, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	return string(h)
}

func login(t *testing.T, h http.Handler, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(loginRequest{Username: username, Password: password})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestLoginSucceedsAndSessionAuthenticatesRequests(t *testing.T) {
	s := newUserTestServer(t, []User{{Username: "alice", PasswordHash: hashFor(t, "correct-horse")}}, "test-session-secret")
	h := s.Handler()

	rr := login(t, h, "alice", "correct-horse")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Token    string `json:"token"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Username != "alice" || out.Token == "" {
		t.Fatalf("expected a token and username=alice, got %+v", out)
	}

	// The issued session token must work as a bearer credential.
	if rr := doJSON(t, h, http.MethodGet, "/api/v1/machines", out.Token, nil); rr.Code != http.StatusOK {
		t.Fatalf("session token should authenticate, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestLoginRejectsWrongPasswordAndUnknownUserIdentically(t *testing.T) {
	s := newUserTestServer(t, []User{{Username: "alice", PasswordHash: hashFor(t, "correct-horse")}}, "test-session-secret")
	h := s.Handler()

	rr := login(t, h, "alice", "wrong-password")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: expected 401, got %d", rr.Code)
	}
	var wrongPwBody map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &wrongPwBody)

	rr = login(t, h, "no-such-user", "whatever")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unknown user: expected 401, got %d", rr.Code)
	}
	var unknownUserBody map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &unknownUserBody)

	if wrongPwBody["error"] != unknownUserBody["error"] {
		t.Fatalf("expected identical error message for wrong-password vs unknown-user to avoid username enumeration, got %q vs %q", wrongPwBody["error"], unknownUserBody["error"])
	}
}

func TestLoginDisabledWhenNoUsersConfigured(t *testing.T) {
	s := newUserTestServer(t, nil, "")
	h := s.Handler()
	rr := login(t, h, "alice", "whatever")
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501 when login is not configured, got %d", rr.Code)
	}
}

func TestAuthConfigReflectsServerState(t *testing.T) {
	s := newUserTestServer(t, []User{{Username: "alice", PasswordHash: hashFor(t, "x")}}, "secret")
	h := s.Handler()
	rr := doJSON(t, h, http.MethodGet, "/api/v1/auth/config", "", nil)
	var cfg struct {
		LoginEnabled bool `json:"loginEnabled"`
		TokenEnabled bool `json:"tokenEnabled"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !cfg.LoginEnabled || cfg.TokenEnabled {
		t.Fatalf("expected loginEnabled=true tokenEnabled=false, got %+v", cfg)
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	s := newUserTestServer(t, []User{{Username: "alice", PasswordHash: hashFor(t, "correct-horse")}}, "test-session-secret")
	h := s.Handler()

	rr := login(t, h, "alice", "correct-horse")
	var out struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)

	if rr := doJSON(t, h, http.MethodGet, "/api/v1/machines", out.Token, nil); rr.Code != http.StatusOK {
		t.Fatalf("expected session to work before logout, got %d", rr.Code)
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logoutReq.Header.Set("Authorization", "Bearer "+out.Token)
	logoutRR := httptest.NewRecorder()
	h.ServeHTTP(logoutRR, logoutReq)
	if logoutRR.Code != http.StatusNoContent {
		t.Fatalf("expected 204 from logout, got %d", logoutRR.Code)
	}

	if rr := doJSON(t, h, http.MethodGet, "/api/v1/machines", out.Token, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected revoked session to be rejected, got %d", rr.Code)
	}
}

func TestSessionRejectsExpiredAndTamperedTokens(t *testing.T) {
	key := []byte("test-session-secret")

	expired, _, err := signSession(key, "alice", -time.Minute)
	if err != nil {
		t.Fatalf("signSession: %v", err)
	}
	if _, _, err := verifySession(key, expired); err == nil {
		t.Fatal("expected expired session to be rejected")
	}

	valid, _, err := signSession(key, "alice", time.Hour)
	if err != nil {
		t.Fatalf("signSession: %v", err)
	}
	tampered := valid[:len(valid)-1] + "x"
	if tampered == valid {
		t.Fatal("test setup did not actually tamper the token")
	}
	if _, _, err := verifySession(key, tampered); err == nil {
		t.Fatal("expected tampered session to be rejected")
	}

	if _, _, err := verifySession([]byte("wrong-key"), valid); err == nil {
		t.Fatal("expected session signed with a different key to be rejected")
	}
}

func TestWithAuthAcceptsEitherStaticTokenOrSession(t *testing.T) {
	fk := newFakeKube()
	srv := httptest.NewServer(fk.handler())
	t.Cleanup(srv.Close)
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	s := &Server{Kube: kc, Token: "shared-secret", Users: []User{{Username: "alice", PasswordHash: hashFor(t, "correct-horse")}}, SessionSecret: []byte("session-secret")}
	h := s.Handler()

	if rr := doJSON(t, h, http.MethodGet, "/api/v1/machines", "shared-secret", nil); rr.Code != http.StatusOK {
		t.Fatalf("legacy shared token should still work, got %d", rr.Code)
	}

	rr := login(t, h, "alice", "correct-horse")
	var out struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if rr := doJSON(t, h, http.MethodGet, "/api/v1/machines", out.Token, nil); rr.Code != http.StatusOK {
		t.Fatalf("session token should also work alongside a configured shared token, got %d", rr.Code)
	}

	if rr := doJSON(t, h, http.MethodGet, "/api/v1/machines", "neither-of-the-above", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a token that is neither, got %d", rr.Code)
	}
}

func TestAuditLogAttributesSessionAuthenticatedRequests(t *testing.T) {
	var logBuf bytes.Buffer
	fk := newFakeKube()
	fk.machines["db"] = model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "default"}}
	srv := httptest.NewServer(fk.handler())
	t.Cleanup(srv.Close)
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	s := &Server{Kube: kc, Log: slog.New(slog.NewTextHandler(&logBuf, nil)), Users: []User{{Username: "alice", PasswordHash: hashFor(t, "correct-horse")}}, SessionSecret: []byte("session-secret")}
	h := s.Handler()

	rr := login(t, h, "alice", "correct-horse")
	var out struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)

	doJSON(t, h, http.MethodPost, "/api/v1/machines/default/db/stop", out.Token, nil)
	logged := logBuf.String()
	if !strings.Contains(logged, "user=alice") {
		t.Fatalf("expected audit log to attribute the request to alice, got: %s", logged)
	}
}
