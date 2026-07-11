package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/spf13/cobra"
)

func TestFormatETA(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{-5 * time.Second, "0s"},
		{0, "0s"},
		{45 * time.Second, "45s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m 0s"},
		{125 * time.Second, "2m 5s"},
		{3599 * time.Second, "59m 59s"},
		{3600 * time.Second, "1h 0m"},
		{3661 * time.Second, "1h 1m"},
	}
	for _, tc := range tests {
		if got := formatETA(tc.in); got != tc.want {
			t.Errorf("formatETA(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStudioCommandIncludesBrowseAlias(t *testing.T) {
	foundBrowse := false
	for _, alias := range studioCmd.Aliases {
		if alias == "browse" {
			foundBrowse = true
		}
	}
	if !foundBrowse {
		t.Fatalf("studio aliases = %v, want browse", studioCmd.Aliases)
	}
	for _, alias := range studioCmd.Aliases {
		if alias == "tui" {
			t.Fatalf("studio aliases = %v, should not include tui", studioCmd.Aliases)
		}
	}
}

func TestSearchCommandDoesNotExposeUnwiredProfileFlags(t *testing.T) {
	for _, name := range []string{"profile", "profiles", "all-profiles"} {
		if flag := searchCmd.Flags().Lookup(name); flag != nil {
			t.Fatalf("search flag %q is exposed but profile search is not wired", name)
		}
	}
}

func TestRootCommandDoesNotExposeUnwiredProfileCommand(t *testing.T) {
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "profile" {
			t.Fatal("root command exposes profile command but profile indexing/search is not wired")
		}
	}
}

func TestStudioCommandAcceptsOptionalPath(t *testing.T) {
	if !strings.Contains(studioCmd.Use, "[path]") {
		t.Fatalf("studio use = %q, want optional path", studioCmd.Use)
	}
	if err := studioCmd.Args(studioCmd, []string{"one", "two"}); err == nil {
		t.Fatal("studio command accepted more than one path argument")
	}
}

func TestRunConfigPresetListsSupportedProfiles(t *testing.T) {
	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)

	if err := runConfigPreset(cmd, nil); err != nil {
		t.Fatalf("runConfigPreset() error = %v", err)
	}
	got := output.String()
	for _, want := range []string{"fast-local", "nomic-embed-text", "quality-code", "qwen3-embedding:0.6b"} {
		if !strings.Contains(got, want) {
			t.Errorf("runConfigPreset() output missing %q:\n%s", want, got)
		}
	}
}

func TestRunConfigPresetAppliesGlobalProfileAndPrintsRebuildSteps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)
	cmd.Flags().Bool("global", false, "")
	if err := cmd.Flags().Set("global", "true"); err != nil {
		t.Fatal(err)
	}

	if err := runConfigPreset(cmd, []string{"quality-code"}); err != nil {
		t.Fatalf("runConfigPreset() error = %v", err)
	}
	globalConfig, err := config.LoadGlobalConfig()
	if err != nil {
		t.Fatalf("LoadGlobalConfig() error = %v", err)
	}
	if got, want := globalConfig.Defaults.Embedding.Model, "qwen3-embedding:0.6b"; got != want {
		t.Errorf("global model = %q, want %q", got, want)
	}
	if got, want := globalConfig.Defaults.Embedding.OllamaContext, 1024; got != want {
		t.Errorf("global Ollama context = %d, want %d", got, want)
	}
	for _, want := range []string{"ollama pull qwen3-embedding:0.6b", "vecgrep index --full"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("runConfigPreset() output missing %q:\n%s", want, output.String())
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".vecgrep", "config.yaml")); err != nil {
		t.Errorf("global config was not written: %v", err)
	}
}

func TestRunBenchmarkEmbeddingsRejectsUnknownPresetBeforeProviderUse(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("root", "../..", "")
	cmd.Flags().String("dataset", "", "")
	cmd.Flags().StringSlice("profiles", []string{"unknown"}, "")
	cmd.Flags().Int("batch-size", 32, "")
	cmd.Flags().Bool("json", false, "")

	err := runBenchmarkEmbeddings(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), `unknown embedding preset "unknown"`) {
		t.Fatalf("runBenchmarkEmbeddings() error = %v, want unknown preset", err)
	}
}
