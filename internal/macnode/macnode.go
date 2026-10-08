// Package macnode lets a Mac run kairon-node without a kubelet: it publishes the Mac as a (virtual) Kubernetes
// Node, with labels, capacity and a Ready heartbeat, so Kairon's controller can schedule Machines to it and
// kairon-node can drive them through FluxVM's Apple Virtualization.framework backend ("vz").
//
// Nothing here talks to hardware directly; Discover (per-OS) fills Info and the rest is pure, testable construction.
package macnode

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

// Label and annotation keys published on the Mac Node.
const (
	LabelBackendVZ     = "kairon.zyvor.dev/backend.vz"
	LabelMLX           = "kairon.zyvor.dev/mlx"
	AnnotationInferURL = "kairon.zyvor.dev/inference-url"
	// TaintVMOnly keeps ordinary pods off the node: it has no kubelet, only Kairon Machines run there.
	TaintVMOnly = "kairon.zyvor.dev/vm-only"
)

// Info describes the Mac. Discover fills it; tests construct it directly.
type Info struct {
	CPUs       int
	MemoryMiB  uint64
	Arch       string // kubernetes.io/arch value, normally "arm64"
	OS         string // kubernetes.io/os value, "darwin"
	InternalIP string
	// MLXURL, when set, is the loopback OpenAI-compatible endpoint of Velora's MLX runtime on this Mac.
	MLXURL string
}

// SystemReserveMiB is the memory left to macOS and the user's own apps: at least 3 GiB or a quarter of RAM.
func SystemReserveMiB(totalMiB uint64) uint64 {
	q := totalMiB / 4
	if q < 3072 {
		q = 3072
	}
	if q > totalMiB {
		return totalMiB
	}
	return q
}

// Allocatable returns the cpu and memory quantities the scheduler may hand out.
func Allocatable(i Info) map[string]string {
	mem := uint64(0)
	if i.MemoryMiB > 0 {
		mem = i.MemoryMiB - SystemReserveMiB(i.MemoryMiB)
	}
	return map[string]string{
		"cpu":    fmt.Sprintf("%d", i.CPUs),
		"memory": fmt.Sprintf("%dMi", mem),
		"pods":   "0",
	}
}

// Labels returns the labels the Mac Node carries.
func Labels(name string, i Info) map[string]string {
	l := map[string]string{
		"kubernetes.io/hostname": name,
		"kubernetes.io/arch":     i.Arch,
		"kubernetes.io/os":       i.OS,
		model.CapableLabel:       "true",
		LabelBackendVZ:           "true",
	}
	if i.MLXURL != "" {
		l[LabelMLX] = "true"
	}
	return l
}

// BuildNode is the object POSTed when the Node does not exist yet.
func BuildNode(name string, i Info) map[string]any {
	meta := map[string]any{"name": name, "labels": Labels(name, i)}
	if i.MLXURL != "" {
		meta["annotations"] = map[string]string{AnnotationInferURL: i.MLXURL}
	}
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Node",
		"metadata":   meta,
		"spec": map[string]any{
			"taints": []map[string]string{{"key": TaintVMOnly, "value": "true", "effect": "NoSchedule"}},
		},
	}
}

// BuildStatus is the status merge-patch sent on registration and then as a heartbeat.
func BuildStatus(i Info, now time.Time) map[string]any {
	t := now.UTC().Format(time.RFC3339)
	alloc := Allocatable(i)
	addr := []map[string]string{}
	if i.InternalIP != "" {
		addr = append(addr, map[string]string{"type": "InternalIP", "address": i.InternalIP})
	}
	return map[string]any{"status": map[string]any{
		"conditions": []map[string]string{{
			"type": "Ready", "status": "True", "reason": "KaironNodeReady",
			"message":           "kairon-node is running on this Mac and can start Machines through FluxVM",
			"lastHeartbeatTime": t, "lastTransitionTime": t,
		}},
		"addresses":   addr,
		"capacity":    map[string]string{"cpu": alloc["cpu"], "memory": fmt.Sprintf("%dMi", i.MemoryMiB), "pods": "0"},
		"allocatable": alloc,
	}}
}

// API is the slice of kube.Client that registration needs.
type API interface {
	GetNode(ctx context.Context, name string) (model.Node, error)
	CreateNode(ctx context.Context, node map[string]any) error
	PatchNode(ctx context.Context, name string, patch map[string]any) error
	PatchNodeStatus(ctx context.Context, name string, patch map[string]any) error
}

// Register creates the Node when missing (or refreshes its labels), then publishes capacity and Ready.
func Register(ctx context.Context, api API, name string, i Info, now time.Time) error {
	if _, err := api.GetNode(ctx, name); err != nil {
		if !kube.IsNotFound(err) {
			return fmt.Errorf("looking up node %s: %w", name, err)
		}
		if err := api.CreateNode(ctx, BuildNode(name, i)); err != nil {
			return fmt.Errorf("creating node %s: %w", name, err)
		}
	} else {
		meta := map[string]any{"labels": Labels(name, i)}
		if i.MLXURL != "" {
			meta["annotations"] = map[string]string{AnnotationInferURL: i.MLXURL}
		}
		if err := api.PatchNode(ctx, name, map[string]any{"metadata": meta}); err != nil {
			return fmt.Errorf("refreshing node %s: %w", name, err)
		}
	}
	if err := api.PatchNodeStatus(ctx, name, BuildStatus(i, now)); err != nil {
		return fmt.Errorf("publishing node status: %w", err)
	}
	return nil
}

// Run registers the node and heartbeats until ctx ends. Errors are reported through onError and retried next tick.
func Run(ctx context.Context, api API, name string, i Info, every time.Duration, onError func(error)) {
	tick := func() {
		if err := Register(ctx, api, name, i, time.Now()); err != nil && onError != nil {
			onError(err)
		}
	}
	tick()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// PrimaryIPv4 returns the first non-loopback private IPv4 address of this host, or "".
func PrimaryIPv4() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if ip := ipn.IP.To4(); ip != nil && !ip.IsLoopback() && ip.IsPrivate() {
				return ip.String()
			}
		}
	}
	return ""
}
