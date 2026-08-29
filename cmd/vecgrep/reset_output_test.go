package main

import (
	"strings"
	"testing"
)

// After a reset, keyword search is dead too: BM25 lives in the same veclite
// collections that reset cleared. Until the project is re-indexed (which needs
// a working embedding provider), `-m keyword` returns nothing — the reset
// output must say so instead of letting users discover it from empty results.
func TestResetReindexNoteMentionsKeywordMode(t *testing.T) {
	note := resetReindexNote()
	if !strings.Contains(note, "vecgrep index") {
		t.Fatalf("note %q should point at 'vecgrep index'", note)
	}
	if !strings.Contains(note, "keyword") {
		t.Fatalf("note %q should mention keyword mode", note)
	}
}

func TestPrintResetNextSteps(t *testing.T) {
	var out strings.Builder
	printResetNextSteps(&out)
	got := out.String()
	for _, want := range []string{"Run 'vecgrep index' to re-index your codebase.", "keyword"} {
		if !strings.Contains(got, want) {
			t.Fatalf("reset next steps %q should contain %q", got, want)
		}
	}
}
