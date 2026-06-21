package mcp

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestFindImportedByGo verifies the Go reverse-dependency resolver. The
// previous substring matcher matched on the package directory, so every file
// in the same package (and any file mentioning that path string) looked like a
// reverse dependency. The parser-based implementation should only report files
// whose import block actually imports the target's package.
func TestFindImportedByGo(t *testing.T) {
	root := t.TempDir()

	// Layout:
	//   internal/db/db.go            (target)
	//   internal/app/session.go      (imports internal/db → should match)
	//   internal/app/helper.go       (same package as session.go, no db import → no match)
	//   internal/search/search.go    (imports internal/db → should match)
	//   internal/db/other.go         (same package as target → skipped, not a reverse dep)
	//   cmd/main.go                  (imports internal/app and internal/db → should match)
	//   notes.txt                    (non-Go → ignored)
	mustWrite := func(rel, content string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite("internal/db/db.go", "package db\n")
	mustWrite("internal/db/other.go", "package db\n")
	mustWrite("internal/app/session.go", "package app\n\nimport \"internal/db\"\n")
	mustWrite("internal/app/helper.go", "package app\n")
	mustWrite("internal/search/search.go", "package search\n\nimport \"internal/db\"\n")
	mustWrite("cmd/main.go", "package main\n\nimport (\n\t\"internal/app\"\n\t\"internal/db\"\n)\n")
	mustWrite("notes.txt", "internal/db")

	got := findImportedByGo(root, "internal/db/db.go")

	want := map[string]bool{
		"internal/app/session.go":   true,
		"internal/search/search.go": true,
		"cmd/main.go":               true,
	}
	// other.go is in the same package as the target and must NOT be reported
	// as a reverse dependency.
	for _, g := range got {
		if g == "internal/db/other.go" {
			t.Errorf("same-package file %s should not be reported as a reverse dependency", g)
		}
	}

	sort.Strings(got)
	if len(got) != len(want) {
		t.Errorf("expected %d reverse deps, got %d: %v", len(want), len(got), got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected reverse dep: %s", g)
		}
	}
}

// TestFindImportedByGoNested verifies suffix matching for nested module paths
// where the module path prefix is unknown to the analyzer.
func TestFindImportedByGoNested(t *testing.T) {
	root := t.TempDir()

	mustWrite := func(rel, content string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Target at internal/db/db.go. Importer uses a fully-qualified module path
	// "github.com/abdul-hamid-achik/vecgrep/internal/db" — the suffix match on
	// "internal/db" should still catch it.
	mustWrite("internal/db/db.go", "package db\n")
	mustWrite("cmd/main.go", "package main\n\nimport \"github.com/abdul-hamid-achik/vecgrep/internal/db\"\n")

	got := findImportedByGo(root, "internal/db/db.go")
	if len(got) != 1 || got[0] != "cmd/main.go" {
		t.Errorf("expected [cmd/main.go], got %v", got)
	}
}
