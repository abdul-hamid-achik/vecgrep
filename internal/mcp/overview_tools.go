package mcp

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/abdul-hamid-achik/vecgrep/internal/search"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// handleOverview handles the vecgrep_overview tool.
func (s *SDKServer) handleOverview(ctx context.Context, req *sdkmcp.CallToolRequest, input OverviewInput) (*sdkmcp.CallToolResult, any, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil, nil
	}

	// Set defaults: if all are false, treat as "use defaults" (all true)
	// Otherwise, use the explicit values from input
	allFalse := !input.IncludeStructure && !input.IncludeEntryPoints && !input.IncludeKeyFiles
	includeStructure := allFalse || input.IncludeStructure
	includeEntryPoints := allFalse || input.IncludeEntryPoints
	includeKeyFiles := allFalse || input.IncludeKeyFiles

	maxDepth := input.MaxDirectoryDepth
	if maxDepth <= 0 {
		maxDepth = 3
	}

	var sb strings.Builder

	// Get codebase name from project root
	projectName := filepath.Base(s.projectRoot)
	fmt.Fprintf(&sb, "# Codebase Overview: %s\n\n", projectName)

	// Get stats from searcher
	stats, err := s.searcher.GetIndexStats(ctx)
	if err == nil {
		sb.WriteString("## Index Statistics\n\n")
		if totalFiles, ok := stats["total_files"].(int64); ok {
			fmt.Fprintf(&sb, "- **Total Files:** %d\n", totalFiles)
		}
		if totalChunks, ok := stats["total_chunks"].(int64); ok {
			fmt.Fprintf(&sb, "- **Total Chunks:** %d\n", totalChunks)
		}

		// Language distribution
		if langStats, ok := stats["languages"].(map[string]int64); ok && len(langStats) > 0 {
			sb.WriteString("\n## Languages\n\n")
			// Sort languages by count
			type langCount struct {
				lang  string
				count int64
			}
			var langs []langCount
			for lang, count := range langStats {
				langs = append(langs, langCount{lang, count})
			}
			sort.Slice(langs, func(i, j int) bool {
				return langs[i].count > langs[j].count
			})
			for _, lc := range langs {
				fmt.Fprintf(&sb, "- **%s:** %d files\n", lc.lang, lc.count)
			}
		}
		sb.WriteString("\n")
	}

	// Directory structure
	if includeStructure {
		sb.WriteString("## Directory Structure\n\n")
		sb.WriteString("```\n")
		structure := buildDirectoryTree(s.projectRoot, maxDepth)
		sb.WriteString(structure)
		sb.WriteString("```\n\n")
	}

	// Entry points
	if includeEntryPoints {
		entryPoints := findEntryPoints(s.projectRoot)
		if len(entryPoints) > 0 {
			sb.WriteString("## Entry Points\n\n")
			for _, ep := range entryPoints {
				fmt.Fprintf(&sb, "- `%s`\n", ep)
			}
			sb.WriteString("\n")
		}
	}

	// Key files
	if includeKeyFiles {
		keyFiles := findKeyFiles(s.projectRoot)
		if len(keyFiles) > 0 {
			sb.WriteString("## Key Files\n\n")
			for _, kf := range keyFiles {
				fmt.Fprintf(&sb, "- `%s`\n", kf)
			}
			sb.WriteString("\n")
		}
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// buildDirectoryTree builds a visual directory tree up to maxDepth.
func buildDirectoryTree(root string, maxDepth int) string {
	var sb strings.Builder
	sb.WriteString(filepath.Base(root) + "/\n")

	buildTreeRecursive(&sb, root, "", 0, maxDepth)
	return sb.String()
}

func buildTreeRecursive(sb *strings.Builder, path string, prefix string, depth int, maxDepth int) {
	if depth >= maxDepth {
		return
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}

	// Filter and sort entries (directories first, then files)
	var dirs, files []os.DirEntry
	for _, entry := range entries {
		name := entry.Name()
		// Skip hidden files and common ignore patterns
		if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "__pycache__" {
			continue
		}
		if entry.IsDir() {
			dirs = append(dirs, entry)
		} else {
			files = append(files, entry)
		}
	}

	// Combine: directories first
	allEntries := append(dirs, files...)

	// Limit entries to avoid huge output
	maxEntries := 15
	if len(allEntries) > maxEntries {
		allEntries = allEntries[:maxEntries]
	}

	for i, entry := range allEntries {
		isLast := i == len(allEntries)-1 || (i == maxEntries-1 && len(entries) > maxEntries)
		connector := "├── "
		nextPrefix := prefix + "│   "
		if isLast {
			connector = "└── "
			nextPrefix = prefix + "    "
		}

		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		sb.WriteString(prefix + connector + name + "\n")

		if entry.IsDir() && depth < maxDepth-1 {
			buildTreeRecursive(sb, filepath.Join(path, entry.Name()), nextPrefix, depth+1, maxDepth)
		}
	}

	// Show if there are more entries
	if len(entries) > maxEntries {
		sb.WriteString(prefix + "    ... and more\n")
	}
}

// findEntryPoints looks for common entry point files.
func findEntryPoints(root string) []string {
	patterns := []string{
		"main.go",
		"cmd/*/main.go",
		"cmd/*/*.go",
		"index.js",
		"index.ts",
		"src/index.js",
		"src/index.ts",
		"src/main.js",
		"src/main.ts",
		"app.py",
		"main.py",
		"__main__.py",
		"src/main.rs",
		"src/lib.rs",
		"Cargo.toml",
		"package.json",
		"go.mod",
		"pyproject.toml",
		"setup.py",
	}

	var found []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			continue
		}
		for _, m := range matches {
			rel, _ := filepath.Rel(root, m)
			found = append(found, rel)
		}
	}

	// Deduplicate and limit
	seen := make(map[string]bool)
	var result []string
	for _, f := range found {
		if !seen[f] {
			seen[f] = true
			result = append(result, f)
		}
		if len(result) >= 10 {
			break
		}
	}

	return result
}

