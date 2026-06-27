package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
)

// ErrCodemapUnavailable is returned when the codemap client is nil or its
// binary is not usable. Callers should fall back to vecgrep's own heuristics.
var ErrCodemapUnavailable = errors.New("codemap unavailable")

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

// HotspotResult holds a single hotspot entry from `codemap hotspots --json`.
//
// codemap emits a HotspotRef per symbol with `in_degree` (the fan-in / call
// reference count that drives the hub score) and `shared_name` (how many
// distinct definitions share this bare name; >1 means the in-degree is
// inflated by name-based collisions and should be down-weighted). The older
// vecgrep struct parsed a `refs` field codemap never emits, so the hub score
// was uniformly 0 and the structural rerank blend was inert.
type HotspotResult struct {
	Symbol     string `json:"symbol"`
	InDegree   int    `json:"in_degree"`
	SharedName int    `json:"shared_name"`
}

// hotspotsReport mirrors codemap's HotspotsReport: the `hotspots --json`
// output is an OBJECT `{"project":…,"hotspots":[…]}`, not a bare array, so we
// must unmarshal the wrapper before reaching the entries.
type hotspotsReport struct {
	Project  string          `json:"project"`
	Hotspots []HotspotResult `json:"hotspots"`
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
	var report hotspotsReport
	if err := json.Unmarshal(out, &report); err != nil {
		return nil, nil
	}
	return report.Hotspots, nil
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

// CodemapStaleness mirrors codemap's index.Staleness — how far the graph has
// drifted from the working tree. codemap emits `stale` as an OBJECT (or null
// when fresh / not computed), NOT an int.
type CodemapStaleness struct {
	Changed int `json:"changed"`
	New     int `json:"new"`
	Deleted int `json:"deleted"`
}

// Any reports whether the index has drifted at all.
func (s *CodemapStaleness) Any() bool {
	return s != nil && (s.Changed > 0 || s.New > 0 || s.Deleted > 0)
}

// StatusResult holds the codemap `status --json` output (C-status). codemap
// reports `registered` (project known to codemap) — there is NO `indexed`
// field; a built graph is implied by Nodes>0. `stale` is an object that is
// absent/null when the index is fresh.
type StatusResult struct {
	Project    string            `json:"project"`
	Registered bool              `json:"registered"`
	Nodes      int               `json:"nodes"`
	Edges      int               `json:"edges"`
	Files      int               `json:"files"`
	Vectors    int               `json:"vectors"`
	Stale      *CodemapStaleness `json:"stale"`
}

// Indexed reports whether codemap has a usable graph for the project (it is
// registered and has at least one node). This replaces the non-existent
// `indexed` JSON field the previous struct tried to parse.
func (r *StatusResult) Indexed() bool {
	return r != nil && r.Registered && r.Nodes > 0
}

// Status calls `codemap status --json` to read the peer graph's state.
// Returns nil (no error) when codemap is unavailable or the call fails — the
// caller degrades silently.
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

// SymbolAtResult maps codemap's C2 contract from
// `codemap symbol-at <file>:<line> --json`:
//
//	{ "file":"x.go","line":42,"symbol":"Foo","fqn":"pkg.Foo","kind":"function",
//	  "start_line":40,"end_line":55,"resolution":"exact|enclosing|none" }
//
// Resolution is "exact" (line is the definition line), "enclosing" (line falls
// inside the symbol's body), or "none" (no symbol there / project not indexed).
type SymbolAtResult struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Symbol     string `json:"symbol"`
	FQN        string `json:"fqn"`
	Kind       string `json:"kind"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	Resolution string `json:"resolution"`
}

// Resolved reports whether codemap actually placed the position on a symbol
// (resolution exact or enclosing, with a non-empty symbol name). A "none"
// resolution — or an empty symbol — means "do not pin here."
func (r *SymbolAtResult) Resolved() bool {
	return r != nil && r.Resolution != "" && r.Resolution != "none" && r.Symbol != ""
}

// SymbolAt resolves a file:line position to its enclosing symbol via
// `codemap symbol-at <file>:<line> --json` (C2). It is the join key that lets a
// vecgrep hit's file:start_line attach to the right graph node instead of a
// regex-extracted name that may be empty or collide.
//
// Returns:
//   - ErrCodemapUnavailable when the client is nil / binary missing.
//   - (nil, nil) on a non-zero exit or unparseable output — a miss, never a
//     guess; codemap also returns resolution="none" when the project is not
//     indexed, so an unindexed peer simply yields an unresolved result.
//   - (*SymbolAtResult, nil) otherwise; check Resolved() before trusting it.
func (c *CodemapClient) SymbolAt(ctx context.Context, projectPath, file string, line int) (*SymbolAtResult, error) {
	if !c.Available() {
		return nil, ErrCodemapUnavailable
	}
	pos := fmt.Sprintf("%s:%d", file, line)
	cmd := exec.CommandContext(ctx, c.bin, "symbol-at", pos, "--json")
	cmd.Dir = projectPath
	out, err := cmd.Output()
	if err != nil {
		// Best-effort: a non-zero exit is treated as "no symbol", never a guess.
		return nil, nil
	}
	var result SymbolAtResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, nil
	}
	return &result, nil
}

// Annotate pins a note on a codemap symbol via `codemap annotate`.
//
// codemap takes the symbol POSITIONALLY (`codemap annotate <symbol> --source
// … --note … --data …`); there is NO `--symbol` flag, so the previous
// invocation was rejected by cobra and the annotate was a silent no-op.
//
// The symbol MUST be non-empty: this is a durable, reindex-proof store, and a
// regex-extracted symbol name can be "" (or collide on a bare name like
// `Foo`), which would write a garbage pin. We therefore refuse to annotate
// without a symbol and return ErrCodemapUnavailable for a missing one so the
// caller can skip rather than corrupt the store. Callers should prefer a
// symbol resolved via SymbolAt (file:line → enclosing node) over a
// regex-extracted name.
func (c *CodemapClient) Annotate(ctx context.Context, projectPath, symbol, note, source string, data any) error {
	if !c.Available() {
		return nil
	}
	if symbol == "" {
		// Never write a pin on an empty/unresolved symbol — it would be a
		// durable garbage annotation. Caller should skip silently.
		return ErrCodemapUnavailable
	}
	// Symbol is positional (args[0]); flags follow.
	args := []string{"annotate", symbol, "--note", note, "--source", source}
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
// confidence score derived from codemap's call graph. It maps one entry of
// the C1 `related[]` array.
type RelatedFile struct {
	RelativePath string
	// Reason is one of codemap's edge kinds: caller|callee|test|import.
	Reason     string
	Confidence float32
}

// relatedFilesEnvelope mirrors codemap's C1 contract emitted by
// `codemap related-files <file> --json`:
//
//	{ "project":"demo", "file":"app/auth.go", "indexed":true,
//	  "related":[ {"relative_path":"app/login.go","reason":"caller","confidence":0.9} ] }
//
// `reason` ∈ caller|callee|test|import.
type relatedFilesEnvelope struct {
	Project string `json:"project"`
	File    string `json:"file"`
	Indexed bool   `json:"indexed"`
	Related []struct {
		RelativePath string  `json:"relative_path"`
		Reason       string  `json:"reason"`
		Confidence   float32 `json:"confidence"`
	} `json:"related"`
}

// RelatedFilesResult is the typed outcome of a related-files query. It lets
// callers distinguish codemap's three peer states without collapsing them to
// a single nil:
//
//   - Indexed == false        → codemap ran but the project is not indexed;
//     the caller should fall back to vecgrep's own heuristics.
//   - a non-nil error from RelatedFiles → real failure (non-zero exit, bad
//     JSON); fall back AND log.
//   - Indexed == true with an empty Related slice → indexed, nothing related;
//     that is a VALID answer, not a fallback trigger.
type RelatedFilesResult struct {
	Indexed bool
	Files   []RelatedFile
}

// RelatedFiles runs a single `codemap related-files <file> --json` exec and
// parses the C1 contract. It replaces the old per-symbol fan-out (symbols →
// impact per symbol, N cold subprocess spawns) with one process.
//
// Return contract (the three typed peer states):
//   - ErrCodemapUnavailable: client is nil / binary missing → fall back.
//   - any other non-nil error: codemap exited non-zero or emitted unparseable
//     JSON → fall back and log.
//   - (*RelatedFilesResult, nil): a real answer. Check Indexed: false means
//     not-indexed (fall back); true with empty Files is a valid empty answer.
func (c *CodemapClient) RelatedFiles(ctx context.Context, projectPath, relPath string, limit int) (*RelatedFilesResult, error) {
	if !c.Available() {
		return nil, ErrCodemapUnavailable
	}

	cmd := exec.CommandContext(ctx, c.bin, "related-files", relPath, "--json")
	cmd.Dir = projectPath
	out, err := cmd.Output()
	if err != nil {
		// State (b): non-zero exit / spawn failure → real error.
		return nil, fmt.Errorf("codemap related-files %q: %w", relPath, err)
	}

	var env relatedFilesEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		// Malformed output is a real error (version skew), not "no results".
		return nil, fmt.Errorf("codemap related-files %q: parse: %w", relPath, err)
	}

	// State (a): codemap ran but the project is not indexed.
	if !env.Indexed {
		return &RelatedFilesResult{Indexed: false}, nil
	}

	// State (c): indexed; map related[] (possibly empty — a valid answer).
	files := make([]RelatedFile, 0, len(env.Related))
	for _, r := range env.Related {
		if r.RelativePath == "" || r.RelativePath == relPath {
			continue
		}
		files = append(files, RelatedFile{
			RelativePath: r.RelativePath,
			Reason:       r.Reason,
			Confidence:   r.Confidence,
		})
	}

	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}

	return &RelatedFilesResult{Indexed: true, Files: files}, nil
}

