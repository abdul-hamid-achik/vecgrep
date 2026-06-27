//go:build unix && !darwin

package daemon

// systemFDCeiling reports no extra per-process ceiling beyond the RLIMIT_NOFILE
// hard limit. Linux and the BSDs enforce the per-process cap through the hard
// limit itself, so there is nothing further to query.
func systemFDCeiling() uint64 { return 0 }
