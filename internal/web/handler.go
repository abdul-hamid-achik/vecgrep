package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"
	"unicode"

	"github.com/abdul-hamid-achik/vecgrep/internal/search"
	"github.com/abdul-hamid-achik/vecgrep/internal/version"
	"github.com/abdul-hamid-achik/vecgrep/internal/web/templates"
)

// titleCase capitalizes the first letter of a string.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// languageDisplayNames maps internal language identifiers to human-readable names.
var languageDisplayNames = map[string]string{
	"go":         "Go",
	"python":     "Python",
	"javascript": "JavaScript",
	"typescript": "TypeScript",
	"rust":       "Rust",
	"java":       "Java",
	"c":          "C",
	"cpp":        "C++",
	"ruby":       "Ruby",
	"php":        "PHP",
	"swift":      "Swift",
	"kotlin":     "Kotlin",
	"shell":      "Shell",
	"sql":        "SQL",
	"markdown":   "Markdown",
	"json":       "JSON",
	"yaml":       "YAML",
	"toml":       "TOML",
	"html":       "HTML",
	"css":        "CSS",
}

// chunkTypeDisplayNames maps internal chunk type identifiers to human-readable names.
var chunkTypeDisplayNames = map[string]string{
	"function": "Function",
	"class":    "Class",
	"block":    "Block",
	"comment":  "Comment",
	"generic":  "Generic",
}

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

// getIndexedFilters fetches languages and chunk types from the vector DB.
func (h *Handler) getIndexedFilters(ctx context.Context) ([]templates.LanguageOption, []templates.ChunkTypeOption) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	stats, err := h.searcher.GetIndexStats(ctx)
	if err != nil {
		return nil, nil
	}

	var languages []templates.LanguageOption
	var chunkTypes []templates.ChunkTypeOption

	// Extract languages from stats
	if langMap, ok := stats["languages"].(map[string]int64); ok {
		for lang, count := range langMap {
			if lang == "" || lang == "unknown" {
				continue
			}
			displayName := languageDisplayNames[lang]
			if displayName == "" {
				displayName = titleCase(lang)
			}
			languages = append(languages, templates.LanguageOption{
				Value: lang,
				Label: fmt.Sprintf("%s (%d)", displayName, count),
				Count: count,
			})
		}
		// Sort by count descending, then by name
		sort.Slice(languages, func(i, j int) bool {
			if languages[i].Count != languages[j].Count {
				return languages[i].Count > languages[j].Count
			}
			return languages[i].Value < languages[j].Value
		})
	}

	// Extract chunk types from stats
	if typeMap, ok := stats["chunk_types"].(map[string]int64); ok {
		for ct, count := range typeMap {
			if ct == "" {
				continue
			}
			displayName := chunkTypeDisplayNames[ct]
			if displayName == "" {
				displayName = titleCase(ct)
			}
			chunkTypes = append(chunkTypes, templates.ChunkTypeOption{
				Value: ct,
				Label: fmt.Sprintf("%s (%d)", displayName, count),
				Count: count,
			})
		}
		// Sort by count descending
		sort.Slice(chunkTypes, func(i, j int) bool {
			if chunkTypes[i].Count != chunkTypes[j].Count {
				return chunkTypes[i].Count > chunkTypes[j].Count
			}
			return chunkTypes[i].Value < chunkTypes[j].Value
		})
	}

	return languages, chunkTypes
}

// Index renders the main search page.
func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	languages, chunkTypes := h.getIndexedFilters(r.Context())

	component := templates.Index(templates.IndexData{
		Query:      "",
		Results:    nil,
		Languages:  languages,
		ChunkTypes: chunkTypes,
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	component.Render(r.Context(), w)
}

// Search handles search requests (for HTMX and direct access).
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
	if mode := r.URL.Query().Get("mode"); mode != "" {
		switch mode {
		case "semantic":
			opts.Mode = search.SearchModeSemantic
		case "keyword":
			opts.Mode = search.SearchModeKeyword
		case "hybrid":
			opts.Mode = search.SearchModeHybrid
		}
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

	// If not an HTMX request, render full page (for bookmarks/refreshes)
	if r.Header.Get("HX-Request") == "" {
		languages, chunkTypes := h.getIndexedFilters(r.Context())
		component := templates.Index(templates.IndexData{
			Query:      query,
			Results:    templateResults,
			Languages:  languages,
			ChunkTypes: chunkTypes,
		})
		component.Render(r.Context(), w)
		return
	}

	// HTMX request: return fragment only
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

	languages, chunkTypes := h.getIndexedFilters(r.Context())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	templates.StatusPage(stats, languages, chunkTypes).Render(r.Context(), w)
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
	if mode := r.URL.Query().Get("mode"); mode != "" {
		switch mode {
		case "semantic":
			opts.Mode = search.SearchModeSemantic
		case "keyword":
			opts.Mode = search.SearchModeKeyword
		case "hybrid":
			opts.Mode = search.SearchModeHybrid
		}
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

// APILanguages returns the indexed languages and chunk types as JSON.
func (h *Handler) APILanguages(w http.ResponseWriter, r *http.Request) {
	languages, chunkTypes := h.getIndexedFilters(r.Context())

	h.jsonResponse(w, map[string]interface{}{
		"languages":   languages,
		"chunk_types": chunkTypes,
	})
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
	languages, chunkTypes := h.getIndexedFilters(r.Context())

	component := templates.Similar(templates.SimilarData{
		ChunkID:    r.URL.Query().Get("chunk_id"),
		FileLoc:    r.URL.Query().Get("file_loc"),
		Text:       r.URL.Query().Get("text"),
		Results:    nil,
		Languages:  languages,
		ChunkTypes: chunkTypes,
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	component.Render(r.Context(), w)
}

// SimilarSearch handles similar code search requests (for HTMX and direct access).
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

	// If not an HTMX request, render full page (for bookmarks/refreshes)
	if r.Header.Get("HX-Request") == "" {
		languages, chunkTypes := h.getIndexedFilters(r.Context())
		component := templates.Similar(templates.SimilarData{
			ChunkID:    chunkIDStr,
			FileLoc:    fileLoc,
			Text:       text,
			Results:    templateResults,
			Languages:  languages,
			ChunkTypes: chunkTypes,
		})
		component.Render(r.Context(), w)
		return
	}

	// HTMX request: return fragment only
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
