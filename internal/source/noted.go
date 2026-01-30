// Package source provides abstractions for different content sources.
package source

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Note represents a note from the noted CLI.
type Note struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

// NotedSource is a source adapter for the noted CLI.
type NotedSource struct {
	binaryPath string
}

// NewNotedSource creates a new NotedSource.
// binaryPath is the path to the noted binary. If empty, "noted" is used (assumes it's in PATH).
func NewNotedSource(binaryPath string) *NotedSource {
	if binaryPath == "" {
		binaryPath = "noted"
	}
	return &NotedSource{
		binaryPath: binaryPath,
	}
}

// Name returns the source name.
func (n *NotedSource) Name() string {
	return "noted"
}

// List returns all notes from noted CLI.
func (n *NotedSource) List(ctx context.Context) ([]Document, error) {
	// Check if noted is available
	if err := n.checkAvailable(ctx); err != nil {
		return nil, err
	}

	// Export notes as JSON
	cmd := exec.CommandContext(ctx, n.binaryPath, "export", "--format", "json")
	output, err := cmd.Output()
	if err != nil {
		// Try alternative: list and then get each note
		return n.listNotesFallback(ctx)
	}

	// Parse JSON output
	var notes []Note
	if err := json.Unmarshal(output, &notes); err != nil {
		return nil, fmt.Errorf("failed to parse noted output: %w", err)
	}

	return n.notesToDocuments(notes), nil
}

// listNotesFallback lists notes using the list command as a fallback.
func (n *NotedSource) listNotesFallback(ctx context.Context) ([]Document, error) {
	// List notes
	cmd := exec.CommandContext(ctx, n.binaryPath, "list", "--json")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list notes: %w", err)
	}

	var notes []Note
	if err := json.Unmarshal(output, &notes); err != nil {
		// Try parsing as newline-delimited JSON
		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var note Note
			if err := json.Unmarshal([]byte(line), &note); err != nil {
				continue
			}
			notes = append(notes, note)
		}
	}

	return n.notesToDocuments(notes), nil
}

// notesToDocuments converts notes to documents.
func (n *NotedSource) notesToDocuments(notes []Note) []Document {
	docs := make([]Document, 0, len(notes))
	for _, note := range notes {
		// Build metadata
		metadata := make(map[string]any)
		if len(note.Tags) > 0 {
			metadata["tags"] = note.Tags
		}
		if note.CreatedAt != "" {
			metadata["created_at"] = note.CreatedAt
		}
		if note.UpdatedAt != "" {
			metadata["updated_at"] = note.UpdatedAt
		}

		// Determine language from content
		lang := "markdown"
		if strings.Contains(note.Content, "```go") {
			lang = "go"
		} else if strings.Contains(note.Content, "```python") {
			lang = "python"
		} else if strings.Contains(note.Content, "```javascript") || strings.Contains(note.Content, "```js") {
			lang = "javascript"
		}

		docs = append(docs, Document{
			ID:       strconv.FormatInt(note.ID, 10),
			Path:     fmt.Sprintf("noted://note/%d", note.ID),
			Content:  note.Content,
			Language: lang,
			Title:    note.Title,
			Metadata: metadata,
		})
	}
	return docs
}

// Watch watches for note changes.
func (n *NotedSource) Watch(ctx context.Context, onChange func([]Document)) error {
	// Polling-based watch since noted doesn't have native watch support
	go func() {
		ticker := time.NewTicker(30 * time.Second) // Check every 30 seconds
		defer ticker.Stop()

		var lastDocs []Document

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				docs, err := n.List(ctx)
				if err != nil {
					continue
				}

				// Check if anything changed
				if n.docsChanged(lastDocs, docs) {
					lastDocs = docs
					onChange(docs)
				}
			}
		}
	}()

	return nil
}

// docsChanged checks if the documents have changed.
func (n *NotedSource) docsChanged(old, new []Document) bool {
	if len(old) != len(new) {
		return true
	}

	oldMap := make(map[string]string)
	for _, d := range old {
		oldMap[d.ID] = d.Content
	}

	for _, d := range new {
		if oldContent, exists := oldMap[d.ID]; !exists || oldContent != d.Content {
			return true
		}
	}

	return false
}

// checkAvailable checks if the noted CLI is available.
func (n *NotedSource) checkAvailable(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, n.binaryPath, "version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("noted CLI not available: %w. Install from https://github.com/noted-eip/noted", err)
	}
	return nil
}

// GetNote retrieves a single note by ID.
func (n *NotedSource) GetNote(ctx context.Context, id int64) (*Document, error) {
	cmd := exec.CommandContext(ctx, n.binaryPath, "show", strconv.FormatInt(id, 10), "--json")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get note %d: %w", id, err)
	}

	var note Note
	if err := json.Unmarshal(output, &note); err != nil {
		return nil, fmt.Errorf("failed to parse note: %w", err)
	}

	docs := n.notesToDocuments([]Note{note})
	if len(docs) == 0 {
		return nil, fmt.Errorf("note %d not found", id)
	}

	return &docs[0], nil
}

// SearchNotes searches notes by query.
func (n *NotedSource) SearchNotes(ctx context.Context, query string) ([]Document, error) {
	cmd := exec.CommandContext(ctx, n.binaryPath, "search", query, "--json")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to search notes: %w", err)
	}

	var notes []Note
	if err := json.Unmarshal(output, &notes); err != nil {
		return nil, fmt.Errorf("failed to parse search results: %w", err)
	}

	return n.notesToDocuments(notes), nil
}

// NotedConfig holds configuration for the noted source.
type NotedConfig struct {
	BinaryPath   string `yaml:"binary_path" mapstructure:"binary_path"`
	PollInterval int    `yaml:"poll_interval" mapstructure:"poll_interval"` // In seconds
}

// DefaultNotedConfig returns sensible defaults.
func DefaultNotedConfig() NotedConfig {
	return NotedConfig{
		BinaryPath:   "noted",
		PollInterval: 30,
	}
}
