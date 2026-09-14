// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kube

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zyvorai/kairon/internal/model"
)

const serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	// Observe, when set, is called after every apiserver request with the
	// HTTP method, how long it took, and its outcome -- kairon-controller/
	// kairon-node/kairon-ui each wire this to their own
	// internal/metrics.Recorder for a kairon_apiserver_request_duration_seconds
	// histogram (see cmd/*/main.go). Optional so internal/kube has no hard
	// dependency on internal/metrics, the same nil-checked-callback
	// pattern internal/admission.Handler already uses for its own
	// log/observe parameters.
	Observe func(method string, d time.Duration, err error)
}

type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("kubernetes %s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

func IsNotFound(err error) bool {
	if e, ok := err.(*APIError); ok {
		return e.StatusCode == http.StatusNotFound
	}
	return false
}

// IsConflict reports whether err is the API server rejecting a write
// because the object's resourceVersion is stale -- internal/leaderelection
// relies on this to detect it lost a race to acquire/renew/take over a
// Lease, rather than treating the write as a hard failure.
func IsConflict(err error) bool {
	if e, ok := err.(*APIError); ok {
		return e.StatusCode == http.StatusConflict
	}
	return false
}

func FromEnvironment() (*Client, error) {
	if base := os.Getenv("KAIRON_KUBE_URL"); base != "" {
		return New(base, os.Getenv("KAIRON_KUBE_TOKEN"), os.Getenv("KAIRON_KUBE_CA"), os.Getenv("KAIRON_KUBE_INSECURE") == "true")
	}
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, fmt.Errorf("kubernetes endpoint not configured: set KAIRON_KUBE_URL or run in cluster")
	}
	token, err := os.ReadFile(filepath.Join(serviceAccountDir, "token"))
	if err != nil {
		return nil, fmt.Errorf("read service-account token: %w", err)
	}
	return New("https://"+host+":"+port, strings.TrimSpace(string(token)), filepath.Join(serviceAccountDir, "ca.crt"), false)
}

func New(baseURL, token, caPath string, insecure bool) (*Client, error) {
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure}} // #nosec G402 - opt-in dev mode
	if caPath != "" && !insecure {
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("read CA file %s: %w", caPath, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("invalid CA file %s", caPath)
		}
		tr.TLSClientConfig.RootCAs = pool
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Transport: tr, Timeout: 20 * time.Second}}, nil
}

func (c *Client) request(ctx context.Context, method, path string, body any, out any, contentType string) error {
	start := time.Now()
	err := c.doRequest(ctx, method, path, body, out, contentType)
	if c.Observe != nil {
		c.Observe(method, time.Since(start), err)
	}
	return err
}

func (c *Client) doRequest(ctx context.Context, method, path string, body any, out any, contentType string) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, r)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode Kubernetes response: %w", err)
		}
	}
	return nil
}

func namespacePath(ns, resource string) string {
	return fmt.Sprintf("/apis/kairon.zyvor.dev/v1alpha1/namespaces/%s/%s", url.PathEscape(ns), resource)
}

func namespacedObjectPath(ns, resource, name string) string {
	return namespacePath(ns, resource) + "/" + url.PathEscape(name)
}

func (c *Client) ListMachines(ctx context.Context) ([]model.Machine, error) {
	var list model.MachineList
	err := c.request(ctx, http.MethodGet, "/apis/kairon.zyvor.dev/v1alpha1/machines", nil, &list, "")
	return list.Items, err
}

func (c *Client) ListMachinesNamespace(ctx context.Context, ns string) ([]model.Machine, error) {
	var list model.MachineList
	err := c.request(ctx, http.MethodGet, namespacePath(ns, "machines"), nil, &list, "")
	return list.Items, err
}

func (c *Client) GetMachine(ctx context.Context, ns, name string) (model.Machine, error) {
	var m model.Machine
	err := c.request(ctx, http.MethodGet, namespacedObjectPath(ns, "machines", name), nil, &m, "")
	return m, err
}

func (c *Client) CreateMachine(ctx context.Context, ns string, m model.Machine) (model.Machine, error) {
	var out model.Machine
	err := c.request(ctx, http.MethodPost, namespacePath(ns, "machines"), m, &out, "")
	return out, err
}

