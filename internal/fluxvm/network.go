// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package fluxvm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/zyvorai/kairon/internal/model"
)

// WireVmNetworkPolicy is the snake_case JSON FluxVM expects for VmNetworkPolicy.
type WireVmNetworkPolicy struct {
	DefaultAllow  bool     `json:"default_allow"`
	AllowCidrs    []string `json:"allow_cidrs,omitempty"`
	DenyCidrs     []string `json:"deny_cidrs,omitempty"`
	AllowPorts    []string `json:"allow_ports,omitempty"`
	MaxEgressMbps *uint32  `json:"max_egress_mbps,omitempty"`
	MaxEgressPps  *uint32  `json:"max_egress_pps,omitempty"`
	AllowFqdns    []string `json:"allow_fqdns,omitempty"`
	Groups        []string `json:"groups,omitempty"`
	Labels        []string `json:"labels,omitempty"`
	Entities      []string `json:"entities,omitempty"`
	AuditMode     bool     `json:"audit_mode,omitempty"`
	AllowIcmp     bool     `json:"allow_icmp,omitempty"`
	SampleRate    uint32   `json:"sample_rate,omitempty"`
}

func ToWirePolicy(p model.VmNetworkPolicy) WireVmNetworkPolicy {
	return WireVmNetworkPolicy{
		DefaultAllow:  p.DefaultAllow,
		AllowCidrs:    p.AllowCidrs,
		DenyCidrs:     p.DenyCidrs,
		AllowPorts:    p.AllowPorts,
		MaxEgressMbps: p.MaxEgressMbps,
		MaxEgressPps:  p.MaxEgressPps,
		AllowFqdns:    p.AllowFqdns,
		Groups:        p.Groups,
		Labels:        p.Labels,
		Entities:      p.Entities,
		AuditMode:     p.AuditMode,
		AllowIcmp:     p.AllowIcmp,
		SampleRate:    p.SampleRate,
	}
}

// DataplaneStatus is FluxVM GET /v1/vms/{id}/network/status.
type DataplaneStatus struct {
	Mode              string              `json:"mode"`
	Required          bool                `json:"required"`
	Attached          bool                `json:"attached"`
	Interface         string              `json:"interface,omitempty"`
	Identity          uint32              `json:"identity"`
	PinDir            string              `json:"pin_dir,omitempty"`
	SchemaVersion     *uint32             `json:"schema_version,omitempty"`
	SchemaCompatible  bool                `json:"schema_compatible"`
	PolicySynced      bool                `json:"policy_synced"`
	PolicyFingerprint *uint64             `json:"policy_fingerprint,omitempty"`
	Policy            WireVmNetworkPolicy `json:"policy,omitempty"`
}

// SecurityGroup is FluxVM /v1/network/groups body.
type SecurityGroup struct {
	Name        string              `json:"name"`
	Labels      []string            `json:"labels,omitempty"`
	Policy      WireVmNetworkPolicy `json:"policy"`
	Identity    uint32              `json:"identity,omitempty"`
	Priority    uint32              `json:"priority,omitempty"`
	Description string              `json:"description,omitempty"`
}

