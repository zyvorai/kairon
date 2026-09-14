// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package csinode

import (
	"context"
	"errors"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
)

// newTestNodeServer builds a NodeServer with every OS-touching seam
// stubbed out -- see NodeServer's own doc comment for why. mounted tracks
// which paths the fake considers mounted, so a test can assert
// NodeStageVolume/NodePublishVolume actually called mountFn (by checking
// the path was added) without ever touching a real filesystem or mount
// syscall.
type testNodeServerFixture struct {
	server  *NodeServer
	run     *fakeCommandRunner
	mounted map[string]bool
}

func newTestNodeServer(t *testing.T) *testNodeServerFixture {
	t.Helper()
	run := newFakeCommandRunner()
	f := &testNodeServerFixture{run: run, mounted: map[string]bool{}}
	f.server = &NodeServer{
		NodeID: "test-node",
		Runner: run,
		iscsi:  &iscsiClient{run: run},
		isMountPoint: func(path string) (bool, error) {
			return f.mounted[path], nil
		},
		mountFn: func(source, target, fsType string, flags uintptr, data string) error {
			f.mounted[target] = true
			return nil
		},
		unmountFn: func(target string) error {
			delete(f.mounted, target)
			return nil
		},
	}
	return f
}

func mountVolumeCapability() *csi.VolumeCapability {
	return &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
	}
}

func TestNodeGetInfoReportsConfiguredNodeID(t *testing.T) {
	f := newTestNodeServer(t)
	f.server.NodeID = "worker-7"
	resp, err := f.server.NodeGetInfo(context.Background(), &csi.NodeGetInfoRequest{})
	if err != nil {
		t.Fatalf("NodeGetInfo: %v", err)
	}
	if resp.GetNodeId() != "worker-7" {
		t.Fatalf("expected node_id worker-7, got %q", resp.GetNodeId())
	}
}

func TestNodeGetCapabilitiesReportsStageUnstage(t *testing.T) {
	f := newTestNodeServer(t)
	resp, err := f.server.NodeGetCapabilities(context.Background(), &csi.NodeGetCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("NodeGetCapabilities: %v", err)
	}
	if len(resp.GetCapabilities()) != 1 || resp.GetCapabilities()[0].GetRpc().GetType() != csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME {
		t.Fatalf("expected exactly STAGE_UNSTAGE_VOLUME, got %+v", resp.GetCapabilities())
	}
}

func setupLoginFixture(t *testing.T, f *testNodeServerFixture) {
	t.Helper()
	dir := t.TempDir()
	realDevice := dir + "/sda"
	if err := writeFile(realDevice, "x"); err != nil {
		t.Fatalf("write fake device: %v", err)
	}
	symlink := dir + "/by-path-link"
	if err := symlinkFile(realDevice, symlink); err != nil {
		t.Fatalf("symlink fake device: %v", err)
	}
	t.Cleanup(setDevicePathForTest(func(iscsiConfig) string { return symlink }))
	f.run.on("blkid", func(args ...string) (string, error) { return "ext4", nil }) // already formatted
}

func TestNodeStageVolumeRequiresFields(t *testing.T) {
	f := newTestNodeServer(t)
	cases := []*csi.NodeStageVolumeRequest{
		{StagingTargetPath: "/x", VolumeCapability: mountVolumeCapability()},
		{VolumeId: "v1", VolumeCapability: mountVolumeCapability()},
		{VolumeId: "v1", StagingTargetPath: "/x"}, // no VolumeCapability
	}
	for i, req := range cases {
		if _, err := f.server.NodeStageVolume(context.Background(), req); err == nil {
			t.Fatalf("case %d: expected an error for an incomplete request", i)
		}
	}
}

func TestNodeStageVolumeRejectsBlockAccessType(t *testing.T) {
	f := newTestNodeServer(t)
	req := &csi.NodeStageVolumeRequest{
		VolumeId:          "v1",
		StagingTargetPath: t.TempDir(),
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
		},
		VolumeContext: map[string]string{volumeAttrPortal: "10.0.0.1:3260", volumeAttrIQN: "iqn.test:disk"},
	}
	if _, err := f.server.NodeStageVolume(context.Background(), req); err == nil {
		t.Fatal("expected raw block volumes to be rejected in this first cut")
	}
}

func TestNodeStageVolumeLogsInFormatsAndMounts(t *testing.T) {
	f := newTestNodeServer(t)
	setupLoginFixture(t, f)
	staging := t.TempDir() + "/stage"

	req := &csi.NodeStageVolumeRequest{
		VolumeId:          encodeVolumeID(iscsiConfig{Portal: "10.0.0.1:3260", IQN: "iqn.test:disk", LUN: "0"}),
		StagingTargetPath: staging,
		VolumeCapability:  mountVolumeCapability(),
		VolumeContext:     map[string]string{volumeAttrPortal: "10.0.0.1:3260", volumeAttrIQN: "iqn.test:disk"},
	}
	if _, err := f.server.NodeStageVolume(context.Background(), req); err != nil {
		t.Fatalf("NodeStageVolume: %v", err)
	}
	if !f.mounted[staging] {
		t.Fatal("expected the staging path to be mounted")
	}
	if len(f.run.callsFor("iscsiadm")) == 0 {
		t.Fatal("expected at least one iscsiadm call")
	}
}

