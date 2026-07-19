package main

import (
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

func TestNeedsInteractiveIndexConfirm(t *testing.T) {
	small := &index.DryRunPreview{FilesToEmbed: 3, ScannedFiles: 10, EstimatedChunks: 5}
	if needsInteractiveIndexConfirm(false, false, small) {
		t.Fatal("small incremental should not confirm")
	}
	if !needsInteractiveIndexConfirm(true, false, small) {
		t.Fatal("full reindex should always confirm")
	}
	if !needsInteractiveIndexConfirm(false, true, small) {
		t.Fatal("empty index should always confirm")
	}
	large := &index.DryRunPreview{FilesToEmbed: index.ConfirmScopeFiles}
	if !needsInteractiveIndexConfirm(false, false, large) {
		t.Fatal("large plan should confirm")
	}
}

func TestFormatPlanBytes(t *testing.T) {
	if got := formatPlanBytes(512); got != "512 B" {
		t.Fatalf("got %q", got)
	}
	if got := formatPlanBytes(2048); got != "2.0 KiB" {
		t.Fatalf("got %q", got)
	}
}