func (c *Client) DeleteMachine(ctx context.Context, ns, name string) error {
	return c.request(ctx, http.MethodDelete, namespacedObjectPath(ns, "machines", name), map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "propagationPolicy": "Foreground"}, nil, "")
}

func (c *Client) PatchMachine(ctx context.Context, ns, name string, patch map[string]any) error {
	return c.request(ctx, http.MethodPatch, namespacedObjectPath(ns, "machines", name), patch, nil, "application/merge-patch+json")
}

func (c *Client) PatchMachineStatus(ctx context.Context, ns, name string, status model.MachineStatus) error {
	return c.request(ctx, http.MethodPatch, namespacedObjectPath(ns, "machines", name)+"/status", map[string]any{"status": status}, nil, "application/merge-patch+json")
}

func (c *Client) ListMachineMigrations(ctx context.Context) ([]model.MachineMigration, error) {
	var list model.MachineMigrationList
	err := c.request(ctx, http.MethodGet, "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations", nil, &list, "")
	return list.Items, err
}

func (c *Client) ListMachineMigrationsNamespace(ctx context.Context, ns string) ([]model.MachineMigration, error) {
	var list model.MachineMigrationList
	err := c.request(ctx, http.MethodGet, namespacePath(ns, "machinemigrations"), nil, &list, "")
	return list.Items, err
}

func (c *Client) GetMachineMigration(ctx context.Context, ns, name string) (model.MachineMigration, error) {
	var m model.MachineMigration
	err := c.request(ctx, http.MethodGet, namespacedObjectPath(ns, "machinemigrations", name), nil, &m, "")
	return m, err
}

func (c *Client) CreateMachineMigration(ctx context.Context, ns string, m model.MachineMigration) (model.MachineMigration, error) {
	var out model.MachineMigration
	err := c.request(ctx, http.MethodPost, namespacePath(ns, "machinemigrations"), m, &out, "")
	return out, err
}

func (c *Client) PatchMachineMigrationStatus(ctx context.Context, ns, name string, status model.MachineMigrationStatus) error {
	return c.request(ctx, http.MethodPatch, namespacedObjectPath(ns, "machinemigrations", name)+"/status", map[string]any{"status": status}, nil, "application/merge-patch+json")
}

func (c *Client) PatchMachineMigration(ctx context.Context, ns, name string, patch map[string]any) error {
	return c.request(ctx, http.MethodPatch, namespacedObjectPath(ns, "machinemigrations", name), patch, nil, "application/merge-patch+json")
}

func (c *Client) ListMachineSnapshots(ctx context.Context) ([]model.MachineSnapshot, error) {
	var list model.MachineSnapshotList
	err := c.request(ctx, http.MethodGet, "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots", nil, &list, "")
	return list.Items, err
}

func (c *Client) ListMachineSnapshotsNamespace(ctx context.Context, ns string) ([]model.MachineSnapshot, error) {
	var list model.MachineSnapshotList
	err := c.request(ctx, http.MethodGet, namespacePath(ns, "machinesnapshots"), nil, &list, "")
	return list.Items, err
}

func (c *Client) GetMachineSnapshot(ctx context.Context, ns, name string) (model.MachineSnapshot, error) {
	var s model.MachineSnapshot
	err := c.request(ctx, http.MethodGet, namespacedObjectPath(ns, "machinesnapshots", name), nil, &s, "")
	return s, err
}

func (c *Client) CreateMachineSnapshot(ctx context.Context, ns string, s model.MachineSnapshot) (model.MachineSnapshot, error) {
	var out model.MachineSnapshot
	err := c.request(ctx, http.MethodPost, namespacePath(ns, "machinesnapshots"), s, &out, "")
	return out, err
}

func (c *Client) PatchMachineSnapshotStatus(ctx context.Context, ns, name string, status model.MachineSnapshotStatus) error {
	return c.request(ctx, http.MethodPatch, namespacedObjectPath(ns, "machinesnapshots", name)+"/status", map[string]any{"status": status}, nil, "application/merge-patch+json")
}

func (c *Client) ListMachineSnapshotRestores(ctx context.Context) ([]model.MachineSnapshotRestore, error) {
	var list model.MachineSnapshotRestoreList
	err := c.request(ctx, http.MethodGet, "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshotrestores", nil, &list, "")
	return list.Items, err
}

