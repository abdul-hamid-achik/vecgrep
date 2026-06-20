package main

import (
	"strings"
	"testing"
)

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
