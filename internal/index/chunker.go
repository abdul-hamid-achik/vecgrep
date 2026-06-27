// Package index provides file indexing and chunking for semantic search.
package index

import (
	"bufio"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ChunkType represents the type of code chunk.
type ChunkType string

const (
	ChunkTypeFunction ChunkType = "function"
	ChunkTypeClass    ChunkType = "class"
	ChunkTypeBlock    ChunkType = "block"
	ChunkTypeComment  ChunkType = "comment"
	ChunkTypeGeneric  ChunkType = "generic"
)

// Chunk represents a piece of code with its metadata.
type Chunk struct {
	Content    string
	StartLine  int
	EndLine    int
	StartByte  int
	EndByte    int
	ChunkType  ChunkType
	SymbolName string
}

// defaultMaxChunkChars is a hard upper bound on the bytes in any single chunk
// handed to the embedder. nomic-embed-text (the default model) has a 2048-token
// context window and silently truncates beyond it. Tokenization density varies
// (~4 chars/token for prose, ~3 for typical code, as low as ~2 for dense JSON /
// minified blobs), so to stay under 2048 tokens for the *worst realistic* case
// the cap must be <= 2048*2 = 4096 bytes. 4096 also matches the chunker's own
// oversized-split threshold (ChunkSize*2 at the default ChunkSize), so it only
// further splits pathological line-based chunks. Oversized inputs are slow to
// embed and lose their tail to truncation, so every chunk path is clamped to
// this as a final pass.
const defaultMaxChunkChars = 4096

// ChunkerConfig holds configuration for the chunker.
type ChunkerConfig struct {
	ChunkSize    int // Target chunk size in characters (approximation of tokens)
	ChunkOverlap int // Overlap between chunks in characters
	// MaxChunkChars is the hard ceiling on a chunk's size in bytes. Any chunk
	// exceeding it is split on rune boundaries before embedding so the model
	// never truncates oversized input. Zero falls back to defaultMaxChunkChars.
	MaxChunkChars int
}

// DefaultChunkerConfig returns default chunker configuration.
func DefaultChunkerConfig() ChunkerConfig {
	return ChunkerConfig{
		ChunkSize:     2048, // ~512 tokens * 4 chars/token
		ChunkOverlap:  256,  // ~64 tokens * 4 chars/token
		MaxChunkChars: defaultMaxChunkChars,
	}
}

// Chunker splits files into semantic chunks for embedding.
type Chunker struct {
	config ChunkerConfig
}

// NewChunker creates a new Chunker with the given configuration.
func NewChunker(cfg ChunkerConfig) *Chunker {
	if cfg.ChunkSize == 0 {
		cfg.ChunkSize = DefaultChunkerConfig().ChunkSize
	}
	if cfg.ChunkOverlap == 0 {
		cfg.ChunkOverlap = DefaultChunkerConfig().ChunkOverlap
	}
	if cfg.MaxChunkChars <= 0 {
		cfg.MaxChunkChars = defaultMaxChunkChars
	}
	return &Chunker{config: cfg}
}

// ChunkFile splits file content into chunks based on language and structure.
func (c *Chunker) ChunkFile(content string, filename string) []Chunk {
	if content == "" {
		return nil
	}

	lang := DetectLanguage(filename)

	// For certain languages, try semantic chunking first; otherwise fall back
	// to line-based chunking.
	chunks := c.semanticChunk(content, lang)
	if len(chunks) == 0 {
		chunks = c.lineBasedChunk(content)
	}

	// Final safety pass: neither chunker guarantees a hard size bound (a single
	// very long line — minified JS, a long Markdown paragraph, a JSON blob — or
	// a large unsplit block can slip through). Clamp every chunk so the embedder
	// never receives oversized input it would silently truncate.
	return c.enforceMaxChunkChars(chunks)
}

// enforceMaxChunkChars splits any chunk whose content exceeds MaxChunkChars
// BYTES into rune-safe sub-chunks, each at most MaxChunkChars bytes. The bound
// is in bytes because that is what the embedder's token budget tracks; measuring
// the guard in bytes and the split in bytes keeps the two consistent (a
// rune-based split would let a multi-byte chunk slip past the byte guard).
// Line numbers are tracked best-effort by counting newlines; these splits only
// ever hit pathological chunks (huge single lines / unsplit blocks), so
// approximate spans are fine.
func (c *Chunker) enforceMaxChunkChars(chunks []Chunk) []Chunk {
	maxChars := c.config.MaxChunkChars
	if maxChars <= 0 {
		return chunks
	}
	var out []Chunk
	for _, chunk := range chunks {
		if len(chunk.Content) <= maxChars {
			out = append(out, chunk)
			continue
		}
		out = append(out, splitByChars(chunk, maxChars)...)
	}
	return out
}

// splitByChars divides a chunk's content into pieces of at most maxBytes bytes,
// never splitting a multi-byte rune (so a piece may be slightly under maxBytes
// to avoid cutting a rune). StartLine/EndLine are advanced by the number of
// newlines consumed so search results still point near the source.
func splitByChars(chunk Chunk, maxBytes int) []Chunk {
	var parts []Chunk
	startLine := chunk.StartLine
	rest := chunk.Content
	for len(rest) > 0 {
		end := len(rest)
		if end > maxBytes {
			// Back off to the last rune boundary at or before maxBytes so we
			// never emit a piece exceeding the byte cap or split a rune.
			end = maxBytes
			for end > 0 && !utf8.RuneStart(rest[end]) {
				end--
			}
			if end == 0 {
				// A single rune longer than maxBytes (only possible with a tiny
				// cap); take the whole rune to make progress.
				_, sz := utf8.DecodeRuneInString(rest)
				end = sz
			}
		}
		piece := rest[:end]
		newlines := strings.Count(piece, "\n")

		part := chunk
		part.Content = piece
		part.StartLine = startLine
		part.EndLine = startLine + newlines
		// Byte offsets can't be recovered cleanly after splitting; drop them
		// rather than report misleading spans.
		part.StartByte = 0
		part.EndByte = 0
		parts = append(parts, part)

		startLine += newlines
		rest = rest[end:]
	}
	return parts
}

// semanticChunk attempts to chunk based on code structure.
func (c *Chunker) semanticChunk(content string, lang Language) []Chunk {
	var chunks []Chunk

	switch lang {
	case LangGo:
		chunks = c.chunkGo(content)
	case LangPython:
		chunks = c.chunkPython(content)
	case LangJavaScript, LangTypeScript:
		chunks = c.chunkJavaScript(content)
	case LangRust:
		chunks = c.chunkRust(content)
	default:
		return nil
	}

	// If semantic chunking produced too few or too large chunks, fall back
	if len(chunks) == 0 {
		return nil
	}

	// Split any oversized chunks
	var result []Chunk
	for _, chunk := range chunks {
		if len(chunk.Content) > c.config.ChunkSize*2 {
			// This chunk is too big, split it
			subChunks := c.splitOversizedChunk(chunk)
			result = append(result, subChunks...)
		} else {
			result = append(result, chunk)
		}
	}

	return result
}

// chunkGo extracts functions and types from Go code.
func (c *Chunker) chunkGo(content string) []Chunk {
	return c.extractByPatterns(content, []blockPattern{
		{start: "func ", end: "\n}", chunkType: ChunkTypeFunction},
		{start: "type ", end: "\n}", chunkType: ChunkTypeClass},
	})
}

// chunkPython extracts functions and classes from Python code.
func (c *Chunker) chunkPython(content string) []Chunk {
	return c.extractPythonBlocks(content)
}

// chunkJavaScript extracts functions and classes from JS/TS code.
func (c *Chunker) chunkJavaScript(content string) []Chunk {
	return c.extractByPatterns(content, []blockPattern{
		{start: "function ", end: "\n}", chunkType: ChunkTypeFunction},
		{start: "class ", end: "\n}", chunkType: ChunkTypeClass},
		{start: "const ", end: "\n}", chunkType: ChunkTypeBlock},
		{start: "export ", end: "\n}", chunkType: ChunkTypeBlock},
	})
}

// chunkRust extracts functions and structs from Rust code.
func (c *Chunker) chunkRust(content string) []Chunk {
	return c.extractByPatterns(content, []blockPattern{
		{start: "fn ", end: "\n}", chunkType: ChunkTypeFunction},
		{start: "struct ", end: "\n}", chunkType: ChunkTypeClass},
		{start: "impl ", end: "\n}", chunkType: ChunkTypeClass},
		{start: "enum ", end: "\n}", chunkType: ChunkTypeClass},
	})
}

// blockPattern defines a pattern for extracting code blocks.
type blockPattern struct {
	start     string
	end       string
	chunkType ChunkType
}

// extractByPatterns extracts code blocks matching the given patterns.
func (c *Chunker) extractByPatterns(content string, patterns []blockPattern) []Chunk {
	var chunks []Chunk
	lines := strings.Split(content, "\n")
	byteOffset := 0
	lineOffsets := make([]int, len(lines)+1)

	// Calculate byte offsets for each line
	for i, line := range lines {
		lineOffsets[i] = byteOffset
		byteOffset += len(line) + 1 // +1 for newline
	}
	lineOffsets[len(lines)] = byteOffset

	for _, pattern := range patterns {
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, pattern.start) ||
				(pattern.start == "export " && strings.Contains(trimmed, "function ")) {

				// Find the symbol name
				symbolName := extractSymbolName(trimmed, pattern.start)

				// Find the end of this block
				endLine := c.findBlockEnd(lines, i, pattern.end)

				// Build the chunk content
				var contentBuilder strings.Builder
				for j := i; j <= endLine && j < len(lines); j++ {
					contentBuilder.WriteString(lines[j])
					if j < endLine {
						contentBuilder.WriteString("\n")
					}
				}

				chunks = append(chunks, Chunk{
					Content:    contentBuilder.String(),
					StartLine:  i + 1, // 1-indexed
					EndLine:    endLine + 1,
					StartByte:  lineOffsets[i],
					EndByte:    lineOffsets[min(endLine+1, len(lines))],
					ChunkType:  pattern.chunkType,
					SymbolName: symbolName,
				})
			}
		}
	}

	return chunks
}

