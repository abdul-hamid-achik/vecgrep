package benchmark

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	DatasetVersion     = 1
	MaxSourceSpanLines = 120
)

//go:embed testdata/embedding.json
var defaultDatasetFS embed.FS

// Dataset is a resolved retrieval corpus. Source-backed documents contain the
// extracted source text after loading.
type Dataset struct {
	Version   int        `json:"version"`
	Documents []Document `json:"documents"`
	Queries   []Query    `json:"queries"`
}

// Document is either inline (Text) or extracted from a project file (Source).
type Document struct {
	ID       string      `json:"id"`
	Language string      `json:"language"`
	Text     string      `json:"text,omitempty"`
	Source   *SourceSpan `json:"source,omitempty"`
}

// SourceSpan selects a bounded range around one unique anchor occurrence.
type SourceSpan struct {
	Path        string `json:"path"`
	Anchor      string `json:"anchor"`
	LinesBefore int    `json:"lines_before,omitempty"`
	LinesAfter  int    `json:"lines_after,omitempty"`
}

// Query labels all documents relevant to one retrieval prompt.
type Query struct {
	ID       string   `json:"id"`
	Text     string   `json:"text"`
	Relevant []string `json:"relevant"`
}

// LoadDefaultDataset resolves the embedded benchmark corpus against projectRoot.
func LoadDefaultDataset(projectRoot string) (Dataset, error) {
	f, err := defaultDatasetFS.Open("testdata/embedding.json")
	if err != nil {
		return Dataset{}, fmt.Errorf("open default embedding dataset: %w", err)
	}
	defer f.Close()
	return LoadDataset(f, projectRoot)
}

// LoadDataset strictly decodes, resolves source spans, and validates a dataset.
func LoadDataset(r io.Reader, projectRoot string) (Dataset, error) {
	if r == nil {
		return Dataset{}, errors.New("load embedding dataset: nil reader")
	}
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	var dataset Dataset
	if err := decoder.Decode(&dataset); err != nil {
		return Dataset{}, fmt.Errorf("decode embedding dataset: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Dataset{}, errors.New("decode embedding dataset: multiple JSON values")
		}
		return Dataset{}, fmt.Errorf("decode embedding dataset: %w", err)
	}
	if err := resolveAndValidateDataset(&dataset, projectRoot); err != nil {
		return Dataset{}, err
	}
	return dataset, nil
}

func resolveAndValidateDataset(dataset *Dataset, projectRoot string) error {
	if dataset.Version != DatasetVersion {
		return fmt.Errorf("validate embedding dataset: unsupported version %d", dataset.Version)
	}
	if len(dataset.Documents) == 0 {
		return errors.New("validate embedding dataset: no documents")
	}
	if len(dataset.Queries) == 0 {
		return errors.New("validate embedding dataset: no queries")
	}

	documentIDs := make(map[string]struct{}, len(dataset.Documents))
	for i := range dataset.Documents {
		doc := &dataset.Documents[i]
		if doc.ID == "" {
			return fmt.Errorf("validate embedding dataset: document %d has empty id", i)
		}
		if _, exists := documentIDs[doc.ID]; exists {
			return fmt.Errorf("validate embedding dataset: duplicate document id %q", doc.ID)
		}
		documentIDs[doc.ID] = struct{}{}
		if doc.Language == "" {
			return fmt.Errorf("validate embedding dataset: document %q has empty language", doc.ID)
		}
		if (doc.Text == "") == (doc.Source == nil) {
			return fmt.Errorf("validate embedding dataset: document %q must have exactly one of text or source", doc.ID)
		}
		if doc.Source != nil {
			text, err := extractSourceSpan(projectRoot, *doc.Source)
			if err != nil {
				return fmt.Errorf("resolve document %q: %w", doc.ID, err)
			}
			doc.Text = text
		}
	}

	queryIDs := make(map[string]struct{}, len(dataset.Queries))
	for i, query := range dataset.Queries {
		if query.ID == "" || query.Text == "" {
			return fmt.Errorf("validate embedding dataset: query %d has empty id or text", i)
		}
		if _, exists := queryIDs[query.ID]; exists {
			return fmt.Errorf("validate embedding dataset: duplicate query id %q", query.ID)
		}
		queryIDs[query.ID] = struct{}{}
		if len(query.Relevant) == 0 {
			return fmt.Errorf("validate embedding dataset: query %q has no relevant documents", query.ID)
		}
		seenRelevant := make(map[string]struct{}, len(query.Relevant))
		for _, id := range query.Relevant {
			if _, exists := documentIDs[id]; !exists {
				return fmt.Errorf("validate embedding dataset: query %q references unknown document %q", query.ID, id)
			}
			if _, duplicate := seenRelevant[id]; duplicate {
				return fmt.Errorf("validate embedding dataset: query %q repeats relevant document %q", query.ID, id)
			}
			seenRelevant[id] = struct{}{}
		}
	}
	return nil
}

func extractSourceSpan(projectRoot string, span SourceSpan) (string, error) {
	if span.Path == "" || span.Anchor == "" {
		return "", errors.New("source path and anchor must be non-empty")
	}
	if span.LinesBefore < 0 || span.LinesAfter < 0 || span.LinesBefore+1+span.LinesAfter > MaxSourceSpanLines {
		return "", fmt.Errorf("source span must contain between 1 and %d lines", MaxSourceSpanLines)
	}
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(span.Path)))
	if err != nil {
		return "", fmt.Errorf("resolve source path: %w", err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("source path %q escapes project root", span.Path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read source %q: %w", span.Path, err)
	}
	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	count := strings.Count(normalized, span.Anchor)
	if count == 0 {
		return "", fmt.Errorf("anchor %q not found in %q", span.Anchor, span.Path)
	}
	if count != 1 {
		return "", fmt.Errorf("anchor %q occurs %d times in %q", span.Anchor, count, span.Path)
	}
	anchorOffset := strings.Index(normalized, span.Anchor)
	anchorLine := strings.Count(normalized[:anchorOffset], "\n")
	lines := strings.Split(normalized, "\n")
	start := max(0, anchorLine-span.LinesBefore)
	end := min(len(lines), anchorLine+span.LinesAfter+1)
	return strings.TrimRight(strings.Join(lines[start:end], "\n"), "\n") + "\n", nil
}
