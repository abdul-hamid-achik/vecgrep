package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
)

// CodemapClient wraps communication with the codemap CLI for graph-based
// queries. It shells out to the codemap binary (resolved from cfg.Codemap.Bin
// or $PATH) and parses JSON output. All methods are best-effort: if codemap
// is not installed, not indexed, or returns an error, the caller should fall
// back to vecgrep's built-in heuristics.
type CodemapClient struct {
	bin string
}

// NewCodemapClient creates a client from the codemap config. If codemap is
// not enabled or the binary cannot be found, the returned client is nil-safe
// (all methods return zero values and Available() returns false).
func NewCodemapClient(cfg config.CodemapConfig) *CodemapClient {
	if !cfg.Enabled {
		return nil
	}
	bin := cfg.Bin
	if bin == "" {
		bin = "codemap"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return nil
	}
	return &CodemapClient{bin: bin}
}

// Available reports whether the codemap binary is usable.
func (c *CodemapClient) Available() bool {
	return c != nil && c.bin != ""
}

// ImpactResult holds the codemap_impact output for a symbol.
type ImpactResult struct {
	Symbol      string   `json:"symbol"`
	Definition  string   `json:"definition"`
	Callers     []string `json:"callers"`
	Callees     []string `json:"callees"`
	Tests       []string `json:"tests"`
	BlastRadius int      `json:"blast_radius"`
}

// Impact calls `codemap impact --json <symbol>` to get the call graph for a
// symbol. Returns nil and no error if codemap is unavailable or the symbol
// is not found.
func (c *CodemapClient) Impact(ctx context.Context, projectPath, symbol string) (*ImpactResult, error) {
	if !c.Available() {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, c.bin, "impact", "--json", symbol)
	cmd.Dir = projectPath
	out, err := cmd.Output()
	if err != nil {
		return nil, nil // best-effort
	}
	var result ImpactResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, nil
	}
	return &result, nil
}

// HotspotResult holds a single hotspot entry from codemap_hotspots.
type HotspotResult struct {
	Symbol string `json:"symbol"`
	Refs   int    `json:"refs"`
}

// Hotspots calls `codemap hotspots --json` to get the most-referenced symbols.
func (c *CodemapClient) Hotspots(ctx context.Context, projectPath string, top int) ([]HotspotResult, error) {
	if !c.Available() {
		return nil, nil
	}
	args := []string{"hotspots", "--json"}
	if top > 0 {
		args = append(args, "--top", fmt.Sprintf("%d", top))
	}
	cmd := exec.CommandContext(ctx, c.bin, args...)
	cmd.Dir = projectPath
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	var results []HotspotResult
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, nil
	}
	return results, nil
}

// FindResult holds a symbol match from codemap_find.
type FindResult struct {
	Symbol string `json:"symbol"`
	File   string `json:"file"`
	Line   int    `json:"line"`
}

// FindSymbol calls `codemap find --json <query>` to locate a symbol by name.
func (c *CodemapClient) FindSymbol(ctx context.Context, projectPath, query string) ([]FindResult, error) {
	if !c.Available() {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, c.bin, "find", "--json", query)
	cmd.Dir = projectPath
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	var results []FindResult
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, nil
	}
	return results, nil
}