// extractPythonBlocks extracts Python functions and classes using indentation.
func (c *Chunker) extractPythonBlocks(content string) []Chunk {
	var chunks []Chunk
	lines := strings.Split(content, "\n")
	byteOffset := 0
	lineOffsets := make([]int, len(lines)+1)

	for i, line := range lines {
		lineOffsets[i] = byteOffset
		byteOffset += len(line) + 1
	}
	lineOffsets[len(lines)] = byteOffset

	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		var chunkType ChunkType
		var symbolName string

		if strings.HasPrefix(trimmed, "def ") {
			chunkType = ChunkTypeFunction
			symbolName = extractPythonSymbol(trimmed, "def ")
		} else if strings.HasPrefix(trimmed, "class ") {
			chunkType = ChunkTypeClass
			symbolName = extractPythonSymbol(trimmed, "class ")
		} else if strings.HasPrefix(trimmed, "async def ") {
			chunkType = ChunkTypeFunction
			symbolName = extractPythonSymbol(trimmed, "async def ")
		} else {
			i++
			continue
		}

		// Get the indentation level of the definition
		baseIndent := len(line) - len(strings.TrimLeft(line, " \t"))

		// Find the end of the block (next line with same or less indentation that's not empty/comment)
		endLine := i
		for j := i + 1; j < len(lines); j++ {
			l := lines[j]
			if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") {
				endLine = j
				continue
			}
			indent := len(l) - len(strings.TrimLeft(l, " \t"))
			if indent <= baseIndent {
				break
			}
			endLine = j
		}

		// Build content
		var contentBuilder strings.Builder
		for j := i; j <= endLine; j++ {
			contentBuilder.WriteString(lines[j])
			if j < endLine {
				contentBuilder.WriteString("\n")
			}
		}

		chunks = append(chunks, Chunk{
			Content:    contentBuilder.String(),
			StartLine:  i + 1,
			EndLine:    endLine + 1,
			StartByte:  lineOffsets[i],
			EndByte:    lineOffsets[min(endLine+1, len(lines))],
			ChunkType:  chunkType,
			SymbolName: symbolName,
		})

		i = endLine + 1
	}

	return chunks
}

