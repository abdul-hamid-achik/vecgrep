-- name: CreateProject :one
INSERT INTO projects (name, root_path, created_at, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
RETURNING *;

-- name: GetProject :one
SELECT * FROM projects WHERE id = ?;

-- name: GetProjectByPath :one
SELECT * FROM projects WHERE root_path = ?;

-- name: ListProjects :many
SELECT * FROM projects ORDER BY updated_at DESC;

-- name: UpdateProjectIndexedAt :exec
UPDATE projects
SET last_indexed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteProject :exec
DELETE FROM projects WHERE id = ?;

-- name: CreateFile :one
INSERT INTO files (project_id, path, relative_path, hash, size, language, indexed_at)
VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
RETURNING *;

-- name: GetFile :one
SELECT * FROM files WHERE id = ?;

-- name: GetFileByPath :one
SELECT * FROM files WHERE project_id = ? AND relative_path = ?;

-- name: ListFilesByProject :many
SELECT * FROM files WHERE project_id = ? ORDER BY relative_path;

-- name: UpdateFileHash :exec
UPDATE files
SET hash = ?, size = ?, indexed_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteFile :exec
DELETE FROM files WHERE id = ?;

-- name: DeleteFilesByProject :exec
DELETE FROM files WHERE project_id = ?;

-- name: CreateChunk :one
INSERT INTO chunks (file_id, content, start_line, end_line, start_byte, end_byte, chunk_type, symbol_name)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetChunk :one
SELECT * FROM chunks WHERE id = ?;

-- name: ListChunksByFile :many
SELECT * FROM chunks WHERE file_id = ? ORDER BY start_line;

-- name: ListChunksByProject :many
SELECT c.* FROM chunks c
JOIN files f ON c.file_id = f.id
WHERE f.project_id = ?
ORDER BY f.relative_path, c.start_line;

-- name: DeleteChunksByFile :exec
DELETE FROM chunks WHERE file_id = ?;

-- name: CountChunksByProject :one
SELECT COUNT(*) FROM chunks c
JOIN files f ON c.file_id = f.id
WHERE f.project_id = ?;

-- name: UpsertStat :exec
INSERT INTO stats (project_id, stat_key, stat_value, recorded_at)
VALUES (?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(project_id, stat_key) DO UPDATE SET
    stat_value = excluded.stat_value,
    recorded_at = CURRENT_TIMESTAMP;

-- name: GetStat :one
SELECT * FROM stats WHERE project_id = ? AND stat_key = ?;

-- name: ListStatsByProject :many
SELECT * FROM stats WHERE project_id = ? ORDER BY stat_key;

-- name: GetChunkWithFile :one
SELECT
    c.id, c.content, c.start_line, c.end_line, c.chunk_type, c.symbol_name,
    f.path, f.relative_path, f.language
FROM chunks c
JOIN files f ON c.file_id = f.id
WHERE c.id = ?;

-- name: CountFilesByProject :one
SELECT COUNT(*) FROM files WHERE project_id = ?;
