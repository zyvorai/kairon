// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Command kairon-migration-adapter-fluxvm is a real implementation of the
// Kairon migration adapter contract (docs/migration-adapter.md), backed by
// FluxVM's own migration receiver endpoint (POST /v1/migration/receivers)
// and its existing source-side QEMU migration REST API
// (/v1/vms/{id}/migration/{start,status,cancel}). Unlike
// kairon-migration-adapter-stub, this adapter drives a real QEMU pre-copy
// migration -- it requires a FluxVM instance with the receiver endpoint
// (see fluxvm's docs/runtime-boundary-phase-2.md), and requires the
// Machine to use NetworkSpec::Tap/Macvtap with an explicit MAC (FluxVM's
// storage contract v1 also requires shared storage -- the same disk path
// must be reachable from both the source and target FluxVM instances).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zyvorai/kairon/internal/migration"
)

// --- FluxVM REST API client (only the endpoints this adapter needs) ---

type fluxClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newFluxClient(baseURL, token string) *fluxClient {
	return &fluxClient{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: &http.Client{Timeout: 20 * time.Second}}
}

func (c *fluxClient) do(ctx context.Context, method, path string, body, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("fluxvm %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode fluxvm response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// networkSpec/createVMSpec mirror only the fields of FluxVM's own
// CreateVmRequest/NetworkSpec this adapter needs to set -- every other
// field has a #[serde(default)] on the Rust side, so omitting them here
// is safe (they resolve to FluxVM's own defaults, e.g. StorageBackend::Default).
type networkSpec struct {
	Mode   string `json:"mode"`
	Bridge string `json:"bridge,omitempty"`
	MAC    string `json:"mac,omitempty"`
}

type createVMSpec struct {
	Name      string      `json:"name"`
	Backend   string      `json:"backend"`
	Image     string      `json:"image"`
	VCPUs     uint32      `json:"vcpus,omitempty"`
	MemoryMiB uint64      `json:"memory_mib,omitempty"`
	Network   networkSpec `json:"network"`
}

type receiverRequest struct {
	Spec               createVMSpec `json:"spec"`
	DiskPath           string       `json:"disk_path"`
	ReceiverTTLSeconds uint64       `json:"receiver_ttl_seconds,omitempty"`
	// MigrationBindAddress, when set, is the literal IP FluxVM's receiver
	// binds/advertises its -incoming listener on instead of 0.0.0.0.
	MigrationBindAddress string `json:"migration_bind_address,omitempty"`
}

type receiverInfo struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Port      uint16 `json:"port"`
	ExpiresAt string `json:"expires_at"`
}

type migrationStartRequest struct {
	Destination     string `json:"destination"`
	Mode            string `json:"mode,omitempty"`
	BandwidthMbps   uint64 `json:"bandwidth_mbps,omitempty"`
	MaxDowntimeMs   uint64 `json:"max_downtime_ms,omitempty"`
	MultifdChannels uint8  `json:"multifd_channels,omitempty"`
}

// migrationStatus mirrors FluxVM's MigrationStatus JSON shape.
type migrationStatus struct {
	Phase          string `json:"phase"`
	Status         string `json:"status"`
	RAMTransferred uint64 `json:"ram_transferred,omitempty"`
	RAMRemaining   uint64 `json:"ram_remaining,omitempty"`
	RAMTotal       uint64 `json:"ram_total,omitempty"`
	TotalTimeMs    uint64 `json:"total_time_ms,omitempty"`
	DowntimeMs     uint64 `json:"downtime_ms,omitempty"`
	Error          string `json:"error,omitempty"`
}

// fluxPhaseToKairon translates FluxVM's MigrationPhase (capitalized, e.g.
// "Completed") into the lowercase phase strings agent.go's projectTransfer
// already understands ("completed"/"transferred", "failed",
// "cancelled"/"canceled", "aborted", else treated as still running).
func fluxPhaseToKairon(phase string) string {
	switch strings.ToLower(phase) {
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "cancelled", "canceled":
		return "cancelled"
	default:
		return "running"
	}
}

func toTransferStatus(transferID string, s migrationStatus) migration.TransferStatus {
	return migration.TransferStatus{
		TransferID:     transferID,
		Phase:          fluxPhaseToKairon(s.Phase),
		Message:        s.Error,
		RAMTransferred: s.RAMTransferred,
		RAMRemaining:   s.RAMRemaining,
		RAMTotal:       s.RAMTotal,
		TotalTimeMs:    s.TotalTimeMs,
		DowntimeMs:     s.DowntimeMs,
	}
}

// --- adapter server: implements the documented Unix-socket contract ---

type adapter struct {
	log            *slog.Logger
	flux           *fluxClient
	advertiseHost  string
	bridge         string
	receiverTTLSec uint64
	// migrationNetworks maps a migration network name (MachineMigrationSpec.MigrationNetwork)
	// to the literal IP the receiver should bind/advertise on, configured
	// via repeated -migration-network name=ip flags. A session with no
	// MigrationNetwork set ignores this map entirely (advertiseHost/0.0.0.0
	// default behavior, unchanged).
	migrationNetworks map[string]string

	mu         sync.Mutex
	destByID   map[string]string // session.ID -> fluxvm receiver id
	sourceRTID map[string]string // session.ID -> source runtimeID (for status/abort)
}

func newAdapter(log *slog.Logger, flux *fluxClient, advertiseHost, bridge string, ttl uint64, migrationNetworks map[string]string) *adapter {
	return &adapter{
		log: log, flux: flux, advertiseHost: advertiseHost, bridge: bridge, receiverTTLSec: ttl,
		migrationNetworks: migrationNetworks,
		destByID:          map[string]string{},
		sourceRTID:        map[string]string{},
	}
}

func (a *adapter) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/destination/prepare", a.prepare)
	mux.HandleFunc("POST /v1/destination/{id}/commit", a.destCommit)
	mux.HandleFunc("POST /v1/destination/{id}/abort", a.destAbort)
	mux.HandleFunc("POST /v1/source/start", a.sourceStart)
	mux.HandleFunc("GET /v1/source/{sessionID}/{transferID}", a.sourceStatus)
	mux.HandleFunc("POST /v1/source/{sessionID}/{transferID}/abort", a.sourceAbort)
	return http.MaxBytesHandler(mux, 1<<20)
}

