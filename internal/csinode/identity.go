// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package csinode

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// DriverVersion is reported via GetPluginInfo -- opaque to the CO
// (Container Orchestrator), bumped by hand when this driver's behavior
// changes in a way worth being able to tell apart in logs/support.
const DriverVersion = "0.1.0"

// IdentityServer implements the CSI Identity service -- required by every
// CSI plugin regardless of which optional services (Controller, Node) it
// also implements.
type IdentityServer struct {
	csi.UnimplementedIdentityServer
}

func (IdentityServer) GetPluginInfo(_ context.Context, _ *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{Name: DriverName, VendorVersion: DriverVersion}, nil
}

// GetPluginCapabilities deliberately reports no Controller service
// capability -- see NodeServer's own doc comment for why this is a
// Node-only, pre-provisioned-volumes-only driver in this first cut.
func (IdentityServer) GetPluginCapabilities(_ context.Context, _ *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	return &csi.GetPluginCapabilitiesResponse{}, nil
}

func (IdentityServer) Probe(_ context.Context, _ *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return &csi.ProbeResponse{Ready: wrapperspb.Bool(true)}, nil
}