// findKeyFiles looks for important configuration and documentation files.
func findKeyFiles(root string) []string {
	patterns := []string{
		"README.md",
		"README.rst",
		"README.txt",
		"LICENSE",
		"LICENSE.md",
		"CONTRIBUTING.md",
		"CHANGELOG.md",
		"Makefile",
		"Dockerfile",
		"docker-compose.yml",
		"docker-compose.yaml",
		".env.example",
		"config.yaml",
		"config.yml",
		"config.json",
	}

	var found []string
	for _, pattern := range patterns {
		path := filepath.Join(root, pattern)
		if _, err := os.Stat(path); err == nil {
			found = append(found, pattern)
		}
	}

	return found
}

// handleBatchSearch handles the vecgrep_batch_search tool.
func (s *SDKServer) handleBatchSearch(ctx context.Context, req *sdkmcp.CallToolRequest, input BatchSearchInput) (*sdkmcp.CallToolResult, any, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil, nil
	}

	// Check embedding provider
	if errResult := s.checkProvider(ctx); errResult != nil {
		return errResult, nil, nil
	}

	if len(input.Queries) == 0 {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Error: 'queries' parameter is required and must contain at least one query."}},
			IsError: true,
		}, nil, nil
	}

	limitPerQuery := input.LimitPerQuery
	if limitPerQuery <= 0 {
		limitPerQuery = 3
	}

	// Deduplicate defaults to true unless explicitly set to false
	deduplicate := input.Deduplicate == nil || *input.Deduplicate

	// Search for each query in parallel
	type queryResult struct {
		query   string
		results []search.Result
		err     error
	}

	resultsChan := make(chan queryResult, len(input.Queries))
	var wg sync.WaitGroup

	for _, query := range input.Queries {
		wg.Add(1)
		go func(q string) {
			defer wg.Done()

			opts := search.SearchOptions{
				Limit:       limitPerQuery,
				Language:    input.Language,
				ChunkType:   input.ChunkType,
				ProjectRoot: s.projectRoot,
				Mode:        search.SearchModeHybrid,
			}

			results, err := s.searcher.Search(ctx, q, opts)
			resultsChan <- queryResult{query: q, results: results, err: err}
		}(query)
	}

	wg.Wait()
	close(resultsChan)

	// Collect results in order
	allResults := make(map[string][]search.Result)
	for qr := range resultsChan {
		if qr.err != nil {
			continue
		}
		allResults[qr.query] = qr.results
	}

	// Format output
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Batch Search Results (%d queries)\n\n", len(input.Queries))

	seen := make(map[int64]bool) // For deduplication
	totalResults := 0

	for _, query := range input.Queries {
		results, ok := allResults[query]
		if !ok {
			fmt.Fprintf(&sb, "## Query: \"%s\"\n\nError: search failed\n\n", query)
			continue
		}

		fmt.Fprintf(&sb, "## Query: \"%s\"\n\n", query)

		if len(results) == 0 {
			sb.WriteString("No results found.\n\n")
			continue
		}

		resultCount := 0
		for _, r := range results {
			// Skip duplicates if deduplication is enabled
			if deduplicate && seen[r.ChunkID] {
				continue
			}
			seen[r.ChunkID] = true

			resultCount++
			totalResults++

			fmt.Fprintf(&sb, "### %s (lines %d-%d, score: %.2f)\n", r.RelativePath, r.StartLine, r.EndLine, r.Score)
			if r.SymbolName != "" {
				fmt.Fprintf(&sb, "**Symbol:** %s\n", r.SymbolName)
			}
			sb.WriteString("\n```")
			if r.Language != "" && r.Language != "unknown" {
				sb.WriteString(r.Language)
			}
			sb.WriteString("\n")
			sb.WriteString(r.Content)
			sb.WriteString("\n```\n\n")
		}

		if resultCount == 0 {
			sb.WriteString("All results were duplicates of previous queries.\n\n")
		}
	}

	fmt.Fprintf(&sb, "---\n**Total unique results:** %d\n", totalResults)

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// handleRelatedFiles handles the vecgrep_related_files tool.
func (s *SDKServer) handleRelatedFiles(ctx context.Context, req *sdkmcp.CallToolRequest, input RelatedFilesInput) (*sdkmcp.CallToolResult, any, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil, nil
	}

	if input.File == "" {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Error: 'file' parameter is required."}},
			IsError: true,
		}, nil, nil
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 10
	}

	relationship := input.Relationship
	if relationship == "" {
		relationship = "all"
	}

	// Resolve file path
	filePath := input.File
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(s.projectRoot, filePath)
	}

	// Check file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Error: File not found: %s", input.File)}},
			IsError: true,
		}, nil, nil
	}

	var sb strings.Builder
	relPath, _ := filepath.Rel(s.projectRoot, filePath)
	fmt.Fprintf(&sb, "# Related Files for: %s\n\n", relPath)

	// Detect language
	ext := strings.ToLower(filepath.Ext(filePath))

	// Find related files based on relationship type
	switch relationship {
	case "imports":
		imports := findImports(filePath, ext)
		sb.WriteString("## Imports\n\n")
		if len(imports) == 0 {
			sb.WriteString("No imports found.\n\n")
		} else {
			for _, imp := range imports {
				fmt.Fprintf(&sb, "- `%s`\n", imp)
			}
			sb.WriteString("\n")
		}

	case "imported_by":
		importedBy := findImportedBy(s.projectRoot, relPath, ext)
		sb.WriteString("## Imported By\n\n")
		if len(importedBy) == 0 {
			sb.WriteString("No files import this file.\n\n")
		} else {
			for i, f := range importedBy {
				if i >= limit {
					fmt.Fprintf(&sb, "... and %d more\n", len(importedBy)-limit)
					break
				}
				fmt.Fprintf(&sb, "- `%s`\n", f)
			}
			sb.WriteString("\n")
		}

	case "tests":
		tests := findTestFiles(s.projectRoot, relPath, ext)
		sb.WriteString("## Test Files\n\n")
		if len(tests) == 0 {
			sb.WriteString("No test files found.\n\n")
		} else {
			for _, t := range tests {
				fmt.Fprintf(&sb, "- `%s`\n", t)
			}
			sb.WriteString("\n")
		}

	case "all":
		fallthrough
	default:
		// Show all relationships
		imports := findImports(filePath, ext)
		if len(imports) > 0 {
			sb.WriteString("## Imports\n\n")
			for i, imp := range imports {
				if i >= limit {
					fmt.Fprintf(&sb, "... and %d more\n", len(imports)-limit)
					break
				}
				fmt.Fprintf(&sb, "- `%s`\n", imp)
			}
			sb.WriteString("\n")
		}

		tests := findTestFiles(s.projectRoot, relPath, ext)
		if len(tests) > 0 {
			sb.WriteString("## Test Files\n\n")
			for _, t := range tests {
				fmt.Fprintf(&sb, "- `%s`\n", t)
			}
			sb.WriteString("\n")
		}

		importedBy := findImportedBy(s.projectRoot, relPath, ext)
		if len(importedBy) > 0 {
			sb.WriteString("## Imported By\n\n")
			for i, f := range importedBy {
				if i >= limit {
					fmt.Fprintf(&sb, "... and %d more\n", len(importedBy)-limit)
					break
				}
				fmt.Fprintf(&sb, "- `%s`\n", f)
			}
			sb.WriteString("\n")
		}

		// Find config files that might relate
		configs := findRelatedConfigs(s.projectRoot, relPath)
		if len(configs) > 0 {
			sb.WriteString("## Related Configs\n\n")
			for _, c := range configs {
				fmt.Fprintf(&sb, "- `%s`\n", c)
			}
			sb.WriteString("\n")
		}
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// findImports parses a file and extracts import statements.
func findImports(filePath string, ext string) []string {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}

	var imports []string
	lines := strings.Split(string(content), "\n")

	switch ext {
	case ".go":
		// Go imports
		inImportBlock := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "import (") {
				inImportBlock = true
				continue
			}
			if inImportBlock {
				if trimmed == ")" {
					inImportBlock = false
					continue
				}
				// Extract import path
				if imp := extractGoImport(trimmed); imp != "" {
					imports = append(imports, imp)
				}
			} else if rest, found := strings.CutPrefix(trimmed, "import "); found {
				// Single import
				if imp := extractGoImport(rest); imp != "" {
					imports = append(imports, imp)
				}
			}
		}

	case ".js", ".ts", ".jsx", ".tsx":
		// JavaScript/TypeScript imports
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "from ") {
				if imp := extractJSImport(trimmed); imp != "" {
					imports = append(imports, imp)
				}
			}
			if strings.Contains(trimmed, "require(") {
				if imp := extractRequire(trimmed); imp != "" {
					imports = append(imports, imp)
				}
			}
		}

	case ".py":
		// Python imports
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "from ") {
				if imp := extractPythonImport(trimmed); imp != "" {
					imports = append(imports, imp)
				}
			}
		}
	}

	return imports
}