func (a *adapter) prepare(w http.ResponseWriter, r *http.Request) {
	var req migration.PrepareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session := req.Session
	if session.DiskPath == "" || session.MAC == "" {
		writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("session missing diskPath/mac -- kairon-node's Session extension is required for this adapter"))
		return
	}
	backend := session.Backend
	if backend == "" {
		backend = "qemu"
	}
	advertiseHost := a.advertiseHost
	bindAddress := ""
	if session.MigrationNetwork != "" {
		ip, ok := a.migrationNetworks[session.MigrationNetwork]
		if !ok {
			writeJSON(w, http.StatusOK, migration.PrepareResult{
				TransferSupported: false,
				Reason:            fmt.Sprintf("unknown migrationNetwork %q (not configured via -migration-network on this adapter)", session.MigrationNetwork),
			})
			return
		}
		bindAddress = ip
		advertiseHost = ip
	}
	receiverReq := receiverRequest{
		Spec: createVMSpec{
			Name:      session.Machine,
			Backend:   backend,
			Image:     session.DiskPath, // ignored by create_receiver's logic, but a required field
			VCPUs:     session.VCPUs,
			MemoryMiB: session.MemoryMiB,
			Network: networkSpec{
				Mode:   "tap",
				Bridge: a.bridge,
				MAC:    session.MAC,
			},
		},
		DiskPath:             session.DiskPath,
		ReceiverTTLSeconds:   a.receiverTTLSec,
		MigrationBindAddress: bindAddress,
	}
	var info receiverInfo
	if _, err := a.flux.do(r.Context(), http.MethodPost, "/v1/migration/receivers", receiverReq, &info); err != nil {
		a.log.Error("fluxvm: create receiver failed", "session", session.ID, "error", err)
		writeJSON(w, http.StatusOK, migration.PrepareResult{TransferSupported: false, Reason: err.Error()})
		return
	}
	a.mu.Lock()
	a.destByID[session.ID] = info.ID
	a.mu.Unlock()
	a.log.Info("fluxvm: receiver created", "session", session.ID, "receiverID", info.ID, "port", info.Port, "migrationNetwork", session.MigrationNetwork)
	writeJSON(w, http.StatusOK, migration.PrepareResult{
		TransferSupported: true,
		Endpoint:          fmt.Sprintf("tcp:%s:%d", advertiseHost, info.Port),
		Backend:           backend,
	})
}

