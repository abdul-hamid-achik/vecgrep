//go:build darwin

package daemon

import (
	"log"
	"syscall"
)

// systemFDCeiling returns the kernel per-process open-file cap
// (kern.maxfilesperproc). macOS rejects RLIMIT_NOFILE values above it with
// EINVAL even when the hard limit reports "unlimited", so the soft limit must be
// clamped to it. Returns 0 if the value can't be read, meaning "no extra clamp".
func systemFDCeiling() uint64 {
	n, err := syscall.SysctlUint32("kern.maxfilesperproc")
	if err != nil {
		log.Printf("daemon: read kern.maxfilesperproc: %v", err)
		return 0
	}
	return uint64(n)
}
