package config

import (
	"path/filepath"
	"testing"
)

func TestLoadResolvedAppliesCodemapEnv(t *testing.T) {
	isolateConfigTestEnv(t)
	projectRoot := t.TempDir()

	t.Setenv("VECGREP_CODEMAP_ENABLED", "true")
	t.Setenv("VECGREP_CODEMAP_BIN", "/usr/local/bin/codemap")
	t.Setenv("VECGREP_CODEMAP_MCP_ENDPOINT", "stdio")
	t.Setenv("VECGREP_CODEMAP_STRUCTURAL_WEIGHT", "0.25")

	resolved, err := LoadResolved(projectRoot)
	if err != nil {
		t.Fatalf("LoadResolved failed: %v", err)
	}

	cfg := resolved.Config
	if !cfg.Codemap.Enabled {
		t.Fatal("codemap.enabled = false, want true")
	}
	if cfg.Codemap.Bin != "/usr/local/bin/codemap" {
		t.Fatalf("codemap.bin = %q, want /usr/local/bin/codemap", cfg.Codemap.Bin)
	}
	if cfg.Codemap.MCPEndpoint != "stdio" {
		t.Fatalf("codemap.mcp_endpoint = %q, want stdio", cfg.Codemap.MCPEndpoint)
	}
	if cfg.Codemap.StructuralWeight != 0.25 {
		t.Fatalf("codemap.structural_weight = %f, want 0.25", cfg.Codemap.StructuralWeight)
	}
}

func TestLoadResolvedAppliesDaemonEnv(t *testing.T) {
	isolateConfigTestEnv(t)
	projectRoot := t.TempDir()

	t.Setenv("VECGREP_DAEMON_AUTOSTART", "true")
	t.Setenv("VECGREP_DAEMON_IDLE_TIMEOUT", "60")
	t.Setenv("VECGREP_DAEMON_EMBED_WORKERS", "4")
	t.Setenv("VECGREP_DAEMON_EMBED_RPS", "10.5")
	t.Setenv("VECGREP_DAEMON_EMBED_MAX_IN_FLIGHT", "8")
	t.Setenv("VECGREP_DAEMON_DEBOUNCE", "300")

	resolved, err := LoadResolved(projectRoot)
	if err != nil {
		t.Fatalf("LoadResolved failed: %v", err)
	}

	cfg := resolved.Config
	if !cfg.Daemon.Autostart {
		t.Fatal("daemon.autostart = false, want true")
	}
	if cfg.Daemon.IdleTimeout != 60 {
		t.Fatalf("daemon.idle_timeout = %d, want 60", cfg.Daemon.IdleTimeout)
	}
	if cfg.Daemon.EmbedWorkers != 4 {
		t.Fatalf("daemon.embed_workers = %d, want 4", cfg.Daemon.EmbedWorkers)
	}
	if cfg.Daemon.EmbedRPS != 10.5 {
		t.Fatalf("daemon.embed_rps = %f, want 10.5", cfg.Daemon.EmbedRPS)
	}
	if cfg.Daemon.EmbedMaxInFlight != 8 {
		t.Fatalf("daemon.embed_max_in_flight = %d, want 8", cfg.Daemon.EmbedMaxInFlight)
	}
	if cfg.Daemon.Debounce != 300 {
		t.Fatalf("daemon.debounce = %d, want 300", cfg.Daemon.Debounce)
	}
}

func TestDefaultConfigHasDaemonDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Daemon.IdleTimeout != DefaultDaemonIdleTimeout {
		t.Errorf("daemon.idle_timeout = %d, want %d", cfg.Daemon.IdleTimeout, DefaultDaemonIdleTimeout)
	}
	if cfg.Daemon.EmbedWorkers != DefaultDaemonEmbedWorkers {
		t.Errorf("daemon.embed_workers = %d, want %d", cfg.Daemon.EmbedWorkers, DefaultDaemonEmbedWorkers)
	}
	if cfg.Daemon.EmbedMaxInFlight != DefaultDaemonEmbedMaxInFlight {
		t.Errorf("daemon.embed_max_in_flight = %d, want %d", cfg.Daemon.EmbedMaxInFlight, DefaultDaemonEmbedMaxInFlight)
	}
	if cfg.Daemon.Debounce != DefaultDaemonDebounceMs {
		t.Errorf("daemon.debounce = %d, want %d", cfg.Daemon.Debounce, DefaultDaemonDebounceMs)
	}
}