// ImpactFile represents a single file in a symbol's blast radius.
type ImpactFile struct {
	RelativePath string `json:"relative_path"`
	Reason       string `json:"reason"` // e.g. "caller", "callee", "test"
}

// ImpactResult holds the output of `codemap impact <symbol> --json`: the
// affected file set (blast radius) and the covering tests. This is the
// structural pre-filter that narrows vecgrep's semantic search scope.
//
// Return contract mirrors RelatedFiles:
//   - ErrCodemapUnavailable: client is nil / binary missing → fall back.
//   - any other non-nil error: codemap exited non-zero or emitted unparseable
//     JSON → fall back and log.
//   - (*ImpactResult, nil): a real answer. Check Indexed: false means
//     not-indexed (fall back); true with empty Files is a valid empty answer.
type ImpactResult struct {
	Indexed     bool         // whether codemap has a graph for this project
	Symbol      string       // the resolved symbol
	Files       []ImpactFile // affected files in the blast radius
	Tests       []string     // covering test files
	BlastRadius int          // total transitive symbols affected
}

// impactNode is one symbol entry in codemap's impact output. The locations,
// direct_callers, blast_radius, and tests lists all share this shape.
type impactNode struct {
	Symbol string `json:"symbol"`
	FQN    string `json:"fqn"`
	Kind   string `json:"kind"`
	File   string `json:"file"`
}

