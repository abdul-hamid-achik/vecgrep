package benchmark

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
)

type fakeProvider struct {
	dimensions int
	vectors    map[string][]float32
	calls      [][]string
	embed      func(context.Context, []string) ([][]float32, error)
}

func (f *fakeProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	batch, err := f.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return batch[0], nil
}

func (f *fakeProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	f.calls = append(f.calls, append([]string(nil), texts...))
	if f.embed != nil {
		return f.embed(ctx, texts)
	}
	output := make([][]float32, len(texts))
	for i, text := range texts {
		vector, ok := f.vectors[text]
		if !ok {
			return nil, fmt.Errorf("no fake vector for %q", text)
		}
		output[i] = append([]float32(nil), vector...)
	}
	return output, nil
}

func (f *fakeProvider) Model() string              { return "fake" }
func (f *fakeProvider) Dimensions() int            { return f.dimensions }
func (f *fakeProvider) Ping(context.Context) error { return nil }
func (f *fakeProvider) Warmup(context.Context) (time.Duration, error) {
	return 7 * time.Millisecond, nil
}

type errorClosingProvider struct {
	*fakeProvider
	closeCalls int
	closeErr   error
}

func (p *errorClosingProvider) Close() error {
	p.closeCalls++
	return p.closeErr
}

type voidClosingProvider struct {
	*fakeProvider
	closeCalls int
}

func (p *voidClosingProvider) Close() {
	p.closeCalls++
}

