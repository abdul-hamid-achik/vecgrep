package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/vecgrep/internal/memory"
	"github.com/spf13/cobra"
)

// memoryCmd is the parent for the agent-memory CLI. Memory was MCP-only;
// these commands expose the same recall/remember logic over the CLI so a
// sibling tool (codemap) can shell it — CLI-only, one hop — to surface
// project-scoped memories beside a symbol without calling vecgrep's MCP server.
var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Recall and store cross-project agent memories",
	Long: `Manage the global agent-memory store (~/.vecai/memory).

Memories are recalled semantically and scoped by tags. For codemap-scoped
memories, follow the G2 convention: tag with ['codemap', <project_key>] where
<project_key> is codemap's 'codemap status --json' project_key. Recall with
BOTH tags (AND) so only that project's memories match.`,
}

// memoryRecallCmd recalls memories by semantic query, scoped by tags (AND).
var memoryRecallCmd = &cobra.Command{
	Use:   "recall <query>",
	Short: "Recall memories by meaning, scoped by tags (AND)",
	Long: `Recall memories semantically similar to <query>.

--tags filters to memories carrying ALL the given tags (AND semantics): a
memory matches only if it carries every requested tag exactly. This is the
scoping that prevents cross-project leakage (e.g. --tags codemap,<project_key>
matches only that project's codemap-scoped memories).

--format json emits a JSON array (the C5 contract) for tools to parse.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runMemoryRecall,
}

// memoryRememberCmd stores a memory with tags + importance.
var memoryRememberCmd = &cobra.Command{
	Use:   "remember <content>",
	Short: "Store a memory with tags and importance",
	Long: `Store a memory. For a codemap-scoped memory, tag it per the G2
convention: --tags codemap,<project_key>[,extra...]. Read codemap's
project_key from 'codemap status --json'; never re-derive it.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runMemoryRemember,
}

// c5Memory is the JSON shape emitted by `memory recall --format json` (C5).
// It mirrors the memory.Memory fields a consumer needs. ID is emitted as a
// string to match the committed C5 contract shape exactly.
type c5Memory struct {
	ID         string   `json:"id"`
	Content    string   `json:"content"`
	Importance float64  `json:"importance"`
	Tags       []string `json:"tags"`
	Score      float32  `json:"score"`
}

// openMemoryStore builds the memory store the same way the MCP server does:
// the default config + an Ollama embedding provider. It pings the provider so
// a clear error is returned when Ollama is unavailable, rather than failing
// deep inside recall.
func openMemoryStore(ctx context.Context) (*memory.MemoryStore, error) {
	cfg := memory.DefaultConfig()
	provider := embed.NewOllamaProvider(embed.OllamaConfig{
		URL:        cfg.OllamaURL,
		Model:      cfg.EmbeddingModel,
		Dimensions: cfg.EmbeddingDimensions,
	})
	if err := provider.Ping(ctx); err != nil {
		return nil, fmt.Errorf("embedding provider not available: %w (ensure Ollama is running with %s)", err, cfg.EmbeddingModel)
	}
	return memory.NewMemoryStore(cfg, provider)
}