// findBlockEnd finds the closing brace for a block starting at line i.
func (c *Chunker) findBlockEnd(lines []string, startLine int, endPattern string) int {
	braceCount := 0
	inString := false
	stringChar := byte(0)

	for i := startLine; i < len(lines); i++ {
		line := lines[i]
		for j := 0; j < len(line); j++ {
			ch := line[j]

			// Handle string literals
			if !inString && (ch == '"' || ch == '\'' || ch == '`') {
				inString = true
				stringChar = ch
				continue
			}
			if inString {
				if ch == stringChar && (j == 0 || line[j-1] != '\\') {
					inString = false
				}
				continue
			}

			// Count braces
			if ch == '{' {
				braceCount++
			} else if ch == '}' {
				braceCount--
				if braceCount == 0 {
					return i
				}
			}
		}
	}

	return len(lines) - 1
}

// splitOversizedChunk splits a chunk that's too large into smaller pieces.
func (c *Chunker) splitOversizedChunk(chunk Chunk) []Chunk {
	lines := strings.Split(chunk.Content, "\n")
	var chunks []Chunk

	currentStart := 0
	currentContent := strings.Builder{}
	currentLines := 0

	for i, line := range lines {
		currentContent.WriteString(line)
		if i < len(lines)-1 {
			currentContent.WriteString("\n")
		}
		currentLines++

		if currentContent.Len() >= c.config.ChunkSize {
			chunks = append(chunks, Chunk{
				Content:    currentContent.String(),
				StartLine:  chunk.StartLine + currentStart,
				EndLine:    chunk.StartLine + currentStart + currentLines - 1,
				ChunkType:  chunk.ChunkType,
				SymbolName: chunk.SymbolName,
			})

			// Start new chunk with overlap
			overlapLines := c.config.ChunkOverlap / 80 // Approximate lines for overlap
			if overlapLines < 1 {
				overlapLines = 1
			}
			currentStart = i - overlapLines + 1
			if currentStart < 0 {
				currentStart = 0
			}

			currentContent.Reset()
			currentLines = 0

			// Add overlap lines
			for j := currentStart; j <= i; j++ {
				currentContent.WriteString(lines[j])
				if j < len(lines)-1 {
					currentContent.WriteString("\n")
				}
				currentLines++
			}
		}
	}

	// Add remaining content
	if currentContent.Len() > 0 {
		chunks = append(chunks, Chunk{
			Content:    currentContent.String(),
			StartLine:  chunk.StartLine + currentStart,
			EndLine:    chunk.EndLine,
			ChunkType:  chunk.ChunkType,
			SymbolName: chunk.SymbolName,
		})
	}

	return chunks
}

