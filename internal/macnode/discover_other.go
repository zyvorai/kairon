//go:build !darwin

package macnode

import (
	"os"
	"runtime"
	"strconv"
)

// Discover on a non-Mac host is only meant for development: memory comes from KAIRON_MAC_MEMORY_MIB.
func Discover() Info {
	mib, _ := strconv.ParseUint(os.Getenv("KAIRON_MAC_MEMORY_MIB"), 10, 64)
	return Info{CPUs: runtime.NumCPU(), MemoryMiB: mib, Arch: runtime.GOARCH, OS: runtime.GOOS, InternalIP: PrimaryIPv4()}
}
