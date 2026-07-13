package index

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNewChunker(t *testing.T) {
	// Test with default config
	c := NewChunker(ChunkerConfig{})
	if c.config.ChunkSize != DefaultChunkerConfig().ChunkSize {
		t.Errorf("Expected default ChunkSize %d, got %d", DefaultChunkerConfig().ChunkSize, c.config.ChunkSize)
	}
	if c.config.ChunkOverlap != DefaultChunkerConfig().ChunkOverlap {
		t.Errorf("Expected default ChunkOverlap %d, got %d", DefaultChunkerConfig().ChunkOverlap, c.config.ChunkOverlap)
	}

	// Test with custom config
	cfg := ChunkerConfig{ChunkSize: 1024, ChunkOverlap: 128}
	c = NewChunker(cfg)
	if c.config.ChunkSize != 1024 {
		t.Errorf("Expected ChunkSize 1024, got %d", c.config.ChunkSize)
	}
	if c.config.ChunkOverlap != 128 {
		t.Errorf("Expected ChunkOverlap 128, got %d", c.config.ChunkOverlap)
	}
}

func TestNewChunker_DefaultMaxChunkChars(t *testing.T) {
	c := NewChunker(ChunkerConfig{})
	if c.config.MaxChunkChars != defaultMaxChunkChars {
		t.Errorf("Expected default MaxChunkChars %d, got %d", defaultMaxChunkChars, c.config.MaxChunkChars)
	}
}

// TestChunkFile_EnforcesMaxChars covers the pathological case behind the
// embedding-truncation flood: a file with a single very long line slips past
// the line-based chunker's size targeting. ChunkFile must clamp every chunk to
// MaxChunkChars (bytes) so the embedder never receives oversized input.
func TestChunkFile_EnforcesMaxChars(t *testing.T) {
	const maxChars = 100
	c := NewChunker(ChunkerConfig{ChunkSize: 2048, ChunkOverlap: 256, MaxChunkChars: maxChars})

	// One giant line (no newlines to break on) — the worst case.
	content := strings.Repeat("x", 1000)
	chunks := c.ChunkFile(content, "data.txt")
	if len(chunks) == 0 {
		t.Fatal("expected chunks for non-empty content")
	}
	total := 0
	for i, ch := range chunks {
		if len(ch.Content) > maxChars {
			t.Errorf("chunk %d exceeds MaxChunkChars: %d > %d bytes", i, len(ch.Content), maxChars)
		}
		total += len(ch.Content)
	}
	if total != len(content) {
		t.Errorf("split lost content: got %d chars, want %d", total, len(content))
	}
}

// TestChunkFile_EnforcesMaxChars_MultiByte guards the byte/rune-unit bug: the
// cap is in BYTES, so multi-byte content (CJK/emoji) must still yield chunks
// whose byte length is within the cap, and every chunk must remain valid UTF-8.
func TestChunkFile_EnforcesMaxChars_MultiByte(t *testing.T) {
	const maxChars = 100
	c := NewChunker(ChunkerConfig{ChunkSize: 2048, ChunkOverlap: 256, MaxChunkChars: maxChars})

	// 1000 3-byte runes = 3000 bytes on one line.
	content := strings.Repeat("世", 1000)
	chunks := c.ChunkFile(content, "data.txt")
	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	total := 0
	for i, ch := range chunks {
		if len(ch.Content) > maxChars {
			t.Errorf("chunk %d exceeds byte cap: %d > %d bytes", i, len(ch.Content), maxChars)
		}
		if !utf8.ValidString(ch.Content) {
			t.Errorf("chunk %d is not valid UTF-8 (rune split mid-character)", i)
		}
		total += len(ch.Content)
	}
	if total != len(content) {
		t.Errorf("split lost content: got %d bytes, want %d", total, len(content))
	}
}

// TestSplitByChars_RuneSafe ensures multi-byte runes are never cut in half and
// the byte cap is respected.
func TestSplitByChars_RuneSafe(t *testing.T) {
	chunk := Chunk{Content: strings.Repeat("é", 50), StartLine: 1, EndLine: 1} // 100 bytes
	parts := splitByChars(chunk, 10)
	rejoined := ""
	for _, p := range parts {
		if len(p.Content) > 10 {
			t.Errorf("piece exceeds byte cap: %d > 10 bytes", len(p.Content))
		}
		if !utf8.ValidString(p.Content) {
			t.Error("piece is not valid UTF-8")
		}
		rejoined += p.Content
	}
	if rejoined != chunk.Content {
		t.Errorf("rune-safe split corrupted content")
	}
}