// lineBasedChunk splits content into chunks based on lines with overlap.
func (c *Chunker) lineBasedChunk(content string) []Chunk {
	var chunks []Chunk
	scanner := bufio.NewScanner(strings.NewReader(content))

	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	if len(lines) == 0 {
		return nil
	}

	// Calculate byte offsets
	byteOffset := 0
	lineOffsets := make([]int, len(lines)+1)
	for i, line := range lines {
		lineOffsets[i] = byteOffset
		byteOffset += len(line) + 1
	}
	lineOffsets[len(lines)] = byteOffset

	// Target lines per chunk (rough estimate: 80 chars per line)
	linesPerChunk := c.config.ChunkSize / 80
	if linesPerChunk < 10 {
		linesPerChunk = 10
	}
	overlapLines := c.config.ChunkOverlap / 80
	if overlapLines < 1 {
		overlapLines = 1
	}

	for start := 0; start < len(lines); {
		end := start + linesPerChunk
		if end > len(lines) {
			end = len(lines)
		}

		// Try to break at a natural boundary (empty line)
		for i := end - 1; i > start+linesPerChunk/2; i-- {
			if strings.TrimSpace(lines[i]) == "" {
				end = i + 1
				break
			}
		}

		var contentBuilder strings.Builder
		for i := start; i < end; i++ {
			contentBuilder.WriteString(lines[i])
			if i < end-1 {
				contentBuilder.WriteString("\n")
			}
		}

		chunks = append(chunks, Chunk{
			Content:   contentBuilder.String(),
			StartLine: start + 1,
			EndLine:   end,
			StartByte: lineOffsets[start],
			EndByte:   lineOffsets[end],
			ChunkType: ChunkTypeGeneric,
		})

		// Move start with overlap
		start = end - overlapLines
		if start <= chunks[len(chunks)-1].StartLine-1 {
			start = end
		}
	}

	return chunks
}

