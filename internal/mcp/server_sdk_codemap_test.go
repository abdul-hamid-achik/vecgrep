package mcp

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestNewSDKServerResolvesCodemapFromProjectConfig is a regression test for
// the root cause of the "Codemap integration: enabled / Status: codemap
// binary not found" false negative reported against vecgrep_status and
// vecgrep_index.
//
// `vecgrep serve` (see cmd/vecgrep/main.go runServe) constructs the server
// with only SDKServerConfig{ProjectRoot: ...} — SDKServerConfig.Codemap is
// left at its zero value. NewSDKServer used to build s.codemap directly from
// that zero-value cfg.Codemap (Enabled: false), which always yields a nil
// client, and then loaded the project's fully resolved config (which DOES
// correctly auto-detect codemap via codemapDetect()/ResolveBinary) only for
// the session/daemon — never re-applying it to s.codemap. The result: every
// server started with a known project root up front reported codemap as
// unavailable no matter what was actually on disk or $PATH, and every
// codemap-dependent feature (structural rerank, impact-based search scoping)
// silently ran in a degraded, always-off mode.
//
// This test does not depend on a real codemap binary or the host's $PATH: it
// points VECGREP_CODEMAP_BIN at a fake, locally-created executable and forces
// VECGREP_CODEMAP_ENABLED=true, both of which take top priority in config
// resolution (see internal/config/resolution.go's documented precedence),
// so it is deterministic regardless of the machine it runs on.
func TestNewSDKServerResolvesCodemapFromProjectConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable-bit semantics are POSIX-specific")
	}

	projectRoot := t.TempDir()

	binDir := t.TempDir()
	fakeCodemap := filepath.Join(binDir, "codemap-fake-server-test")
	if err := os.WriteFile(fakeCodemap, []byte("#!/bin/sh\necho '{}'\n"), 0o755); err != nil {
		t.Fatalf("failed to write fake codemap binary: %v", err)
	}

	t.Setenv("VECGREP_CODEMAP_ENABLED", "true")
	t.Setenv("VECGREP_CODEMAP_BIN", fakeCodemap)

	// Mirror exactly what cmd/vecgrep/main.go's runServe does: pass only
	// ProjectRoot, leaving SDKServerConfig.Codemap unset.
	s := NewSDKServer(SDKServerConfig{ProjectRoot: projectRoot})

	if s.codemap == nil || !s.codemap.Available() {
		t.Fatalf("expected s.codemap to be resolved from the project's config (bin=%s), got nil/unavailable client", fakeCodemap)
	}
}