// impactEnvelope mirrors codemap v0.17.0's `impact --json` output:
//
//	{ "found":true, "symbol":"OpenSession", "project":"vecgrep",
//	  "locations":[{"file":"internal/app/session.go",...}],
//	  "direct_callers":[{"file":"cmd/vecgrep/main.go",...}],
//	  "blast_radius":[{"file":"...","depth":1,...}],   // transitive callers
//	  "tests":[{"file":"..._test.go",...}], "untested":false }
//
// Older codemap emitted `indexed`, a flat `files` array, and an int
// `blast_radius`. The affected-file set now has to be derived from the symbol's
// own location(s) plus the transitive blast-radius nodes, and "found" replaced
// "indexed". (`blast_radius` is now an array, so the old `int` tag would even
// fail to unmarshal — the parse errored out entirely against current codemap.)
type impactEnvelope struct {
	Project       string       `json:"project"`
	Symbol        string       `json:"symbol"`
	Found         bool         `json:"found"`
	Locations     []impactNode `json:"locations"`
	DirectCallers []impactNode `json:"direct_callers"`
	BlastRadius   []impactNode `json:"blast_radius"`
	Tests         []impactNode `json:"tests"`
}

// Impact runs `codemap impact <symbol> --json` to compute the blast radius
// of a changed symbol: the set of files transitively affected by a change to
// that symbol, plus the tests that cover those paths. The returned file set
// is used to scope vecgrep's semantic search so only files within the blast
// radius are ranked.
//
// depth controls the transitive traversal depth (0 = use codemap's default,
// typically 3). When depth > 0, `--depth N` is passed.
//
// Degrades silently: when codemap is unavailable, returns ErrCodemapUnavailable
// so the caller can fall back to unscoped search.
func (c *CodemapClient) Impact(ctx context.Context, projectPath, symbol string, depth int) (*ImpactResult, error) {
	if !c.Available() {
		return nil, ErrCodemapUnavailable
	}

	args := []string{"impact", symbol, "--json"}
	if depth > 0 {
		args = append(args, "--depth", fmt.Sprintf("%d", depth))
	}
	cmd := exec.CommandContext(ctx, c.bin, args...)
	cmd.Dir = projectPath
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("codemap impact %q: %w", symbol, err)
	}

	var env impactEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("codemap impact %q: parse: %w", symbol, err)
	}

	if !env.Found {
		return &ImpactResult{Indexed: false, Symbol: env.Symbol}, nil
	}

	// The affected-file set used to scope the search is the symbol's own
	// definition site(s) plus every file in the transitive blast radius
	// (callers). Deduplicate, preserving first-seen order so the definition
	// leads. direct_callers are a depth-1 subset of blast_radius, so iterating
	// blast_radius already covers them.
	seen := make(map[string]bool)
	var files []ImpactFile
	addFile := func(file, reason string) {
		if file == "" || seen[file] {
			return
		}
		seen[file] = true
		files = append(files, ImpactFile{RelativePath: file, Reason: reason})
	}
	for _, n := range env.Locations {
		addFile(n.File, "definition")
	}
	for _, n := range env.BlastRadius {
		addFile(n.File, "caller")
	}

	var tests []string
	testSeen := make(map[string]bool)
	for _, n := range env.Tests {
		if n.File != "" && !testSeen[n.File] {
			testSeen[n.File] = true
			tests = append(tests, n.File)
		}
	}

	return &ImpactResult{
		Indexed:     true,
		Symbol:      env.Symbol,
		Files:       files,
		Tests:       tests,
		BlastRadius: len(env.BlastRadius),
	}, nil
}

