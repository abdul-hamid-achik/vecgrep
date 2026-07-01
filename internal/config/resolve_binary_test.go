package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestResolveBinaryFallsBackToCommonDirs is a regression test for the false
// negative reported against vecgrep's codemap-integration status: when
// vecgrep runs as an MCP server subprocess, its inherited $PATH can be a
// minimal one (e.g. "/usr/bin:/bin:/usr/sbin:/sbin") that omits directories
// like /opt/homebrew/bin — even though the binary in question (codemap, in
// the original report) is genuinely installed and on the interactive
// shell's PATH. A bare exec.LookPath against that minimal PATH reports
// "not found", which is wrong. ResolveBinary must fall back to checking
// commonBinDirs (which includes $HOME/go/bin) before giving up.
func TestResolveBinaryFallsBackToCommonDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("commonBinDirs / executable-bit semantics are POSIX-specific")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HOMEBREW_PREFIX", "")

	goBin := filepath.Join(home, "go", "bin")
	if err := os.MkdirAll(goBin, 0o755); err != nil {
		t.Fatalf("failed to create fake $HOME/go/bin: %v", err)
	}
	fakeBin := filepath.Join(goBin, "codemap-fake-xyz")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\necho fake\n"), 0o755); err != nil {
		t.Fatalf("failed to write fake binary: %v", err)
	}

	// Simulate the minimal PATH a subprocess can inherit from a non-login
	// parent process — deliberately excluding $HOME/go/bin.
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")

	resolved, err := ResolveBinary("codemap-fake-xyz")
	if err != nil {
		t.Fatalf("ResolveBinary should have found the fake binary via the $HOME/go/bin fallback, got error: %v", err)
	}
	if resolved != fakeBin {
		t.Fatalf("resolved path = %q, want %q", resolved, fakeBin)
	}
}

// TestResolveBinaryPrefersPathLookup ensures a binary that is genuinely on
// $PATH is returned via the normal, fast exec.LookPath route rather than
// falling through to the common-dirs scan.
func TestResolveBinaryPrefersPathLookup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable-bit semantics are POSIX-specific")
	}

	dir := t.TempDir()
	binPath := filepath.Join(dir, "on-path-xyz")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatalf("failed to write fake binary: %v", err)
	}
	t.Setenv("PATH", dir)

	resolved, err := ResolveBinary("on-path-xyz")
	if err != nil {
		t.Fatalf("expected ResolveBinary to find on-path-xyz, got error: %v", err)
	}
	if resolved != binPath {
		t.Fatalf("resolved path = %q, want %q", resolved, binPath)
	}
}

// TestResolveBinaryNotFoundAnywhere confirms a genuinely absent binary still
// produces an error instead of a false positive.
func TestResolveBinaryNotFoundAnywhere(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HOMEBREW_PREFIX", "")
	t.Setenv("PATH", filepath.Join(home, "empty-bin-dir"))

	if _, err := ResolveBinary("definitely-not-a-real-binary-xyz"); err == nil {
		t.Fatal("expected an error for a binary that does not exist anywhere")
	}
}

// TestResolveBinaryExplicitPathSkipsFallback confirms that when the caller
// passes an explicit path (containing a path separator, e.g. a
// user-configured codemap.bin), ResolveBinary does not try to reinterpret it
// as a bare name and search commonBinDirs.
func TestResolveBinaryExplicitPathSkipsFallback(t *testing.T) {
	if _, err := ResolveBinary("/definitely/not/a/real/absolute/path/codemap"); err == nil {
		t.Fatal("expected an error for a nonexistent explicit path")
	}
}
