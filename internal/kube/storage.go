package kube

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/zyvorai/kairon/internal/model"
)

func (c *Client) ListMachineImages(ctx context.Context) ([]model.MachineImage, error) {
	var list model.MachineImageList
	err := c.request(ctx, http.MethodGet, "/apis/kairon.zyvor.dev/v1alpha1/machineimages", nil, &list, "")
	return list.Items, err
}

func (c *Client) GetMachineImage(ctx context.Context, ns, name string) (model.MachineImage, error) {
	var out model.MachineImage
	path := fmt.Sprintf("/apis/kairon.zyvor.dev/v1alpha1/namespaces/%s/machineimages/%s", url.PathEscape(ns), url.PathEscape(name))
	err := c.request(ctx, http.MethodGet, path, nil, &out, "")
	return out, err
}

func (c *Client) PatchMachineImageStatus(ctx context.Context, ns, name string, status model.MachineImageStatus) error {
	path := fmt.Sprintf("/apis/kairon.zyvor.dev/v1alpha1/namespaces/%s/machineimages/%s/status", url.PathEscape(ns), url.PathEscape(name))
	return c.request(ctx, http.MethodPatch, path, map[string]any{"status": status}, nil, "application/merge-patch+json")
}

func (c *Client) ListVirtualDisks(ctx context.Context) ([]model.VirtualDisk, error) {
	var list model.VirtualDiskList
	err := c.request(ctx, http.MethodGet, "/apis/kairon.zyvor.dev/v1alpha1/virtualdisks", nil, &list, "")
	return list.Items, err
}

func (c *Client) GetVirtualDisk(ctx context.Context, ns, name string) (model.VirtualDisk, error) {
	var out model.VirtualDisk
	path := fmt.Sprintf("/apis/kairon.zyvor.dev/v1alpha1/namespaces/%s/virtualdisks/%s", url.PathEscape(ns), url.PathEscape(name))
	err := c.request(ctx, http.MethodGet, path, nil, &out, "")
	return out, err
}

func (c *Client) PatchVirtualDiskStatus(ctx context.Context, ns, name string, status model.VirtualDiskStatus) error {
	path := fmt.Sprintf("/apis/kairon.zyvor.dev/v1alpha1/namespaces/%s/virtualdisks/%s/status", url.PathEscape(ns), url.PathEscape(name))
	return c.request(ctx, http.MethodPatch, path, map[string]any{"status": status}, nil, "application/merge-patch+json")
}

func (c *Client) ListMachineSnapshots(ctx context.Context) ([]model.MachineSnapshot, error) {
	var list model.MachineSnapshotList
	err := c.request(ctx, http.MethodGet, "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots", nil, &list, "")
	return list.Items, err
}

func (c *Client) PatchMachineSnapshotStatus(ctx context.Context, ns, name string, status model.MachineSnapshotStatus) error {
	path := fmt.Sprintf("/apis/kairon.zyvor.dev/v1alpha1/namespaces/%s/machinesnapshots/%s/status", url.PathEscape(ns), url.PathEscape(name))
	return c.request(ctx, http.MethodPatch, path, map[string]any{"status": status}, nil, "application/merge-patch+json")
}

func (c *Client) GetPVC(ctx context.Context, ns, name string) (model.PersistentVolumeClaim, error) {
	var out model.PersistentVolumeClaim
	path := fmt.Sprintf("/api/v1/namespaces/%s/persistentvolumeclaims/%s", url.PathEscape(ns), url.PathEscape(name))
	err := c.request(ctx, http.MethodGet, path, nil, &out, "")
	return out, err
}

func (c *Client) GetPV(ctx context.Context, name string) (model.PersistentVolume, error) {
	var out model.PersistentVolume
	path := fmt.Sprintf("/api/v1/persistentvolumes/%s", url.PathEscape(name))
	err := c.request(ctx, http.MethodGet, path, nil, &out, "")
	return out, err
}
