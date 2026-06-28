package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestCanonicalRootIsAbsoluteAndIdempotent(t *testing.T) {
	got := canonicalRoot(".")
	if !filepath.IsAbs(got) {
		t.Fatalf("canonicalRoot(\".\") = %q, want absolute", got)
	}
	if again := canonicalRoot(got); again != got {
		t.Fatalf("canonicalRoot not idempotent: %q -> %q", got, again)
	}
}

func TestHubListProjectsEmpty(t *testing.T) {
	d := &Daemon{workers: map[string]*projectWorker{}}
	resp := d.handleListProjects(&jsonRPCRequest{ID: json.RawMessage("1")})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	var got struct {
		Projects []string `json:"projects"`
	}
	reencode(t, resp.Result, &got)
	if len(got.Projects) != 0 {
		t.Fatalf("expected no projects, got %v", got.Projects)
	}
}

func TestHubStatusEmpty(t *testing.T) {
	d := &Daemon{workers: map[string]*projectWorker{}}
	resp := d.handleStatus(&jsonRPCRequest{ID: json.RawMessage("1")})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	var got struct {
		PID      int           `json:"pid"`
		Projects []DaemonState `json:"projects"`
	}
	reencode(t, resp.Result, &got)
	if got.PID == 0 {
		t.Fatal("expected a non-zero hub PID in status")
	}
	if len(got.Projects) != 0 {
		t.Fatalf("expected no open projects, got %d", len(got.Projects))
	}
}

func TestHubSearchRequiresProject(t *testing.T) {
	d := &Daemon{}
	params, _ := json.Marshal(map[string]any{"query": "x"})
	resp := d.handleSearch(context.Background(), &jsonRPCRequest{ID: json.RawMessage("1"), Params: params})
	if resp.Error == nil {
		t.Fatal("expected error when project is missing")
	}
}

func TestHubRemoveUnknownProject(t *testing.T) {
	d := &Daemon{workers: map[string]*projectWorker{}}
	params, _ := json.Marshal(map[string]any{"project": "/no/such/project"})
	resp := d.handleRemoveProject(&jsonRPCRequest{ID: json.RawMessage("1"), Params: params})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	var got struct {
		Removed bool `json:"removed"`
	}
	reencode(t, resp.Result, &got)
	if got.Removed {
		t.Fatal("expected removed=false for an unknown project")
	}
}

func TestHubUnknownMethod(t *testing.T) {
	d := &Daemon{}
	resp := d.handleRequest(context.Background(), &jsonRPCRequest{ID: json.RawMessage("1"), Method: "daemon.bogus"})
	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
}

// reencode round-trips an RPC result (any) into a typed struct for assertions.
func reencode(t *testing.T, v any, dst any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
}