func extractGoImport(line string) string {
	line = strings.TrimSpace(line)
	// Remove alias if present
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return ""
	}
	imp := parts[len(parts)-1]
	// Remove quotes
	imp = strings.Trim(imp, `"`)
	if imp == "" || imp == "_" {
		return ""
	}
	return imp
}

func extractJSImport(line string) string {
	// Extract from: import ... from 'module' or import 'module'
	if idx := strings.LastIndex(line, "from "); idx != -1 {
		rest := strings.TrimSpace(line[idx+5:])
		return strings.Trim(rest, `"';`)
	}
	if rest, found := strings.CutPrefix(line, "import "); found {
		rest = strings.TrimSpace(rest)
		return strings.Trim(rest, `"';`)
	}
	return ""
}

func extractRequire(line string) string {
	// Extract from: require('module')
	_, rest, found := strings.Cut(line, "require(")
	if !found {
		return ""
	}
	end := strings.IndexAny(rest, `"')`)
	if end == -1 {
		return ""
	}
	// Find the closing quote
	closeIdx := strings.IndexAny(rest[end+1:], `"')`)
	if closeIdx == -1 {
		return strings.Trim(rest[:end+1], `"'`)
	}
	return strings.Trim(rest[end+1:end+1+closeIdx], `"'`)
}

