package db

import (
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/veclite"
)

// hybridTestFixture inserts three chunks with hand-crafted embeddings so we
// can control vector similarity precisely:
//   - "body": a substantive function body that is a near-exact vector match
//     for the query embedding AND contains the query keywords.
//   - "import": a trivial 1-line import chunk that is a strong keyword match
//     (BM25 loves short documents) but semantically unrelated (orthogonal
//     embedding).
//   - "other": unrelated filler.
//
// Returns the DB and the query embedding.
func hybridTestFixture(t *testing.T) (*DB, []float32) {
	t.Helper()
	tmpDir := t.TempDir()
	dimensions := 8

	database, err := Open(tmpDir+"/test.db", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	queryEmbedding := []float32{1, 0, 0, 0, 0, 0, 0, 0}

	insert := func(relPath, content, chunkType, symbol string, embedding []float32) {
		t.Helper()
		chunk := NewChunkRecord(
			"/tmp/test/"+relPath, relPath, "hash-"+relPath, 100,
			"typescript", content,
			1, 1+len(content)/40, 0, len(content),
			chunkType, symbol, "/tmp/test",
		)
		if _, err := database.InsertChunk(chunk, embedding); err != nil {
			t.Fatalf("InsertChunk(%s) failed: %v", relPath, err)
		}
	}

	// Near-exact semantic match, substantive body, contains the keywords.
	insert("storage.ts",
		"export function loadCampaignPlan(): CampaignPlan | null {\n"+
			"  const raw = sessionStorage.getItem(STORAGE_KEY)\n"+
			"  if (!raw) return null\n"+
			"  const parsed = campaignPlanSchema.safeParse(JSON.parse(raw)) // zod schema validation\n"+
			"  return parsed.success ? parsed.data : null\n"+
			"}",
		"function", "loadCampaignPlan",
		[]float32{0.99, 0.1, 0, 0, 0, 0, 0, 0})

	// Trivial import-only chunk: top BM25 hit for "zod", orthogonal vector.
	insert("schemas.ts",
		`import { z } from "zod"`,
		"generic", "",
		[]float32{0, 1, 0, 0, 0, 0, 0, 0})

	// Unrelated filler.
	insert("math.ts",
		"export function add(a: number, b: number): number {\n  return a + b\n}",
		"function", "add",
		[]float32{0, 0, 1, 0, 0, 0, 0, 0})

	return database, queryEmbedding
}

// TestHybridSearchScoresAreDiscriminative is a regression test for the bug
// where hybrid search surfaced raw Reciprocal Rank Fusion scores (bounded by
// 1/(k+1) = ~0.016 with k=60) as if they were similarity scores, so every
// result — including near-exact matches — reported 0.01-0.02.
func TestHybridSearchScoresAreDiscriminative(t *testing.T) {
	database, queryEmbedding := hybridTestFixture(t)

	results, err := database.HybridSearch(queryEmbedding, "sessionStorage zod schema validation", 10, FilterOptions{}, 0.7)
	if err != nil {
		t.Fatalf("HybridSearch failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("HybridSearch returned no results")
	}

	top := results[0]
	if top.Chunk == nil || top.Chunk.RelativePath != "storage.ts" {
		t.Errorf("expected substantive near-exact match storage.ts on top, got %+v", top.Chunk)
	}

	// A chunk that is both the best vector match and a keyword match must
	// score meaningfully high — not an RRF constant of ~0.016.
	if top.Distance < 0.5 {
		t.Errorf("top hybrid score = %.4f, want >= 0.5 (raw RRF scores leaked to the user?)", top.Distance)
	}

	// Scores must stay a calibrated 0-1 range and be strictly ordered.
	for i, r := range results {
		if r.Distance < 0 || r.Distance > 1.0001 {
			t.Errorf("result %d score %.4f outside [0,1]", i, r.Distance)
		}
		if i > 0 && r.Distance > results[i-1].Distance {
			t.Errorf("results not sorted by score: [%d]=%.4f > [%d]=%.4f", i, r.Distance, i-1, results[i-1].Distance)
		}
	}

	// Scores must discriminate: the near-exact match should clearly beat the
	// unrelated chunks rather than everything collapsing into one tiny band.
	last := results[len(results)-1]
	if top.Distance-last.Distance < 0.1 {
		t.Errorf("scores are not discriminative: top=%.4f last=%.4f", top.Distance, last.Distance)
	}
}

// TestHybridSearchPrefersSubstantiveChunks is a regression test for trivial
// import-only chunks outranking substantive code bodies. BM25 length
// normalization makes a 1-line `import { z } from "zod"` chunk an unbeatable
// keyword match for any query mentioning "zod"; rank-only RRF fusion then let
// it beat the actual schema/storage helper bodies.
func TestHybridSearchPrefersSubstantiveChunks(t *testing.T) {
	database, queryEmbedding := hybridTestFixture(t)

	// Keyword that only the import chunk and the substantive body share.
	results, err := database.HybridSearch(queryEmbedding, "zod", 10, FilterOptions{}, 0.7)
	if err != nil {
		t.Fatalf("HybridSearch failed: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	var bodyScore, importScore float32
	var bodyFound, importFound bool
	for _, r := range results {
		if r.Chunk == nil {
			continue
		}
		switch r.Chunk.RelativePath {
		case "storage.ts":
			bodyScore, bodyFound = r.Distance, true
		case "schemas.ts":
			importScore, importFound = r.Distance, true
		}
	}
	if !bodyFound {
		t.Fatal("substantive body chunk missing from results")
	}
	if importFound && importScore >= bodyScore {
		t.Errorf("trivial import chunk (%.4f) outranks substantive body (%.4f)", importScore, bodyScore)
	}
	if results[0].Chunk == nil || results[0].Chunk.RelativePath != "storage.ts" {
		t.Errorf("expected storage.ts first, got %v", results[0].Chunk)
	}
}

// TestFuseWeightedScores unit-tests the fusion math directly.
func TestFuseWeightedScores(t *testing.T) {
	longBody := strings.Repeat("x", 2*substantiveChunkChars)
	rec := func(id uint64, content string) *veclite.Record {
		return &veclite.Record{ID: id, Payload: map[string]any{"content": content}}
	}

	body := rec(1, longBody)     // substantive
	imp := rec(2, "import zod")  // trivial
	vecOnly := rec(3, longBody)  // vector-only match
	textOnly := rec(4, longBody) // keyword-only match

	vectorResults := []veclite.Result{
		{Record: body, Score: 0.95},
		{Record: vecOnly, Score: 0.80},
		{Record: imp, Score: 0.10},
	}
	// BM25 raw scores: trivial import chunk tops the keyword list.
	textResults := []veclite.Result{
		{Record: imp, Score: 8.0},
		{Record: body, Score: 6.0},
		{Record: textOnly, Score: 4.0},
	}

	fused := fuseWeightedScores(vectorResults, textResults, 0.7, 0.3)
	if len(fused) != 4 {
		t.Fatalf("expected 4 fused results, got %d", len(fused))
	}

	scores := make(map[uint64]float64, len(fused))
	for _, r := range fused {
		scores[r.Record.ID] = float64(r.Score)
	}

	// body: 0.7*0.95 + 0.3*(6/8)*1.0 = 0.665 + 0.225 = 0.89
	if got := scores[1]; got < 0.88 || got > 0.90 {
		t.Errorf("body fused score = %.4f, want ~0.89", got)
	}
	// imp: 0.7*0.10 + 0.3*1.0*substance("import zod") — substance ramps from
	// 0.3: 0.3 + 0.7*10/200 = 0.335 → 0.07 + 0.1005 = ~0.1705
	if got := scores[2]; got > 0.25 {
		t.Errorf("trivial import chunk fused score = %.4f, want < 0.25 (substance down-weight missing?)", got)
	}
	// vector-only: 0.7*0.80 = 0.56
	if got := scores[3]; got < 0.55 || got > 0.57 {
		t.Errorf("vector-only fused score = %.4f, want ~0.56", got)
	}
	// keyword-only: 0.3*(4/8) = 0.15
	if got := scores[4]; got < 0.14 || got > 0.16 {
		t.Errorf("keyword-only fused score = %.4f, want ~0.15", got)
	}

	// The substantive chunk that ranked #2 on BM25 must beat the trivial
	// import chunk that ranked #1 on BM25 — the exact mis-ranking RRF caused.
	if scores[1] <= scores[2] {
		t.Errorf("substantive body (%.4f) must outrank trivial import chunk (%.4f)", scores[1], scores[2])
	}

	// Ordering: descending scores.
	for i := 1; i < len(fused); i++ {
		if fused[i].Score > fused[i-1].Score {
			t.Errorf("fused results not sorted: [%d]=%.4f > [%d]=%.4f", i, fused[i].Score, i-1, fused[i-1].Score)
		}
	}
}

// TestChunkSubstanceFactor covers the tiny-chunk down-weighting heuristic.
func TestChunkSubstanceFactor(t *testing.T) {
	cases := []struct {
		name    string
		content string
		min     float64
		max     float64
	}{
		{"nil record content", "", minSubstanceFactor, minSubstanceFactor},
		{"one-line import", `import { z } from "zod"`, minSubstanceFactor, 0.5},
		{"substantive body", strings.Repeat("code ", 100), 1.0, 1.0},
		{"whitespace only", "   \n\t  ", minSubstanceFactor, minSubstanceFactor},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &veclite.Record{ID: 1, Payload: map[string]any{"content": tc.content}}
			got := chunkSubstanceFactor(rec)
			if got < tc.min-1e-9 || got > tc.max+1e-9 {
				t.Errorf("chunkSubstanceFactor(%q) = %.4f, want in [%.2f, %.2f]", tc.content, got, tc.min, tc.max)
			}
		})
	}
	if got := chunkSubstanceFactor(nil); got != minSubstanceFactor {
		t.Errorf("chunkSubstanceFactor(nil) = %.4f, want %.4f", got, minSubstanceFactor)
	}
}

// TestHybridSearchVectorOnlyAndKeywordOnlyContributions verifies fusion
// weight semantics on the calibrated scale: a vector-only match is capped by
// the vector weight, a keyword-only match is capped by the text weight.
func TestHybridSearchVectorOnlyAndKeywordOnlyContributions(t *testing.T) {
	database, queryEmbedding := hybridTestFixture(t)

	// "add" appears only in math.ts, which is vector-orthogonal to the query.
	results, err := database.HybridSearch(queryEmbedding, "add numbers", 10, FilterOptions{}, 0.7)
	if err != nil {
		t.Fatalf("HybridSearch failed: %v", err)
	}

	for _, r := range results {
		if r.Chunk == nil {
			continue
		}
		if r.Chunk.RelativePath == "math.ts" {
			// Keyword-only match: score must not exceed the text weight.
			if r.Distance > 0.3001 {
				t.Errorf("keyword-only match scored %.4f, want <= text weight 0.3", r.Distance)
			}
		}
		if r.Chunk.RelativePath == "storage.ts" {
			// Vector-only match here (no "add" in the body): capped by vector weight.
			if r.Distance > 0.7001 {
				t.Errorf("vector-only match scored %.4f, want <= vector weight 0.7", r.Distance)
			}
		}
	}
}
