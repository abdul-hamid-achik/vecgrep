package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTryDaemonSearchSurfacesWarnings is a regression test for the CLI's
// daemon fast-path silently dropping degraded-mode warnings. The daemon RPC
// result carries {"warnings": [...]} when hybrid search degraded to
// keyword-only (embedder down); the CLI must render them, not swallow them —
// a silently degraded search is indistinguishable from a healthy one.
func TestTryDaemonSearchSurfacesWarnings(t *testing.T) {
	// Unix socket paths are limited to ~104 bytes on macOS; t.TempDir() under
	// /var/folders is too long, so anchor the fake home directly under /tmp.
	home, err := os.MkdirTemp("/tmp", "vecgrep-home-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)

	// Project root marker so FindProjectRootFrom resolves the temp project.
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "vecgrep.yaml"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	oldWD, wdErr := os.Getwd()
	if wdErr != nil {
		t.Fatalf("getwd: %v", wdErr)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	// Fake daemon hub on the global socket answering one daemon.search call
	// with a degraded (keyword-only) result carrying a warning.
	sockDir := filepath.Join(home, ".vecgrep")
	if err := os.MkdirAll(sockDir, 0o755); err != nil {
		t.Fatalf("mkdir global config dir: %v", err)
	}
	ln, err := net.Listen("unix", filepath.Join(sockDir, "daemon.sock"))
	if err != nil {
		t.Fatalf("listen on fake daemon socket: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	const warning = "embedding provider unavailable at query time (connection refused): results are keyword-only (BM25); semantic ranking was skipped"
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		dec := json.NewDecoder(conn)
		var req map[string]any
		if err := dec.Decode(&req); err != nil {
			return
		}
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result": map[string]any{
				"results":  []any{},
				"mode":     "keyword",
				"warnings": []string{warning},
			},
		}
		_ = json.NewEncoder(conn).Encode(resp)
	}()

	// Capture stdout (default format is human-facing, warnings go to stdout).
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	handled := tryDaemonSearch(context.Background(), "sessionStorage zod", 5, "hybrid", "", nil, nil, "", "", "", 0, 0, 0, false, "default", nil, "")
	_ = w.Close()
	os.Stdout = oldStdout
	out, _ := io.ReadAll(r)

	if !handled {
		t.Fatal("tryDaemonSearch should have handled the search via the fake daemon socket")
	}
	if !strings.Contains(string(out), warning) {
		t.Errorf("daemon-served degraded search must surface the warning; output was:\n%s", out)
	}
}
