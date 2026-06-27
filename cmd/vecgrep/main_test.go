package main

import (
	"strings"
	"testing"
	"time"
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
