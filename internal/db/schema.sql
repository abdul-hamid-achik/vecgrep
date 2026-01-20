-- vecgrep database schema
-- SQLite with sqlite-vec extension for vector search

-- Projects table: tracks indexed projects
CREATE TABLE IF NOT EXISTS projects (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    root_path TEXT NOT NULL UNIQUE,
    embedding_model TEXT DEFAULT 'nomic-embed-text',
    embedding_dim INTEGER DEFAULT 768,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_indexed_at DATETIME
);

-- Files table: tracks individual files for incremental indexing
CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    relative_path TEXT NOT NULL,
    hash TEXT NOT NULL,
    size INTEGER NOT NULL,
    language TEXT,
    indexed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, relative_path)
);

CREATE INDEX IF NOT EXISTS idx_files_project ON files(project_id);
CREATE INDEX IF NOT EXISTS idx_files_hash ON files(hash);

-- Chunks table: stores code chunks with metadata
CREATE TABLE IF NOT EXISTS chunks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    content TEXT NOT NULL,
    start_line INTEGER NOT NULL,
    end_line INTEGER NOT NULL,
    start_byte INTEGER NOT NULL,
    end_byte INTEGER NOT NULL,
    chunk_type TEXT,  -- function, class, block, etc.
    symbol_name TEXT, -- name of function/class if applicable
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_chunks_file ON chunks(file_id);
CREATE INDEX IF NOT EXISTS idx_chunks_symbol ON chunks(symbol_name);

-- Stats table: stores index statistics
CREATE TABLE IF NOT EXISTS stats (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    stat_key TEXT NOT NULL,
    stat_value TEXT NOT NULL,
    recorded_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, stat_key)
);

CREATE INDEX IF NOT EXISTS idx_stats_project ON stats(project_id);

-- Note: The vec_chunks virtual table for vector search is created programmatically
-- after loading the sqlite-vec extension. Schema:
-- CREATE VIRTUAL TABLE vec_chunks USING vec0(
--     chunk_id INTEGER PRIMARY KEY,
--     embedding FLOAT[768]
-- );