func TestNodeStageVolumeIsIdempotent(t *testing.T) {
	f := newTestNodeServer(t)
	staging := t.TempDir() + "/stage"
	f.mounted[staging] = true // already staged, as if a previous call succeeded

	req := &csi.NodeStageVolumeRequest{
		VolumeId:          "v1",
		StagingTargetPath: staging,
		VolumeCapability:  mountVolumeCapability(),
		VolumeContext:     map[string]string{volumeAttrPortal: "10.0.0.1:3260", volumeAttrIQN: "iqn.test:disk"},
	}
	if _, err := f.server.NodeStageVolume(context.Background(), req); err != nil {
		t.Fatalf("NodeStageVolume: %v", err)
	}
	if len(f.run.calls) != 0 {
		t.Fatalf("expected no commands to run against an already-staged volume, got %v", f.run.calls)
	}
}

func TestNodeStageVolumePropagatesLoginFailure(t *testing.T) {
	f := newTestNodeServer(t)
	f.run.on("iscsiadm", func(args ...string) (string, error) { return "", errors.New("boom") })
	t.Cleanup(setDeviceWaitTimeoutForTest())

	req := &csi.NodeStageVolumeRequest{
		VolumeId:          "v1",
		StagingTargetPath: t.TempDir() + "/stage",
		VolumeCapability:  mountVolumeCapability(),
		VolumeContext:     map[string]string{volumeAttrPortal: "10.0.0.1:3260", volumeAttrIQN: "iqn.test:disk"},
	}
	if _, err := f.server.NodeStageVolume(context.Background(), req); err == nil {
		t.Fatal("expected an iscsi login failure to be propagated")
	}
}

func TestNodeUnstageVolumeUnmountsAndLogsOut(t *testing.T) {
	f := newTestNodeServer(t)
	staging := t.TempDir() + "/stage"
	f.mounted[staging] = true

	req := &csi.NodeUnstageVolumeRequest{
		VolumeId:          encodeVolumeID(iscsiConfig{Portal: "10.0.0.1:3260", IQN: "iqn.test:disk", LUN: "0"}),
		StagingTargetPath: staging,
	}
	if _, err := f.server.NodeUnstageVolume(context.Background(), req); err != nil {
		t.Fatalf("NodeUnstageVolume: %v", err)
	}
	if f.mounted[staging] {
		t.Fatal("expected the staging path to be unmounted")
	}
	if len(f.run.callsFor("iscsiadm")) == 0 {
		t.Fatal("expected a logout iscsiadm call")
	}
}

func TestNodeUnstageVolumeIsIdempotentWhenNotMounted(t *testing.T) {
	f := newTestNodeServer(t)
	req := &csi.NodeUnstageVolumeRequest{
		VolumeId:          encodeVolumeID(iscsiConfig{Portal: "10.0.0.1:3260", IQN: "iqn.test:disk", LUN: "0"}),
		StagingTargetPath: t.TempDir() + "/never-mounted",
	}
	if _, err := f.server.NodeUnstageVolume(context.Background(), req); err != nil {
		t.Fatalf("expected NodeUnstageVolume to succeed against an already-unstaged path, got: %v", err)
	}
}

func TestNodeUnstageVolumeRejectsUnrecognizedVolumeID(t *testing.T) {
	f := newTestNodeServer(t)
	req := &csi.NodeUnstageVolumeRequest{VolumeId: "not-a-kairon-handle", StagingTargetPath: t.TempDir()}
	if _, err := f.server.NodeUnstageVolume(context.Background(), req); err == nil {
		t.Fatal("expected an error for a volume_id this driver didn't mint")
	}
}

func TestNodePublishVolumeBindMounts(t *testing.T) {
	f := newTestNodeServer(t)
	staging := t.TempDir() + "/stage"
	target := t.TempDir() + "/target"

	req := &csi.NodePublishVolumeRequest{
		VolumeId:          "v1",
		StagingTargetPath: staging,
		TargetPath:        target,
		VolumeCapability:  mountVolumeCapability(),
	}
	if _, err := f.server.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatalf("NodePublishVolume: %v", err)
	}
	if !f.mounted[target] {
		t.Fatal("expected the target path to be bind-mounted")
	}
}

func TestNodePublishVolumeIsIdempotent(t *testing.T) {
	f := newTestNodeServer(t)
	target := t.TempDir() + "/target"
	f.mounted[target] = true

	req := &csi.NodePublishVolumeRequest{
		VolumeId: "v1", StagingTargetPath: t.TempDir() + "/stage", TargetPath: target, VolumeCapability: mountVolumeCapability(),
	}
	if _, err := f.server.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatalf("NodePublishVolume: %v", err)
	}
}

func TestNodePublishVolumeRequiresStagingPath(t *testing.T) {
	f := newTestNodeServer(t)
	req := &csi.NodePublishVolumeRequest{VolumeId: "v1", TargetPath: t.TempDir(), VolumeCapability: mountVolumeCapability()}
	if _, err := f.server.NodePublishVolume(context.Background(), req); err == nil {
		t.Fatal("expected an error when staging_target_path is missing")
	}
}

func TestNodeUnpublishVolumeUnmounts(t *testing.T) {
	f := newTestNodeServer(t)
	target := t.TempDir() + "/target"
	f.mounted[target] = true

	req := &csi.NodeUnpublishVolumeRequest{VolumeId: "v1", TargetPath: target}
	if _, err := f.server.NodeUnpublishVolume(context.Background(), req); err != nil {
		t.Fatalf("NodeUnpublishVolume: %v", err)
	}
	if f.mounted[target] {
		t.Fatal("expected the target path to be unmounted")
	}
}

func TestNodeUnpublishVolumeIsIdempotentWhenNotMounted(t *testing.T) {
	f := newTestNodeServer(t)
	req := &csi.NodeUnpublishVolumeRequest{VolumeId: "v1", TargetPath: t.TempDir() + "/never-mounted"}
	if _, err := f.server.NodeUnpublishVolume(context.Background(), req); err != nil {
		t.Fatalf("expected NodeUnpublishVolume to succeed against an already-unpublished path, got: %v", err)
	}
}
