package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/search"
)

type SearchRequest struct {
	Query       string
	Limit       int
	Mode        search.SearchMode
	Languages   []string
	Language    string
	ChunkTypes  []string
	ChunkType   string
	FilePattern string
	Directory   string
	MinLine     int
	MaxLine     int
	ProjectRoot string
	Explain     bool
}

type SearchResponse struct {
	Results     []search.Result
	Diagnostics *search.SearchExplanation
	Mode        search.SearchMode
	Duration    time.Duration
}

type SimilarTargetKind string

const (
	SimilarTargetID       SimilarTargetKind = "id"
	SimilarTargetLocation SimilarTargetKind = "location"
	SimilarTargetText     SimilarTargetKind = "text"
)

type SimilarTarget struct {
	Kind     SimilarTargetKind
	ChunkID  int64
	FilePath string
	Line     int
	Text     string
}

type SimilarRequest struct {
	Target          SimilarTarget
	Limit           int
	Language        string
	Languages       []string
	ChunkType       string
	ChunkTypes      []string
	FilePattern     string
	Directory       string
	MinLine         int
	MaxLine         int
	ExcludeSameFile bool
}

func (s *Service) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	if s == nil || s.session == nil {
		return nil, fmt.Errorf("service not initialized")
	}

	mode := req.Mode
	if mode == "" {
		mode = ParseSearchMode("", s.session.Config.Search.DefaultMode)
	}

	if (mode == search.SearchModeSemantic || mode == search.SearchModeHybrid) && s.session.Provider == nil {
		return nil, ErrProviderRequired
	}
	if mode == search.SearchModeSemantic || mode == search.SearchModeHybrid {
		if err := s.ensureEmbeddingProfileMatches(); err != nil {
			return nil, err
		}
	}

	opts := search.SearchOptions{
		Limit:        req.Limit,
		Language:     req.Language,
		Languages:    req.Languages,
		ChunkType:    req.ChunkType,
		ChunkTypes:   req.ChunkTypes,
		FilePattern:  req.FilePattern,
		Directory:    req.Directory,
		MinLine:      req.MinLine,
		MaxLine:      req.MaxLine,
		ProjectRoot:  req.ProjectRoot,
		Mode:         mode,
		VectorWeight: s.session.Config.Search.VectorWeight,
		TextWeight:   s.session.Config.Search.TextWeight,
		Explain:      req.Explain,
	}
	if opts.ProjectRoot == "" {
		opts.ProjectRoot = s.session.ProjectRoot
	}

	searcher := search.NewSearcher(s.session.DB, s.session.Provider)
	start := time.Now()

	var (
		results []search.Result
		diag    *search.SearchExplanation
		err     error
	)
	if req.Explain && mode != search.SearchModeKeyword {
		results, diag, err = searcher.SearchWithExplain(ctx, req.Query, opts)
	} else {
		results, err = searcher.Search(ctx, req.Query, opts)
		if req.Explain {
			diag = &search.SearchExplanation{Mode: mode, Duration: time.Since(start)}
		}
	}
	if err != nil {
		return nil, err
	}

	return &SearchResponse{
		Results:     results,
		Diagnostics: diag,
		Mode:        mode,
		Duration:    time.Since(start),
	}, nil
}

func (s *Service) Similar(ctx context.Context, req SimilarRequest) (*SearchResponse, error) {
	if s == nil || s.session == nil {
		return nil, fmt.Errorf("service not initialized")
	}
	if err := s.ensureEmbeddingProfileMatches(); err != nil {
		return nil, err
	}

	opts := search.SimilarOptions{
		SearchOptions: search.SearchOptions{
			Limit:       req.Limit,
			Language:    req.Language,
			Languages:   req.Languages,
			ChunkType:   req.ChunkType,
			ChunkTypes:  req.ChunkTypes,
			FilePattern: req.FilePattern,
			Directory:   req.Directory,
			MinLine:     req.MinLine,
			MaxLine:     req.MaxLine,
			ProjectRoot: s.session.ProjectRoot,
		},
		ExcludeSameFile: req.ExcludeSameFile,
		ExcludeSourceID: true,
	}

	searcher := search.NewSearcher(s.session.DB, s.session.Provider)
	start := time.Now()

	var (
		results []search.Result
		err     error
	)
	switch req.Target.Kind {
	case SimilarTargetID:
		results, err = searcher.SearchSimilarByID(ctx, req.Target.ChunkID, opts)
	case SimilarTargetLocation:
		results, err = searcher.SearchSimilarByLocation(ctx, req.Target.FilePath, req.Target.Line, opts)
	case SimilarTargetText:
		if s.session.Provider == nil {
			return nil, ErrProviderRequired
		}
		results, err = searcher.SearchSimilarByText(ctx, req.Target.Text, opts)
	default:
		return nil, fmt.Errorf("unknown similar target: %s", req.Target.Kind)
	}
	if err != nil {
		return nil, err
	}

	return &SearchResponse{
		Results:  results,
		Mode:     search.SearchModeSemantic,
		Duration: time.Since(start),
	}, nil
}

func ParseSimilarTarget(positional, text string) (SimilarTarget, error) {
	if text != "" && positional != "" {
		return SimilarTarget{}, fmt.Errorf("cannot specify both --text and a positional target")
	}
	if text != "" {
		return SimilarTarget{Kind: SimilarTargetText, Text: text}, nil
	}
	if positional == "" {
		return SimilarTarget{}, fmt.Errorf("target required: provide a chunk ID, file:line location, or use --text")
	}

	if chunkID, err := strconv.ParseInt(positional, 10, 64); err == nil {
		return SimilarTarget{Kind: SimilarTargetID, ChunkID: chunkID}, nil
	}

	if strings.Contains(positional, ":") {
		parts := strings.SplitN(positional, ":", 2)
		line, err := strconv.Atoi(parts[1])
		if err != nil {
			return SimilarTarget{}, fmt.Errorf("invalid line number in %s: %w", positional, err)
		}
		return SimilarTarget{Kind: SimilarTargetLocation, FilePath: parts[0], Line: line}, nil
	}

	return SimilarTarget{}, fmt.Errorf("invalid target format: %s (expected chunk ID or file:line)", positional)
}

func ParseSearchMode(modeStr, defaultMode string) search.SearchMode {
	if modeStr == "" {
		modeStr = defaultMode
	}
	switch strings.ToLower(modeStr) {
	case "semantic":
		return search.SearchModeSemantic
	case "keyword":
		return search.SearchModeKeyword
	case "hybrid":
		return search.SearchModeHybrid
	default:
		return search.SearchModeHybrid
	}
}

func ParseLineRange(rangeStr string) (int, int) {
	parts := strings.Split(rangeStr, "-")
	if len(parts) != 2 {
		return 0, 0
	}
	min, _ := strconv.Atoi(parts[0])
	max, _ := strconv.Atoi(parts[1])
	return min, max
}
