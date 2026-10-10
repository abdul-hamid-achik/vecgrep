package app

import (
	"os"
	"runtime"
)

// syncDir flushes a directory's metadata to stable storage where the platform
// supports it. On Windows, os.File.Sync on a directory handle always fails with
// ERROR_ACCESS_DENIED: FlushFileBuffers requires a handle opened with
// GENERIC_WRITE access, which os.Open does not provide for directories.
// Directory syncing is a best-effort durability step, so skip it on Windows and
// keep it on the platforms where it works.
func syncDir(dir *os.File) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	return dir.Sync()
}
