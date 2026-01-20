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
			ChunkID:      r.ChunkID,
			FilePath:     r.RelativePath,
			Content:      r.Content,
			StartLine:    r.StartLine,
			EndLine:      r.EndLine,
			ChunkType:    r.ChunkType,
			SymbolName:   r.SymbolName,
			Language:     r.Language,
			Score:        r.Score,
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
