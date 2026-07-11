package benchmark

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
)

const DefaultBatchSize = 32

// EmbeddingProfile contains the semantic inputs needed by the runner. A
// ProviderFactory may use the remaining fields to select a backend/model, but
// must leave provider-side templates empty because Runner applies them.
type EmbeddingProfile struct {
	Name             string
	Provider         string
	Model            string
	Dimensions       int
	OllamaContext    int
	OllamaOptions    map[string]any
	QueryTemplate    string
	DocumentTemplate string
}

// ProviderFactory constructs a provider without writing indexes or config.
type ProviderFactory func(context.Context, EmbeddingProfile) (embed.Provider, error)

// Runner benchmarks one immutable, already-resolved dataset.
type Runner struct {
	Dataset   Dataset
	BatchSize int
}

// Metrics are macro-averaged over queries. Recall is the fraction of all
// labeled relevant documents retrieved within the indicated cutoff.
type Metrics struct {
	Top1       float64 `json:"top1"`
	RecallAt5  float64 `json:"recall_at_5"`
	RecallAt10 float64 `json:"recall_at_10"`
	MRR        float64 `json:"mrr"`
}

// QueryResult records the deterministic ranking used for metric calculation.
type QueryResult struct {
	QueryID           string   `json:"query_id"`
	RankedDocumentIDs []string `json:"ranked_document_ids"`
}

// ProfileReport is the complete result for one profile.
type ProfileReport struct {
	Profile              EmbeddingProfile `json:"profile"`
	Metrics              Metrics          `json:"metrics"`
	WarmupLatency        time.Duration    `json:"warmup_latency"`
	ProviderLoadDuration time.Duration    `json:"provider_load_duration"`
	CorpusLatency        time.Duration    `json:"corpus_latency"`
	QueryLatency         time.Duration    `json:"query_latency"`
	DocumentsPerSecond   float64          `json:"documents_per_second"`
	QueriesPerSecond     float64          `json:"queries_per_second"`
	Results              []QueryResult    `json:"results"`
}

