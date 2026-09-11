package fluxvm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zyvorai/kairon/internal/model"
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

type Record struct {
	IDValue string `json:"id,omitempty"`
	UUID    string `json:"uuid,omitempty"`
	Name    string `json:"name,omitempty"`
	Status  string `json:"status,omitempty"`
	GuestIP string `json:"guest_ip,omitempty"`
}

func (r Record) ID() string {
	if r.IDValue != "" {
		return r.IDValue
	}
	return r.UUID
}

type CloudInitPayload struct {
	Hostname          string             `json:"hostname,omitempty"`
	User              string             `json:"user,omitempty"`
	SSHAuthorizedKeys []string           `json:"ssh_authorized_keys,omitempty"`
	Packages          []string           `json:"packages,omitempty"`
	RunCmd            []string           `json:"runcmd,omitempty"`
	WriteFiles        []CloudInitFilePay `json:"write_files,omitempty"`
}

type CloudInitFilePay struct {
	Path        string `json:"path"`
	Content     string `json:"content"`
	Permissions string `json:"permissions,omitempty"`
}

type SharedFolderPay struct {
	HostPath  string `json:"host_path"`
	GuestPath string `json:"guest_path"`
	ReadOnly  bool   `json:"read_only,omitempty"`
}

// CreateRequest mirrors FluxVM CreateVmRequest JSON contract.
type CreateRequest struct {
	Name          string            `json:"name"`
	Tenant        string            `json:"tenant,omitempty"`
	Backend       string            `json:"backend"`
	Image         string            `json:"image"`
	Kernel        string            `json:"kernel,omitempty"`
	VCPUs         uint32            `json:"vcpus"`
	MemoryMiB     uint64            `json:"memory_mib"`
	DiskSizeGiB   *uint64           `json:"disk_size_gib,omitempty"`
	Network       map[string]any    `json:"network,omitempty"`
	TTLSeconds    int64             `json:"ttl_seconds,omitempty"`
	CloudInit     *CloudInitPayload `json:"cloud_init,omitempty"`
	Storage       string            `json:"storage,omitempty"`
	SharedFolders []SharedFolderPay `json:"shared_folders,omitempty"`
	SecureBoot    bool              `json:"secure_boot,omitempty"`
	TPM           bool              `json:"tpm,omitempty"`
}

type ConsoleInfo struct {
	URL    string `json:"url,omitempty"`
	Type   string `json:"type,omitempty"`
	Serial string `json:"serial,omitempty"`
	VNC    string `json:"vnc,omitempty"`
}

func New(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fluxvm %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (c *Client) Ready(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "/readyz", nil)
	return err
}

func (c *Client) LookupByName(ctx context.Context, name string) (*Record, error) {
	data, err := c.do(ctx, http.MethodGet, "/v1/vms?name="+url.QueryEscape(name), nil)
	if err != nil {
		return nil, err
	}
	var arr []Record
	if json.Unmarshal(data, &arr) == nil {
		if len(arr) == 0 {
			return nil, nil
		}
		return &arr[0], nil
	}
	var wrapper struct {
		Items []Record `json:"items"`
		VMs   []Record `json:"vms"`
	}
	if err := json.Unmarshal(data, &wrapper); err == nil {
		if len(wrapper.Items) > 0 {
			return &wrapper.Items[0], nil
		}
		if len(wrapper.VMs) > 0 {
			return &wrapper.VMs[0], nil
		}
		return nil, nil
	}
	var one Record
	if err := json.Unmarshal(data, &one); err != nil {
		return nil, fmt.Errorf("decode FluxVM lookup: %w", err)
	}
	if one.ID() == "" && one.Name == "" {
		return nil, nil
	}
	return &one, nil
}