// TestSplitByChars_LineSpansMonotonic checks that splitting a multi-line chunk
// produces non-decreasing, contiguous line spans (best-effort tracking).
func TestSplitByChars_LineSpansMonotonic(t *testing.T) {
	// 60 lines of 50 bytes each (~3060 bytes); cap 100 forces many splits that
	// each span a couple of lines.
	var b strings.Builder
	for i := 0; i < 60; i++ {
		b.WriteString(strings.Repeat("a", 49))
		b.WriteByte('\n')
	}
	chunk := Chunk{Content: b.String(), StartLine: 10, EndLine: 69}
	parts := splitByChars(chunk, 100)
	if len(parts) < 2 {
		t.Fatalf("expected multiple parts, got %d", len(parts))
	}
	prevEnd := chunk.StartLine
	for i, p := range parts {
		if p.StartLine < prevEnd {
			t.Errorf("part %d StartLine %d went backwards (prev end %d)", i, p.StartLine, prevEnd)
		}
		if p.EndLine < p.StartLine {
			t.Errorf("part %d EndLine %d < StartLine %d", i, p.EndLine, p.StartLine)
		}
		prevEnd = p.EndLine
	}
	if got := parts[0].StartLine; got != chunk.StartLine {
		t.Errorf("first part StartLine = %d, want %d", got, chunk.StartLine)
	}
}

func TestChunkFile_EmptyContent(t *testing.T) {
	c := NewChunker(DefaultChunkerConfig())
	chunks := c.ChunkFile("", "test.go")
	if chunks != nil {
		t.Error("Expected nil for empty content")
	}
}

func TestChunkFile_GoFunction(t *testing.T) {
	c := NewChunker(DefaultChunkerConfig())
	content := `package main

func Hello() string {
	return "Hello, World!"
}

func Goodbye() string {
	return "Goodbye!"
}
`
	chunks := c.ChunkFile(content, "main.go")
	if len(chunks) == 0 {
		t.Fatal("Expected at least one chunk")
	}

	// Check that we got function chunks
	foundHello := false
	foundGoodbye := false
	for _, chunk := range chunks {
		if chunk.SymbolName == "Hello" {
			foundHello = true
			if chunk.ChunkType != ChunkTypeFunction {
				t.Errorf("Expected ChunkType 'function', got '%s'", chunk.ChunkType)
			}
		}
		if chunk.SymbolName == "Goodbye" {
			foundGoodbye = true
		}
	}

	if !foundHello {
		t.Error("Expected to find Hello function")
	}
	if !foundGoodbye {
		t.Error("Expected to find Goodbye function")
	}
}

func TestChunkFile_SemanticHintsPreserveUncoveredSource(t *testing.T) {
	c := NewChunker(DefaultChunkerConfig())
	content := "// package documentation\npackage demo\n\nimport \"fmt\"\n\nfunc Run() {\n\tfmt.Println(\"run\")\n}\n\nvar TailSentinel = true\n"
	chunks := c.ChunkFile(content, "main.go")
	if len(chunks) < 3 {
		t.Fatalf("chunks = %+v, want prologue + function + tail", chunks)
	}
	var reconstructed strings.Builder
	for _, chunk := range chunks {
		reconstructed.WriteString(chunk.Content)
	}
	if got := reconstructed.String(); got != content {
		t.Fatalf("semantic chunking lost uncovered source\n--- got ---\n%s\n--- want ---\n%s", got, content)
	}
}

