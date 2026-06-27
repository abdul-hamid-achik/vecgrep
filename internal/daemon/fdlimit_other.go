//go:build !unix

package daemon

// raiseFDLimit is a no-op on platforms without a POSIX RLIMIT_NOFILE
// (e.g. Windows). The daemon's unix-socket design makes those platforms
// unsupported anyway; this exists only to keep cross-compilation working.
func raiseFDLimit() {}
