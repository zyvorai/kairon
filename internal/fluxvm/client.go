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
}

func (r Record) ID() string {
	if r.IDValue != "" {
		return r.IDValue
	}
	return r.UUID
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
	payload := CreateRequest{Name: m.RuntimeName(), Tenant: tenant, Backend: backend, Image: m.Spec.Image.Path, Kernel: m.Spec.Runtime.Kernel, VCPUs: cpu, MemoryMiB: mem, Network: network, TTLSeconds: m.Spec.TTLSeconds, VFIODevices: vfioDevices}
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