// ServiceBackend is one VIP backend entry.
type ServiceBackend struct {
	Address string `json:"address"`
	Port    uint16 `json:"port"`
	Weight  uint16 `json:"weight,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
	State   string `json:"state,omitempty"`
}

// ServiceSpec is FluxVM /v1/network/services body (partial; membership updates merge backends).
type ServiceSpec struct {
	Name     string           `json:"name"`
	VIP      string           `json:"vip,omitempty"`
	Port     uint16           `json:"port,omitempty"`
	Protocol string           `json:"protocol,omitempty"`
	Backends []ServiceBackend `json:"backends,omitempty"`
}

func (c *Client) NetworkStatus(ctx context.Context, id string) (*DataplaneStatus, error) {
	data, err := c.do(ctx, http.MethodGet, "/v1/vms/"+url.PathEscape(id)+"/network/status", nil)
	if err != nil {
		return nil, err
	}
	var st DataplaneStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("decode network status: %w", err)
	}
	return &st, nil
}

func (c *Client) SetVMNetworkPolicy(ctx context.Context, id string, policy model.VmNetworkPolicy) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/vms/"+url.PathEscape(id)+"/network/policy", ToWirePolicy(policy))
	return err
}

func (c *Client) GetVMNetworkEffective(ctx context.Context, id string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/v1/vms/"+url.PathEscape(id)+"/network/effective", nil)
}

func (c *Client) UpsertNetworkGroup(ctx context.Context, group SecurityGroup) (*SecurityGroup, error) {
	data, err := c.do(ctx, http.MethodPost, "/v1/network/groups", group)
	if err != nil {
		return nil, err
	}
	var out SecurityGroup
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode security group: %w", err)
	}
	return &out, nil
}

func (c *Client) DeleteNetworkGroup(ctx context.Context, name string) error {
	_, err := c.do(ctx, http.MethodDelete, "/v1/network/groups/"+url.PathEscape(name), nil)
	return err
}

func (c *Client) ApplyCNP(ctx context.Context, doc map[string]any) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/network/cnp", doc)
	return err
}

func (c *Client) GetNetworkService(ctx context.Context, name string) (*ServiceSpec, error) {
	data, err := c.do(ctx, http.MethodGet, "/v1/network/services/"+url.PathEscape(name), nil)
	if err != nil {
		return nil, err
	}
	var out ServiceSpec
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode network service: %w", err)
	}
	return &out, nil
}

func (c *Client) UpsertNetworkService(ctx context.Context, spec ServiceSpec) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/network/services", spec)
	return err
}

func (c *Client) NetworkMigrationQuiesce(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/vms/"+url.PathEscape(id)+"/network/migration/quiesce", map[string]any{})
	return err
}

func (c *Client) NetworkMigrationExport(ctx context.Context, id string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/v1/vms/"+url.PathEscape(id)+"/network/migration/export", nil)
}

func (c *Client) NetworkMigrationRestore(ctx context.Context, id string, snapshot json.RawMessage) error {
	var body any
	if len(snapshot) == 0 {
		body = map[string]any{}
	} else if err := json.Unmarshal(snapshot, &body); err != nil {
		body = json.RawMessage(snapshot)
	}
	_, err := c.do(ctx, http.MethodPost, "/v1/vms/"+url.PathEscape(id)+"/network/migration/restore", body)
	return err
}

func (c *Client) NetworkMigrationResume(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/vms/"+url.PathEscape(id)+"/network/migration/resume", map[string]any{})
	return err
}

func PolicyFingerprintString(fp *uint64) string {
	if fp == nil {
		return ""
	}
	return strconv.FormatUint(*fp, 10)
}

// BuildNetworkMap builds the FluxVM create `network` object (tagged enum JSON).
func BuildNetworkMap(n model.NetworkSpec) map[string]any {
	mode := n.Mode
	if mode == "" {
		mode = "user"
	}
	network := map[string]any{"mode": mode}
	switch mode {
	case "user":
		if len(n.Forwards) > 0 {
			forwards := make([]map[string]any, 0, len(n.Forwards))
			for _, f := range n.Forwards {
				proto := f.Protocol
				if proto == "" {
					proto = "tcp"
				}
				forwards = append(forwards, map[string]any{
					"host_port":  f.HostPort,
					"guest_port": f.GuestPort,
					"protocol":   proto,
				})
			}
			network["forwards"] = forwards
		}
	case "tap":
		if n.TapName != "" {
			network["tap_name"] = n.TapName
		}
		if n.Bridge != "" {
			network["bridge"] = n.Bridge
		}
		if n.MAC != "" {
			network["mac"] = n.MAC
		}
		if n.NetNS {
			network["netns"] = true
		}
	case "macvtap":
		if n.Parent != "" {
			network["parent"] = n.Parent
		}
		if n.MacvtapMode != "" {
			network["macvtap_mode"] = n.MacvtapMode
		}
		if n.MAC != "" {
			network["mac"] = n.MAC
		}
	default:
		if n.NetNS {
			network["netns"] = true
		}
		if n.Bridge != "" {
			network["bridge"] = n.Bridge
		}
		if n.Parent != "" {
			network["parent"] = n.Parent
		}
		if n.MAC != "" {
			network["mac"] = n.MAC
		}
		if n.TapName != "" {
			network["tap_name"] = n.TapName
		}
	}
	return network
}