// Run evaluates profiles in their supplied order. Documents and queries are
// submitted in dataset order and fixed contiguous batches.
func (r Runner) Run(ctx context.Context, profiles []EmbeddingProfile, factory ProviderFactory) ([]ProfileReport, error) {
	if factory == nil {
		return nil, errors.New("run embedding benchmark: nil provider factory")
	}
	if err := validateResolvedDataset(r.Dataset); err != nil {
		return nil, fmt.Errorf("run embedding benchmark: %w", err)
	}
	batchSize := r.BatchSize
	if batchSize == 0 {
		batchSize = DefaultBatchSize
	}
	if batchSize < 0 {
		return nil, errors.New("run embedding benchmark: batch size must be positive")
	}

	reports := make([]ProfileReport, 0, len(profiles))
	for _, inputProfile := range profiles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		profile := cloneProfile(inputProfile)
		if profile.Name == "" || profile.Dimensions <= 0 {
			return nil, fmt.Errorf("run embedding benchmark: profile %q must have a name and positive dimensions", profile.Name)
		}
		provider, err := factory(ctx, cloneProfile(profile))
		if err != nil {
			return nil, fmt.Errorf("run embedding benchmark profile %q: create provider: %w", profile.Name, err)
		}
		if provider == nil {
			return nil, fmt.Errorf("run embedding benchmark profile %q: provider factory returned nil", profile.Name)
		}

		report, err := r.runAndCloseProfile(ctx, profile, provider, batchSize)
		if err != nil {
			return nil, fmt.Errorf("run embedding benchmark profile %q: %w", profile.Name, err)
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func (r Runner) runAndCloseProfile(ctx context.Context, profile EmbeddingProfile, provider embed.Provider, batchSize int) (report ProfileReport, err error) {
	defer func() {
		if closeErr := closeProvider(provider); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close provider: %w", closeErr))
		}
	}()
	if got := provider.Dimensions(); got != profile.Dimensions {
		return ProfileReport{}, fmt.Errorf("%w: profile expects %d, provider reports %d", embed.ErrDimensionMismatch, profile.Dimensions, got)
	}
	return r.runProfile(ctx, profile, provider, batchSize)
}

func closeProvider(provider embed.Provider) error {
	if closer, ok := provider.(interface{ Close() error }); ok {
		return closer.Close()
	}
	if closer, ok := provider.(interface{ Close() }); ok {
		closer.Close()
	}
	return nil
}

func (r Runner) runProfile(ctx context.Context, profile EmbeddingProfile, provider embed.Provider, batchSize int) (ProfileReport, error) {
	warmupStart := time.Now()
	loadDuration, err := provider.Warmup(ctx)
	warmupLatency := time.Since(warmupStart)
	if err != nil {
		return ProfileReport{}, fmt.Errorf("warm up provider: %w", err)
	}

	documentTexts := make([]string, len(r.Dataset.Documents))
	for i, document := range r.Dataset.Documents {
		documentTexts[i] = applyTemplate(profile.DocumentTemplate, document.Text)
	}
	corpusStart := time.Now()
	documentVectors, err := embedBatches(ctx, provider, documentTexts, batchSize, profile.Dimensions, "document")
	corpusLatency := time.Since(corpusStart)
	if err != nil {
		return ProfileReport{}, err
	}

	queryTexts := make([]string, len(r.Dataset.Queries))
	for i, query := range r.Dataset.Queries {
		queryTexts[i] = applyTemplate(profile.QueryTemplate, query.Text)
	}
	queryStart := time.Now()
	queryVectors, err := embedBatches(ctx, provider, queryTexts, batchSize, profile.Dimensions, "query")
	queryLatency := time.Since(queryStart)
	if err != nil {
		return ProfileReport{}, err
	}

	metrics, results, err := scoreDataset(ctx, r.Dataset, documentVectors, queryVectors)
	if err != nil {
		return ProfileReport{}, err
	}
	return ProfileReport{
		Profile:              profile,
		Metrics:              metrics,
		WarmupLatency:        warmupLatency,
		ProviderLoadDuration: loadDuration,
		CorpusLatency:        corpusLatency,
		QueryLatency:         queryLatency,
		DocumentsPerSecond:   throughput(len(documentTexts), corpusLatency),
		QueriesPerSecond:     throughput(len(queryTexts), queryLatency),
		Results:              results,
	}, nil
}

func embedBatches(ctx context.Context, provider embed.Provider, texts []string, batchSize, dimensions int, kind string) ([][]float32, error) {
	vectors := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += batchSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(start+batchSize, len(texts))
		batch, err := provider.EmbedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, fmt.Errorf("embed %s batch %d-%d: %w", kind, start, end, err)
		}
		if len(batch) != end-start {
			return nil, fmt.Errorf("embed %s batch %d-%d: returned %d vectors for %d inputs", kind, start, end, len(batch), end-start)
		}
		for i, vector := range batch {
			if len(vector) != dimensions {
				return nil, fmt.Errorf("embed %s %d: %w: expected %d, got %d", kind, start+i, embed.ErrDimensionMismatch, dimensions, len(vector))
			}
		}
		vectors = append(vectors, batch...)
	}
	return vectors, nil
}

