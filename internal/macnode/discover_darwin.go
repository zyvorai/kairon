//go:build darwin

package macnode

import (
	"runtime"

	"golang.org/x/sys/unix"
)

// Discover reads this Mac's CPU count and unified memory.
func Discover() Info {
	mem, _ := unix.SysctlUint64("hw.memsize")
	arch := "arm64"
	if runtime.GOARCH == "amd64" {
		arch = "amd64"
	}
	return Info{CPUs: runtime.NumCPU(), MemoryMiB: mem / (1024 * 1024), Arch: arch, OS: "darwin", InternalIP: PrimaryIPv4()}
}