// SymbolsResult holds symbols defined in a file from codemap_symbols.
type SymbolsResult struct {
	Symbol string `json:"symbol"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Kind   string `json:"kind"`
}

// Symbols calls `codemap symbols --json <file>` to list symbols in a file.
func (c *CodemapClient) Symbols(ctx context.Context, projectPath, file string) ([]SymbolsResult, error) {
	if !c.Available() {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, c.bin, "symbols", "--json", file)
	cmd.Dir = projectPath
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	var results []SymbolsResult
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, nil
	}
	return results, nil
}

// StatusResult holds the codemap status output.
type StatusResult struct {
	Indexed bool   `json:"indexed"`
	Nodes   int    `json:"nodes"`
	Edges   int    `json:"edges"`
	Project string `json:"project"`
	Stale   int    `json:"stale"`
}

// Status calls `codemap status --json` to check if the project is indexed.
func (c *CodemapClient) Status(ctx context.Context, projectPath string) (*StatusResult, error) {
	if !c.Available() {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, c.bin, "status", "--json")
	cmd.Dir = projectPath
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	var result StatusResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, nil
	}
	return &result, nil
}

// Annotate pins a note on a codemap symbol via `codemap annotate`.
func (c *CodemapClient) Annotate(ctx context.Context, projectPath, symbol, note, source string, data any) error {
	if !c.Available() {
		return nil
	}
	args := []string{"annotate", "--symbol", symbol, "--note", note, "--source", source}
	if data != nil {
		dataJSON, err := json.Marshal(data)
		if err == nil {
			args = append(args, "--data", string(dataJSON))
		}
	}
	cmd := exec.CommandContext(ctx, c.bin, args...)
	cmd.Dir = projectPath
	_, _ = cmd.Output() // best-effort
	return nil
}

// Callers calls `codemap callers --json <symbol>` to find who calls a symbol.
func (c *CodemapClient) Callers(ctx context.Context, projectPath, symbol string) ([]string, error) {
	if !c.Available() {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, c.bin, "callers", "--json", symbol)
	cmd.Dir = projectPath
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	var results []struct {
		Symbol string `json:"symbol"`
		File   string `json:"file"`
		Line   int    `json:"line"`
	}
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, nil
	}
	callers := make([]string, 0, len(results))
	for _, r := range results {
		callers = append(callers, r.File)
	}
	return callers, nil
}

// RelatedFile represents a file related to a target, with a reason and
// confidence score derived from codemap's call graph.
type RelatedFile struct {
	RelativePath string
	Reason       string
	Confidence   float32
}

// RelatedFiles aggregates codemap graph data into a list of related files.
// It resolves symbols in the target file, then fetches callers, callees,
// tests, and blast radius for each. Files are aggregated and ranked by
// the number of graph edges connecting them to the target.
func (c *CodemapClient) RelatedFiles(ctx context.Context, projectPath, relPath string, limit int) ([]RelatedFile, error) {
	if !c.Available() {
		return nil, nil
	}

	// Get symbols defined in the target file
	symbols, err := c.Symbols(ctx, projectPath, relPath)
	if err != nil || len(symbols) == 0 {
		return nil, nil
	}

	// Aggregate related files by edge count
	fileScores := make(map[string]int)
	fileReasons := make(map[string][]string)

	for _, sym := range symbols {
		ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
		impact, err := c.Impact(ctx2, projectPath, sym.Symbol)
		cancel()
		if err != nil || impact == nil {
			continue
		}

		for _, caller := range impact.Callers {
			if caller == "" || caller == relPath {
				continue
			}
			fileScores[caller]++
			fileReasons[caller] = append(fileReasons[caller], "caller of "+sym.Symbol)
		}
		for _, callee := range impact.Callees {
			if callee == "" || callee == relPath {
				continue
			}
			fileScores[callee]++
			fileReasons[callee] = append(fileReasons[callee], "called by "+sym.Symbol)
		}
		for _, test := range impact.Tests {
			if test == "" || test == relPath {
				continue
			}
			fileScores[test]++
			fileReasons[test] = append(fileReasons[test], "tests "+sym.Symbol)
		}
	}

	// Build and sort results
	results := make([]RelatedFile, 0, len(fileScores))
	maxScore := 1
	for file, score := range fileScores {
		if score > maxScore {
			maxScore = score
		}
		results = append(results, RelatedFile{
			RelativePath: file,
			Reason:       strings.Join(fileReasons[file], "; "),
			Confidence:   float32(score),
		})
	}

	// Normalize confidence to 0..1
	for i := range results {
		results[i].Confidence = results[i].Confidence / float32(maxScore)
	}

	// Sort by confidence descending
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Confidence > results[i].Confidence {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

// CodemapRerankResult holds a search result with structural metadata from codemap.
type CodemapRerankResult struct {
	Result          codemapSearchResult
	StructuralScore float32 // normalized 0..1 from codemap hotspot/impact
	FinalScore      float32 // blended final score
}

// codemapSearchResult is a lightweight alias for the fields we need from
// search.Result to avoid an import cycle (search package could import mcp).
type codemapSearchResult struct {
	RelativePath string
	SymbolName   string
	StartLine    int
	Score        float32
}

// Rerank re-orders search results by blending the original vecgrep score
// with codemap's structural importance (fan-in hub score + blast radius).
// structuralWeight is 0..1; 0 means no re-ranking (results returned as-is).
func (c *CodemapClient) Rerank(ctx context.Context, projectPath string, results []CodemapRerankResult, structuralWeight float32) []CodemapRerankResult {
	if !c.Available() || structuralWeight <= 0 || len(results) == 0 {
		return results
	}

	// Fetch hotspot scores (fan-in)
	hotspots, _ := c.Hotspots(ctx, projectPath, 200)
	hubScore := make(map[string]int, len(hotspots))
	maxHub := 1
	for _, h := range hotspots {
		hubScore[h.Symbol] = h.Refs
		if h.Refs > maxHub {
			maxHub = h.Refs
		}
	}

	semWeight := 1.0 - structuralWeight

	for i := range results {
		// Lookup the symbol's hub score; default to 0 if not found
		hs := 0
		if sym := results[i].Result.SymbolName; sym != "" {
			hs = hubScore[sym]
		}
		// Normalize hub score to 0..1
		normalizedHub := float32(hs) / float32(maxHub)
		results[i].StructuralScore = normalizedHub

		// Blend: final = sem * (1-w) + struct * w
		results[i].FinalScore = results[i].Result.Score*semWeight + normalizedHub*structuralWeight
	}

	// Sort by final score descending
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].FinalScore > results[i].FinalScore {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	return results
}
