package config

import (
	"slices"
	"testing"
)

func TestDefaultConfigIgnoresLocalDataDirectory(t *testing.T) {
	patterns := DefaultConfig().Indexing.IgnorePatterns
	if !slices.Contains(patterns, ".vecgrep/**") {
		t.Fatalf("default ignore patterns = %v, want .vecgrep/**", patterns)
	}
}