// CodemapRerankResult holds a search result with structural metadata from codemap.
type CodemapRerankResult struct {
	Result          codemapSearchResult
	StructuralScore float32 // normalized 0..1 from codemap's fan-in hub score
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

// Rerank re-orders search results by blending the original vecgrep score with
// codemap's structural importance. The structural signal is the symbol's
// fan-in hub score (codemap's in_degree), down-weighted when shared_name>1 so
// a name-inflated hub does not outrank a genuinely-referenced one. We
// deliberately do NOT fold in blast-radius size: codemap's hotspots feed
// doesn't carry it, so parsing it here would be dead code implying a signal
// that isn't wired. structuralWeight is 0..1; 0 means no re-ranking.
func (c *CodemapClient) Rerank(ctx context.Context, projectPath string, results []CodemapRerankResult, structuralWeight float32) []CodemapRerankResult {
	if !c.Available() || structuralWeight <= 0 || len(results) == 0 {
		return results
	}

	// Fetch hotspot scores (fan-in). codemap's in_degree is the hub signal;
	// shared_name>1 marks a name-inflated hub (a name-based index counted
	// every same-named definition together), so we discount those so a
	// genuinely-referenced hub outranks a collision artifact.
	hotspots, _ := c.Hotspots(ctx, projectPath, 200)
	hubScore := make(map[string]float32, len(hotspots))
	var maxHub float32 = 1
	for _, h := range hotspots {
		score := float32(h.InDegree)
		// Down-weight inflated hubs: divide the fan-in by the number of
		// same-named definitions it was (likely over-)counted across.
		if h.SharedName > 1 {
			score /= float32(h.SharedName)
		}
		// Keep the strongest score when a name appears more than once.
		if score > hubScore[h.Symbol] {
			hubScore[h.Symbol] = score
		}
		if hubScore[h.Symbol] > maxHub {
			maxHub = hubScore[h.Symbol]
		}
	}

	semWeight := 1.0 - structuralWeight

	for i := range results {
		// Lookup the symbol's hub score; default to 0 if not found
		var hs float32
		if sym := results[i].Result.SymbolName; sym != "" {
			hs = hubScore[sym]
		}
		// Normalize hub score to 0..1
		normalizedHub := hs / maxHub
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
