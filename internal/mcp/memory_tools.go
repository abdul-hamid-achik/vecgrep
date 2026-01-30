package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/memory"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// handleMemoryRemember handles the memory_remember tool.
func (s *SDKServer) handleMemoryRemember(ctx context.Context, req *sdkmcp.CallToolRequest, input MemoryRememberInput) (*sdkmcp.CallToolResult, any, error) {
	if err := s.ensureMemoryInitialized(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Memory initialization failed: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	if input.Content == "" {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Error: 'content' parameter is required."}},
			IsError: true,
		}, nil, nil
	}

	opts := memory.RememberOptions{
		Importance: input.Importance,
		Tags:       input.Tags,
		TTLHours:   input.TTLHours,
	}

	id, err := s.memoryStore.Remember(ctx, input.Content, opts)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to store memory: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Memory stored successfully (ID: %d)\n\n", id)
	fmt.Fprintf(&sb, "- Content: %s\n", truncateString(input.Content, 100))
	fmt.Fprintf(&sb, "- Importance: %.2f\n", opts.Importance)
	if len(opts.Tags) > 0 {
		fmt.Fprintf(&sb, "- Tags: %s\n", strings.Join(opts.Tags, ", "))
	}
	if opts.TTLHours > 0 {
		fmt.Fprintf(&sb, "- Expires in: %d hours\n", opts.TTLHours)
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// handleMemoryRecall handles the memory_recall tool.
func (s *SDKServer) handleMemoryRecall(ctx context.Context, req *sdkmcp.CallToolRequest, input MemoryRecallInput) (*sdkmcp.CallToolResult, any, error) {
	if err := s.ensureMemoryInitialized(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Memory initialization failed: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	if input.Query == "" {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Error: 'query' parameter is required."}},
			IsError: true,
		}, nil, nil
	}

	opts := memory.RecallOptions{
		Limit:         input.Limit,
		Tags:          input.Tags,
		MinImportance: input.MinImportance,
	}

	memories, err := s.memoryStore.Recall(ctx, input.Query, opts)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Search failed: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	if len(memories) == 0 {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "No memories found matching your query."}},
		}, nil, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d memories:\n\n", len(memories))

	for i, m := range memories {
		fmt.Fprintf(&sb, "### Memory %d (ID: %d, score: %.2f)\n", i+1, m.ID, m.Score)
		fmt.Fprintf(&sb, "**Importance:** %.2f\n", m.Importance)
		if len(m.Tags) > 0 {
			fmt.Fprintf(&sb, "**Tags:** %s\n", strings.Join(m.Tags, ", "))
		}
		fmt.Fprintf(&sb, "**Created:** %s\n", m.CreatedAt.Format(time.RFC3339))
		if m.ExpiresAt != nil {
			fmt.Fprintf(&sb, "**Expires:** %s\n", m.ExpiresAt.Format(time.RFC3339))
		}
		sb.WriteString("\n```\n")
		sb.WriteString(m.Content)
		sb.WriteString("\n```\n\n")
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// handleMemoryForget handles the memory_forget tool.
func (s *SDKServer) handleMemoryForget(ctx context.Context, req *sdkmcp.CallToolRequest, input MemoryForgetInput) (*sdkmcp.CallToolResult, any, error) {
	if err := s.ensureMemoryInitialized(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Memory initialization failed: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	// Single ID deletion doesn't require confirmation
	if input.ID > 0 {
		opts := memory.ForgetOptions{ID: input.ID}
		deleted, err := s.memoryStore.Forget(ctx, opts)
		if err != nil {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to delete memory: %v", err)}},
				IsError: true,
			}, nil, nil
		}
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Deleted memory ID %d (%d memories removed)", input.ID, deleted)}},
		}, nil, nil
	}

	// Bulk deletion requires confirmation
	if len(input.Tags) > 0 || input.OlderThanHours > 0 {
		if input.Confirm != "yes" {
			var sb strings.Builder
			sb.WriteString("WARNING: This will delete multiple memories.\n\n")
			if len(input.Tags) > 0 {
				fmt.Fprintf(&sb, "- Deleting memories with tags: %s\n", strings.Join(input.Tags, ", "))
			}
			if input.OlderThanHours > 0 {
				fmt.Fprintf(&sb, "- Deleting memories older than %d hours\n", input.OlderThanHours)
			}
			sb.WriteString("\nSet confirm='yes' to proceed.")
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
				IsError: true,
			}, nil, nil
		}

		opts := memory.ForgetOptions{
			Tags:           input.Tags,
			OlderThanHours: input.OlderThanHours,
		}
		deleted, err := s.memoryStore.Forget(ctx, opts)
		if err != nil {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to delete memories: %v", err)}},
				IsError: true,
			}, nil, nil
		}
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Deleted %d memories", deleted)}},
		}, nil, nil
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Error: Specify id, tags, or older_than_hours to delete memories."}},
		IsError: true,
	}, nil, nil
}

// handleMemoryStats handles the memory_stats tool.
func (s *SDKServer) handleMemoryStats(ctx context.Context, req *sdkmcp.CallToolRequest, input MemoryStatsInput) (*sdkmcp.CallToolResult, any, error) {
	if err := s.ensureMemoryInitialized(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Memory initialization failed: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	stats, err := s.memoryStore.Stats(ctx)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to get stats: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	var sb strings.Builder
	sb.WriteString("Memory Store Statistics:\n\n")
	fmt.Fprintf(&sb, "- Total memories: %d\n", stats.TotalMemories)
	fmt.Fprintf(&sb, "- Total unique tags: %d\n", stats.TotalTags)
	fmt.Fprintf(&sb, "- Expired memories: %d\n", stats.ExpiredMemories)

	if stats.OldestMemory != nil {
		fmt.Fprintf(&sb, "- Oldest memory: %s\n", stats.OldestMemory.Format(time.RFC3339))
	}
	if stats.NewestMemory != nil {
		fmt.Fprintf(&sb, "- Newest memory: %s\n", stats.NewestMemory.Format(time.RFC3339))
	}

	if len(stats.TagCounts) > 0 {
		sb.WriteString("\nTag distribution:\n")
		for tag, count := range stats.TagCounts {
			fmt.Fprintf(&sb, "  - %s: %d\n", tag, count)
		}
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// truncateString truncates a string to maxLen characters, adding "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
