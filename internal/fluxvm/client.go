// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

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
	TapName string `json:"tap_name,omitempty"`
	// Disk is the real qcow2/raw path FluxVM currently has open for this
	// VM -- distinct from the Machine's original spec.image.path, which
	// only names the base image a create() request provisioned from. A
	// real migration adapter needs this exact, live path (storage
	// contract v1: shared storage, no disk copy) to build a
	// MigrationReceiverRequest on the target FluxVM.
	Disk string `json:"disk,omitempty"`
	// Workspace is the per-VM directory FluxVM allocates on the VM host --
	// e.g. its VNC socket lives at Workspace+"/vnc.sock" (see
	// internal/consoleproxy). Already present on FluxVM's own VmRecord and
	// returned by GET /v1/vms/{id}; Kairon just wasn't capturing it before.
	Workspace string `json:"workspace,omitempty"`
	// Request mirrors just the fields of FluxVM's own CreateVmRequest a
	// real migration adapter needs to reconstruct a topology-matching
	// receiver spec -- not the full request shape, which Kairon has no
	// other use for.
	Request struct {
		VCPUs     uint32 `json:"vcpus,omitempty"`
		MemoryMiB uint64 `json:"memory_mib,omitempty"`
		Network   struct {
			MAC string `json:"mac,omitempty"`
		} `json:"network,omitempty"`
	} `json:"request,omitempty"`
}

func (r Record) ID() string {
	if r.IDValue != "" {
		return r.IDValue
	}
	return r.UUID
}

// CloudInitSpec mirrors FluxVM's own cloud-init NoCloud datasource fields
// (fluxvm-core/src/model.rs CloudInitSpec) that Kairon currently exposes.
// FluxVM also has a write_files field; not yet surfaced here.
type CloudInitSpec struct {
	Hostname          string   `json:"hostname,omitempty"`
	User              string   `json:"user,omitempty"`
	SSHAuthorizedKeys []string `json:"ssh_authorized_keys,omitempty"`
	Packages          []string `json:"packages,omitempty"`
	RunCmd            []string `json:"runcmd,omitempty"`
	StaticNetwork     bool     `json:"static_network,omitempty"`
}

type CreateRequest struct {
	Name        string         `json:"name"`
	Tenant      string         `json:"tenant,omitempty"`
	Backend     string         `json:"backend"`
	Image       string         `json:"image"`
	Kernel      string         `json:"kernel,omitempty"`
	VCPUs       uint32         `json:"vcpus"`
	MemoryMiB   uint64         `json:"memory_mib"`
	Network     map[string]any `json:"network,omitempty"`
	CloudInit   *CloudInitSpec `json:"cloud_init,omitempty"`
	PodUID      string         `json:"pod_uid,omitempty"`
	TTLSeconds  int64          `json:"ttl_seconds,omitempty"`
	VFIODevices []string       `json:"vfio_devices,omitempty"`
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
	defer func() { _ = resp.Body.Close() }()
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

func (c *Client) Create(ctx context.Context, m model.Machine, defaultBackend string) (*Record, error) {
	return c.CreateWithVFIO(ctx, m, defaultBackend, nil)
}

func (c *Client) CreateWithVFIO(ctx context.Context, m model.Machine, defaultBackend string, vfioDevices []string) (*Record, error) {
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
	tenant := m.Namespace()
	network := BuildNetworkMap(m.Spec.Network)
	payload := CreateRequest{
		Name:        m.RuntimeName(),
		Tenant:      tenant,
		Backend:     backend,
		Image:       m.Spec.Image.Path,
		Kernel:      m.Spec.Runtime.Kernel,
		VCPUs:       cpu,
		MemoryMiB:   mem,
		Network:     network,
		TTLSeconds:  m.Spec.TTLSeconds,
		VFIODevices: vfioDevices,
		PodUID:      m.Spec.Network.PodUID,
	}
	ci := m.Spec.CloudInit
	if m.Spec.Network.StaticNetwork || ci.Hostname != "" || ci.User != "" || len(ci.SSHAuthorizedKeys) > 0 || len(ci.Packages) > 0 || len(ci.RunCmd) > 0 {
		payload.CloudInit = &CloudInitSpec{
			Hostname:          ci.Hostname,
			User:              ci.User,
			SSHAuthorizedKeys: ci.SSHAuthorizedKeys,
			Packages:          ci.Packages,
			RunCmd:            ci.RunCmd,
			StaticNetwork:     m.Spec.Network.StaticNetwork,
		}
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
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fluxvm DELETE /v1/vms/%s: HTTP %d: %s", id, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}