func TestLoadDatasetExtractsStableBoundedSourceSpan(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "source.go")
	if err := os.WriteFile(path, []byte("package sample\n\nfunc before() {}\nfunc target() {\n\tprintln(\"needle\")\n}\nfunc after() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := `{"version":1,"documents":[{"id":"target","language":"go","source":{"path":"source.go","anchor":"func target() {","lines_before":1,"lines_after":2}}],"queries":[{"id":"find","text":"target function","relevant":["target"]}]}`
	first, err := LoadDataset(strings.NewReader(raw), root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadDataset(strings.NewReader(raw), root)
	if err != nil {
		t.Fatal(err)
	}
	want := "func before() {}\nfunc target() {\n\tprintln(\"needle\")\n}\n"
	if first.Documents[0].Text != want {
		t.Fatalf("extracted text:\n%q\nwant:\n%q", first.Documents[0].Text, want)
	}
	if first.Documents[0].Text != second.Documents[0].Text {
		t.Fatal("source extraction is not stable")
	}
}

func TestLoadDatasetRejectsMissingAndDuplicateAnchors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("repeat\nmiddle\nrepeat\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, anchor, message string
	}{
		{name: "missing", anchor: "absent", message: "not found"},
		{name: "duplicate", anchor: "repeat", message: "occurs 2 times"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"version":1,"documents":[{"id":"doc","language":"text","source":{"path":"source.txt","anchor":%q}}],"queries":[{"id":"q","text":"query","relevant":["doc"]}]}`, test.anchor)
			_, err := LoadDataset(strings.NewReader(raw), root)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v, want message containing %q", err, test.message)
			}
		})
	}
}

func TestLoadDefaultDatasetCorpusAndRelevance(t *testing.T) {
	dataset, err := LoadDefaultDataset(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(dataset.Documents); got < 70 {
		t.Fatalf("documents = %d, want at least 70", got)
	}
	if got := len(dataset.Queries); got < 60 {
		t.Fatalf("queries = %d, want at least 60", got)
	}
	languages := map[string]bool{}
	for _, document := range dataset.Documents {
		languages[document.Language] = true
	}
	for _, language := range []string{"go", "typescript", "python", "rust", "yaml", "markdown", "shell", "java", "sql", "json", "toml", "c", "dockerfile", "hcl"} {
		if !languages[language] {
			t.Errorf("default corpus has no %s document", language)
		}
	}
}

func TestRunnerMetricMath(t *testing.T) {
	dataset := Dataset{Version: 1}
	vectors := map[string][]float32{
		"d1": {1, 0}, "d2": {0.98, 0.2}, "d3": {0.9, 0.4},
		"d4": {0.7, 0.7}, "d5": {0.4, 0.9}, "d6": {0, 1},
		"q1": {1, 0}, "q2": {1, 0},
	}
	for _, id := range []string{"d6", "d4", "d2", "d5", "d1", "d3"} {
		dataset.Documents = append(dataset.Documents, Document{ID: id, Language: "text", Text: id})
	}
	dataset.Queries = []Query{
		{ID: "q1", Text: "q1", Relevant: []string{"d1", "d6"}},
		{ID: "q2", Text: "q2", Relevant: []string{"d2"}},
	}
	provider := &fakeProvider{dimensions: 2, vectors: vectors}
	reports, err := (Runner{Dataset: dataset, BatchSize: 3}).Run(context.Background(), []EmbeddingProfile{{Name: "fake", Dimensions: 2}}, func(context.Context, EmbeddingProfile) (embed.Provider, error) { return provider, nil })
	if err != nil {
		t.Fatal(err)
	}
	metrics := reports[0].Metrics
	assertNear(t, metrics.Top1, 0.5)
	assertNear(t, metrics.RecallAt5, 0.75)
	assertNear(t, metrics.RecallAt10, 1)
	assertNear(t, metrics.MRR, 0.75)
	if reports[0].ProviderLoadDuration != 7*time.Millisecond {
		t.Fatalf("load duration = %s", reports[0].ProviderLoadDuration)
	}
}

func TestRunnerAppliesTemplatesAndBatchesInOrder(t *testing.T) {
	dataset := smallDataset([]string{"doc-c", "doc-a", "doc-b"}, []string{"query-b", "query-a"})
	provider := &fakeProvider{dimensions: 2, vectors: map[string][]float32{
		"doc: doc-c": {1, 0}, "doc: doc-a": {0, 1}, "doc: doc-b": {0.5, 0.5},
		"Represent query: query-b": {1, 0}, "Represent query: query-a": {0, 1},
	}}
	profile := EmbeddingProfile{Name: "templated", Dimensions: 2, DocumentTemplate: "doc: {{text}}", QueryTemplate: "Represent query: "}
	_, err := (Runner{Dataset: dataset, BatchSize: 2}).Run(context.Background(), []EmbeddingProfile{profile}, func(context.Context, EmbeddingProfile) (embed.Provider, error) { return provider, nil })
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"doc: doc-c", "doc: doc-a"}, {"doc: doc-b"}, {"Represent query: query-b", "Represent query: query-a"}}
	if !reflect.DeepEqual(provider.calls, want) {
		t.Fatalf("batches = %#v, want %#v", provider.calls, want)
	}
}

func TestRunnerRejectsDimensionMismatch(t *testing.T) {
	dataset := smallDataset([]string{"doc"}, []string{"query"})
	t.Run("provider declaration", func(t *testing.T) {
		provider := &fakeProvider{dimensions: 3}
		_, err := (Runner{Dataset: dataset}).Run(context.Background(), []EmbeddingProfile{{Name: "bad", Dimensions: 2}}, func(context.Context, EmbeddingProfile) (embed.Provider, error) { return provider, nil })
		if !errors.Is(err, embed.ErrDimensionMismatch) {
			t.Fatalf("error = %v, want dimension mismatch", err)
		}
	})
	t.Run("returned vector", func(t *testing.T) {
		provider := &fakeProvider{dimensions: 2, vectors: map[string][]float32{"doc": {1}, "query": {1, 0}}}
		_, err := (Runner{Dataset: dataset}).Run(context.Background(), []EmbeddingProfile{{Name: "bad", Dimensions: 2}}, func(context.Context, EmbeddingProfile) (embed.Provider, error) { return provider, nil })
		if !errors.Is(err, embed.ErrDimensionMismatch) {
			t.Fatalf("error = %v, want dimension mismatch", err)
		}
	})
}

func TestRunnerCancellation(t *testing.T) {
	dataset := smallDataset([]string{"doc-a", "doc-b"}, []string{"query"})
	ctx, cancel := context.WithCancel(context.Background())
	provider := &fakeProvider{dimensions: 2}
	provider.embed = func(ctx context.Context, _ []string) ([][]float32, error) {
		cancel()
		return nil, ctx.Err()
	}
	_, err := (Runner{Dataset: dataset, BatchSize: 1}).Run(ctx, []EmbeddingProfile{{Name: "cancel", Dimensions: 2}}, func(context.Context, EmbeddingProfile) (embed.Provider, error) { return provider, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestRunnerOrderingIsDeterministic(t *testing.T) {
	dataset := Dataset{
		Version:   1,
		Documents: []Document{{ID: "zeta", Language: "text", Text: "z"}, {ID: "alpha", Language: "text", Text: "a"}, {ID: "middle", Language: "text", Text: "m"}},
		Queries:   []Query{{ID: "query", Text: "q", Relevant: []string{"middle"}}},
	}
	profile := []EmbeddingProfile{{Name: "ties", Dimensions: 2}}
	var rankings [][]string
	for range 3 {
		provider := &fakeProvider{dimensions: 2, vectors: map[string][]float32{"z": {1, 0}, "a": {1, 0}, "m": {1, 0}, "q": {1, 0}}}
		reports, err := (Runner{Dataset: dataset, BatchSize: 2}).Run(context.Background(), profile, func(context.Context, EmbeddingProfile) (embed.Provider, error) { return provider, nil })
		if err != nil {
			t.Fatal(err)
		}
		rankings = append(rankings, reports[0].Results[0].RankedDocumentIDs)
	}
	want := []string{"alpha", "middle", "zeta"}
	for _, ranking := range rankings {
		if !reflect.DeepEqual(ranking, want) {
			t.Fatalf("ranking = %v, want %v", ranking, want)
		}
	}
}

func TestRunnerClosesProvidersOnSuccessAndError(t *testing.T) {
	dataset := smallDataset([]string{"doc"}, []string{"query"})
	vectors := map[string][]float32{"doc": {1, 0}, "query": {1, 0}}

	t.Run("error-returning closer after successful run", func(t *testing.T) {
		closeErr := errors.New("close failed")
		provider := &errorClosingProvider{
			fakeProvider: &fakeProvider{dimensions: 2, vectors: vectors},
			closeErr:     closeErr,
		}
		_, err := (Runner{Dataset: dataset}).Run(context.Background(), []EmbeddingProfile{{Name: "close-error", Dimensions: 2}}, func(context.Context, EmbeddingProfile) (embed.Provider, error) {
			return provider, nil
		})
		if !errors.Is(err, closeErr) {
			t.Fatalf("error = %v, want close failure", err)
		}
		if provider.closeCalls != 1 {
			t.Fatalf("close calls = %d, want 1", provider.closeCalls)
		}
	})

	t.Run("void closer after successful run", func(t *testing.T) {
		provider := &voidClosingProvider{fakeProvider: &fakeProvider{dimensions: 2, vectors: vectors}}
		_, err := (Runner{Dataset: dataset}).Run(context.Background(), []EmbeddingProfile{{Name: "void-close", Dimensions: 2}}, func(context.Context, EmbeddingProfile) (embed.Provider, error) {
			return provider, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if provider.closeCalls != 1 {
			t.Fatalf("close calls = %d, want 1", provider.closeCalls)
		}
	})

	t.Run("run failure still closes", func(t *testing.T) {
		runErr := errors.New("embed failed")
		provider := &errorClosingProvider{fakeProvider: &fakeProvider{
			dimensions: 2,
			embed: func(context.Context, []string) ([][]float32, error) {
				return nil, runErr
			},
		}}
		_, err := (Runner{Dataset: dataset}).Run(context.Background(), []EmbeddingProfile{{Name: "run-error", Dimensions: 2}}, func(context.Context, EmbeddingProfile) (embed.Provider, error) {
			return provider, nil
		})
		if !errors.Is(err, runErr) {
			t.Fatalf("error = %v, want embed failure", err)
		}
		if provider.closeCalls != 1 {
			t.Fatalf("close calls = %d, want 1", provider.closeCalls)
		}
	})
}

func TestRunnerCopiesProfileOptionsAndPropagatesContext(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "benchmark-context")
	dataset := smallDataset([]string{"doc"}, []string{"query"})
	options := map[string]any{
		"num_batch": 128,
		"nested":    map[string]any{"threads": 4},
		"stops":     []any{"one", "two"},
	}
	profile := EmbeddingProfile{
		Name:          "quality",
		Dimensions:    2,
		OllamaContext: 4096,
		OllamaOptions: options,
	}
	provider := &fakeProvider{dimensions: 2}
	provider.embed = func(callCtx context.Context, texts []string) ([][]float32, error) {
		if got := callCtx.Value(contextKey{}); got != "benchmark-context" {
			return nil, fmt.Errorf("context value = %v", got)
		}
		output := make([][]float32, len(texts))
		for i := range output {
			output[i] = []float32{1, 0}
		}
		return output, nil
	}
	reports, err := (Runner{Dataset: dataset}).Run(ctx, []EmbeddingProfile{profile}, func(factoryCtx context.Context, got EmbeddingProfile) (embed.Provider, error) {
		if factoryCtx.Value(contextKey{}) != "benchmark-context" {
			return nil, errors.New("factory context value missing")
		}
		if got.OllamaContext != 4096 || got.OllamaOptions["num_batch"] != 128 {
			return nil, fmt.Errorf("factory profile = %#v", got)
		}
		got.OllamaOptions["num_batch"] = 1
		got.OllamaOptions["nested"].(map[string]any)["threads"] = 1
		got.OllamaOptions["stops"].([]any)[0] = "mutated"
		return provider, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"num_batch": 128,
		"nested":    map[string]any{"threads": 4},
		"stops":     []any{"one", "two"},
	}
	if !reflect.DeepEqual(profile.OllamaOptions, want) {
		t.Fatalf("input options mutated: %#v", profile.OllamaOptions)
	}
	if !reflect.DeepEqual(reports[0].Profile.OllamaOptions, want) {
		t.Fatalf("reported options mutated: %#v", reports[0].Profile.OllamaOptions)
	}
	options["num_batch"] = 64
	if reports[0].Profile.OllamaOptions["num_batch"] != 128 {
		t.Fatal("report options alias caller options")
	}
}

func smallDataset(documents, queries []string) Dataset {
	dataset := Dataset{Version: 1}
	for _, text := range documents {
		dataset.Documents = append(dataset.Documents, Document{ID: text, Language: "text", Text: text})
	}
	for _, text := range queries {
		dataset.Queries = append(dataset.Queries, Query{ID: text, Text: text, Relevant: []string{documents[0]}})
	}
	return dataset
}

func assertNear(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("got %v, want %v", got, want)
	}
}
