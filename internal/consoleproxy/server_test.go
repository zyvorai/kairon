// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package consoleproxy

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/zyvorai/kairon/internal/fluxvm"
)

// shortTempDir returns a short-path temp directory: AF_UNIX socket paths
// are capped at ~104 bytes on macOS (108 on Linux), and t.TempDir()'s
// test-name-based nesting routinely exceeds that.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cp")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func newFakeFlux(t *testing.T, workspace string) *fluxvm.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "vm-1", "status": "Running", "workspace": workspace})
	}))
	t.Cleanup(srv.Close)
	return fluxvm.New(srv.URL, "")
}

// dialWS wraps websocket.Dial and always closes the handshake response
// body (the *websocket.Conn returned on success owns the underlying
// connection itself and needs no separate close here).
func dialWS(ctx context.Context, u string, opts *websocket.DialOptions) (*websocket.Conn, error) {
	conn, resp, err := websocket.Dial(ctx, u, opts)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	return conn, err
}

// listenVNCSocket starts a fake VNC server on <dir>/vnc.sock that echoes
// whatever it receives back to the caller -- enough to prove bytes make it
// all the way through the relay in both directions.
func listenVNCSocket(t *testing.T, dir string) {
	t.Helper()
	l, err := net.Listen("unix", filepath.Join(dir, "vnc.sock"))
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				if _, werr := conn.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
}

func TestHandleConsoleRejectsWrongOrMissingToken(t *testing.T) {
	dir := shortTempDir(t)
	listenVNCSocket(t, dir)
	s := &Server{Flux: newFakeFlux(t, dir), Token: "secret"}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/console/vm-1"

	if _, err := dialWS(context.Background(), wsURL, nil); err == nil {
		t.Fatal("expected dial without a token to fail")
	}
	if _, err := dialWS(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer wrong"}},
	}); err == nil {
		t.Fatal("expected dial with the wrong token to fail")
	}
}

func TestHandleConsoleRelaysBytesToVNCSocket(t *testing.T) {
	dir := shortTempDir(t)
	listenVNCSocket(t, dir)
	s := &Server{Flux: newFakeFlux(t, dir), Token: "secret"}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/console/vm-1"
	conn, err := dialWS(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer secret"}},
	})
	if err != nil {
		t.Fatalf("dial with correct token: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("RFB 003.008\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "RFB 003.008\n" {
		t.Fatalf("expected the fake VNC socket to echo back what was sent, got %q", data)
	}
}

// newFakeFluxExec returns a fake FluxVM server that answers
// POST /v1/vms/vm-1/qga/exec, capturing the decoded request body for the
// caller to inspect.
func newFakeFluxExec(t *testing.T, gotBody *map[string]any, exitCode int64, stdout, stderr string) *fluxvm.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/vms/vm-1/qga/exec" {
			http.Error(w, "bad route", http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"exit_code": exitCode, "stdout": stdout, "stderr": stderr})
	}))
	t.Cleanup(srv.Close)
	return fluxvm.New(srv.URL, "")
}

func TestHandleExecRejectsWrongOrMissingToken(t *testing.T) {
	var got map[string]any
	s := &Server{Flux: newFakeFluxExec(t, &got, 0, "", ""), Token: "secret"}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	body := strings.NewReader(`{"path":"/bin/echo"}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/exec/vm-1", body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no token, got %d", resp.StatusCode)
	}
}

func TestHandleExecRelaysRequestAndResponse(t *testing.T) {
	var got map[string]any
	s := &Server{Flux: newFakeFluxExec(t, &got, 0, "hello\n", ""), Token: "secret"}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	body := strings.NewReader(`{"path":"/bin/echo","args":["hello"]}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/exec/vm-1", body)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out execResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 0 || out.Stdout != "hello\n" {
		t.Fatalf("unexpected response: %+v", out)
	}
	if got["path"] != "/bin/echo" {
		t.Fatalf("expected the request to be forwarded to FluxVM, got %+v", got)
	}
}

func TestHandleExecPropagatesFluxVMFailure(t *testing.T) {
	srv404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "guest agent is not enabled for this VM", http.StatusBadRequest)
	}))
	defer srv404.Close()
	s := &Server{Flux: fluxvm.New(srv404.URL, ""), Token: "secret"}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	body := strings.NewReader(`{"path":"/bin/echo"}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/exec/vm-1", body)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 when FluxVM rejects the exec, got %d", resp.StatusCode)
	}
}

func TestHandleConsoleRejectsUnknownRuntime(t *testing.T) {
	// A fake Flux client that always 404s -- exercises the "lookup
	// runtime" failure path distinct from "no workspace on the record".
	srv404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv404.Close()
	s := &Server{Flux: fluxvm.New(srv404.URL, ""), Token: "secret"}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/console/nope"
	if _, err := dialWS(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer secret"}},
	}); err == nil {
		t.Fatal("expected dial for an unknown runtime to fail")
	}
}
