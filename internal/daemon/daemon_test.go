package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandleSwitchBranchIsRejected(t *testing.T) {
	// The daemon cannot switch branches in place (it holds an exclusive lock on
	// one branch's index dir), so the handler must reject the request with
	// guidance rather than deadlock or corrupt state. A zero-value Daemon is
	// fine: the handler touches no fields.
	d := &Daemon{}
	resp := d.handleSwitchBranch(context.Background(), &jsonRPCRequest{
		ID:     json.RawMessage("1"),
		Method: "daemon.switchBranch",
		Params: json.RawMessage(`{"branch":"feature"}`),
	})
	if resp.Error == nil {
		t.Fatal("expected handleSwitchBranch to return an error")
	}
	if !strings.Contains(resp.Error.Message, "vecgrep branch switch") {
		t.Fatalf("error should point to the CLI branch-switch flow, got: %q", resp.Error.Message)
	}
}

func TestReadStateNoFile(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := ReadState(tmpDir)
	if err == nil {
		t.Fatal("expected error when state file does not exist")
	}
}

func TestReadStateValid(t *testing.T) {
	tmpDir := t.TempDir()
	state := DaemonState{
		ProjectRoot:  "/tmp/project",
		ProjectName:  "test-project",
		PID:          12345,
		StartedAt:    time.Now().Truncate(time.Second),
		LastActivity: time.Now().Truncate(time.Second),
		ActiveBranch: "main",
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "daemon.json"), data, 0644); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	loaded, err := ReadState(tmpDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	if loaded.ProjectName != state.ProjectName {
		t.Errorf("ProjectName = %q, want %q", loaded.ProjectName, state.ProjectName)
	}
	if loaded.PID != state.PID {
		t.Errorf("PID = %d, want %d", loaded.PID, state.PID)
	}
	if loaded.ActiveBranch != state.ActiveBranch {
		t.Errorf("ActiveBranch = %q, want %q", loaded.ActiveBranch, state.ActiveBranch)
	}
}

func TestReadStateCorrupt(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "daemon.json"), []byte("not json"), 0644); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}
	_, err := ReadState(tmpDir)
	if err == nil {
		t.Fatal("expected error for corrupt state file")
	}
}

func TestIsRunningNoLockFile(t *testing.T) {
	tmpDir := t.TempDir()
	if IsRunning(tmpDir) {
		t.Fatal("expected IsRunning=false when no lock file exists")
	}
}

func TestIsRunningStaleLock(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a lock file but no socket → stale lock
	if err := os.WriteFile(filepath.Join(tmpDir, "daemon.lock"), []byte("99999\n"), 0644); err != nil {
		t.Fatalf("write lock file: %v", err)
	}

	if IsRunning(tmpDir) {
		t.Fatal("expected IsRunning=false when lock exists but socket is not responding")
	}
}

func TestDaemonStateJSONRoundTrip(t *testing.T) {
	original := DaemonState{
		ProjectRoot:  "/home/user/project",
		ProjectName:  "myproject",
		PID:          42,
		StartedAt:    time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		LastActivity: time.Date(2026, 1, 1, 12, 5, 0, 0, time.UTC),
		ActiveBranch: "feature/test",
		QueueDepth:   3,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var loaded DaemonState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if loaded.ProjectRoot != original.ProjectRoot {
		t.Errorf("ProjectRoot = %q, want %q", loaded.ProjectRoot, original.ProjectRoot)
	}
	if loaded.PID != original.PID {
		t.Errorf("PID = %d, want %d", loaded.PID, original.PID)
	}
	if loaded.QueueDepth != original.QueueDepth {
		t.Errorf("QueueDepth = %d, want %d", loaded.QueueDepth, original.QueueDepth)
	}
}

func TestParseSweepInterval(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"invalid", 0},
		{"-1h", 0},
		{"24h", 24 * time.Hour},
		{"6h", 6 * time.Hour},
		{"30m", 30 * time.Minute},
	}
	for _, tc := range tests {
		got := parseSweepInterval(tc.input)
		if got != tc.want {
			t.Errorf("parseSweepInterval(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}