func (c *Client) ListMachineSnapshotRestoresNamespace(ctx context.Context, ns string) ([]model.MachineSnapshotRestore, error) {
	var list model.MachineSnapshotRestoreList
	err := c.request(ctx, http.MethodGet, namespacePath(ns, "machinesnapshotrestores"), nil, &list, "")
	return list.Items, err
}

func (c *Client) CreateMachineSnapshotRestore(ctx context.Context, ns string, r model.MachineSnapshotRestore) (model.MachineSnapshotRestore, error) {
	var out model.MachineSnapshotRestore
	err := c.request(ctx, http.MethodPost, namespacePath(ns, "machinesnapshotrestores"), r, &out, "")
	return out, err
}

func (c *Client) PatchMachineSnapshotRestoreStatus(ctx context.Context, ns, name string, status model.MachineSnapshotRestoreStatus) error {
	return c.request(ctx, http.MethodPatch, namespacedObjectPath(ns, "machinesnapshotrestores", name)+"/status", map[string]any{"status": status}, nil, "application/merge-patch+json")
}

func (c *Client) ListMachineNetworkPolicies(ctx context.Context) ([]model.MachineNetworkPolicy, error) {
	var list model.MachineNetworkPolicyList
	err := c.request(ctx, http.MethodGet, "/apis/kairon.zyvor.dev/v1alpha1/machinenetworkpolicies", nil, &list, "")
	return list.Items, err
}

func (c *Client) PatchMachineNetworkPolicy(ctx context.Context, ns, name string, patch map[string]any) error {
	return c.request(ctx, http.MethodPatch, namespacedObjectPath(ns, "machinenetworkpolicies", name), patch, nil, "application/merge-patch+json")
}

func (c *Client) PatchMachineNetworkPolicyStatus(ctx context.Context, ns, name string, status model.MachineNetworkPolicyStatus) error {
	return c.request(ctx, http.MethodPatch, namespacedObjectPath(ns, "machinenetworkpolicies", name)+"/status", map[string]any{"status": status}, nil, "application/merge-patch+json")
}

func (c *Client) ListNetworkSecurityGroups(ctx context.Context) ([]model.NetworkSecurityGroup, error) {
	var list model.NetworkSecurityGroupList
	err := c.request(ctx, http.MethodGet, "/apis/kairon.zyvor.dev/v1alpha1/networksecuritygroups", nil, &list, "")
	return list.Items, err
}

func (c *Client) PatchNetworkSecurityGroup(ctx context.Context, ns, name string, patch map[string]any) error {
	return c.request(ctx, http.MethodPatch, namespacedObjectPath(ns, "networksecuritygroups", name), patch, nil, "application/merge-patch+json")
}

func (c *Client) PatchNetworkSecurityGroupStatus(ctx context.Context, ns, name string, status model.NetworkSecurityGroupStatus) error {
	return c.request(ctx, http.MethodPatch, namespacedObjectPath(ns, "networksecuritygroups", name)+"/status", map[string]any{"status": status}, nil, "application/merge-patch+json")
}

func (c *Client) GetResourceClaim(ctx context.Context, ns, name string) (model.ResourceClaim, error) {
	var claim model.ResourceClaim
	path := fmt.Sprintf("/apis/resource.k8s.io/v1/namespaces/%s/resourceclaims/%s", url.PathEscape(ns), url.PathEscape(name))
	err := c.request(ctx, http.MethodGet, path, nil, &claim, "")
	return claim, err
}

// ListResourceClaims and ListResourceSlices back
// internal/controller's DRA topology-awareness scheduling hint (see
// draPreferredNode) -- both are read-only, cluster-wide lists, called at
// most once per reconcile tick and only when at least one Machine actually
// has spec.deviceClaims set, so a cluster without the DRA API enabled at
// all never pays for (or errors on) either call.
func (c *Client) ListResourceClaims(ctx context.Context) ([]model.ResourceClaim, error) {
	var list model.ResourceClaimList
	err := c.request(ctx, http.MethodGet, "/apis/resource.k8s.io/v1/resourceclaims", nil, &list, "")
	return list.Items, err
}

func (c *Client) ListResourceSlices(ctx context.Context) ([]model.ResourceSlice, error) {
	var list model.ResourceSliceList
	err := c.request(ctx, http.MethodGet, "/apis/resource.k8s.io/v1/resourceslices", nil, &list, "")
	return list.Items, err
}

