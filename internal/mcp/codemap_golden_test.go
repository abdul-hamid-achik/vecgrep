package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeCodemap writes a stub `codemap` executable into a temp dir that, when
// invoked as `codemap related-files <file> --json`, prints the contents of the
// given fixture file verbatim and exits 0. It returns the absolute path to the
// stub binary. On non-zero exitCode, it prints nothing and exits with that
// code (to simulate codemap's "real error" / non-zero-exit peer state).
//
// This drives the REAL exec + JSON-parse path of CodemapClient against a
// pinned C1 fixture — the cross-tool golden contract test. The three silent
// no-ops the integration shipped with are exactly what this would have caught.
func fakeCodemap(t *testing.T, fixtureAbs string, exitCode int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake codemap shell stub is POSIX-only")
	}
	dir := t.TempDir()
	binPath := filepath.Join(dir, "codemap")

	var script string
	if exitCode != 0 {
		script = fmt.Sprintf("#!/bin/sh\nexit %d\n", exitCode)
	} else {
		// cat the fixture regardless of args; the client always asks for
		// `related-files <file> --json`.
		script = fmt.Sprintf("#!/bin/sh\ncat %q\n", fixtureAbs)
	}
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return binPath
}

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("abs fixture path: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
	return abs
}

// TestRelatedFilesParsesC1Contract is the golden contract test: it pins the C1
// JSON shape and asserts RelatedFiles maps it to the right output. If codemap's
// emitted shape ever drifts from C1, this fails in CI instead of silently
// falling back like the original five-shape coupling did.
func TestRelatedFilesParsesC1Contract(t *testing.T) {
	fixture := fixturePath(t, "related_files_c1.json")
	bin := fakeCodemap(t, fixture, 0)
	c := &CodemapClient{bin: bin}

	res, err := c.RelatedFiles(context.Background(), t.TempDir(), "app/auth.go", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil {
		t.Fatal("expected a result, got nil")
	}
	if !res.Indexed {
		t.Fatal("fixture is indexed:true; result.Indexed should be true")
	}
	if len(res.Files) != 3 {
		t.Fatalf("expected 3 related files, got %d: %+v", len(res.Files), res.Files)
	}

	want := []RelatedFile{
		{RelativePath: "app/login.go", Reason: "caller", Confidence: 0.9},
		{RelativePath: "app/auth_test.go", Reason: "test", Confidence: 1.0},
		{RelativePath: "app/token.go", Reason: "callee", Confidence: 0.8},
	}
	for i, w := range want {
		got := res.Files[i]
		if got.RelativePath != w.RelativePath || got.Reason != w.Reason || got.Confidence != w.Confidence {
			t.Errorf("file[%d] = %+v, want %+v", i, got, w)
		}
	}
}

// TestRelatedFilesNotIndexedState asserts peer state (a): codemap ran but the
// project is not indexed. This must be a real (non-nil) result with
// Indexed==false, NOT an error — so the caller can fall back to heuristics
// while still distinguishing it from a crash.
func TestRelatedFilesNotIndexedState(t *testing.T) {
	fixture := fixturePath(t, "related_files_not_indexed.json")
	bin := fakeCodemap(t, fixture, 0)
	c := &CodemapClient{bin: bin}

	res, err := c.RelatedFiles(context.Background(), t.TempDir(), "app/auth.go", 10)
	if err != nil {
		t.Fatalf("not-indexed must not be an error, got: %v", err)
	}
	if res == nil || res.Indexed {
		t.Fatalf("expected non-nil result with Indexed=false, got %+v", res)
	}
	if len(res.Files) != 0 {
		t.Fatalf("expected no files when not indexed, got %d", len(res.Files))
	}
}

// TestRelatedFilesEmptyButIndexed asserts peer state (c): indexed, nothing
// related. That is a VALID answer (Indexed==true, empty Files, nil error),
// not a fallback trigger.
func TestRelatedFilesEmptyButIndexed(t *testing.T) {
	fixture := fixturePath(t, "related_files_empty.json")
	bin := fakeCodemap(t, fixture, 0)
	c := &CodemapClient{bin: bin}

	res, err := c.RelatedFiles(context.Background(), t.TempDir(), "app/auth.go", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil || !res.Indexed {
		t.Fatalf("expected non-nil result with Indexed=true, got %+v", res)
	}
	if len(res.Files) != 0 {
		t.Fatalf("indexed-but-empty should yield 0 files, got %d", len(res.Files))
	}
}

// TestRelatedFilesNonZeroExitIsError asserts peer state (b): a non-zero exit
// from codemap is surfaced as a real error so the caller can fall back AND log,
// rather than collapsing it into an indistinguishable nil.
func TestRelatedFilesNonZeroExitIsError(t *testing.T) {
	bin := fakeCodemap(t, "", 1)
	c := &CodemapClient{bin: bin}

	res, err := c.RelatedFiles(context.Background(), t.TempDir(), "app/auth.go", 10)
	if err == nil {
		t.Fatal("expected an error on non-zero exit")
	}
	if errors.Is(err, ErrCodemapUnavailable) {
		t.Fatal("non-zero exit is a real failure, not ErrCodemapUnavailable")
	}
	if res != nil {
		t.Fatalf("expected nil result on error, got %+v", res)
	}
}

// TestRelatedFilesLimitTruncates asserts the limit is honored on the parsed
// related set.
func TestRelatedFilesLimitTruncates(t *testing.T) {
	fixture := fixturePath(t, "related_files_c1.json")
	bin := fakeCodemap(t, fixture, 0)
	c := &CodemapClient{bin: bin}

	res, err := c.RelatedFiles(context.Background(), t.TempDir(), "app/auth.go", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil || len(res.Files) != 2 {
		t.Fatalf("expected 2 files with limit=2, got %+v", res)
	}
}

// TestHotspotsParsesWrapperObject asserts F2's parse fix: codemap emits an
// OBJECT {"project":…,"hotspots":[…]} with `in_degree`/`shared_name` fields —
// not a bare array of `{refs}`. The old struct produced an empty/zeroed parse.
func TestHotspotsParsesWrapperObject(t *testing.T) {
	fixture := fixturePath(t, "hotspots_c.json")
	bin := fakeCodemap(t, fixture, 0)
	c := &CodemapClient{bin: bin}

	hs, err := c.Hotspots(context.Background(), t.TempDir(), 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hs) != 3 {
		t.Fatalf("expected 3 hotspots, got %d: %+v", len(hs), hs)
	}
	if hs[0].Symbol != "Hub" || hs[0].InDegree != 100 || hs[0].SharedName != 0 {
		t.Errorf("hotspot[0] = %+v, want Hub/100/0", hs[0])
	}
	if hs[1].Symbol != "Inflated" || hs[1].InDegree != 100 || hs[1].SharedName != 10 {
		t.Errorf("hotspot[1] = %+v, want Inflated/100/10", hs[1])
	}
}

// TestRerankChangesOrdering proves F2 is actually wired: with a real in_degree
// and a non-zero structural weight, a high-hub symbol with a weak semantic
// score must be lifted above a low-hub symbol — i.e. the blend changes the
// order. With the old (refs-always-0) code this was impossible.
func TestRerankChangesOrdering(t *testing.T) {
	fixture := fixturePath(t, "hotspots_c.json")
	bin := fakeCodemap(t, fixture, 0)
	c := &CodemapClient{bin: bin}

	// "Hub" has weaker semantic score than "Minor" but is the top hub.
	input := []CodemapRerankResult{
		{Result: codemapSearchResult{SymbolName: "Minor", RelativePath: "a.go", Score: 0.60}},
		{Result: codemapSearchResult{SymbolName: "Hub", RelativePath: "b.go", Score: 0.55}},
	}
	// Heavy structural weight so the hub signal dominates.
	out := c.Rerank(context.Background(), t.TempDir(), input, 0.8)
	if out[0].Result.SymbolName != "Hub" {
		t.Fatalf("rerank should lift the hub to first; got order %s, %s",
			out[0].Result.SymbolName, out[1].Result.SymbolName)
	}
	if out[0].StructuralScore == 0 {
		t.Fatal("hub structural score should be non-zero (rerank would be inert)")
	}
}

// TestRerankDownWeightsInflatedHub asserts F2's shared_name>1 down-weighting:
// "Hub" and "Inflated" have identical in_degree (100), but Inflated's count is
// spread across 10 same-named defs, so its structural score must be lower.
func TestRerankDownWeightsInflatedHub(t *testing.T) {
	fixture := fixturePath(t, "hotspots_c.json")
	bin := fakeCodemap(t, fixture, 0)
	c := &CodemapClient{bin: bin}

	input := []CodemapRerankResult{
		{Result: codemapSearchResult{SymbolName: "Hub", RelativePath: "b.go", Score: 0.5}},
		{Result: codemapSearchResult{SymbolName: "Inflated", RelativePath: "c.go", Score: 0.5}},
	}
	out := c.Rerank(context.Background(), t.TempDir(), input, 0.8)

	score := map[string]float32{}
	for _, r := range out {
		score[r.Result.SymbolName] = r.StructuralScore
	}
	if !(score["Hub"] > score["Inflated"]) {
		t.Fatalf("inflated hub must be down-weighted: Hub=%f Inflated=%f",
			score["Hub"], score["Inflated"])
	}
}

// TestAnnotateGatesEmptySymbol asserts F3's gate: annotating with an empty
// symbol is refused (returns ErrCodemapUnavailable) so we never write a durable
// garbage pin on an unresolved/regex-empty symbol name.
func TestAnnotateGatesEmptySymbol(t *testing.T) {
	fixture := fixturePath(t, "related_files_c1.json") // contents irrelevant
	bin := fakeCodemap(t, fixture, 0)
	c := &CodemapClient{bin: bin}

	err := c.Annotate(context.Background(), t.TempDir(), "", "note", "vecgrep", nil)
	if !errors.Is(err, ErrCodemapUnavailable) {
		t.Fatalf("empty symbol should be gated, got %v", err)
	}
}