func extractPythonImport(line string) string {
	if strings.HasPrefix(line, "from ") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			return parts[1]
		}
	}
	if rest, found := strings.CutPrefix(line, "import "); found {
		parts := strings.Split(rest, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(strings.Split(parts[0], " as ")[0])
		}
	}
	return ""
}

// findImportedBy searches for files that import the given file.
func findImportedBy(root, relPath, ext string) []string {
	// This is a simplified implementation
	// A full implementation would parse all files and check imports

	baseName := filepath.Base(relPath)
	baseNameNoExt := strings.TrimSuffix(baseName, ext)
	dir := filepath.Dir(relPath)

	var importedBy []string

	// Search patterns based on file type
	var searchPatterns []string
	switch ext {
	case ".go":
		// Go: look for package imports
		searchPatterns = []string{filepath.Dir(relPath)}
	case ".js", ".ts", ".jsx", ".tsx":
		// JS/TS: look for relative imports
		searchPatterns = []string{
			fmt.Sprintf("./%s", baseNameNoExt),
			fmt.Sprintf("../%s/%s", filepath.Base(dir), baseNameNoExt),
		}
	case ".py":
		// Python: look for module imports
		searchPatterns = []string{baseNameNoExt}
	}

	// Walk through files and search
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		pathExt := strings.ToLower(filepath.Ext(path))
		if pathExt != ext {
			return nil
		}

		// Skip the file itself
		pathRel, _ := filepath.Rel(root, path)
		if pathRel == relPath {
			return nil
		}

		// Check if file imports the target
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		contentStr := string(content)
		for _, pattern := range searchPatterns {
			if strings.Contains(contentStr, pattern) {
				importedBy = append(importedBy, pathRel)
				break
			}
		}

		return nil
	})

	return importedBy
}

