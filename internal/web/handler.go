package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/search"
	"github.com/abdul-hamid-achik/vecgrep/internal/version"
	"github.com/abdul-hamid-achik/vecgrep/internal/web/templates"
)

// Handler handles HTTP requests for the web UI.
type Handler struct {
	searcher    *search.Searcher
	projectRoot string
}

// NewHandler creates a new Handler.
func NewHandler(searcher *search.Searcher, projectRoot string) *Handler {
	return &Handler{
		searcher:    searcher,
		projectRoot: projectRoot,
	}
}

// Index renders the main search page.
func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	component := templates.Index(templates.IndexData{
		Query:   "",
		Results: nil,
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	component.Render(r.Context(), w)
}

// Search handles search requests (for HTMX).
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		w.WriteHeader(http.StatusBadRequest)
		templates.Error("Query is required").Render(r.Context(), w)
		return
	}

	opts := search.DefaultSearchOptions()
	opts.ProjectRoot = h.projectRoot

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			opts.Limit = limit
		}
	}
	if lang := r.URL.Query().Get("lang"); lang != "" {
		opts.Language = lang
	}
	if chunkType := r.URL.Query().Get("type"); chunkType != "" {
		opts.ChunkType = chunkType
	}
	if filePattern := r.URL.Query().Get("file"); filePattern != "" {
		opts.FilePattern = filePattern
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	results, err := h.searcher.Search(ctx, query, opts)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		templates.Error(err.Error()).Render(r.Context(), w)
		return
	}

	// Convert to template results
	templateResults := make([]templates.SearchResult, len(results))
	for i, r := range results {
		templateResults[i] = templates.SearchResult{
			ChunkID:    r.ChunkID,
			FilePath:   r.RelativePath,
			Content:    r.Content,
			StartLine:  r.StartLine,
			EndLine:    r.EndLine,
			ChunkType:  r.ChunkType,
			SymbolName: r.SymbolName,
			Language:   r.Language,
			Score:      r.Score,
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	templates.SearchResults(templateResults).Render(r.Context(), w)
}

// Status renders the status page.
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	stats, err := h.searcher.GetIndexStats(ctx)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		templates.Error(err.Error()).Render(r.Context(), w)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	templates.StatusPage(stats).Render(r.Context(), w)
}

// APISearch handles JSON API search requests.
func (h *Handler) APISearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		h.jsonError(w, "query parameter 'q' is required", http.StatusBadRequest)
		return
	}

	opts := search.DefaultSearchOptions()
	opts.ProjectRoot = h.projectRoot

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			opts.Limit = limit
		}
	}
	if lang := r.URL.Query().Get("lang"); lang != "" {
		opts.Language = lang
	}
	if chunkType := r.URL.Query().Get("type"); chunkType != "" {
		opts.ChunkType = chunkType
	}
	if filePattern := r.URL.Query().Get("file"); filePattern != "" {
		opts.FilePattern = filePattern
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	results, err := h.searcher.Search(ctx, query, opts)
	if err != nil {
		h.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.jsonResponse(w, map[string]interface{}{
		"query":   query,
		"count":   len(results),
		"results": results,
	})
}

// APIStatus returns index status as JSON.
func (h *Handler) APIStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	stats, err := h.searcher.GetIndexStats(ctx)
	if err != nil {
		h.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.jsonResponse(w, stats)
}

// Health returns a simple health check response.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	h.jsonResponse(w, map[string]interface{}{
		"status":  "ok",
		"version": version.Version,
	})
}

// jsonResponse writes a JSON response.
func (h *Handler) jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// jsonError writes a JSON error response.
func (h *Handler) jsonError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error": message,
	})
}