func (a *adapter) destCommit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.mu.Lock()
	receiverID, ok := a.destByID[id]
	a.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown destination session %q", id))
		return
	}
	if _, err := a.flux.do(r.Context(), http.MethodPost, "/v1/migration/receivers/"+receiverID+"/activate", nil, nil); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	a.log.Info("fluxvm: receiver activated", "session", id, "receiverID", receiverID)
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (a *adapter) destAbort(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.mu.Lock()
	receiverID, ok := a.destByID[id]
	delete(a.destByID, id)
	a.mu.Unlock()
	if ok {
		if _, err := a.flux.do(r.Context(), http.MethodDelete, "/v1/migration/receivers/"+receiverID, nil, nil); err != nil {
			a.log.Warn("fluxvm: receiver abort failed", "session", id, "receiverID", receiverID, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (a *adapter) sourceStart(w http.ResponseWriter, r *http.Request) {
	var req migration.SourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.RuntimeID == "" {
		writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("source request missing runtimeID"))
		return
	}
	mode := req.Options.Mode
	if mode == "" {
		mode = "pre-copy"
	}
	startReq := migrationStartRequest{
		Destination:     req.Endpoint,
		Mode:            mode,
		BandwidthMbps:   req.Options.BandwidthMbps,
		MaxDowntimeMs:   req.Options.MaxDowntimeMs,
		MultifdChannels: req.Options.MultifdChannels,
	}
	var status migrationStatus
	if _, err := a.flux.do(r.Context(), http.MethodPost, "/v1/vms/"+req.RuntimeID+"/migration/start", startReq, &status); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	transferID := req.Session.ID // FluxVM's migration state is keyed by the source VM id, not a separate transfer id -- reuse the session id as our own handle.
	a.mu.Lock()
	a.sourceRTID[transferID] = req.RuntimeID
	a.mu.Unlock()
	a.log.Info("fluxvm: source migration started", "session", req.Session.ID, "runtimeID", req.RuntimeID, "destination", req.Endpoint)
	writeJSON(w, http.StatusOK, toTransferStatus(transferID, status))
}

func (a *adapter) sourceStatus(w http.ResponseWriter, r *http.Request) {
	transferID := r.PathValue("transferID")
	a.mu.Lock()
	runtimeID, ok := a.sourceRTID[transferID]
	a.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown transfer %q", transferID))
		return
	}
	var status migrationStatus
	if _, err := a.flux.do(r.Context(), http.MethodGet, "/v1/vms/"+runtimeID+"/migration/status", nil, &status); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, toTransferStatus(transferID, status))
}

func (a *adapter) sourceAbort(w http.ResponseWriter, r *http.Request) {
	transferID := r.PathValue("transferID")
	a.mu.Lock()
	runtimeID, ok := a.sourceRTID[transferID]
	a.mu.Unlock()
	if ok {
		if _, err := a.flux.do(r.Context(), http.MethodPost, "/v1/vms/"+runtimeID+"/migration/cancel", nil, nil); err != nil {
			a.log.Warn("fluxvm: source migration cancel failed", "runtimeID", runtimeID, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// migrationNetworkFlag accumulates repeated -migration-network name=ip
// flags into a name -> IP map.
type migrationNetworkFlag map[string]string

func (m migrationNetworkFlag) String() string {
	parts := make([]string, 0, len(m))
	for name, ip := range m {
		parts = append(parts, name+"="+ip)
	}
	return strings.Join(parts, ",")
}

func (m migrationNetworkFlag) Set(value string) error {
	name, ip, ok := strings.Cut(value, "=")
	if !ok || name == "" || ip == "" {
		return fmt.Errorf("expected name=ip, got %q", value)
	}
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("invalid IP %q for migration network %q", ip, name)
	}
	m[name] = ip
	return nil
}

func main() {
	socket := flag.String("socket", "", "Unix socket path to listen on (required)")
	fluxURL := flag.String("fluxvm-url", "http://127.0.0.1:8080", "local FluxVM REST API base URL")
	fluxToken := flag.String("fluxvm-token", os.Getenv("FLUXVM_TOKEN"), "FluxVM admin Bearer token (default: $FLUXVM_TOKEN)")
	advertiseHost := flag.String("advertise-host", "127.0.0.1", "address migration peers should use to reach this host's receivers")
	bridge := flag.String("bridge", "virbr0", "host bridge for receiver tap devices")
	receiverTTL := flag.Uint64("receiver-ttl-seconds", 300, "how long an unclaimed receiver is kept before FluxVM reclaims it")
	migrationNetworks := make(migrationNetworkFlag)
	flag.Var(migrationNetworks, "migration-network", "name=ip mapping a MachineMigration's migrationNetwork to the address the receiver binds/advertises (repeatable)")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if *socket == "" {
		log.Error("--socket is required")
		os.Exit(2)
	}

	flux := newFluxClient(*fluxURL, *fluxToken)
	a := newAdapter(log, flux, *advertiseHost, *bridge, *receiverTTL, migrationNetworks)

	_ = os.Remove(*socket)
	listener, err := net.Listen("unix", *socket)
	if err != nil {
		log.Error("listen", "error", err)
		os.Exit(1)
	}
	if err := os.Chmod(*socket, 0o660); err != nil {
		log.Error("chmod socket", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	srv := &http.Server{Handler: a.handler()}
	go func() {
		<-ctx.Done()
		c, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = srv.Shutdown(c)
	}()

	log.Info("kairon-migration-adapter-fluxvm listening", "socket", *socket, "fluxvmURL", *fluxURL)
	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Error("serve", "error", err)
		os.Exit(1)
	}
}