// findTestFiles finds test files related to the given file.
func findTestFiles(root, relPath, ext string) []string {
	dir := filepath.Dir(relPath)
	baseName := filepath.Base(relPath)
	baseNameNoExt := strings.TrimSuffix(baseName, ext)

	var tests []string

	// Common test file patterns
	testPatterns := []string{
		filepath.Join(dir, baseNameNoExt+"_test"+ext),          // Go style: foo_test.go
		filepath.Join(dir, baseNameNoExt+".test"+ext),          // JS style: foo.test.js
		filepath.Join(dir, baseNameNoExt+".spec"+ext),          // JS style: foo.spec.js
		filepath.Join(dir, "__tests__", baseName),              // React style
		filepath.Join(dir, "tests", "test_"+baseName),          // Python style
		filepath.Join(dir, "tests", baseNameNoExt+"_test"+ext), // Python style
	}

	for _, pattern := range testPatterns {
		fullPath := filepath.Join(root, pattern)
		if _, err := os.Stat(fullPath); err == nil {
			tests = append(tests, pattern)
		}
	}

	return tests
}

// findRelatedConfigs finds config files that might relate to the given file.
func findRelatedConfigs(root, relPath string) []string {
	dir := filepath.Dir(relPath)
	var configs []string

	// Look for config files in the same directory or parent
	configPatterns := []string{
		"config.yaml",
		"config.yml",
		"config.json",
		".env",
		"settings.yaml",
		"settings.json",
	}

	// Check same directory
	for _, pattern := range configPatterns {
		path := filepath.Join(dir, pattern)
		fullPath := filepath.Join(root, path)
		if _, err := os.Stat(fullPath); err == nil {
			configs = append(configs, path)
		}
	}

	return configs
}

// expandContextLines expands a result's content to include surrounding lines.
func expandContextLines(projectRoot string, result search.Result, contextLines int) string {
	if contextLines <= 0 {
		return result.Content
	}

	// Read the file
	filePath := filepath.Join(projectRoot, result.RelativePath)
	file, err := os.Open(filePath)
	if err != nil {
		return result.Content
	}
	defer file.Close()

	// Read all lines
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	if scanner.Err() != nil {
		return result.Content
	}

	// Calculate line range (1-indexed to 0-indexed)
	startLine := result.StartLine - 1 - contextLines
	endLine := result.EndLine - 1 + contextLines

	if startLine < 0 {
		startLine = 0
	}
	if endLine >= len(lines) {
		endLine = len(lines) - 1
	}

	// Build expanded content
	var sb strings.Builder
	for i := startLine; i <= endLine; i++ {
		sb.WriteString(lines[i])
		if i < endLine {
			sb.WriteString("\n")
		}
	}

	return sb.String()
}
