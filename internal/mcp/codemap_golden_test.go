package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

// TestImpactParsesV017Contract pins codemap v0.17.0's `impact --json` shape:
// `found` (not `indexed`); the affected-file set derived from `locations` plus
// the transitive `blast_radius` array (there is no flat `files` array); and
// `blast_radius` as an array (not an int — the old `int` tag failed to
// unmarshal). The old parser silently fell back on all of these and there was
// no golden test for impact, so vecgrep_investigate always searched unscoped.
func TestImpactParsesV017Contract(t *testing.T) {
	fixture := fixturePath(t, "impact_c.json")
	bin := fakeCodemap(t, fixture, 0)
	c := &CodemapClient{bin: bin}

	res, err := c.Impact(context.Background(), t.TempDir(), "ValidateToken", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil || !res.Indexed {
		t.Fatalf("fixture is found:true; result.Indexed should be true, got %+v", res)
	}
	// definition (auth.go) + transitive blast radius (login.go, handler.go),
	// deduplicated and definition-first; direct_callers are a subset of
	// blast_radius so login.go is not double-counted.
	wantFiles := []string{"app/auth.go", "app/login.go", "api/handler.go"}
	if len(res.Files) != len(wantFiles) {
		t.Fatalf("expected %d files, got %d: %+v", len(wantFiles), len(res.Files), res.Files)
	}
	for i, w := range wantFiles {
		if res.Files[i].RelativePath != w {
			t.Errorf("file[%d] = %q, want %q", i, res.Files[i].RelativePath, w)
		}
	}
	if res.BlastRadius != 2 {
		t.Errorf("BlastRadius = %d, want 2 (len of blast_radius array)", res.BlastRadius)
	}
	if len(res.Tests) != 1 || res.Tests[0] != "app/auth_test.go" {
		t.Errorf("Tests = %v, want [app/auth_test.go]", res.Tests)
	}
}

// TestImpactNotFound asserts the not-found state (codemap ran, symbol/graph not
// resolvable) is a real result with Indexed==false and no error — so the caller
// falls back to an unscoped search cleanly rather than treating it as a crash.
func TestImpactNotFound(t *testing.T) {
	fixture := fixturePath(t, "impact_not_found.json")
	bin := fakeCodemap(t, fixture, 0)
	c := &CodemapClient{bin: bin}

	res, err := c.Impact(context.Background(), t.TempDir(), "Nope", 0)
	if err != nil {
		t.Fatalf("not-found must not be an error, got: %v", err)
	}
	if res == nil || res.Indexed {
		t.Fatalf("expected non-nil result with Indexed=false, got %+v", res)
	}
	if len(res.Files) != 0 {
		t.Fatalf("expected no files when not found, got %d", len(res.Files))
	}
}

// TestImpactAgainstRealCodemap is a live smoke test: when the real codemap
// binary is installed and this repo is codemap-indexed, it proves Impact parses
// the ACTUAL installed codemap's output, not just a fixture. It skips when
// codemap is unavailable or the project isn't indexed, so CI without codemap
// still passes.
func TestImpactAgainstRealCodemap(t *testing.T) {
	if _, err := exec.LookPath("codemap"); err != nil {
		t.Skip("codemap not on PATH")
	}
	root := repoRoot(t)
	c := &CodemapClient{bin: "codemap"}

	res, err := c.Impact(context.Background(), root, "OpenSession", 0)
	if err != nil {
		t.Skipf("codemap impact failed (project may not be indexed): %v", err)
	}
	if !res.Indexed {
		t.Skip("codemap project not indexed; run codemap_index first")
	}
	if len(res.Files) == 0 {
		t.Fatalf("real codemap impact returned indexed=true but zero files — parser likely drifted again: %+v", res)
	}
	if res.BlastRadius == 0 {
		t.Errorf("expected a non-zero blast radius for a widely-called symbol")
	}
	t.Logf("live: OpenSession blast radius = %d files, radius %d", len(res.Files), res.BlastRadius)
}

// repoRoot walks up from this test file to the module root (where go.mod is).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from test file")
		}
		dir = parent
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

