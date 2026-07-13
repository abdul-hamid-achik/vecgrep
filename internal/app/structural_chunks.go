package app

import (
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- compatibility identifier, not cryptography
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

const (
	structuralExportSchemaVersion = 1
	// Symbol chunks are ultimately embedded into a 4096-byte model budget. Ask
	// codemap for a modest source window rather than retaining 256 KiB for every
	// symbol. Signature and docstring are independently capped by the producer,
	// so the decoded page can still be several times this amount.
	structuralExportPageLimit       = 128
	structuralExportMaxContent      = 16 * 1024
	structuralExportPageTimeout     = 30 * time.Second
	structuralExportMaxPageOutput   = 64 * 1024 * 1024
	structuralExportMaxTotalOutput  = 128 * 1024 * 1024
	structuralPreviewMaxBytes       = 4096
	structuralEmbeddingMaxBytes     = 4096
	structuralEmbeddingDocBytes     = 1024
	structuralEmbeddingSignatureMax = 512
	structuralChunkProfileVersion   = "vecgrep-codemap-structural-consumer-v3-lossless"
)

// StructuralChunksMode controls whether indexing consumes codemap's stable
// structural export contract.
type StructuralChunksMode string

const (
	StructuralChunksAuto     StructuralChunksMode = "auto"
	StructuralChunksOff      StructuralChunksMode = "off"
	StructuralChunksRequired StructuralChunksMode = "required"
)

// ParseStructuralChunksMode validates an index flag or config value. Empty is
// treated as auto so older configuration files remain compatible.
func ParseStructuralChunksMode(value string) (StructuralChunksMode, error) {
	switch StructuralChunksMode(strings.ToLower(strings.TrimSpace(value))) {
	case "", StructuralChunksAuto:
		return StructuralChunksAuto, nil
	case StructuralChunksOff:
		return StructuralChunksOff, nil
	case StructuralChunksRequired:
		return StructuralChunksRequired, nil
	default:
		return "", fmt.Errorf("invalid structural chunks mode %q: want auto, off, or required", value)
	}
}

// ConfigureStructuralChunks wires the CLI adapter into an indexer. override is
// normally the --structural-chunks value; empty preserves configuration. Auto
// is deliberately best-effort. Required resolves the binary eagerly and makes
// any export/validation error fail the index run.
func ConfigureStructuralChunks(idx *index.Indexer, cfg config.CodemapConfig, override string) error {
	_, err := configureStructuralChunks(idx, cfg, override)
	return err
}

type structuralChunksSetup struct {
	RequestedMode  StructuralChunksMode
	SourceEnabled  bool
	FallbackCode   string
	FallbackReason string
}

func configureStructuralChunks(idx *index.Indexer, cfg config.CodemapConfig, override string) (structuralChunksSetup, error) {
	var setup structuralChunksSetup
	if idx == nil {
		return setup, fmt.Errorf("indexer is nil")
	}
	configured := cfg.StructuralChunks
	if override != "" {
		configured = override
	}
	mode, err := ParseStructuralChunksMode(configured)
	if err != nil {
		return setup, err
	}
	setup.RequestedMode = mode
	if mode == StructuralChunksOff {
		idx.SetStructuralChunkSource(nil, false)
		return setup, nil
	}
	if mode == StructuralChunksAuto && !cfg.Enabled {
		idx.SetStructuralChunkSource(nil, false)
		setup.FallbackCode = "codemap_disabled"
		setup.FallbackReason = "codemap integration is disabled"
		return setup, nil
	}

	bin := cfg.Bin
	if bin == "" {
		bin = "codemap"
	}
	resolved, err := config.ResolveBinary(bin)
	if err != nil {
		if mode == StructuralChunksRequired {
			return setup, fmt.Errorf("codemap structural chunks required: %w", err)
		}
		idx.SetStructuralChunkSource(nil, false)
		setup.FallbackCode = "producer_unavailable"
		setup.FallbackReason = "codemap producer is unavailable"
		return setup, nil
	}
	idx.SetStructuralChunkSource(newCodemapStructuralSource(resolved), mode == StructuralChunksRequired)
	setup.SourceEnabled = true
	return setup, nil
}

type structuralExportReport struct {
	SchemaVersion    int                      `json:"schema_version"`
	Project          string                   `json:"project"`
	ProjectKey       string                   `json:"project_key"`
	IndexFingerprint string                   `json:"index_fingerprint"`
	Offset           int                      `json:"offset"`
	Limit            int                      `json:"limit"`
	MaxContentBytes  int                      `json:"max_content_bytes"`
	TotalRecords     int                      `json:"total_records"`
	ReturnedRecords  int                      `json:"returned_records"`
	Complete         bool                     `json:"complete"`
	NextOffset       int                      `json:"next_offset"`
	Records          []structuralSymbolRecord `json:"records"`
}

type structuralSymbolRecord struct {
	SchemaVersion      int    `json:"schema_version"`
	Ordinal            *int   `json:"ordinal,omitempty"`
	Project            string `json:"project"`
	ProjectKey         string `json:"project_key"`
	IndexFingerprint   string `json:"index_fingerprint"`
	File               string `json:"file"`
	StartLine          int    `json:"start_line"`
	EndLine            int    `json:"end_line"`
	Symbol             string `json:"symbol"`
	FQN                string `json:"fqn"`
	Kind               string `json:"kind"`
	Language           string `json:"language"`
	Signature          string `json:"signature"`
	Docstring          string `json:"docstring"`
	SourceHash         string `json:"source_hash"`
	Content            string `json:"content"`
	ContentHash        string `json:"content_hash"`
	SignatureTruncated bool   `json:"signature_truncated"`
	DocstringTruncated bool   `json:"docstring_truncated"`
	ContentTruncated   bool   `json:"content_truncated"`
	ContentOmitted     bool   `json:"content_omitted"`
	OmissionReason     string `json:"omission_reason"`
	FileStale          bool   `json:"file_stale"`
}

type structuralPageRunner func(context.Context, string, string, int, int, int) ([]byte, error)

type codemapStructuralSource struct {
	bin           string
	pageLimit     int
	maxContent    int
	maxTotalBytes int
	runPage       structuralPageRunner
}

func newCodemapStructuralSource(bin string) *codemapStructuralSource {
	return &codemapStructuralSource{
		bin:           bin,
		pageLimit:     structuralExportPageLimit,
		maxContent:    structuralExportMaxContent,
		maxTotalBytes: structuralExportMaxTotalOutput,
		runPage:       runCodemapStructuralPage,
	}
}

func runCodemapStructuralPage(ctx context.Context, bin, projectRoot string, offset, limit, maxContent int) ([]byte, error) {
	pageCtx, cancel := context.WithTimeout(ctx, structuralExportPageTimeout)
	defer cancel()
	cmd := exec.CommandContext(pageCtx, bin,
		"export-symbols", "--json",
		"--offset", strconv.Itoa(offset),
		"--limit", strconv.Itoa(limit),
		"--max-content-bytes", strconv.Itoa(maxContent),
	)
	cmd.Dir = projectRoot
	stdout := newCappedCommandOutput(structuralExportMaxPageOutput)
	stderr := newCappedCommandOutput(64 * 1024)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err != nil {
		if pageCtx.Err() != nil {
			return nil, fmt.Errorf("codemap export-symbols: %w", pageCtx.Err())
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = codemapFailureMessage(stdout.Bytes())
		}
		if message != "" {
			return nil, fmt.Errorf("codemap export-symbols: %s", message)
		}
		return nil, fmt.Errorf("codemap export-symbols: %w", err)
	}
	if stdout.Overflowed() {
		return nil, fmt.Errorf("codemap export-symbols exceeded the %d-byte page output limit", structuralExportMaxPageOutput)
	}
	return stdout.Bytes(), nil
}

// codemapFailureMessage decodes codemap's stable --json hard-error envelope,
// which is written to stdout by contract. Never echo arbitrary/partial stdout:
// a crashed producer may have emitted source records before failing.
func codemapFailureMessage(out []byte) string {
	var envelope struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		Code  string `json:"code"`
		Hint  string `json:"hint"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil || envelope.OK || envelope.Error == "" {
		return ""
	}
	message := envelope.Error
	if envelope.Code != "" {
		message += " [" + envelope.Code + "]"
	}
	if envelope.Hint != "" {
		message += "; " + envelope.Hint
	}
	return message
}

type cappedCommandOutput struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func newCappedCommandOutput(limit int) *cappedCommandOutput {
	return &cappedCommandOutput{limit: limit}
}

func (b *cappedCommandOutput) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		keep := min(remaining, len(p))
		_, _ = b.buf.Write(p[:keep])
	}
	if len(p) > remaining {
		b.overflow = true
	}
	return written, nil
}

func (b *cappedCommandOutput) Bytes() []byte    { return b.buf.Bytes() }
func (b *cappedCommandOutput) String() string   { return b.buf.String() }
func (b *cappedCommandOutput) Overflowed() bool { return b.overflow }

func (s *codemapStructuralSource) LoadStructuralChunks(ctx context.Context, projectRoot string) (*index.StructuralChunkSet, error) {
	if s == nil || s.bin == "" || s.runPage == nil {
		return nil, fmt.Errorf("codemap structural adapter unavailable")
	}
	expectedProjectKey, err := structuralProjectKey(projectRoot)
	if err != nil {
		return nil, err
	}

	var (
		records     []structuralSymbolRecord
		project     string
		fingerprint string
		total       = -1
		offset      int
		fileIssues  = make(map[string][]error)
		totalBytes  int
		haveLast    bool
		lastRecord  structuralSymbolRecord
		ordinalMode = -1 // -1 unknown, 0 legacy comparator, 1 authoritative ordinal
		selectors   = make(map[string]struct{})
	)
	maxTotalBytes := s.maxTotalBytes
	if maxTotalBytes <= 0 {
		maxTotalBytes = structuralExportMaxTotalOutput
	}
	for {
		out, err := s.runPage(ctx, s.bin, projectRoot, offset, s.pageLimit, s.maxContent)
		if err != nil {
			return nil, err
		}
		totalBytes += len(out)
		if totalBytes > maxTotalBytes {
			return nil, fmt.Errorf("codemap structural export exceeded the %d-byte aggregate budget", maxTotalBytes)
		}
		var page structuralExportReport
		if err := json.Unmarshal(out, &page); err != nil {
			return nil, fmt.Errorf("decode codemap structural export page at offset %d: %w", offset, err)
		}
		if err := validateStructuralPage(page, offset, s.pageLimit, s.maxContent, expectedProjectKey, project, fingerprint, total); err != nil {
			return nil, err
		}
		if offset == 0 {
			project = page.Project
			fingerprint = page.IndexFingerprint
			total = page.TotalRecords
		}
		for _, record := range page.Records {
			selectorKey, issue, err := validateStructuralRecord(record, page)
			if err != nil {
				return nil, err
			}
			hasOrdinal := record.Ordinal != nil
			if ordinalMode < 0 {
				ordinalMode = 0
				if hasOrdinal {
					ordinalMode = 1
				}
			} else if hasOrdinal != (ordinalMode == 1) {
				return nil, fmt.Errorf("codemap structural export mixed ordinal and legacy records at %s", selectorKey)
			}
			if hasOrdinal {
				expected := len(records) + 1
				if *record.Ordinal != expected {
					return nil, fmt.Errorf("codemap structural record ordinal = %d, want %d at %s", *record.Ordinal, expected, selectorKey)
				}
			} else if haveLast && !structuralRecordLess(lastRecord, record) {
				return nil, fmt.Errorf("legacy codemap structural records are duplicated or out of producer order at %s", selectorKey)
			}
			haveLast = true
			lastRecord = record
			if _, duplicate := selectors[selectorKey]; duplicate {
				fileIssues[record.File] = append(fileIssues[record.File], fmt.Errorf("codemap structural selector is duplicated for %s:%d", record.File, record.StartLine))
			} else {
				selectors[selectorKey] = struct{}{}
			}
			if issue != nil {
				fileIssues[record.File] = append(fileIssues[record.File], issue)
			}
			records = append(records, record)
		}
		if page.Complete {
			break
		}
		offset = page.NextOffset
	}
	if len(records) != total {
		return nil, fmt.Errorf("codemap structural export record count mismatch: got %d, want %d", len(records), total)
	}
	return buildStructuralChunkSet(projectRoot, expectedProjectKey, fingerprint, records, fileIssues), nil
}

func validateStructuralPage(page structuralExportReport, offset, limit, maxContent int, projectKey, project, fingerprint string, total int) error {
	if page.SchemaVersion != structuralExportSchemaVersion {
		return fmt.Errorf("unsupported codemap structural export schema %d", page.SchemaVersion)
	}
	if page.Project == "" || page.ProjectKey != projectKey || !validLowerHex(page.ProjectKey, 12) {
		return fmt.Errorf("codemap structural export project_key mismatch")
	}
	if !validLowerHex(page.IndexFingerprint, 64) {
		return fmt.Errorf("codemap structural export has invalid index_fingerprint")
	}
	if offset > 0 && (page.Project != project || page.IndexFingerprint != fingerprint || page.TotalRecords != total) {
		return fmt.Errorf("codemap structural export changed between pages")
	}
	if page.Offset != offset || page.Limit != limit || page.MaxContentBytes != maxContent {
		return fmt.Errorf("codemap structural export pagination contract mismatch at offset %d", offset)
	}
	if page.TotalRecords < 0 || page.ReturnedRecords != len(page.Records) || page.ReturnedRecords > page.Limit {
		return fmt.Errorf("codemap structural export has invalid record counts at offset %d", offset)
	}
	end := page.Offset + page.ReturnedRecords
	if page.Complete {
		if page.NextOffset != 0 || end != page.TotalRecords {
			return fmt.Errorf("codemap structural export has invalid final page at offset %d", offset)
		}
		return nil
	}
	if page.ReturnedRecords == 0 || page.NextOffset != end || page.NextOffset >= page.TotalRecords {
		return fmt.Errorf("codemap structural export has invalid next_offset at offset %d", offset)
	}
	return nil
}

func validateStructuralRecord(record structuralSymbolRecord, page structuralExportReport) (string, error, error) {
	if record.SchemaVersion != structuralExportSchemaVersion || record.Project != page.Project || record.ProjectKey != page.ProjectKey || record.IndexFingerprint != page.IndexFingerprint {
		return "", nil, fmt.Errorf("codemap structural record envelope mismatch for %s", record.File)
	}
	clean := filepath.Clean(filepath.FromSlash(record.File))
	if record.File == "" || filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.ToSlash(clean) != record.File {
		return "", nil, fmt.Errorf("codemap structural record has unsafe file path %q", record.File)
	}
	key := structuralSelectorKey(record)
	if record.StartLine < 1 || record.EndLine < record.StartLine || record.Kind == "" || record.Language == "" {
		return key, fmt.Errorf("codemap structural record has invalid selector for %s:%d", record.File, record.StartLine), nil
	}
	if record.FileStale || record.ContentOmitted || record.OmissionReason != "" {
		return key, fmt.Errorf("codemap structural record is stale or omitted for %s:%d", record.File, record.StartLine), nil
	}
	if !validLowerHex(record.ContentHash, 64) {
		return key, fmt.Errorf("codemap structural record has invalid content_hash for %s:%d", record.File, record.StartLine), nil
	}
	if !record.ContentTruncated && sha256Hex(record.Content) != record.ContentHash {
		return key, fmt.Errorf("codemap structural record content_hash mismatch for %s:%d", record.File, record.StartLine), nil
	}
	if !validLowerHex(record.SourceHash, 64) {
		return key, fmt.Errorf("codemap structural record has invalid source_hash for %s:%d", record.File, record.StartLine), nil
	}
	return key, nil, nil
}

func structuralSelectorKey(record structuralSymbolRecord) string {
	return fmt.Sprintf("%s\x00%012d\x00%s\x00%s", record.File, record.StartLine, record.FQN, record.Kind)
}

// structuralRecordLess validates transitional v1 producers that predate the
// authoritative ordinal field. Signature/docstring truncation means a consumer
// cannot reconstruct every full producer tie-breaker; current producers always
// emit Ordinal and bypass this compatibility path.
func structuralRecordLess(a, b structuralSymbolRecord) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	if a.StartLine != b.StartLine {
		return a.StartLine < b.StartLine
	}
	if a.EndLine != b.EndLine {
		return a.EndLine < b.EndLine
	}
	if a.FQN != b.FQN {
		return a.FQN < b.FQN
	}
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	if a.Symbol != b.Symbol {
		return a.Symbol < b.Symbol
	}
	if a.Language != b.Language {
		return a.Language < b.Language
	}
	if a.Signature != b.Signature {
		return a.Signature < b.Signature
	}
	if a.Docstring != b.Docstring {
		return a.Docstring < b.Docstring
	}
	return a.SourceHash < b.SourceHash
}

func buildStructuralChunkSet(projectRoot, projectKey, fingerprint string, records []structuralSymbolRecord, fileIssues map[string][]error) *index.StructuralChunkSet {
	byFile := make(map[string][]structuralSymbolRecord)
	for _, record := range records {
		byFile[filepath.Clean(filepath.FromSlash(record.File))] = append(byFile[filepath.Clean(filepath.FromSlash(record.File))], record)
	}
	set := &index.StructuralChunkSet{
		ProjectKey:       projectKey,
		IndexFingerprint: fingerprint,
		Complete:         true,
		Files:            make(map[string]index.StructuralFileChunks, len(byFile)),
	}
	paths := make([]string, 0, len(byFile))
	for rel := range byFile {
		paths = append(paths, rel)
	}
	slices.Sort(paths)
	for _, rel := range paths {
		fileRecords := byFile[rel]
		contractPath := filepath.ToSlash(rel)
		if issues := fileIssues[contractPath]; len(issues) > 0 {
			set.Issues = append(set.Issues, fmt.Errorf("%s: %w", contractPath, errors.Join(issues...)))
			continue
		}
		content, err := readStructuralFile(projectRoot, rel)
		if err != nil {
			set.Issues = append(set.Issues, err)
			continue
		}
		profile := sha256.New()
		_, _ = profile.Write([]byte(structuralChunkProfileVersion))
		_, _ = profile.Write([]byte{0})
		usable := true
		for _, record := range fileRecords {
			current := structuralLineRange(content, record.StartLine, record.EndLine)
			current = strings.ToValidUTF8(current, "\uFFFD")
			if sha256Hex(current) != record.ContentHash || (!record.ContentTruncated && current != record.Content) || (record.ContentTruncated && !strings.HasPrefix(current, record.Content)) {
				set.Issues = append(set.Issues, fmt.Errorf("source changed while consuming codemap structural export for %s:%d", record.File, record.StartLine))
				usable = false
				break
			}
			for _, field := range []string{record.File, strconv.Itoa(record.StartLine), strconv.Itoa(record.EndLine), record.Symbol, record.FQN, record.Kind, record.Language, record.Signature, record.Docstring, record.SourceHash, record.ContentHash} {
				_, _ = profile.Write([]byte(field))
				_, _ = profile.Write([]byte{0})
			}
		}
		if !usable {
			continue
		}
		chunks := composeStructuralChunks(content, fileRecords)
		set.Files[rel] = index.StructuralFileChunks{
			FileHash:    sha256HexBytes(content),
			FileSize:    int64(len(content)),
			ProfileHash: hex.EncodeToString(profile.Sum(nil)),
			Chunks:      chunks,
		}
	}
	return set
}

// composeStructuralChunks turns possibly nested symbol spans into a complete,
// non-overlapping source partition. Each line belongs to the narrowest symbol
// that covers it; uncovered lines become generic chunks. This preserves file
// prologues, imports, globals, and container regions such as Vue template/style
// while avoiding duplicated class bodies around nested methods.
func composeStructuralChunks(content []byte, records []structuralSymbolRecord) []index.Chunk {
	if len(content) == 0 {
		return nil
	}
	lineStarts := structuralLineStarts(content)
	owners := make([]int, len(lineStarts))
	for i := range owners {
		owners[i] = -1
	}
	for recordIndex, record := range records {
		start := max(record.StartLine-1, 0)
		end := min(record.EndLine-1, len(owners)-1)
		if start > end {
			continue
		}
		span := end - start
		for line := start; line <= end; line++ {
			current := owners[line]
			if current < 0 || span < structuralRecordSpan(records[current], len(owners)) {
				owners[line] = recordIndex
			}
		}
	}

	chunks := make([]index.Chunk, 0, len(records)+1)
	for start := 0; start < len(owners); {
		owner := owners[start]
		end := start
		for end+1 < len(owners) && owners[end+1] == owner {
			end++
		}
		startByte := lineStarts[start]
		endByte := len(content)
		if end+1 < len(lineStarts) {
			endByte = lineStarts[end+1]
		}
		if startByte < endByte {
			source := strings.ToValidUTF8(string(content[startByte:endByte]), "\uFFFD")
			if owner < 0 {
				chunks = append(chunks, boundedStructuralChunks(source, start+1, nil)...)
			} else {
				record := records[owner]
				chunks = append(chunks, boundedStructuralChunks(source, start+1, &record)...)
			}
		}
		start = end + 1
	}
	return chunks
}

func structuralLineStarts(content []byte) []int {
	starts := []int{0}
	for i, b := range content {
		if b == '\n' && i+1 < len(content) {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func structuralRecordSpan(record structuralSymbolRecord, lineCount int) int {
	start := max(record.StartLine-1, 0)
	end := min(record.EndLine-1, lineCount-1)
	if end < start {
		return lineCount
	}
	return end - start
}

func boundedStructuralChunks(source string, startLine int, record *structuralSymbolRecord) []index.Chunk {
	var chunks []index.Chunk
	maxSourceBytes := structuralPreviewMaxBytes
	if record != nil {
		maxSourceBytes = min(maxSourceBytes, structuralEmbeddingSourceBudget(*record))
	}
	for len(source) > 0 {
		end := len(source)
		if end > maxSourceBytes {
			end = maxSourceBytes
			for end > 0 && !utf8.RuneStart(source[end]) {
				end--
			}
			if newline := strings.LastIndexByte(source[:end], '\n'); newline >= end/2 {
				end = newline + 1
			}
		}
		if end == 0 {
			_, size := utf8.DecodeRuneInString(source)
			end = size
		}
		piece := source[:end]
		newlines := strings.Count(piece, "\n")
		endLine := startLine + newlines
		if strings.HasSuffix(piece, "\n") && endLine > startLine {
			endLine--
		}
		chunk := index.Chunk{
			Content:   piece,
			StartLine: startLine,
			EndLine:   endLine,
			ChunkType: index.ChunkTypeGeneric,
			Origin:    index.ChunkOriginGap,
		}
		if record != nil {
			chunk.ChunkType = structuralChunkType(record.Kind)
			chunk.SymbolName = structuralSymbolName(*record)
			chunk.EmbeddingContent = structuralEmbeddingTextForSource(*record, piece)
			chunk.Origin = index.ChunkOriginStructural
		}
		chunks = append(chunks, chunk)
		startLine += newlines
		source = source[end:]
	}
	return chunks
}

func structuralEmbeddingSourceBudget(record structuralSymbolRecord) int {
	used := 0
	parts := 0
	for _, field := range []struct {
		value string
		limit int
	}{
		{value: record.Docstring, limit: structuralEmbeddingDocBytes},
		{value: record.Signature, limit: structuralEmbeddingSignatureMax},
	} {
		if strings.TrimSpace(field.value) == "" {
			continue
		}
		value, _ := truncateStructuralUTF8(field.value, field.limit)
		if strings.TrimSpace(value) == "" {
			continue
		}
		used += len(value)
		parts++
	}
	if parts > 0 {
		used += 2 * parts
	}
	return max(1, structuralEmbeddingMaxBytes-used)
}

func readStructuralFile(projectRoot, rel string) ([]byte, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve structural project root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve structural project root symlinks: %w", err)
	}
	joined := filepath.Join(root, rel)
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return nil, fmt.Errorf("read structural source %s: %w", rel, err)
	}
	within, err := filepath.Rel(root, resolved)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("structural source escapes project: %s", rel)
	}
	// EvalSymlinks establishes containment, but ReadFile on a FIFO/device can
	// block indefinitely. Inspect the resolved final component without following
	// a newly-swapped symlink, then follow it once more for the effective type.
	// A replacement after these checks remains a narrow TOCTOU window; callers
	// still get deterministic protection for stable and legacy export paths.
	lstat, err := os.Lstat(resolved)
	if err != nil {
		return nil, fmt.Errorf("inspect structural source %s: %w", rel, err)
	}
	if !lstat.Mode().IsRegular() {
		return nil, fmt.Errorf("structural source %s is not a regular file", rel)
	}
	stat, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("inspect structural source %s: %w", rel, err)
	}
	if !stat.Mode().IsRegular() {
		return nil, fmt.Errorf("structural source %s is not a regular file", rel)
	}
	content, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("read structural source %s: %w", rel, err)
	}
	return content, nil
}

func structuralProjectKey(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve structural project root: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	h := sha1.Sum([]byte(abs)) // #nosec G401 -- mirrors codemap's public project_key contract
	return hex.EncodeToString(h[:])[:12], nil
}

func structuralLineRange(content []byte, start, end int) string {
	lines := strings.Split(string(content), "\n")
	if end > len(lines) {
		end = len(lines)
	}
	if start < 1 || start > end {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}

func structuralEmbeddingText(record structuralSymbolRecord) string {
	return structuralEmbeddingTextForSource(record, record.Content)
}

func structuralEmbeddingTextForSource(record structuralSymbolRecord, source string) string {
	parts := make([]string, 0, 3)
	appendPart := func(value string, maxBytes int) {
		if strings.TrimSpace(value) == "" || maxBytes <= 0 {
			return
		}
		value, _ = truncateStructuralUTF8(value, maxBytes)
		if strings.TrimSpace(value) != "" {
			parts = append(parts, value)
		}
	}
	appendPart(record.Docstring, structuralEmbeddingDocBytes)
	appendPart(record.Signature, structuralEmbeddingSignatureMax)
	used := 0
	for _, part := range parts {
		used += len(part)
	}
	if len(parts) > 0 {
		used += 2 * len(parts) // includes the separator before source
	}
	appendPart(source, structuralEmbeddingMaxBytes-used)
	joined := strings.Join(parts, "\n\n")
	joined, _ = truncateStructuralUTF8(joined, structuralEmbeddingMaxBytes)
	return joined
}

func structuralPreviewText(content string) string {
	preview, _ := truncateStructuralUTF8(content, structuralPreviewMaxBytes)
	return preview
}

func truncateStructuralUTF8(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return value, false
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end], true
}

func structuralChunkType(kind string) index.ChunkType {
	switch strings.ToLower(kind) {
	case "function", "method", "test", "constructor":
		return index.ChunkTypeFunction
	case "class", "type", "struct", "interface", "enum":
		return index.ChunkTypeClass
	default:
		return index.ChunkTypeBlock
	}
}

func structuralSymbolName(record structuralSymbolRecord) string {
	if record.FQN != "" {
		return record.FQN
	}
	return record.Symbol
}

func validLowerHex(value string, size int) bool {
	if len(value) != size || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sha256Hex(value string) string { return sha256HexBytes([]byte(value)) }

func sha256HexBytes(value []byte) string {
	h := sha256.Sum256(value)
	return hex.EncodeToString(h[:])
}
