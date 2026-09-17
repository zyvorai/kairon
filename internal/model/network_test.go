// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

import "testing"

func TestValidateVmNetworkPolicyAllowsEmptyPolicy(t *testing.T) {
	if err := ValidateVmNetworkPolicy(VmNetworkPolicy{}); err != nil {
		t.Fatalf("expected an empty policy to be valid, got %v", err)
	}
}

func TestValidateVmNetworkPolicyAllowsWellFormedFields(t *testing.T) {
	mbps := uint32(250)
	pps := uint32(100000)
	p := VmNetworkPolicy{
		DefaultAllow:  false,
		AllowCidrs:    []string{"10.0.0.0/8", "2001:db8:20::/48"},
		DenyCidrs:     []string{"192.168.1.0/24"},
		AllowPorts:    []string{"tcp/443", "UDP/53", "icmp/0", " tcp / 22 "},
		MaxEgressMbps: &mbps,
		MaxEgressPps:  &pps,
	}
	if err := ValidateVmNetworkPolicy(p); err != nil {
		t.Fatalf("expected a well-formed policy to be valid, got %v", err)
	}
}

func TestValidateVmNetworkPolicyDeniesCIDRMissingPrefix(t *testing.T) {
	err := ValidateVmNetworkPolicy(VmNetworkPolicy{AllowCidrs: []string{"10.0.0.0"}})
	if err == nil {
		t.Fatal("expected a CIDR missing /prefix to be rejected")
	}
}

func TestValidateVmNetworkPolicyDeniesCIDROutOfRangePrefix(t *testing.T) {
	err := ValidateVmNetworkPolicy(VmNetworkPolicy{DenyCidrs: []string{"10.0.0.0/33"}})
	if err == nil {
		t.Fatal("expected an IPv4 /33 to be rejected")
	}
}

func TestValidateVmNetworkPolicyDeniesGarbageCIDR(t *testing.T) {
	err := ValidateVmNetworkPolicy(VmNetworkPolicy{AllowCidrs: []string{"not-an-ip/8"}})
	if err == nil {
		t.Fatal("expected a garbage address to be rejected")
	}
}

func TestValidateVmNetworkPolicyDeniesPortRuleMissingProto(t *testing.T) {
	err := ValidateVmNetworkPolicy(VmNetworkPolicy{AllowPorts: []string{"443"}})
	if err == nil {
		t.Fatal("expected a port rule with no proto/ prefix to be rejected")
	}
}

func TestValidateVmNetworkPolicyDeniesPortRuleUnsupportedProtocol(t *testing.T) {
	err := ValidateVmNetworkPolicy(VmNetworkPolicy{AllowPorts: []string{"http/443"}})
	if err == nil {
		t.Fatal("expected an unsupported protocol to be rejected")
	}
}

func TestValidateVmNetworkPolicyDeniesPortRuleZeroPortForTCP(t *testing.T) {
	err := ValidateVmNetworkPolicy(VmNetworkPolicy{AllowPorts: []string{"tcp/0"}})
	if err == nil {
		t.Fatal("expected tcp/0 to be rejected (port 0 is only valid for icmp/icmp6)")
	}
}

func TestValidateVmNetworkPolicyDeniesPortRuleOutOfRangePort(t *testing.T) {
	err := ValidateVmNetworkPolicy(VmNetworkPolicy{AllowPorts: []string{"tcp/70000"}})
	if err == nil {
		t.Fatal("expected a port above 65535 to be rejected")
	}
}

func TestValidateVmNetworkPolicyDeniesZeroMaxEgressMbps(t *testing.T) {
	zero := uint32(0)
	err := ValidateVmNetworkPolicy(VmNetworkPolicy{MaxEgressMbps: &zero})
	if err == nil {
		t.Fatal("expected an explicit maxEgressMbps: 0 to be rejected")
	}
}

func TestValidateVmNetworkPolicyDeniesZeroMaxEgressPps(t *testing.T) {
	zero := uint32(0)
	err := ValidateVmNetworkPolicy(VmNetworkPolicy{MaxEgressPps: &zero})
	if err == nil {
		t.Fatal("expected an explicit maxEgressPps: 0 to be rejected")
	}
}

func TestValidateVmNetworkPolicyAllowsIcmp6ZeroPort(t *testing.T) {
	if err := ValidateVmNetworkPolicy(VmNetworkPolicy{AllowPorts: []string{"icmp6/0", "icmpv6/0"}}); err != nil {
		t.Fatalf("expected icmp6/icmpv6 with port 0 to be valid, got %v", err)
	}
}