// Similar renders the similar code search page.
func (h *Handler) Similar(w http.ResponseWriter, r *http.Request) {
	component := templates.Similar(templates.SimilarData{
		ChunkID: r.URL.Query().Get("chunk_id"),
		FileLoc: r.URL.Query().Get("file_loc"),
		Text:    r.URL.Query().Get("text"),
		Results: nil,
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	component.Render(r.Context(), w)
}

// SimilarSearch handles similar code search requests (for HTMX).
func (h *Handler) SimilarSearch(w http.ResponseWriter, r *http.Request) {
	chunkIDStr := r.URL.Query().Get("chunk_id")
	fileLoc := r.URL.Query().Get("file_loc")
	text := r.URL.Query().Get("text")

	// Validate: exactly one of the three must be provided
	inputs := 0
	if chunkIDStr != "" {
		inputs++
	}
	if fileLoc != "" {
		inputs++
	}
	if text != "" {
		inputs++
	}

	if inputs == 0 {
		w.WriteHeader(http.StatusBadRequest)
		templates.Error("Provide a chunk ID, file:line location, or text snippet").Render(r.Context(), w)
		return
	}
	if inputs > 1 {
		w.WriteHeader(http.StatusBadRequest)
		templates.Error("Provide only one of: chunk ID, file:line location, or text snippet").Render(r.Context(), w)
		return
	}

	opts := search.SimilarOptions{
		SearchOptions: search.SearchOptions{
			Limit:       10,
			ProjectRoot: h.projectRoot,
		},
		ExcludeSourceID: true,
	}

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			opts.Limit = limit
		}
	}
	if lang := r.URL.Query().Get("lang"); lang != "" {
		opts.Language = lang
	}
	if chunkType := r.URL.Query().Get("type"); chunkType != "" {
		opts.ChunkType = chunkType
	}
	if filePattern := r.URL.Query().Get("file"); filePattern != "" {
		opts.FilePattern = filePattern
	}
	if r.URL.Query().Get("exclude_same_file") == "true" {
		opts.ExcludeSameFile = true
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var results []search.Result
	var err error

	if chunkIDStr != "" {
		chunkID, parseErr := strconv.ParseInt(chunkIDStr, 10, 64)
		if parseErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			templates.Error("Invalid chunk ID").Render(r.Context(), w)
			return
		}
		results, err = h.searcher.SearchSimilarByID(ctx, chunkID, opts)
	} else if fileLoc != "" {
		parts := splitFileLoc(fileLoc)
		if len(parts) != 2 {
			w.WriteHeader(http.StatusBadRequest)
			templates.Error("Invalid file:line format").Render(r.Context(), w)
			return
		}
		line, lineErr := strconv.Atoi(parts[1])
		if lineErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			templates.Error("Invalid line number").Render(r.Context(), w)
			return
		}
		results, err = h.searcher.SearchSimilarByLocation(ctx, parts[0], line, opts)
	} else if text != "" {
		results, err = h.searcher.SearchSimilarByText(ctx, text, opts)
	}

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		templates.Error(err.Error()).Render(r.Context(), w)
		return
	}

	// Convert to template results
	templateResults := make([]templates.SearchResult, len(results))
	for i, r := range results {
		templateResults[i] = templates.SearchResult{
			ChunkID:    r.ChunkID,
			FilePath:   r.RelativePath,
			Content:    r.Content,
			StartLine:  r.StartLine,
			EndLine:    r.EndLine,
			ChunkType:  r.ChunkType,
			SymbolName: r.SymbolName,
			Language:   r.Language,
			Score:      r.Score,
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	templates.SearchResults(templateResults).Render(r.Context(), w)
}

// APISimilar handles JSON API similar search requests.
func (h *Handler) APISimilar(w http.ResponseWriter, r *http.Request) {
	chunkIDStr := r.URL.Query().Get("chunk_id")
	fileLoc := r.URL.Query().Get("file_loc")
	text := r.URL.Query().Get("text")

	// Validate: exactly one of the three must be provided
	inputs := 0
	if chunkIDStr != "" {
		inputs++
	}
	if fileLoc != "" {
		inputs++
	}
	if text != "" {
		inputs++
	}

	if inputs == 0 {
		h.jsonError(w, "provide one of: chunk_id, file_loc, or text", http.StatusBadRequest)
		return
	}
	if inputs > 1 {
		h.jsonError(w, "provide only one of: chunk_id, file_loc, or text", http.StatusBadRequest)
		return
	}

	opts := search.SimilarOptions{
		SearchOptions: search.SearchOptions{
			Limit:       10,
			ProjectRoot: h.projectRoot,
		},
		ExcludeSourceID: true,
	}

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			opts.Limit = limit
		}
	}
	if lang := r.URL.Query().Get("lang"); lang != "" {
		opts.Language = lang
	}
	if chunkType := r.URL.Query().Get("type"); chunkType != "" {
		opts.ChunkType = chunkType
	}
	if filePattern := r.URL.Query().Get("file"); filePattern != "" {
		opts.FilePattern = filePattern
	}
	if r.URL.Query().Get("exclude_same_file") == "true" {
		opts.ExcludeSameFile = true
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var results []search.Result
	var err error

	if chunkIDStr != "" {
		chunkID, parseErr := strconv.ParseInt(chunkIDStr, 10, 64)
		if parseErr != nil {
			h.jsonError(w, "invalid chunk_id", http.StatusBadRequest)
			return
		}
		results, err = h.searcher.SearchSimilarByID(ctx, chunkID, opts)
	} else if fileLoc != "" {
		parts := splitFileLoc(fileLoc)
		if len(parts) != 2 {
			h.jsonError(w, "invalid file_loc format (expected file:line)", http.StatusBadRequest)
			return
		}
		line, lineErr := strconv.Atoi(parts[1])
		if lineErr != nil {
			h.jsonError(w, "invalid line number in file_loc", http.StatusBadRequest)
			return
		}
		results, err = h.searcher.SearchSimilarByLocation(ctx, parts[0], line, opts)
	} else if text != "" {
		results, err = h.searcher.SearchSimilarByText(ctx, text, opts)
	}

	if err != nil {
		h.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.jsonResponse(w, map[string]interface{}{
		"count":   len(results),
		"results": results,
	})
}

// splitFileLoc splits a file:line string into parts.
func splitFileLoc(loc string) []string {
	// Find the last colon (to handle Windows paths like C:\foo\bar:10)
	lastColon := -1
	for i := len(loc) - 1; i >= 0; i-- {
		if loc[i] == ':' {
			lastColon = i
			break
		}
	}
	if lastColon <= 0 || lastColon == len(loc)-1 {
		return nil
	}
	return []string{loc[:lastColon], loc[lastColon+1:]}
}
