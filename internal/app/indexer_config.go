package app

import (
	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

const approximateCharsPerToken = 4

// BuildIndexerConfig resolves the indexing settings shared by every surface.
// Config chunk sizes are expressed in tokens while the chunker works in
// characters, so both sizes use the same documented approximation here.
func BuildIndexerConfig(cfg *config.Config, additionalIgnores []string) index.IndexerConfig {
	resolved := index.DefaultIndexerConfig()
	if cfg == nil {
		resolved.IgnorePatterns = append(resolved.IgnorePatterns, additionalIgnores...)
		return resolved
	}

	if cfg.Indexing.ChunkSize > 0 {
		resolved.ChunkSize = cfg.Indexing.ChunkSize * approximateCharsPerToken
	}
	if cfg.Indexing.ChunkOverlap > 0 {
		resolved.ChunkOverlap = cfg.Indexing.ChunkOverlap * approximateCharsPerToken
	}
	if cfg.Indexing.MaxFileSize > 0 {
		resolved.MaxFileSize = cfg.Indexing.MaxFileSize
	}
	if cfg.Indexing.SourceBufferBytes > 0 {
		resolved.SourceBufferBytes = cfg.Indexing.SourceBufferBytes
	}
	if cfg.Indexing.SyncInterval > 0 {
		resolved.SyncInterval = cfg.Indexing.SyncInterval
	}
	if cfg.Indexing.SyncIntervalDuration > 0 {
		resolved.SyncIntervalDuration = cfg.Indexing.SyncIntervalDuration
	}
	resolved.IgnorePatterns = append(resolved.IgnorePatterns, cfg.Indexing.IgnorePatterns...)
	resolved.IgnorePatterns = append(resolved.IgnorePatterns, additionalIgnores...)
	return resolved
}

// NewConfiguredIndexer constructs an indexer with the same resolved settings
// and structural-chunk policy for CLI, daemon, MCP, and read-only previews.
func NewConfiguredIndexer(database *db.DB, provider embed.Provider, cfg *config.Config, additionalIgnores []string, structuralOverride string) (*index.Indexer, error) {
	indexer := index.NewIndexer(database, provider, BuildIndexerConfig(cfg, additionalIgnores))
	if cfg == nil {
		return indexer, nil
	}
	setup, err := configureStructuralChunks(indexer, cfg.Codemap, structuralOverride)
	if err != nil {
		return nil, err
	}
	if database != nil && database.DataDir() != "" {
		indexer.SetIndexRunObserver(newIngestionReceiptObserver(database.DataDir(), setup))
	}
	return indexer, nil
}
