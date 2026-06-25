package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/memory"
	"github.com/spf13/cobra"
)

// TestMemoryRecallC5Golden pins the C5 contract: `memory recall --format json`
// emits a JSON array of {id,content,importance,tags,score}. If the emitted
// shape drifts from C5, this fails instead of silently breaking codemap's
// parser (the version-skew failure mode the cross-tool goldens guard against).
func TestMemoryRecallC5Golden(t *testing.T) {
	memories := []memory.Memory{
		{
			ID:         7,
			Content:    "auth token rotation: refresh before the 5m skew window",
			Importance: 0.7,
			Tags:       []string{"codemap", "abc123def456"},
			CreatedAt:  time.Unix(1700000000, 0),
			Score:      0.55,
		},
		{
			ID:         12,
			Content:    "ValidateToken must reject the empty-kid JWT",
			Importance: 0.9,
			Tags:       []string{"codemap", "abc123def456", "bug"},
			CreatedAt:  time.Unix(1700000100, 0),
			Score:      0.41,
		},
	}

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	if err := writeMemoriesJSON(cmd, memories); err != nil {
		t.Fatalf("writeMemoriesJSON: %v", err)
	}

	golden := filepath.Join("testdata", "memory_recall_c5.json")
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got := buf.String(); got != string(want) {
		t.Errorf("C5 output mismatch.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestMemoryRecallEmptyIsArrayNotNull asserts the fail-closed shape: an empty
// recall emits "[]\n", not "null", so a consumer always parses a valid array.
func TestMemoryRecallEmptyIsArrayNotNull(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	if err := writeMemoriesJSON(cmd, nil); err != nil {
		t.Fatalf("writeMemoriesJSON: %v", err)
	}
	if got := buf.String(); got != "[]\n" {
		t.Errorf("empty recall json = %q, want %q", got, "[]\n")
	}
}

// TestParseTags asserts the CSV tag parsing used for --tags (trim + drop blanks).
func TestParseTags(t *testing.T) {
	cases := map[string][]string{
		"":                   nil,
		"codemap":            {"codemap"},
		"codemap,abc123":     {"codemap", "abc123"},
		" codemap , abc123 ": {"codemap", "abc123"},
		"codemap,,abc123,":   {"codemap", "abc123"},
	}
	for in, want := range cases {
		got := parseTags(in)
		if len(got) != len(want) {
			t.Errorf("parseTags(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("parseTags(%q)[%d] = %q, want %q", in, i, got[i], want[i])
			}
		}
	}
}

// TestMemoryCommandWiring asserts the cobra command tree and flags exist so the
// CLI surface codemap shells stays present (recall is the G2 dependency).
func TestMemoryCommandWiring(t *testing.T) {
	sub := map[string]bool{}
	for _, c := range memoryCmd.Commands() {
		sub[c.Name()] = true
	}
	for _, want := range []string{"recall", "remember"} {
		if !sub[want] {
			t.Errorf("memory command missing subcommand %q", want)
		}
	}
	for _, f := range []string{"tags", "min-importance", "limit", "format"} {
		if memoryRecallCmd.Flags().Lookup(f) == nil {
			t.Errorf("memory recall missing --%s flag", f)
		}
	}
	for _, f := range []string{"tags", "importance", "ttl-hours"} {
		if memoryRememberCmd.Flags().Lookup(f) == nil {
			t.Errorf("memory remember missing --%s flag", f)
		}
	}
}
