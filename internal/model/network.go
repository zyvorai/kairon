// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

import "time"

const (
	KindMachineNetworkPolicy = "MachineNetworkPolicy"
	KindNetworkSecurityGroup = "NetworkSecurityGroup"
	FinalizerNetworkPolicy   = "kairon.zyvor.dev/network-policy"
	FinalizerNetworkGroup    = "kairon.zyvor.dev/network-group"
)

// PortForward maps FluxVM user-mode NAT forwards (host↔guest).
type PortForward struct {
	HostPort  uint16 `json:"hostPort"`
	GuestPort uint16 `json:"guestPort"`
	Protocol  string `json:"protocol,omitempty"` // tcp|udp; default tcp
}

// NetworkSpec is Machine.create parity with Fabric / FluxVM NetworkSpec.
type NetworkSpec struct {
	Mode              string        `json:"mode,omitempty"` // user|tap|macvtap
	NetNS             bool          `json:"netns,omitempty"`
	Bridge            string        `json:"bridge,omitempty"`
	Parent            string        `json:"parent,omitempty"`
	MAC               string        `json:"mac,omitempty"`
	TapName           string        `json:"tapName,omitempty"`
	MacvtapMode       string        `json:"macvtapMode,omitempty"` // bridge|vepa|private|passthru
	Forwards          []PortForward `json:"forwards,omitempty"`
	StaticNetwork     bool          `json:"staticNetwork,omitempty"`
	PodUID            string        `json:"podUID,omitempty"`
	DataplaneRequired bool          `json:"dataplaneRequired,omitempty"`
}

// ServiceFabricSpec declares FluxVM Service Fabric VIP membership for a Machine.
type ServiceFabricSpec struct {
	Services []ServiceFabricMembership `json:"services,omitempty"`
}

// ServiceFabricMembership registers the Machine guest IP as a backend of a named service.
type ServiceFabricMembership struct {
	Name   string `json:"name"`
	Port   uint16 `json:"port"`
	Weight uint16 `json:"weight,omitempty"`
}

// MachineNetworkStatus is projected for Fabric's Dataplane tab.
type MachineNetworkStatus struct {
	GuestIP   string                  `json:"guestIP,omitempty"`
	GuestIPs  []string                `json:"guestIPs,omitempty"`
	TapName   string                  `json:"tapName,omitempty"`
	Dataplane *MachineDataplaneStatus `json:"dataplane,omitempty"`
}

// MachineDataplaneStatus mirrors FluxVM GET …/network/status fields Fabric expects.
type MachineDataplaneStatus struct {
	Attached          bool    `json:"attached"`
	Mode              string  `json:"mode,omitempty"`
	SchemaVersion     *uint32 `json:"schemaVersion,omitempty"`
	Identity          uint32  `json:"identity,omitempty"`
	PolicyFingerprint string  `json:"policyFingerprint,omitempty"`
	PolicySynced      bool    `json:"policySynced,omitempty"`
	Interface         string  `json:"interface,omitempty"`
}

// VmNetworkPolicy mirrors FluxVM VmNetworkPolicy (snake_case on the wire via fluxvm client).
type VmNetworkPolicy struct {
	DefaultAllow  bool     `json:"defaultAllow"`
	AllowCidrs    []string `json:"allowCidrs,omitempty"`
	DenyCidrs     []string `json:"denyCidrs,omitempty"`
	AllowPorts    []string `json:"allowPorts,omitempty"`
	MaxEgressMbps *uint32  `json:"maxEgressMbps,omitempty"`
	MaxEgressPps  *uint32  `json:"maxEgressPps,omitempty"`
	AllowFqdns    []string `json:"allowFqdns,omitempty"`
	Groups        []string `json:"groups,omitempty"`
	Labels        []string `json:"labels,omitempty"`
	Entities      []string `json:"entities,omitempty"`
	AuditMode     bool     `json:"auditMode,omitempty"`
	AllowIcmp     bool     `json:"allowIcmp,omitempty"`
	SampleRate    uint32   `json:"sampleRate,omitempty"`
}

// MachineNetworkPolicy selects Machines and applies FluxVM VM-edge policy.
type MachineNetworkPolicy struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta                 `json:"metadata"`
	Spec     MachineNetworkPolicySpec   `json:"spec"`
	Status   MachineNetworkPolicyStatus `json:"status,omitempty"`
}

type MachineNetworkPolicyList struct {
	TypeMeta `json:",inline"`
	Items    []MachineNetworkPolicy `json:"items"`
}

type MachineNetworkPolicySpec struct {
	// MachineName targets a single Machine in the same namespace (takes precedence over selector).
	MachineName string `json:"machineName,omitempty"`
	// Selector matches Machine labels when MachineName is empty.
	Selector map[string]string `json:"selector,omitempty"`
	Policy   VmNetworkPolicy   `json:"policy"`
	// CNP is an optional FluxVM CiliumNetworkPolicy-shaped document posted to /v1/network/cnp.
	CNP map[string]any `json:"cnp,omitempty"`
}

type MachineNetworkPolicyStatus struct {
	Phase string `json:"phase,omitempty"`
	// No omitempty -- this whole status is patched as one object; under
	// JSON merge-patch semantics an absent key never clears a
	// previously-set value (see model.MachineStatus.Message's comment).
	Message          string     `json:"message"`
	ObservedMachines int        `json:"observedMachines,omitempty"`
	LastAppliedTime  *time.Time `json:"lastAppliedTime,omitempty"`
	EffectiveSynced  bool       `json:"effectiveSynced,omitempty"`
}

func (p MachineNetworkPolicy) Namespace() string {
	if p.Metadata.Namespace == "" {
		return DefaultNamespace
	}
	return p.Metadata.Namespace
}

// NetworkSecurityGroup maps to FluxVM POST /v1/network/groups.
type NetworkSecurityGroup struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta                 `json:"metadata"`
	Spec     NetworkSecurityGroupSpec   `json:"spec"`
	Status   NetworkSecurityGroupStatus `json:"status,omitempty"`
}

type NetworkSecurityGroupList struct {
	TypeMeta `json:",inline"`
	Items    []NetworkSecurityGroup `json:"items"`
}

type NetworkSecurityGroupSpec struct {
	// GroupName is the FluxVM group name; defaults to metadata.name.
	GroupName   string          `json:"groupName,omitempty"`
	Labels      []string        `json:"labels,omitempty"`
	Priority    uint32          `json:"priority,omitempty"`
	Description string          `json:"description,omitempty"`
	Policy      VmNetworkPolicy `json:"policy,omitempty"`
}

type NetworkSecurityGroupStatus struct {
	Phase string `json:"phase,omitempty"`
	// No omitempty -- see MachineStatus.Message's comment.
	Message   string `json:"message"`
	Identity  uint32 `json:"identity,omitempty"`
	AppliedOn string `json:"appliedOn,omitempty"` // node that last upserted
}

func (g NetworkSecurityGroup) Namespace() string {
	if g.Metadata.Namespace == "" {
		return DefaultNamespace
	}
	return g.Metadata.Namespace
}

func (g NetworkSecurityGroup) FluxGroupName() string {
	if g.Spec.GroupName != "" {
		return g.Spec.GroupName
	}
	return g.Metadata.Name
}

// LabelsMatch returns true when every selector key/value is present on labels.
func LabelsMatch(labels, selector map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for k, v := range selector {
		if labels == nil || labels[k] != v {
			return false
		}
	}
	return true
}