func TestChunkFile_GoType(t *testing.T) {
	c := NewChunker(DefaultChunkerConfig())
	content := `package main

type User struct {
	Name string
	Age  int
}
`
	chunks := c.ChunkFile(content, "types.go")
	if len(chunks) == 0 {
		t.Fatal("Expected at least one chunk")
	}

	found := false
	for _, chunk := range chunks {
		if chunk.SymbolName == "User" && chunk.ChunkType == ChunkTypeClass {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected to find User type")
	}
}

func TestChunkFile_Python(t *testing.T) {
	c := NewChunker(DefaultChunkerConfig())
	content := `def hello():
    return "Hello"

def goodbye():
    return "Goodbye"

class MyClass:
    def __init__(self):
        pass
`
	chunks := c.ChunkFile(content, "main.py")
	if len(chunks) == 0 {
		t.Fatal("Expected at least one chunk")
	}

	// Check for function and class chunks
	var foundHello, foundGoodbye, foundClass bool
	for _, chunk := range chunks {
		if chunk.SymbolName == "hello" && chunk.ChunkType == ChunkTypeFunction {
			foundHello = true
		}
		if chunk.SymbolName == "goodbye" && chunk.ChunkType == ChunkTypeFunction {
			foundGoodbye = true
		}
		if chunk.SymbolName == "MyClass" && chunk.ChunkType == ChunkTypeClass {
			foundClass = true
		}
	}

	if !foundHello {
		t.Error("Expected to find hello function")
	}
	if !foundGoodbye {
		t.Error("Expected to find goodbye function")
	}
	if !foundClass {
		t.Error("Expected to find MyClass")
	}
}

func TestChunkFile_JavaScript(t *testing.T) {
	c := NewChunker(DefaultChunkerConfig())
	content := `function hello() {
    return "Hello";
}

class MyComponent {
    constructor() {
        this.name = "test";
    }
}
`
	chunks := c.ChunkFile(content, "main.js")
	if len(chunks) == 0 {
		t.Fatal("Expected at least one chunk")
	}

	var foundFunc, foundClass bool
	for _, chunk := range chunks {
		if chunk.SymbolName == "hello" && chunk.ChunkType == ChunkTypeFunction {
			foundFunc = true
		}
		if chunk.SymbolName == "MyComponent" && chunk.ChunkType == ChunkTypeClass {
			foundClass = true
		}
	}

	if !foundFunc {
		t.Error("Expected to find hello function")
	}
	if !foundClass {
		t.Error("Expected to find MyComponent class")
	}
}

func TestChunkFile_Rust(t *testing.T) {
	c := NewChunker(DefaultChunkerConfig())
	content := `fn main() {
    println!("Hello");
}

struct User {
    name: String,
}

impl User {
    fn new(name: String) -> User {
        User { name }
    }
}
`
	chunks := c.ChunkFile(content, "main.rs")
	if len(chunks) == 0 {
		t.Fatal("Expected at least one chunk")
	}

	var foundFn, foundStruct bool
	for _, chunk := range chunks {
		if chunk.SymbolName == "main" && chunk.ChunkType == ChunkTypeFunction {
			foundFn = true
		}
		if chunk.SymbolName == "User" && chunk.ChunkType == ChunkTypeClass {
			foundStruct = true
		}
	}

	if !foundFn {
		t.Error("Expected to find main function")
	}
	if !foundStruct {
		t.Error("Expected to find User struct")
	}
}

func TestChunkFile_LineBasedFallback(t *testing.T) {
	c := NewChunker(DefaultChunkerConfig())
	// Use a file type that doesn't have semantic chunking
	content := `This is a text file
with multiple lines
that should be chunked
based on line count
`
	chunks := c.ChunkFile(content, "readme.txt")
	if len(chunks) == 0 {
		t.Fatal("Expected at least one chunk")
	}

	// Verify it falls back to generic chunk type
	for _, chunk := range chunks {
		if chunk.ChunkType != ChunkTypeGeneric {
			t.Errorf("Expected ChunkTypeGeneric for text file, got %s", chunk.ChunkType)
		}
	}
}

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		filename string
		expected Language
	}{
		{"main.go", LangGo},
		{"main.py", LangPython},
		{"app.js", LangJavaScript},
		{"app.ts", LangTypeScript},
		{"app.tsx", LangTypeScript},
		{"main.rs", LangRust},
		{"Main.java", LangJava},
		{"main.c", LangC},
		{"main.cpp", LangCPP},
		{"app.rb", LangRuby},
		{"index.php", LangPHP},
		{"app.swift", LangSwift},
		{"Main.kt", LangKotlin},
		{"script.sh", LangShell},
		{"query.sql", LangSQL},
		{"README.md", LangMarkdown},
		{"config.json", LangJSON},
		{"config.yaml", LangYAML},
		{"config.yml", LangYAML},
		{"Cargo.toml", LangTOML},
		{"index.html", LangHTML},
		{"style.css", LangCSS},
		{"Makefile", LangShell},
		{"Dockerfile", LangShell},
		{"unknown.xyz", LangUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := DetectLanguage(tt.filename)
			if got != tt.expected {
				t.Errorf("DetectLanguage(%q) = %q, want %q", tt.filename, got, tt.expected)
			}
		})
	}
}

func TestIsTextFile(t *testing.T) {
	tests := []struct {
		name     string
		content  []byte
		expected bool
	}{
		{"empty", []byte{}, true},
		{"text", []byte("Hello, World!"), true},
		{"utf8", []byte("こんにちは"), true},
		{"binary with null", []byte{0x00, 0x01, 0x02}, false},
		{"mixed with null", []byte("Hello\x00World"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsTextFile(tt.content)
			if got != tt.expected {
				t.Errorf("IsTextFile() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestChunk_LineNumbers(t *testing.T) {
	c := NewChunker(DefaultChunkerConfig())
	content := `package main

func First() {
	// do something
}

func Second() {
	// do something else
}
`
	chunks := c.ChunkFile(content, "main.go")

	for _, chunk := range chunks {
		if chunk.StartLine < 1 {
			t.Errorf("StartLine should be >= 1, got %d", chunk.StartLine)
		}
		if chunk.EndLine < chunk.StartLine {
			t.Errorf("EndLine (%d) should be >= StartLine (%d)", chunk.EndLine, chunk.StartLine)
		}
	}
}

func TestExtractSymbolName(t *testing.T) {
	tests := []struct {
		line     string
		prefix   string
		expected string
	}{
		{"func Hello() {", "func ", "Hello"},
		{"type User struct {", "type ", "User"},
		{"type Handler interface {", "type ", "Handler"},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := extractSymbolName(tt.line, tt.prefix)
			if got != tt.expected {
				t.Errorf("extractSymbolName(%q, %q) = %q, want %q", tt.line, tt.prefix, got, tt.expected)
			}
		})
	}
}