func (c *Client) Get(ctx context.Context, id string) (*Record, error) {
	data, err := c.do(ctx, http.MethodGet, "/v1/vms/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func buildCloudInit(ci model.CloudInitSpec) *CloudInitPayload {
	if ci.Hostname == "" && ci.User == "" && len(ci.SSHPublicKeys) == 0 && len(ci.Packages) == 0 && len(ci.RunCmd) == 0 && len(ci.WriteFiles) == 0 && ci.UserData == "" {
		return nil
	}
	out := &CloudInitPayload{
		Hostname:          ci.Hostname,
		User:              ci.User,
		SSHAuthorizedKeys: append([]string{}, ci.SSHPublicKeys...),
		Packages:          append([]string{}, ci.Packages...),
		RunCmd:            append([]string{}, ci.RunCmd...),
	}
	for _, f := range ci.WriteFiles {
		out.WriteFiles = append(out.WriteFiles, CloudInitFilePay{Path: f.Path, Content: f.Content, Permissions: f.Permissions})
	}
	if ci.UserData != "" {
		out.WriteFiles = append(out.WriteFiles, CloudInitFilePay{
			Path:        "/var/lib/kairon/user-data",
			Content:     ci.UserData,
			Permissions: "0644",
		})
	}
	return out
}

func (c *Client) Create(ctx context.Context, m model.Machine, defaultBackend string) (*Record, error) {
	cpu, err := model.ParseVCPUs(m.Spec.Resources.CPU)
	if err != nil {
		return nil, err
	}
	mem, err := model.ParseMemoryMiB(m.Spec.Resources.Memory)
	if err != nil {
		return nil, err
	}
	backend := m.Spec.Runtime.Backend
	if backend == "" || backend == "auto" {
		backend = defaultBackend
	}
	if backend == "" {
		backend = "qemu"
	}
	network := map[string]any{}
	if mode := m.Spec.Network.Mode; mode != "" {
		network["mode"] = mode
	} else {
		network["mode"] = "user"
	}
	if m.Spec.Network.NetNS {
		network["netns"] = true
	}
	if m.Spec.Network.Bridge != "" {
		network["bridge"] = m.Spec.Network.Bridge
	}
	if m.Spec.Network.Parent != "" {
		network["parent"] = m.Spec.Network.Parent
	}
	if m.Spec.Network.MAC != "" {
		network["mac"] = m.Spec.Network.MAC
	}
	storage := m.Spec.Storage
	if storage == "" {
		storage = "default"
	}
	payload := CreateRequest{
		Name:       m.RuntimeName(),
		Tenant:     m.Namespace(),
		Backend:    backend,
		Image:      m.Spec.Image.Path,
		Kernel:     m.Spec.Runtime.Kernel,
		VCPUs:      cpu,
		MemoryMiB:  mem,
		Network:    network,
		TTLSeconds: m.Spec.TTLSeconds,
		CloudInit:  buildCloudInit(m.Spec.CloudInit),
		Storage:    storage,
		SecureBoot: m.Spec.Security.SecureBoot,
		TPM:        m.Spec.Security.TPM,
	}
	if m.Spec.DiskSizeGiB > 0 {
		v := uint64(m.Spec.DiskSizeGiB)
		payload.DiskSizeGiB = &v
	}
	for _, sf := range m.Spec.SharedFolders {
		payload.SharedFolders = append(payload.SharedFolders, SharedFolderPay{
			HostPath: sf.HostPath, GuestPath: sf.GuestPath, ReadOnly: sf.ReadOnly,
		})
	}
	data, err := c.do(ctx, http.MethodPost, "/v1/vms", payload)
	if err != nil {
		return nil, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("decode FluxVM create: %w", err)
	}
	return &rec, nil
}

func (c *Client) Console(ctx context.Context, id string) (*ConsoleInfo, error) {
	data, err := c.do(ctx, http.MethodGet, "/v1/vms/"+url.PathEscape(id)+"/console", nil)
	if err != nil {
		return nil, err
	}
	var info ConsoleInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("decode FluxVM console: %w", err)
	}
	return &info, nil
}

func (c *Client) Snapshot(ctx context.Context, id, tag string) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/vms/"+url.PathEscape(id)+"/snapshot", map[string]string{"tag": tag})
	return err
}

func (c *Client) StartFromSnapshot(ctx context.Context, id, tag string) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/vms/"+url.PathEscape(id)+"/start-from-snapshot", map[string]string{"tag": tag})
	return err
}

type MigrationStartRequest struct {
	Destination     string  `json:"destination"`
	Mode            string  `json:"mode,omitempty"`
	BandwidthMbps   *uint64 `json:"bandwidth_mbps,omitempty"`
	MaxDowntimeMs   *uint64 `json:"max_downtime_ms,omitempty"`
	MultifdChannels *uint8  `json:"multifd_channels,omitempty"`
}

type MigrationStatus struct {
	Phase  string `json:"phase,omitempty"`
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (c *Client) StartMigration(ctx context.Context, id string, req MigrationStartRequest) (*MigrationStatus, error) {
	data, err := c.do(ctx, http.MethodPost, "/v1/vms/"+url.PathEscape(id)+"/migration/start", req)
	if err != nil {
		return nil, err
	}
	var st MigrationStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (c *Client) MigrationStatus(ctx context.Context, id string) (*MigrationStatus, error) {
	data, err := c.do(ctx, http.MethodGet, "/v1/vms/"+url.PathEscape(id)+"/migration/status", nil)
	if err != nil {
		return nil, err
	}
	var st MigrationStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (c *Client) CancelMigration(ctx context.Context, id string) (*MigrationStatus, error) {
	data, err := c.do(ctx, http.MethodPost, "/v1/vms/"+url.PathEscape(id)+"/migration/cancel", map[string]any{})
	if err != nil {
		return nil, err
	}
	var st MigrationStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (c *Client) Delete(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+"/v1/vms/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fluxvm DELETE /v1/vms/%s: HTTP %d: %s", id, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}
