// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeDestination struct {
	prepare PrepareResult
	prepN   int
	commitN int
	abortN  int
	commitE error
}

func (f *fakeDestination) Prepare(context.Context, Session) (PrepareResult, error) {
	f.prepN++
	return f.prepare, nil
}
func (f *fakeDestination) Commit(context.Context, Session) error { f.commitN++; return f.commitE }
func (f *fakeDestination) Abort(context.Context, Session) error  { f.abortN++; return nil }

func testSession() Session {
	return Session{ID: "kmm-123", Namespace: "prod", Machine: "db", SourceNode: "node-a", TargetNode: "node-b", RuntimeID: "vm-1"}
}

func TestPrepareIsIdempotentAndConflictsOnIdentityChange(t *testing.T) {
	d := &fakeDestination{prepare: PrepareResult{TransferSupported: true, Endpoint: "opaque://session/123", Backend: "fake"}}
	s := &Server{NodeName: "node-b", Store: NewFileStore(t.TempDir()), Driver: d}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	c := NewClient(ts.Client())

	first, err := c.Prepare(context.Background(), ts.URL, testSession())
	if err != nil || !first.TransferSupported || first.Phase != "Prepared" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := c.Prepare(context.Background(), ts.URL, testSession())
	if err != nil || second.SessionID != first.SessionID || d.prepN != 1 {
		t.Fatalf("second=%+v err=%v prepares=%d", second, err, d.prepN)
	}
	changed := testSession()
	changed.Machine = "other"
	if _, err := c.Prepare(context.Background(), ts.URL, changed); err == nil {
		t.Fatal("expected identity conflict")
	}
}

func TestPrepareRejectsWrongTargetNode(t *testing.T) {
	d := &fakeDestination{prepare: PrepareResult{TransferSupported: true, Endpoint: "opaque://session/123"}}
	ts := httptest.NewServer((&Server{NodeName: "node-c", Store: NewFileStore(t.TempDir()), Driver: d}).Handler())
	defer ts.Close()
	c := NewClient(ts.Client())
	if _, err := c.Prepare(context.Background(), ts.URL, testSession()); err == nil {
		t.Fatal("expected wrong target node to be rejected")
	}
	if d.prepN != 0 {
		t.Fatalf("driver prepare called %d times", d.prepN)
	}
}

func TestCommitAndAbortLifecycle(t *testing.T) {
	d := &fakeDestination{prepare: PrepareResult{TransferSupported: true, Endpoint: "opaque://session/123"}}
	store := NewFileStore(t.TempDir())
	ts := httptest.NewServer((&Server{Store: store, Driver: d}).Handler())
	defer ts.Close()
	c := NewClient(ts.Client())
	if _, err := c.Prepare(context.Background(), ts.URL, testSession()); err != nil {
		t.Fatal(err)
	}
	if err := c.Commit(context.Background(), ts.URL, "kmm-123"); err != nil {
		t.Fatal(err)
	}
	if err := c.Commit(context.Background(), ts.URL, "kmm-123"); err != nil {
		t.Fatalf("commit must be idempotent: %v", err)
	}
	if d.commitN != 1 {
		t.Fatalf("commit count=%d", d.commitN)
	}
	if err := c.Abort(context.Background(), ts.URL, "kmm-123"); err == nil {
		t.Fatal("committed session must not be abortable")
	}
}

type fakeDiagnosableDestination struct {
	fakeDestination
	diagnosis DiagnosisResult
	diagErr   error
	diagCalls int
}

func (f *fakeDiagnosableDestination) Diagnose(context.Context, Session) (DiagnosisResult, error) {
	f.diagCalls++
	return f.diagnosis, f.diagErr
}

func TestDiagnosisFallsBackToSessionPhaseWhenDriverIsNotDiagnosable(t *testing.T) {
	d := &fakeDestination{prepare: PrepareResult{TransferSupported: true, Endpoint: "opaque://session/123"}}
	store := NewFileStore(t.TempDir())
	ts := httptest.NewServer((&Server{Store: store, Driver: d}).Handler())
	defer ts.Close()
	c := NewClient(ts.Client())
	if _, err := c.Prepare(context.Background(), ts.URL, testSession()); err != nil {
		t.Fatal(err)
	}
	result, err := c.Diagnose(context.Background(), ts.URL, "kmm-123")
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionPhase != "Prepared" {
		t.Errorf("expected SessionPhase=Prepared, got %+v", result)
	}
	if result.DestinationRuntimeFound {
		t.Errorf("a non-Diagnosable driver must not report DestinationRuntimeFound, got %+v", result)
	}
}

func TestDiagnosisUsesDriverWhenAvailable(t *testing.T) {
	d := &fakeDiagnosableDestination{
		fakeDestination: fakeDestination{prepare: PrepareResult{TransferSupported: true, Endpoint: "opaque://session/123"}},
		diagnosis:       DiagnosisResult{SessionPhase: "Prepared", DestinationRuntimeFound: true, DestinationRuntimeStatus: "Running", DestinationRuntimeID: "vm-2"},
	}
	store := NewFileStore(t.TempDir())
	ts := httptest.NewServer((&Server{Store: store, Driver: d}).Handler())
	defer ts.Close()
	c := NewClient(ts.Client())
	if _, err := c.Prepare(context.Background(), ts.URL, testSession()); err != nil {
		t.Fatal(err)
	}
	result, err := c.Diagnose(context.Background(), ts.URL, "kmm-123")
	if err != nil {
		t.Fatal(err)
	}
	if d.diagCalls != 1 {
		t.Errorf("expected exactly one Diagnose call, got %d", d.diagCalls)
	}
	if result != d.diagnosis {
		t.Errorf("expected the driver's diagnosis to be returned verbatim, got %+v", result)
	}
}

