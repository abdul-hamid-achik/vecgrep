//go:build unix

package daemon

import (
	"runtime"
	"syscall"
	"testing"
)

func TestFDLimitTarget(t *testing.T) {
	const unlimited = uint64(1) << 62
	tests := []struct {
		name               string
		cur, hard, ceiling uint64
		want               uint64
	}{
		{"clamped to macos ceiling", 256, unlimited, 61440, 61440},
		{"no ceiling uses desired", 256, unlimited, 0, desiredFDLimit},
		{"clamped to hard limit", 256, 20000, 61440, 20000},
		{"clamped to smaller ceiling", 256, 20000, 10000, 10000},
		{"hard and ceiling both zero", 256, 0, 0, desiredFDLimit},
		{"already above target returns cur", 70000, unlimited, 61440, 70000},
		{"already at desired", desiredFDLimit, unlimited, 0, desiredFDLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fdLimitTarget(tt.cur, tt.hard, tt.ceiling)
			if got != tt.want {
				t.Fatalf("fdLimitTarget(%d, %d, %d) = %d, want %d", tt.cur, tt.hard, tt.ceiling, got, tt.want)
			}
			if got < tt.cur {
				t.Errorf("fdLimitTarget returned %d below current %d", got, tt.cur)
			}
		})
	}
}

func TestSystemFDCeiling(t *testing.T) {
	got := systemFDCeiling()
	if runtime.GOOS == "darwin" && got == 0 {
		t.Fatal("expected non-zero kern.maxfilesperproc on darwin")
	}
}

func TestRaiseFDLimitNeverLowers(t *testing.T) {
	var before syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &before); err != nil {
		t.Skipf("getrlimit unavailable: %v", err)
	}

	raiseFDLimit() // must be safe to call and never reduce the soft limit

	var after syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &after); err != nil {
		t.Fatalf("getrlimit: %v", err)
	}
	if after.Cur < before.Cur {
		t.Fatalf("raiseFDLimit lowered soft limit: %d → %d", before.Cur, after.Cur)
	}
}
