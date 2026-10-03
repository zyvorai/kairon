// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package ebpfedge

import (
	"encoding/binary"
	"testing"
	"time"
)

func TestStableIdentitySurvivesIPChange(t *testing.T) {
	a := StableIdentity("demo", "web")
	b := StableIdentity("demo", "web")
	if a != b {
		t.Fatalf("identity not stable: %d vs %d", a, b)
	}
	if a < IdentityReserved {
		t.Fatalf("identity %d fell in the reserved range", a)
	}
	if StableIdentity("demo", "db") == a {
		t.Fatal("different machines collided")
	}
	spec, err := Compile(EdgeSpec{Namespace: "demo", Machine: "web", AssignedIP: "10.0.0.8"})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := Compile(EdgeSpec{Namespace: "demo", Machine: "web", AssignedIP: "10.0.0.9"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Identity != moved.Identity {
		t.Fatalf("identity changed across IP move: %d -> %d", spec.Identity, moved.Identity)
	}
}

func TestAntiSpoofAndAttributedDrop(t *testing.T) {
	spec, err := Compile(EdgeSpec{
		Namespace:    "demo",
		Machine:      "web",
		AntiSpoof:    true,
		AssignedMAC:  "52:54:00:aa:bb:cc",
		AssignedIP:   "10.0.0.8",
		PolicyName:   "web-tier",
		DefaultAllow: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ev, dropped := Attribute(spec, PacketMeta{SrcMAC: "52:54:00:11:22:33", SrcIP: "10.0.0.8", DstIP: "1.1.1.1"})
	if !dropped || ev.Reason != ReasonSpoofMAC || ev.PolicyName != "web-tier" {
		t.Fatalf("spoof not attributed: %+v dropped=%v", ev, dropped)
	}
	ev, dropped = Attribute(spec, PacketMeta{SrcMAC: "52:54:00:aa:bb:cc", SrcIP: "10.9.9.9", DstIP: "1.1.1.1"})
	if !dropped || ev.Reason != ReasonSpoofIP {
		t.Fatalf("ip spoof not attributed: %+v", ev)
	}
	if _, dropped := Attribute(spec, PacketMeta{SrcMAC: "52-54-00-AA-BB-CC", SrcIP: "10.0.0.8", DstIP: "1.1.1.1"}); dropped {
		t.Fatal("normalized assigned mac was dropped")
	}
}

func TestSNIAndDNSDeny(t *testing.T) {
	spec, err := Compile(EdgeSpec{
		Namespace:    "demo",
		Machine:      "web",
		DefaultAllow: true,
		AllowSNI:     []string{"*.vendor.com", "api.internal"},
		AllowDNS:     []string{"api.vendor.com"},
		PolicyName:   "egress-443",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d := Decide(spec, PacketMeta{SNI: "edge.vendor.com"}); d.Reason != ReasonAllow {
		t.Fatalf("suffix SNI should allow, got %s", d.Reason)
	}
	ev, dropped := Attribute(spec, PacketMeta{SNI: "evil.example", DstIP: "9.9.9.9"})
	if !dropped || ev.Reason != ReasonSNIDeny || ev.PolicyName != "egress-443" {
		t.Fatalf("sni deny: %+v", ev)
	}
	ev, dropped = Attribute(spec, PacketMeta{DNSQName: "malware.test."})
	if !dropped || ev.Reason != ReasonDNSDeny {
		t.Fatalf("dns deny: %+v", ev)
	}
	if _, err := Compile(EdgeSpec{Namespace: "demo", Machine: "web", AllowSNI: []string{"http://nope"}}); err == nil {
		t.Fatal("expected invalid SNI")
	}
}

func TestConntrackRestoreIdentityAndBlackhole(t *testing.T) {
	id := StableIdentity("demo", "web")
	now := time.Date(2026, 10, 3, 17, 0, 0, 0, time.UTC)
	snap, err := ExportConntrack(id, 7, []ConntrackEntry{{
		Proto: "tcp", SrcIP: "10.0.0.8", DstIP: "10.1.0.4", SrcPort: 40000, DstPort: 443, State: "established",
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := MarshalSnapshot(snap)
	if err != nil {
		t.Fatal(err)
	}
	back, err := UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	res, err := RestoreConntrack(id, back, now.Add(40*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if res.Restored != 1 || res.BlackholeWindowMs != 40 || res.Identity != id {
		t.Fatalf("restore result: %+v", res)
	}
	if _, err := RestoreConntrack(id+1, back, now); err == nil {
		t.Fatal("identity mismatch must fail closed")
	}
}

func TestLearnIPFromARPDHCPAndND(t *testing.T) {
	arp := make([]byte, 42)
	binary.BigEndian.PutUint16(arp[12:14], 0x0806)
	binary.BigEndian.PutUint16(arp[14+6:14+8], 2)
	copy(arp[14+14:14+18], []byte{10, 0, 0, 8})
	ip, src, ok := LearnIP(arp)
	if !ok || ip != "10.0.0.8" || src != IPSourceARP {
		t.Fatalf("arp learn: %s %s %v", ip, src, ok)
	}

	dhcp := make([]byte, 14+20+8+240)
	binary.BigEndian.PutUint16(dhcp[12:14], 0x0800)
	dhcp[14] = 0x45
	dhcp[14+9] = 17
	binary.BigEndian.PutUint16(dhcp[14+20+2:14+20+4], 68)
	binary.BigEndian.PutUint32(dhcp[14+20+8+236:14+20+8+240], 0x63825363)
	copy(dhcp[14+20+8+16:14+20+8+20], []byte{10, 0, 0, 15})
	ip, src, ok = LearnIP(dhcp)
	if !ok || ip != "10.0.0.15" || src != IPSourceDHCP {
		t.Fatalf("dhcp learn: %s %s %v", ip, src, ok)
	}
}

func TestCaptureBound(t *testing.T) {
	now := time.Now().UTC()
	s, err := NewCapture("demo", "web", "tcp port 443", 15, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Token) != 32 || s.ExpiresAt.Sub(now) != 15*time.Second {
		t.Fatalf("session: %+v", s)
	}
	if _, err := NewCapture("demo", "web", "", 31, now); err == nil {
		t.Fatal("capture over 30s must be rejected")
	}
}

func TestDefaultDenyAndCIDR(t *testing.T) {
	spec, err := Compile(EdgeSpec{
		Namespace:  "demo",
		Machine:    "web",
		AllowCIDRs: []string{"10.0.0.0/8"},
		PolicyName: "east-west",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d := Decide(spec, PacketMeta{DstIP: "10.1.2.3"}); d.Action != "allow" {
		t.Fatalf("allow cidr dropped: %+v", d)
	}
	ev, dropped := Attribute(spec, PacketMeta{DstIP: "8.8.8.8", Proto: "tcp", DstPort: 443})
	if !dropped || ev.Reason != ReasonPolicyDeny || ev.PolicyName != "east-west" {
		t.Fatalf("cidr deny: %+v", ev)
	}
}
