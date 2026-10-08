package fluxvm

import (
	"fmt"

	"github.com/zyvorai/kairon/internal/model"
)

// BackendApple is FluxVM's backend name for Apple Virtualization.framework (macOS on Apple silicon).
const BackendApple = "vz"

// AppleCapability mirrors the capability matrix in FluxVM's docs/macos.md (fluxvm_apple::CAPABILITIES): the
// vz backend runs ARM64 Linux guests on VZ NAT and supports a deliberately small feature set. Keeping the table here
// lets a Machine be rejected with a Kairon-side message instead of a bare HTTP 400 from FluxVM.
type AppleCapability struct {
	Feature   string
	Supported bool
	Note      string
}

var AppleCapabilities = []AppleCapability{
	{"vcpus, memory, raw disk, cloud-init", true, "NoCloud seed built with hdiutil"},
	{"network user (NAT) or none", true, "the guest is reachable at its NAT address; no host port forwards"},
	{"pause / resume, graceful shutdown, force stop", true, ""},
	{"guest agent console", true, "needs the FluxVM guest agent in the image"},
	{"tap / macvtap networking, port forwards", false, "Linux kernel features / VZ NAT has no forwards"},
	{"NUMA, hugepages, cpuSet, CPU pinning, VFIO", false, "no such controls on Apple silicon"},
	{"secure boot, TPM", false, "not offered by Virtualization.framework"},
	{"hotplug (maxCpu / maxMemory above the boot size)", false, "not supported"},
	{"cdroms, shared folders, data disks, non-default storage", false, "not implemented for the vz backend"},
	{"QEMU guest agent (spec.guestAgent.enabled)", false, "QEMU-only channel"},
}

// checkAppleBackend rejects, with a specific message, every Machine feature the vz backend cannot honour.
func checkAppleBackend(m model.Machine, cpu uint32, mem uint64, maxVCPUs *uint32, maxMemoryMiB *uint64, vfio []string) error {
	n := m.EffectiveNetwork()
	switch n.Mode {
	case "", "user":
		if len(n.Forwards) > 0 {
			return fmt.Errorf("the vz backend has no host port forwards (VZ NAT); reach the guest at its address instead of spec.network.forwards")
		}
	case "none":
	default:
		return fmt.Errorf("spec.network.mode %q is not available on the vz backend (Apple Virtualization.framework offers NAT only: use user or none)", n.Mode)
	}
	if m.Spec.Resources.CPUPinning {
		return fmt.Errorf("spec.resources.cpuPinning is not available on the vz backend")
	}
	if maxVCPUs != nil && *maxVCPUs > cpu {
		return fmt.Errorf("spec.resources.maxCpu (CPU hotplug) is not available on the vz backend")
	}
	if maxMemoryMiB != nil && *maxMemoryMiB > mem {
		return fmt.Errorf("spec.resources.maxMemory (memory hotplug) is not available on the vz backend")
	}
	if len(vfio) > 0 || len(m.Spec.DeviceClaims) > 0 {
		return fmt.Errorf("device passthrough is not available on the vz backend")
	}
	if m.Spec.GuestAgent.Enabled {
		return fmt.Errorf("spec.guestAgent.enabled is the QEMU guest-agent channel; the vz backend supports spec.guestAgent.console only")
	}
	if len(m.Spec.Disks) > 0 {
		return fmt.Errorf("spec.disks (data disks) are not available on the vz backend")
	}
	return nil
}