func TestDiagnosisOnUnknownSessionIsNotFound(t *testing.T) {
	d := &fakeDestination{}
	ts := httptest.NewServer((&Server{Store: NewFileStore(t.TempDir()), Driver: d}).Handler())
	defer ts.Close()
	c := NewClient(ts.Client())
	if _, err := c.Diagnose(context.Background(), ts.URL, "does-not-exist"); err == nil {
		t.Fatal("expected an error for an unknown session id")
	}
}

func TestUnsupportedDestinationIsPersistedWithoutEndpoint(t *testing.T) {
	d := UnsupportedDestinationDriver{Reason: "adapter absent"}
	store := NewFileStore(t.TempDir())
	ts := httptest.NewServer((&Server{Store: store, Driver: d}).Handler())
	defer ts.Close()
	c := NewClient(ts.Client())
	got, err := c.Prepare(context.Background(), ts.URL, testSession())
	if err != nil {
		t.Fatal(err)
	}
	if got.TransferSupported || got.Phase != "Unsupported" || got.Endpoint != "" || got.Reason != "adapter absent" {
		t.Fatalf("response=%+v", got)
	}
}

func TestFileStoreRoundTripAndRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	s := testSession()
	s.Phase = "Prepared"
	if err := store.Put(s); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(s.ID)
	if err != nil || !got.SameIdentity(s) || got.Phase != "Prepared" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	info, err := os.Stat(filepath.Join(dir, s.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	if _, err := store.Get("../escape"); err == nil {
		t.Fatal("expected traversal id rejection")
	}
}

func TestServerTLSRequiresClientCertificate(t *testing.T) {
	dir := t.TempDir()
	ca, serverCert, serverKey, clientCert, clientKey := makeTLSFiles(t, dir)
	watcher, err := NewCertWatcher(slog.New(slog.NewTextHandler(io.Discard, nil)), ca, serverCert, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := ServerTLSConfig(ca, watcher)
	if err != nil {
		t.Fatal(err)
	}
	if tlsConfig.MinVersion != 0x0304 || tlsConfig.ClientAuth != 4 { // TLS1.3 / RequireAndVerifyClientCert
		t.Fatalf("unexpected tls config: min=%x auth=%v", tlsConfig.MinVersion, tlsConfig.ClientAuth)
	}

	// httptest.Server.StartTLS() auto-injects its own dummy certificate
	// whenever tlsConfig.Certificates is empty -- true here since the
	// production ServerTLSConfig now serves via the GetCertificate
	// callback instead, same as internal/tlsreload's whole point. Wire up
	// a real net.Listen + tls.NewListener server by hand instead, exactly
	// the same construction cmd/kairon-node/main.go's configureMigration
	// actually uses, so this test exercises the real GetCertificate path
	// rather than httptest's unrelated auto-cert convenience behavior.
	driver := &fakeDestination{prepare: PrepareResult{TransferSupported: true, Endpoint: "opaque://ok"}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: (&Server{NodeName: "node-b", Store: NewFileStore(t.TempDir()), Driver: driver}).Handler(), TLSConfig: tlsConfig}
	tlsListener := tls.NewListener(listener, tlsConfig)
	go func() { _ = srv.Serve(tlsListener) }()
	defer func() { _ = srv.Close() }()
	url := "https://" + listener.Addr().String()

	pool := x509.NewCertPool()
	pemData, _ := os.ReadFile(ca)
	pool.AppendCertsFromPEM(pemData)
	noCert := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLSForTest(pool, nil)}}
	if _, err := NewClient(noCert).Prepare(context.Background(), url, testSession()); err == nil {
		t.Fatal("expected peer without client certificate to be rejected")
	}
	pair, err := tlsLoad(clientCert, clientKey)
	if err != nil {
		t.Fatal(err)
	}
	withCert := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLSForTest(pool, &pair)}}
	if _, err := NewClient(withCert).Prepare(context.Background(), url, testSession()); err != nil {
		t.Fatalf("mTLS client failed: %v", err)
	}
}

func clientTLSForTest(pool *x509.CertPool, cert *tls.Certificate) *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	return cfg
}

func tlsLoad(cert, key string) (tls.Certificate, error) { return tls.LoadX509KeyPair(cert, key) }

func makeTLSFiles(t *testing.T, dir string) (string, string, string, string, string) {
	t.Helper()
	caKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	now := time.Now().Add(-time.Minute)
	caT := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "kairon-test-ca"}, NotBefore: now, NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caT, caT, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "ca.crt")
	writePEM(t, caPath, "CERTIFICATE", caDER, 0o600)

	issue := func(serial int64, cn string, ips []net.IP, eku x509.ExtKeyUsage) (string, string) {
		key, _ := rsa.GenerateKey(rand.Reader, 2048)
		certT := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: cn}, NotBefore: now, NotAfter: now.Add(time.Hour), IPAddresses: ips, ExtKeyUsage: []x509.ExtKeyUsage{eku}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment}
		der, err := x509.CreateCertificate(rand.Reader, certT, caT, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		certPath := filepath.Join(dir, cn+".crt")
		keyPath := filepath.Join(dir, cn+".key")
		writePEM(t, certPath, "CERTIFICATE", der, 0o600)
		writePEM(t, keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key), 0o600)
		return certPath, keyPath
	}
	serverCert, serverKey := issue(2, "server", []net.IP{net.ParseIP("127.0.0.1")}, x509.ExtKeyUsageServerAuth)
	clientCert, clientKey := issue(3, "client", nil, x509.ExtKeyUsageClientAuth)
	return caPath, serverCert, serverKey, clientCert, clientKey
}

func writePEM(t *testing.T, path, typ string, der []byte, mode os.FileMode) {
	t.Helper()
	data := pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}
