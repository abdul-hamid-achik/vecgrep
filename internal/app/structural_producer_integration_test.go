package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/index"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
)

func TestCodemapProducerThroughStoredSearch(t *testing.T) {
	bin := os.Getenv("VECGREP_TEST_CODEMAP_BIN")
	if bin == "" {
		t.Skip("set VECGREP_TEST_CODEMAP_BIN to test the real producer")
	}
	root, cfg, database := newCoordinatorFixture(t)
	configDir, data := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("CODEMAP_DATA", data)
	configPath := filepath.Join(configDir, "codemap.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEMAP_CONFIG", configPath)
	content := "package fixture\ntype Worker struct{}\nfunc (Worker) Run() string { return \"needlecontext\" }\n"
	writeStructuralFixture(t, root, "main.go", content)
	writeStructuralFixture(t, root, "go.mod", "module fixture\n\ngo 1.25\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, args := range [][]string{{"init", "--json"}, {"index", "--no-embed", "--no-lsp", "--cache=false", "--json"}} {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("codemap %v: %v: %s", args, err, out)
		}
	}
	provider := &coordinatorProvider{dimensions: cfg.Embedding.Dimensions, model: cfg.Embedding.Model}
	idx := index.NewIndexer(database, provider, index.DefaultIndexerConfig())
	idx.SetStructuralChunkSource(newCodemapStructuralSource(bin), true)
	if _, err := idx.Index(ctx, root); err != nil {
		t.Fatal(err)
	}
	opts := search.DefaultSearchOptions()
	opts.Mode = search.SearchModeKeyword
	opts.ProjectRoot = root
	hits, err := search.NewSearcher(database, provider).Search(ctx, "needlecontext", opts)
	if err != nil || len(hits) == 0 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
	hit := hits[0]
	if hit.Selector == nil || hit.Selector.File != "main.go" || hit.Selector.StartLine != 3 || hit.Selector.FQN == "" || hit.SourceHash != sha256Hex(content) {
		t.Fatalf("identity lost: %+v", hit)
	}
	encoded, err := json.Marshal(hit.Selector)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, bin, "impact", "--selector", string(encoded), "--json")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var impact struct {
		Results []struct {
			Found bool `json:"found"`
		} `json:"results"`
	}
	if err := json.Unmarshal(out, &impact); err != nil || len(impact.Results) != 1 || !impact.Results[0].Found {
		t.Fatalf("impact=%s err=%v", out, err)
	}
}
