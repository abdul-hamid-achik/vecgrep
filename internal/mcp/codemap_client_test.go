package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
)

func TestNewCodemapClientDisabledReturnsNil(t *testing.T) {
	c := NewCodemapClient(config.CodemapConfig{Enabled: false})
	if c != nil {
		t.Fatal("expected nil client when codemap is disabled")
	}
}

func TestNewCodemapClientEnabledButNotInstalledReturnsNil(t *testing.T) {
	// "codemap" is almost certainly not on $PATH in the test environment
	c := NewCodemapClient(config.CodemapConfig{Enabled: true, Bin: "codemap-nonexistent-binary-xyz"})
	if c != nil {
		t.Fatal("expected nil client when codemap binary is not found")
	}
}

func TestCodemapClientAvailableNil(t *testing.T) {
	var c *CodemapClient
	if c.Available() {
		t.Fatal("nil client should not be available")
	}
}

func TestCodemapClientHotspotsUnavailableReturnsNil(t *testing.T) {
	var c *CodemapClient
	results, err := c.Hotspots(context.Background(), "/tmp", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Fatal("expected nil results when client is unavailable")
	}
}

func TestCodemapClientFindSymbolUnavailableReturnsNil(t *testing.T) {
	var c *CodemapClient
	results, err := c.FindSymbol(context.Background(), "/tmp", "query")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Fatal("expected nil results when client is unavailable")
	}
}

func TestCodemapClientSymbolsUnavailableReturnsNil(t *testing.T) {
	var c *CodemapClient
	results, err := c.Symbols(context.Background(), "/tmp", "main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Fatal("expected nil results when client is unavailable")
	}
}

func TestCodemapClientStatusUnavailableReturnsNil(t *testing.T) {
	var c *CodemapClient
	result, err := c.Status(context.Background(), "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatal("expected nil result when client is unavailable")
	}
}

func TestCodemapClientAnnotateUnavailableNoError(t *testing.T) {
	var c *CodemapClient
	err := c.Annotate(context.Background(), "/tmp", "Symbol", "note", "vecgrep", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCodemapClientCallersUnavailableReturnsNil(t *testing.T) {
	var c *CodemapClient
	callers, err := c.Callers(context.Background(), "/tmp", "Symbol")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callers != nil {
		t.Fatal("expected nil callers when client is unavailable")
	}
}

func TestCodemapClientRelatedFilesUnavailableReturnsErr(t *testing.T) {
	var c *CodemapClient
	res, err := c.RelatedFiles(context.Background(), "/tmp", "main.go", 10)
	if !errors.Is(err, ErrCodemapUnavailable) {
		t.Fatalf("expected ErrCodemapUnavailable, got %v", err)
	}
	if res != nil {
		t.Fatal("expected nil result when client is unavailable")
	}
}

func TestCodemapClientRerankUnavailableReturnsOriginal(t *testing.T) {
	var c *CodemapClient
	input := []CodemapRerankResult{
		{Result: codemapSearchResult{SymbolName: "A", Score: 0.9}},
		{Result: codemapSearchResult{SymbolName: "B", Score: 0.5}},
	}
	result := c.Rerank(context.Background(), "/tmp", input, 0.3)
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
	// When unavailable, results should be returned as-is (no re-ranking)
	if result[0].Result.SymbolName != "A" {
		t.Fatalf("expected first result A, got %s", result[0].Result.SymbolName)
	}
}

func TestCodemapClientRerankZeroWeightReturnsOriginal(t *testing.T) {
	// Even with an available client, weight=0 should be a no-op.
	// Since we can't have a real codemap binary in tests, this tests the
	// weight guard path.
	c := &CodemapClient{bin: "/nonexistent"}
	input := []CodemapRerankResult{
		{Result: codemapSearchResult{SymbolName: "A", Score: 0.9}},
	}
	result := c.Rerank(context.Background(), "/tmp", input, 0)
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if result[0].FinalScore != 0 {
		t.Fatalf("expected FinalScore 0 (no re-ranking), got %f", result[0].FinalScore)
	}
}