// extractSymbolName extracts the symbol name from a line.
func extractSymbolName(line, prefix string) string {
	// Remove the prefix if present
	idx := strings.Index(line, prefix)
	if idx >= 0 {
		line = line[idx+len(prefix):]
	}

	// Find the symbol name (up to first non-identifier char)
	var name strings.Builder
	for _, r := range line {
		if r == '(' || r == '{' || r == ' ' || r == '<' || r == ':' {
			break
		}
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			name.WriteRune(r)
		}
	}
	return name.String()
}

// extractPythonSymbol extracts symbol name from Python def/class line.
func extractPythonSymbol(line, prefix string) string {
	line = strings.TrimPrefix(line, prefix)
	var name strings.Builder
	for _, r := range line {
		if r == '(' || r == ':' || r == ' ' {
			break
		}
		name.WriteRune(r)
	}
	return name.String()
}

// Language represents a programming language.
type Language string

const (
	LangGo         Language = "go"
	LangPython     Language = "python"
	LangJavaScript Language = "javascript"
	LangTypeScript Language = "typescript"
	LangRust       Language = "rust"
	LangJava       Language = "java"
	LangC          Language = "c"
	LangCPP        Language = "cpp"
	LangRuby       Language = "ruby"
	LangPHP        Language = "php"
	LangSwift      Language = "swift"
	LangKotlin     Language = "kotlin"
	LangShell      Language = "shell"
	LangSQL        Language = "sql"
	LangMarkdown   Language = "markdown"
	LangJSON       Language = "json"
	LangYAML       Language = "yaml"
	LangTOML       Language = "toml"
	LangHTML       Language = "html"
	LangCSS        Language = "css"
	LangUnknown    Language = "unknown"
)

// languageExtensions maps file extensions to languages.
var languageExtensions = map[string]Language{
	".go":    LangGo,
	".py":    LangPython,
	".pyw":   LangPython,
	".js":    LangJavaScript,
	".mjs":   LangJavaScript,
	".cjs":   LangJavaScript,
	".jsx":   LangJavaScript,
	".ts":    LangTypeScript,
	".tsx":   LangTypeScript,
	".mts":   LangTypeScript,
	".rs":    LangRust,
	".java":  LangJava,
	".c":     LangC,
	".h":     LangC,
	".cpp":   LangCPP,
	".cc":    LangCPP,
	".cxx":   LangCPP,
	".hpp":   LangCPP,
	".hxx":   LangCPP,
	".rb":    LangRuby,
	".php":   LangPHP,
	".swift": LangSwift,
	".kt":    LangKotlin,
	".kts":   LangKotlin,
	".sh":    LangShell,
	".bash":  LangShell,
	".zsh":   LangShell,
	".fish":  LangShell,
	".sql":   LangSQL,
	".md":    LangMarkdown,
	".json":  LangJSON,
	".yaml":  LangYAML,
	".yml":   LangYAML,
	".toml":  LangTOML,
	".html":  LangHTML,
	".htm":   LangHTML,
	".css":   LangCSS,
	".scss":  LangCSS,
	".sass":  LangCSS,
	".less":  LangCSS,
}

// DetectLanguage detects the programming language from a filename.
func DetectLanguage(filename string) Language {
	ext := strings.ToLower(filepath.Ext(filename))
	if lang, ok := languageExtensions[ext]; ok {
		return lang
	}

	// Check for special filenames
	base := strings.ToLower(filepath.Base(filename))
	switch {
	case base == "makefile" || base == "gnumakefile":
		return LangShell
	case base == "dockerfile" || strings.HasPrefix(base, "dockerfile."):
		return LangShell
	case base == "jenkinsfile":
		return LangShell
	case strings.HasSuffix(base, "rc") && !strings.Contains(base, "."):
		return LangShell
	}

	return LangUnknown
}

// IsTextFile checks if content appears to be text (not binary).
func IsTextFile(content []byte) bool {
	if len(content) == 0 {
		return true
	}

	// Check first 8KB for null bytes or invalid UTF-8
	checkSize := 8192
	if len(content) < checkSize {
		checkSize = len(content)
	}

	sample := content[:checkSize]

	// Check for null bytes (binary indicator)
	for _, b := range sample {
		if b == 0 {
			return false
		}
	}

	// Check if valid UTF-8
	return utf8.Valid(sample)
}