// parseTags splits a comma-separated tag list, trimming blanks.
func parseTags(csv string) []string {
	if csv == "" {
		return nil
	}
	var tags []string
	for _, t := range strings.Split(csv, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

func runMemoryRecall(cmd *cobra.Command, args []string) error {
	query := strings.Join(args, " ")
	tagsCSV, _ := cmd.Flags().GetString("tags")
	minImportance, _ := cmd.Flags().GetFloat64("min-importance")
	limit, _ := cmd.Flags().GetInt("limit")
	format, _ := cmd.Flags().GetString("format")

	opts := memory.RecallOptions{
		Limit:         limit,
		Tags:          parseTags(tagsCSV),
		MinImportance: minImportance,
	}

	store, err := openMemoryStore(cmd.Context())
	if err != nil {
		// Provider unreachable at open time. For the json contract, keep
		// stdout empty and emit the degraded-signal envelope to stderr with
		// a distinct exit code so a consumer can distinguish "recall
		// unavailable" from "recall ran, no matches". For human output,
		// fall through so cobra prints the cause and exits non-zero.
		if format == "json" && errors.Is(err, embed.ErrProviderUnavailable) {
			writeMemoryProviderUnavailable(cmd.ErrOrStderr())
			os.Exit(exitProviderUnavailable)
		}
		if format == "json" {
			fmt.Fprintln(cmd.OutOrStdout(), "[]")
		}
		return err
	}
	defer func() { _ = store.Close() }()

	memories, err := store.Recall(cmd.Context(), query, opts)
	if err != nil {
		// A Recall failure mid-call may be the provider going down between
		// ping and embed; re-ping to classify. If the provider is down, emit
		// the degraded-signal envelope instead of an empty array (which a
		// consumer would mistake for an authoritative "no memory").
		if format == "json" && store.Ping(cmd.Context()) != nil {
			_ = store.Close()
			writeMemoryProviderUnavailable(cmd.ErrOrStderr())
			os.Exit(exitProviderUnavailable)
		}
		if format == "json" {
			fmt.Fprintln(cmd.OutOrStdout(), "[]")
		}
		return fmt.Errorf("recall failed: %w", err)
	}

	if format == "json" {
		return writeMemoriesJSON(cmd, memories)
	}
	writeMemoriesHuman(cmd, memories)
	return nil
}

// writeMemoriesJSON emits the C5 contract: a JSON array (never null), so a
// consumer always parses a well-formed array even when there are no matches.
func writeMemoriesJSON(cmd *cobra.Command, memories []memory.Memory) error {
	out := make([]c5Memory, 0, len(memories))
	for _, m := range memories {
		tags := m.Tags
		if tags == nil {
			tags = []string{}
		}
		out = append(out, c5Memory{
			ID:         strconv.FormatUint(m.ID, 10),
			Content:    m.Content,
			Importance: m.Importance,
			Tags:       tags,
			Score:      m.Score,
		})
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func writeMemoriesHuman(cmd *cobra.Command, memories []memory.Memory) {
	w := cmd.OutOrStdout()
	if len(memories) == 0 {
		fmt.Fprintln(w, "No memories found matching your query.")
		return
	}
	fmt.Fprintf(w, "Found %d memories:\n\n", len(memories))
	for i, m := range memories {
		fmt.Fprintf(w, "%d. (id %d, score %.2f, importance %.2f) %s\n", i+1, m.ID, m.Score, m.Importance, m.Content)
		if len(m.Tags) > 0 {
			fmt.Fprintf(w, "   tags: %s\n", strings.Join(m.Tags, ", "))
		}
	}
}

// exitProviderUnavailable is the exit code used when `memory recall` cannot
// reach the embedding provider. Distinct from the generic cobra failure (1)
// so a consumer can tell "recall unavailable" from "recall ran, no matches".
const exitProviderUnavailable = 3

// writeMemoryProviderUnavailable writes the degraded-signal envelope that
// `memory recall --format json` emits when the embedding provider is down:
// a single JSON object on the given writer (stderr in production) so stdout
// stays empty and a consumer can decode the failure without conflating it
// with an empty-but-authoritative result.
func writeMemoryProviderUnavailable(w io.Writer) {
	fmt.Fprintln(w, `{"error":"provider_unavailable"}`)
}

func runMemoryRemember(cmd *cobra.Command, args []string) error {
	content := strings.Join(args, " ")
	tagsCSV, _ := cmd.Flags().GetString("tags")
	importance, _ := cmd.Flags().GetFloat64("importance")
	ttlHours, _ := cmd.Flags().GetInt("ttl-hours")

	store, err := openMemoryStore(cmd.Context())
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	id, err := store.Remember(cmd.Context(), content, memory.RememberOptions{
		Importance: importance,
		Tags:       parseTags(tagsCSV),
		TTLHours:   ttlHours,
	})
	if err != nil {
		return fmt.Errorf("remember failed: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Memory stored (id %d)\n", id)
	return nil
}