func scoreDataset(ctx context.Context, dataset Dataset, documents, queries [][]float32) (Metrics, []QueryResult, error) {
	type scoredDocument struct {
		id    string
		score float64
	}
	var metrics Metrics
	results := make([]QueryResult, len(dataset.Queries))
	for queryIndex, query := range dataset.Queries {
		if err := ctx.Err(); err != nil {
			return Metrics{}, nil, err
		}
		scores := make([]scoredDocument, len(dataset.Documents))
		for documentIndex, document := range dataset.Documents {
			similarity, err := cosineSimilarity(queries[queryIndex], documents[documentIndex])
			if err != nil {
				return Metrics{}, nil, fmt.Errorf("score query %q against document %q: %w", query.ID, document.ID, err)
			}
			scores[documentIndex] = scoredDocument{id: document.ID, score: similarity}
		}
		sort.Slice(scores, func(i, j int) bool {
			if scores[i].score == scores[j].score {
				return scores[i].id < scores[j].id
			}
			return scores[i].score > scores[j].score
		})

		relevant := make(map[string]struct{}, len(query.Relevant))
		for _, id := range query.Relevant {
			relevant[id] = struct{}{}
		}
		ranked := make([]string, len(scores))
		hits5, hits10 := 0, 0
		firstRelevantRank := 0
		for rank, scored := range scores {
			ranked[rank] = scored.id
			if _, ok := relevant[scored.id]; !ok {
				continue
			}
			if rank == 0 {
				metrics.Top1++
			}
			if rank < 5 {
				hits5++
			}
			if rank < 10 {
				hits10++
			}
			if firstRelevantRank == 0 {
				firstRelevantRank = rank + 1
			}
		}
		metrics.RecallAt5 += float64(hits5) / float64(len(relevant))
		metrics.RecallAt10 += float64(hits10) / float64(len(relevant))
		if firstRelevantRank > 0 {
			metrics.MRR += 1 / float64(firstRelevantRank)
		}
		results[queryIndex] = QueryResult{QueryID: query.ID, RankedDocumentIDs: ranked}
	}
	count := float64(len(dataset.Queries))
	metrics.Top1 /= count
	metrics.RecallAt5 /= count
	metrics.RecallAt10 /= count
	metrics.MRR /= count
	return metrics, results, nil
}

func cosineSimilarity(a, b []float32) (float64, error) {
	var dot, normA, normB float64
	for i := range a {
		av, bv := float64(a[i]), float64(b[i])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if normA == 0 || normB == 0 {
		return 0, errors.New("zero-length embedding vector")
	}
	result := dot / math.Sqrt(normA*normB)
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return 0, errors.New("non-finite cosine similarity")
	}
	return result, nil
}

func applyTemplate(template, text string) string {
	if template == "" {
		return text
	}
	if strings.Contains(template, "{{text}}") {
		return strings.ReplaceAll(template, "{{text}}", text)
	}
	return template + text
}

func throughput(count int, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(count) / elapsed.Seconds()
}

func cloneProfile(profile EmbeddingProfile) EmbeddingProfile {
	profile.OllamaOptions = cloneOptions(profile.OllamaOptions)
	return profile
}

func cloneOptions(options map[string]any) map[string]any {
	if options == nil {
		return nil
	}
	cloned := make(map[string]any, len(options))
	for key, value := range options {
		cloned[key] = cloneOptionValue(value)
	}
	return cloned
}

func cloneOptionValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneOptions(typed)
	case []any:
		cloned := make([]any, len(typed))
		for i, item := range typed {
			cloned[i] = cloneOptionValue(item)
		}
		return cloned
	default:
		return value
	}
}

func validateResolvedDataset(dataset Dataset) error {
	if dataset.Version != DatasetVersion || len(dataset.Documents) == 0 || len(dataset.Queries) == 0 {
		return errors.New("invalid or empty resolved dataset")
	}
	documentIDs := make(map[string]struct{}, len(dataset.Documents))
	for _, document := range dataset.Documents {
		if document.ID == "" || document.Text == "" {
			return errors.New("resolved dataset contains an empty document")
		}
		if _, duplicate := documentIDs[document.ID]; duplicate {
			return fmt.Errorf("resolved dataset contains duplicate document %q", document.ID)
		}
		documentIDs[document.ID] = struct{}{}
	}
	for _, query := range dataset.Queries {
		if query.ID == "" || query.Text == "" || len(query.Relevant) == 0 {
			return errors.New("resolved dataset contains an invalid query")
		}
		for _, id := range query.Relevant {
			if _, exists := documentIDs[id]; !exists {
				return fmt.Errorf("query %q references unknown document %q", query.ID, id)
			}
		}
	}
	return nil
}
