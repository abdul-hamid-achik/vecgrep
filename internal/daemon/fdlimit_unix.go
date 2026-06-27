//go:build unix

package daemon

import (
	"log"
	"syscall"
)

// desiredFDLimit is the soft open-file limit the daemon aims for. The recursive
// file watcher opens one descriptor per directory (kqueue on macOS), so the
// default soft limit — 256 under launchd and on a stock macOS login — is easily
// exhausted before the daemon's unix socket can bind, surfacing as a misleading
// "listen unix ...: too many open files".
const desiredFDLimit = 65536

// raiseFDLimit best-effort raises the process soft RLIMIT_NOFILE as high as the
// OS allows, up to desiredFDLimit. Rather than guessing, it computes the real
// ceiling — the hard limit, and on macOS the kern.maxfilesperproc cap — and
// sets the soft limit to it in a single call. Any failure is logged; it never
// aborts daemon startup.
func raiseFDLimit() {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		log.Printf("daemon: read open-file limit: %v", err)
		return
	}

	target := fdLimitTarget(rl.Cur, rl.Max, systemFDCeiling())
	if target <= rl.Cur {
		return // already at or above what we can usefully set
	}

	prev := rl.Cur
	rl.Cur = target
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		log.Printf("daemon: could not raise open-file limit from %d to %d: %v", prev, target, err)
		return
	}
	log.Printf("daemon: raised open-file limit %d → %d", prev, target)
}

// fdLimitTarget computes the soft limit to request: desiredFDLimit, clamped down
// to the hard limit and to the OS per-process ceiling (each ignored when 0). It
// never returns less than cur, so callers can treat target <= cur as "nothing
// to do".
func fdLimitTarget(cur, hard, ceiling uint64) uint64 {
	target := uint64(desiredFDLimit)
	if hard > 0 && hard < target {
		target = hard
	}
	if ceiling > 0 && ceiling < target {
		target = ceiling
	}
	if target < cur {
		return cur
	}
	return target
}