func TestSetConfigCodemapValuesResolve(t *testing.T) {
	isolateConfigTestEnv(t)
	projectRoot := t.TempDir()
	configPath := filepath.Join(projectRoot, "vecgrep.yaml")

	settings := map[string]string{
		"codemap.enabled":           "true",
		"codemap.bin":               "/usr/local/bin/codemap",
		"codemap.structural_weight": "0.2",
		"daemon.autostart":          "true",
		"daemon.idle_timeout":       "45",
		"daemon.embed_workers":      "3",
	}

	for key, value := range settings {
		if err := SetConfigValueInFile(configPath, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	resolved, err := LoadResolved(projectRoot)
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	cfg := resolved.Config

	if !cfg.Codemap.Enabled {
		t.Fatal("codemap.enabled = false, want true")
	}
	if cfg.Codemap.Bin != "/usr/local/bin/codemap" {
		t.Fatalf("codemap.bin = %q, want /usr/local/bin/codemap", cfg.Codemap.Bin)
	}
	if cfg.Codemap.StructuralWeight != 0.2 {
		t.Fatalf("codemap.structural_weight = %f, want 0.2", cfg.Codemap.StructuralWeight)
	}
	if !cfg.Daemon.Autostart {
		t.Fatal("daemon.autostart = false, want true")
	}
	if cfg.Daemon.IdleTimeout != 45 {
		t.Fatalf("daemon.idle_timeout = %d, want 45", cfg.Daemon.IdleTimeout)
	}
	if cfg.Daemon.EmbedWorkers != 3 {
		t.Fatalf("daemon.embed_workers = %d, want 3", cfg.Daemon.EmbedWorkers)
	}
}

func TestMergeCodemapConfig(t *testing.T) {
	dst := DefaultConfig()
	src := &Config{
		Codemap: CodemapConfig{
			Enabled:          true,
			Bin:              "/custom/codemap",
			StructuralWeight: 0.5,
		},
	}
	mergeCodemapConfig(dst, src)

	if !dst.Codemap.Enabled {
		t.Error("codemap.enabled not merged")
	}
	if dst.Codemap.Bin != "/custom/codemap" {
		t.Errorf("codemap.bin = %q, want /custom/codemap", dst.Codemap.Bin)
	}
	if dst.Codemap.StructuralWeight != 0.5 {
		t.Errorf("codemap.structural_weight = %f, want 0.5", dst.Codemap.StructuralWeight)
	}
}

func TestMergeDaemonConfig(t *testing.T) {
	dst := DefaultConfig()
	src := &Config{
		Daemon: DaemonConfig{
			Autostart:        true,
			IdleTimeout:      90,
			EmbedWorkers:     6,
			EmbedRPS:         20,
			EmbedMaxInFlight: 10,
			Debounce:         250,
		},
	}
	mergeDaemonConfig(dst, src)

	if !dst.Daemon.Autostart {
		t.Error("daemon.autostart not merged")
	}
	if dst.Daemon.IdleTimeout != 90 {
		t.Errorf("daemon.idle_timeout = %d, want 90", dst.Daemon.IdleTimeout)
	}
	if dst.Daemon.EmbedWorkers != 6 {
		t.Errorf("daemon.embed_workers = %d, want 6", dst.Daemon.EmbedWorkers)
	}
	if dst.Daemon.EmbedRPS != 20 {
		t.Errorf("daemon.embed_rps = %f, want 20", dst.Daemon.EmbedRPS)
	}
	if dst.Daemon.EmbedMaxInFlight != 10 {
		t.Errorf("daemon.embed_max_in_flight = %d, want 10", dst.Daemon.EmbedMaxInFlight)
	}
	if dst.Daemon.Debounce != 250 {
		t.Errorf("daemon.debounce = %d, want 250", dst.Daemon.Debounce)
	}
}

func TestLoadResolvedIsolationClearsCodemapDaemonEnv(t *testing.T) {
	isolateConfigTestEnv(t) // stubs codemap as not installed
	projectRoot := t.TempDir()

	// Ensure env vars are cleared (isolateConfigTestEnv should handle this,
	// but verify the defaults come through)
	resolved, err := LoadResolved(projectRoot)
	if err != nil {
		t.Fatalf("LoadResolved failed: %v", err)
	}

	if resolved.Config.Codemap.Enabled {
		t.Fatal("codemap.enabled should default to false when codemap is not installed")
	}
	if resolved.Config.Daemon.Autostart {
		t.Fatal("daemon.autostart should default to false")
	}
}

// TestLoadResolvedAutoEnablesCodemapWhenInstalled pins the new default: with no
// explicit setting, codemap.enabled defaults to ON when the codemap CLI is
// installed (detected on PATH), so the integration works out of the box.
func TestLoadResolvedAutoEnablesCodemapWhenInstalled(t *testing.T) {
	isolateConfigTestEnv(t)
	codemapDetect = func() bool { return true } // simulate codemap installed
	projectRoot := t.TempDir()

	resolved, err := LoadResolved(projectRoot)
	if err != nil {
		t.Fatalf("LoadResolved failed: %v", err)
	}
	if !resolved.Config.Codemap.Enabled {
		t.Fatal("codemap.enabled should auto-default to true when codemap is installed")
	}
}

// TestLoadResolvedExplicitDisableOverridesDetection ensures an explicit
// VECGREP_CODEMAP_ENABLED=false still wins even when codemap is installed — the
// auto-enable is only the resolution-base default, not a forced override.
func TestLoadResolvedExplicitDisableOverridesDetection(t *testing.T) {
	isolateConfigTestEnv(t)
	codemapDetect = func() bool { return true }  // installed...
	t.Setenv("VECGREP_CODEMAP_ENABLED", "false") // ...but explicitly disabled
	projectRoot := t.TempDir()

	resolved, err := LoadResolved(projectRoot)
	if err != nil {
		t.Fatalf("LoadResolved failed: %v", err)
	}
	if resolved.Config.Codemap.Enabled {
		t.Fatal("explicit VECGREP_CODEMAP_ENABLED=false must override the install-detected default")
	}
}