func (c *Client) GetVolumeSnapshot(ctx context.Context, ns, name string) (model.VolumeSnapshot, error) {
	var snap model.VolumeSnapshot
	path := fmt.Sprintf("/apis/snapshot.storage.k8s.io/v1/namespaces/%s/volumesnapshots/%s", url.PathEscape(ns), url.PathEscape(name))
	err := c.request(ctx, http.MethodGet, path, nil, &snap, "")
	return snap, err
}

func (c *Client) CreateVolumeSnapshot(ctx context.Context, ns string, snap model.VolumeSnapshot) (model.VolumeSnapshot, error) {
	var out model.VolumeSnapshot
	path := fmt.Sprintf("/apis/snapshot.storage.k8s.io/v1/namespaces/%s/volumesnapshots", url.PathEscape(ns))
	err := c.request(ctx, http.MethodPost, path, snap, &out, "")
	return out, err
}

func (c *Client) ListMachineQuotas(ctx context.Context) ([]model.MachineQuota, error) {
	var list model.MachineQuotaList
	err := c.request(ctx, http.MethodGet, "/apis/kairon.zyvor.dev/v1alpha1/machinequotas", nil, &list, "")
	return list.Items, err
}

func (c *Client) ListMachineQuotasNamespace(ctx context.Context, ns string) ([]model.MachineQuota, error) {
	var list model.MachineQuotaList
	err := c.request(ctx, http.MethodGet, namespacePath(ns, "machinequotas"), nil, &list, "")
	return list.Items, err
}

func (c *Client) PatchMachineQuotaStatus(ctx context.Context, ns, name string, status model.MachineQuotaStatus) error {
	return c.request(ctx, http.MethodPatch, namespacedObjectPath(ns, "machinequotas", name)+"/status", map[string]any{"status": status}, nil, "application/merge-patch+json")
}

func (c *Client) ListMachineDisruptionBudgets(ctx context.Context) ([]model.MachineDisruptionBudget, error) {
	var list model.MachineDisruptionBudgetList
	err := c.request(ctx, http.MethodGet, "/apis/kairon.zyvor.dev/v1alpha1/machinedisruptionbudgets", nil, &list, "")
	return list.Items, err
}

func (c *Client) ListMachineDisruptionBudgetsNamespace(ctx context.Context, ns string) ([]model.MachineDisruptionBudget, error) {
	var list model.MachineDisruptionBudgetList
	err := c.request(ctx, http.MethodGet, namespacePath(ns, "machinedisruptionbudgets"), nil, &list, "")
	return list.Items, err
}

func (c *Client) PatchMachineDisruptionBudgetStatus(ctx context.Context, ns, name string, status model.MachineDisruptionBudgetStatus) error {
	return c.request(ctx, http.MethodPatch, namespacedObjectPath(ns, "machinedisruptionbudgets", name)+"/status", map[string]any{"status": status}, nil, "application/merge-patch+json")
}

func (c *Client) CreatePersistentVolumeClaim(ctx context.Context, ns string, pvc model.PersistentVolumeClaim) (model.PersistentVolumeClaim, error) {
	var out model.PersistentVolumeClaim
	err := c.request(ctx, http.MethodPost, fmt.Sprintf("/api/v1/namespaces/%s/persistentvolumeclaims", url.PathEscape(ns)), pvc, &out, "")
	return out, err
}

func (c *Client) GetPersistentVolumeClaim(ctx context.Context, ns, name string) (model.PersistentVolumeClaim, error) {
	var pvc model.PersistentVolumeClaim
	path := fmt.Sprintf("/api/v1/namespaces/%s/persistentvolumeclaims/%s", url.PathEscape(ns), url.PathEscape(name))
	err := c.request(ctx, http.MethodGet, path, nil, &pvc, "")
	return pvc, err
}

func (c *Client) GetPersistentVolume(ctx context.Context, name string) (model.PersistentVolume, error) {
	var pv model.PersistentVolume
	path := "/api/v1/persistentvolumes/" + url.PathEscape(name)
	err := c.request(ctx, http.MethodGet, path, nil, &pv, "")
	return pv, err
}

