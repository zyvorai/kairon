package admission

import (
	"fmt"
	"strings"

	"github.com/zyvorai/kairon/internal/model"
	"github.com/zyvorai/kairon/internal/storage"
)

// ValidateMachine returns a human-readable denial reason, or "" if admitted.
func ValidateMachine(m model.Machine) string {
	img := m.Spec.Image
	if img.Path == "" && img.MachineImageName == "" && img.VirtualDiskName == "" {
		return "spec.image.path, machineImageName, or virtualDiskName is required"
	}
	if img.Digest != "" {
		algo, hex, ok := strings.Cut(img.Digest, ":")
		if !ok || algo != "sha256" || len(hex) < 32 {
			return "spec.image.digest must be sha256:<hex>"
		}
	}
	switch storage.NormalizeStorage(m.Spec.Storage) {
	case "default", "lvm-thin", "nbd", "ceph-rbd":
	default:
		return fmt.Sprintf("unsupported spec.storage %q", m.Spec.Storage)
	}
	if m.Spec.DiskSizeGiB < 0 {
		return "spec.diskSizeGiB must be >= 0"
	}
	ps := m.DesiredPowerState()
	if ps != "Running" && ps != "Stopped" {
		return "spec.powerState must be Running or Stopped"
	}
	return ""
}