// TestSymbolAtParsesC2Contract is the F3/F4 golden: a resolved file:line maps
// to the enclosing symbol with resolution "exact" and Resolved()==true.
func TestSymbolAtParsesC2Contract(t *testing.T) {
	fixture := fixturePath(t, "symbol_at_exact.json")
	bin := fakeCodemap(t, fixture, 0)
	c := &CodemapClient{bin: bin}

	sa, err := c.SymbolAt(context.Background(), t.TempDir(), "app/auth.go", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sa == nil || !sa.Resolved() {
		t.Fatalf("expected a resolved symbol, got %+v", sa)
	}
	if sa.Symbol != "ValidateToken" || sa.FQN != "app.ValidateToken" || sa.Resolution != "exact" {
		t.Errorf("symbol-at = %+v, want ValidateToken/app.ValidateToken/exact", sa)
	}
	if sa.StartLine != 40 || sa.EndLine != 55 {
		t.Errorf("line range = %d-%d, want 40-55", sa.StartLine, sa.EndLine)
	}
}

// TestSymbolAtNoneIsNotResolved asserts a "none" resolution (no symbol at the
// position, or project not indexed) is parsed but reports Resolved()==false so
// the caller skips annotation rather than guessing.
func TestSymbolAtNoneIsNotResolved(t *testing.T) {
	fixture := fixturePath(t, "symbol_at_none.json")
	bin := fakeCodemap(t, fixture, 0)
	c := &CodemapClient{bin: bin}

	sa, err := c.SymbolAt(context.Background(), t.TempDir(), "app/auth.go", 999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sa == nil {
		t.Fatal("expected a parsed (unresolved) result, not nil")
	}
	if sa.Resolved() {
		t.Fatalf("resolution=none must report Resolved()==false, got %+v", sa)
	}
}

// TestSymbolAtUnavailableReturnsErr asserts a nil client yields the typed
// unavailable error so callers don't pin a guess.
func TestSymbolAtUnavailableReturnsErr(t *testing.T) {
	var c *CodemapClient
	sa, err := c.SymbolAt(context.Background(), "/tmp", "app/auth.go", 1)
	if !errors.Is(err, ErrCodemapUnavailable) {
		t.Fatalf("expected ErrCodemapUnavailable, got %v", err)
	}
	if sa.Resolved() {
		t.Fatal("nil result must not be Resolved()")
	}
}

// TestSymbolAtNonZeroExitIsMiss asserts a non-zero exit is treated as a miss
// (nil, nil) — never a guess.
func TestSymbolAtNonZeroExitIsMiss(t *testing.T) {
	bin := fakeCodemap(t, "", 2)
	c := &CodemapClient{bin: bin}
	sa, err := c.SymbolAt(context.Background(), t.TempDir(), "app/auth.go", 1)
	if err != nil {
		t.Fatalf("non-zero exit should be a silent miss, got err %v", err)
	}
	if sa.Resolved() {
		t.Fatalf("non-zero exit must not yield a resolved symbol, got %+v", sa)
	}
}

// TestStatusParsesStaleObject asserts G4's parse fix: codemap's `status --json`
// emits `registered` (no `indexed` field) and `stale` as an OBJECT, not an int.
// The previous struct parsed `indexed`/`stale int` codemap never emits.
func TestStatusParsesStaleObject(t *testing.T) {
	fixture := fixturePath(t, "status_stale.json")
	bin := fakeCodemap(t, fixture, 0)
	c := &CodemapClient{bin: bin}

	st, err := c.Status(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st == nil || !st.Indexed() {
		t.Fatalf("registered with nodes>0 should be Indexed(), got %+v", st)
	}
	if st.Nodes != 1280 || st.Edges != 3410 {
		t.Errorf("nodes/edges = %d/%d, want 1280/3410", st.Nodes, st.Edges)
	}
	if !st.Stale.Any() {
		t.Fatal("stale object {3,1,2} should report Any()==true")
	}
	if st.Stale.Changed != 3 || st.Stale.New != 1 || st.Stale.Deleted != 2 {
		t.Errorf("stale = %+v, want 3/1/2", st.Stale)
	}
}

// TestStatusFreshHasNoStaleness asserts a null `stale` parses to no drift.
func TestStatusFreshHasNoStaleness(t *testing.T) {
	fixture := fixturePath(t, "status_fresh.json")
	bin := fakeCodemap(t, fixture, 0)
	c := &CodemapClient{bin: bin}

	st, err := c.Status(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !st.Indexed() {
		t.Fatalf("fresh indexed status should be Indexed(), got %+v", st)
	}
	if st.Stale.Any() {
		t.Fatalf("null stale should report Any()==false, got %+v", st.Stale)
	}
}

// TestStatusNotIndexed asserts registered:false / nodes:0 is not Indexed().
func TestStatusNotIndexed(t *testing.T) {
	fixture := fixturePath(t, "status_not_indexed.json")
	bin := fakeCodemap(t, fixture, 0)
	c := &CodemapClient{bin: bin}

	st, err := c.Status(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.Indexed() {
		t.Fatalf("unregistered project must not be Indexed(), got %+v", st)
	}
}