// PatchSecretStringData merge-patches a core/v1 Secret's stringData field
// -- the API server base64-encodes each value into .data itself, so
// callers never handle encoding. Used by internal/uiapi to persist a
// runtime password change back into the kairon-ui-users Secret (see
// uiapi.Server.persistUsers); there is deliberately no generic PatchSecret
// or GetSecret here, mirroring this client's existing one-method-per-intent
// shape rather than a passthrough.
func (c *Client) PatchSecretStringData(ctx context.Context, ns, name string, stringData map[string]string) error {
	path := fmt.Sprintf("/api/v1/namespaces/%s/secrets/%s", url.PathEscape(ns), url.PathEscape(name))
	return c.request(ctx, http.MethodPatch, path, map[string]any{"stringData": stringData}, nil, "application/merge-patch+json")
}

// GetSecret reads back a core/v1 Secret -- used by internal/uiapi to
// notice a password change another kairon-ui replica made and persisted
// (see PatchSecretStringData), since Server.Users is otherwise only
// loaded once at startup.
func (c *Client) GetSecret(ctx context.Context, ns, name string) (model.Secret, error) {
	var s model.Secret
	path := fmt.Sprintf("/api/v1/namespaces/%s/secrets/%s", url.PathEscape(ns), url.PathEscape(name))
	err := c.request(ctx, http.MethodGet, path, nil, &s, "")
	return s, err
}

// GetConfigMap and PatchConfigMapData back internal/uiapi's cross-replica
// session/lockout/console-ticket state (see internal/uiapi/sharedstate.go).
// PatchConfigMapData's data values are `any` rather than `string` so a
// caller can pass a nil value for a key to delete it -- an
// application/merge-patch+json body with a null value removes that key
// (RFC 7386), leaving every other key in .data untouched. That per-key
// merge-patch shape is exactly why concurrent writers touching different
// keys never conflict: there is no whole-object read-modify-write here.
func (c *Client) GetConfigMap(ctx context.Context, ns, name string) (model.ConfigMap, error) {
	var cm model.ConfigMap
	path := fmt.Sprintf("/api/v1/namespaces/%s/configmaps/%s", url.PathEscape(ns), url.PathEscape(name))
	err := c.request(ctx, http.MethodGet, path, nil, &cm, "")
	return cm, err
}

func (c *Client) PatchConfigMapData(ctx context.Context, ns, name string, data map[string]any) error {
	path := fmt.Sprintf("/api/v1/namespaces/%s/configmaps/%s", url.PathEscape(ns), url.PathEscape(name))
	return c.request(ctx, http.MethodPatch, path, map[string]any{"data": data}, nil, "application/merge-patch+json")
}

func (c *Client) ListNodes(ctx context.Context) ([]model.Node, error) {
	var list model.NodeList
	err := c.request(ctx, http.MethodGet, "/api/v1/nodes", nil, &list, "")
	return list.Items, err
}

func leasePath(ns, name string) string {
	return fmt.Sprintf("/apis/coordination.k8s.io/v1/namespaces/%s/leases/%s", url.PathEscape(ns), url.PathEscape(name))
}

// GetLease, CreateLease, and UpdateLease back kairon-controller's own
// leader election (see internal/leaderelection) -- a coordination.k8s.io/v1
// Lease is a core Kubernetes API type, not one of Kairon's own
// kairon.zyvor.dev CRDs, hence the separate leasePath helper rather than
// namespacePath/namespacedObjectPath above.
func (c *Client) GetLease(ctx context.Context, ns, name string) (model.Lease, error) {
	var l model.Lease
	err := c.request(ctx, http.MethodGet, leasePath(ns, name), nil, &l, "")
	return l, err
}

func (c *Client) CreateLease(ctx context.Context, ns string, lease model.Lease) (model.Lease, error) {
	var out model.Lease
	path := fmt.Sprintf("/apis/coordination.k8s.io/v1/namespaces/%s/leases", url.PathEscape(ns))
	err := c.request(ctx, http.MethodPost, path, lease, &out, "")
	return out, err
}

// UpdateLease requires lease.Metadata.ResourceVersion to be the version
// last read. The API server rejects the write with HTTP 409 (surfaced as
// an *APIError that IsConflict recognizes) if the Lease changed since --
// exactly how internal/leaderelection detects it lost a race to renew or
// take over the lease, without needing a separate compare-and-swap
// primitive of its own.
func (c *Client) UpdateLease(ctx context.Context, ns string, lease model.Lease) (model.Lease, error) {
	var out model.Lease
	err := c.request(ctx, http.MethodPut, leasePath(ns, lease.Metadata.Name), lease, &out, "")
	return out, err
}
