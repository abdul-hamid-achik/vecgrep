package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

type failingStructuralSource struct{ err error }

func (s failingStructuralSource) LoadStructuralChunks(context.Context, string) (*index.StructuralChunkSet, error) {
	return nil, s.err
}

func TestCodemapStructuralSourcePaginatesAndFallsBackPerFile(t *testing.T) {
	root := t.TempDir()
	fresh := `func Fresh() string { return "fresh" }`
	stale := `func Stale() string { return "changed" }`
	truncatedFull := `func Truncated() string { return "truncated" }`
	writeStructuralFixture(t, root, "fresh.go", fresh)
	writeStructuralFixture(t, root, "stale.go", stale)
	writeStructuralFixture(t, root, "truncated.go", truncatedFull)

	projectKey, err := structuralProjectKey(root)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("a", 64)
	records := []structuralSymbolRecord{
		validStructuralRecord(projectKey, fingerprint, "fresh.go", "Fresh", fresh),
		{
			SchemaVersion:    1,
			Project:          "fixture",
			ProjectKey:       projectKey,
			IndexFingerprint: fingerprint,
			File:             "stale.go",
			StartLine:        1,
			EndLine:          1,
			Symbol:           "Stale",
			FQN:              "fixture.Stale",
			Kind:             "function",
			Language:         "go",
			ContentOmitted:   true,
			OmissionReason:   "stale_index",
			FileStale:        true,
		},
		validStructuralRecord(projectKey, fingerprint, "truncated.go", "Truncated", truncatedFull),
	}
	records[0].Docstring = "Explains Fresh."
	records[0].Signature = "func Fresh() string"
	records[2].Content = truncatedFull[:12]
	records[2].ContentTruncated = true
	records[2].ContentHash = sha256Hex(truncatedFull)

	var offsets []int
	source := &codemapStructuralSource{
		bin:        "codemap",
		pageLimit:  2,
		maxContent: 64,
		runPage: func(_ context.Context, _, _ string, offset, limit, maxContent int) ([]byte, error) {
			offsets = append(offsets, offset)
			end := min(offset+limit, len(records))
			page := structuralExportReport{
				SchemaVersion:    1,
				Project:          "fixture",
				ProjectKey:       projectKey,
				IndexFingerprint: fingerprint,
				Offset:           offset,
				Limit:            limit,
				MaxContentBytes:  maxContent,
				TotalRecords:     len(records),
				ReturnedRecords:  end - offset,
				Complete:         end == len(records),
				Records:          records[offset:end],
			}
			if !page.Complete {
				page.NextOffset = end
			}
			return json.Marshal(page)
		},
	}

	set, err := source.LoadStructuralChunks(context.Background(), root)
	if err != nil {
		t.Fatalf("LoadStructuralChunks: %v", err)
	}
	if !reflect.DeepEqual(offsets, []int{0, 2}) {
		t.Fatalf("offsets = %v, want [0 2]", offsets)
	}
	if len(set.Issues) != 1 || !strings.Contains(set.Issues[0].Error(), "stale.go") {
		t.Fatalf("issues = %v, want one stale.go issue", set.Issues)
	}
	if _, ok := set.Files["stale.go"]; ok {
		t.Fatal("stale.go should fall back to the built-in chunker")
	}
	if len(set.Files) != 2 {
		t.Fatalf("structural files = %d, want fresh + truncated", len(set.Files))
	}
	if got := set.Files["truncated.go"].Chunks[0].Content; got != truncatedFull {
		t.Fatalf("truncated producer content lost current source: %q", got)
	}

	wantGolden, err := os.ReadFile(filepath.Join("testdata", "structural_embedding.golden"))
	if err != nil {
		t.Fatal(err)
	}
	freshChunk := set.Files["fresh.go"].Chunks[0]
	if got, want := freshChunk.EmbeddingContent, strings.TrimSuffix(string(wantGolden), "\n"); got != want {
		t.Fatalf("embedding content mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if freshChunk.Content != fresh {
		t.Fatalf("preview content = %q, want clean source %q", freshChunk.Content, fresh)
	}
}

func TestCodemapStructuralSourceRejectsFingerprintChangeBetweenPages(t *testing.T) {
	root := t.TempDir()
	writeStructuralFixture(t, root, "a.go", "package a")
	writeStructuralFixture(t, root, "b.go", "package b")
	projectKey, _ := structuralProjectKey(root)
	fingerprint := strings.Repeat("a", 64)
	records := []structuralSymbolRecord{
		validStructuralRecord(projectKey, fingerprint, "a.go", "A", "package a"),
		validStructuralRecord(projectKey, fingerprint, "b.go", "B", "package b"),
	}
	source := &codemapStructuralSource{
		bin:        "codemap",
		pageLimit:  1,
		maxContent: 64,
		runPage: func(_ context.Context, _, _ string, offset, limit, maxContent int) ([]byte, error) {
			pageFingerprint := fingerprint
			if offset > 0 {
				pageFingerprint = strings.Repeat("b", 64)
				records[offset].IndexFingerprint = pageFingerprint
			}
			page := structuralExportReport{
				SchemaVersion:    1,
				Project:          "fixture",
				ProjectKey:       projectKey,
				IndexFingerprint: pageFingerprint,
				Offset:           offset,
				Limit:            limit,
				MaxContentBytes:  maxContent,
				TotalRecords:     2,
				ReturnedRecords:  1,
				Complete:         offset == 1,
				Records:          records[offset : offset+1],
			}
			if !page.Complete {
				page.NextOffset = 1
			}
			return json.Marshal(page)
		},
	}
	_, err := source.LoadStructuralChunks(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "changed between pages") {
		t.Fatalf("error = %v, want changed-between-pages failure", err)
	}
}

func TestCodemapStructuralSourceEnforcesAggregateBudget(t *testing.T) {
	root := t.TempDir()
	projectKey, _ := structuralProjectKey(root)
	fingerprint := strings.Repeat("a", 64)
	source := singleStructuralPageSource(projectKey, fingerprint, nil)
	source.maxTotalBytes = 1
	_, err := source.LoadStructuralChunks(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "aggregate budget") {
		t.Fatalf("error = %v, want aggregate budget failure", err)
	}
}

func TestCappedCommandOutputDiscardsBeyondLimit(t *testing.T) {
	output := newCappedCommandOutput(4)
	if n, err := output.Write([]byte("abcdef")); err != nil || n != 6 {
		t.Fatalf("write = %d, %v", n, err)
	}
	if got := output.String(); got != "abcd" || !output.Overflowed() {
		t.Fatalf("output = %q overflow=%t", got, output.Overflowed())
	}
}

func TestCodemapStructuralSourceAcceptsValidEmptySnapshot(t *testing.T) {
	root := t.TempDir()
	projectKey, _ := structuralProjectKey(root)
	fingerprint := strings.Repeat("a", 64)
	source := &codemapStructuralSource{
		bin:        "codemap",
		pageLimit:  2,
		maxContent: 64,
		runPage: func(_ context.Context, _, _ string, offset, limit, maxContent int) ([]byte, error) {
			return json.Marshal(structuralExportReport{
				SchemaVersion:    1,
				Project:          "fixture",
				ProjectKey:       projectKey,
				IndexFingerprint: fingerprint,
				Offset:           offset,
				Limit:            limit,
				MaxContentBytes:  maxContent,
				TotalRecords:     0,
				ReturnedRecords:  0,
				Complete:         true,
				Records:          []structuralSymbolRecord{},
			})
		},
	}
	set, err := source.LoadStructuralChunks(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !set.Complete || len(set.Files) != 0 || len(set.Issues) != 0 {
		t.Fatalf("empty snapshot = %+v", set)
	}
}

func TestCodemapStructuralSourceRejectsChangedTailOfTruncatedContent(t *testing.T) {
	root := t.TempDir()
	oldContent := `func Truncated() string { return "old" }`
	currentContent := `func Truncated() string { return "new" }`
	writeStructuralFixture(t, root, "truncated.go", currentContent)
	projectKey, _ := structuralProjectKey(root)
	fingerprint := strings.Repeat("a", 64)
	record := validStructuralRecord(projectKey, fingerprint, "truncated.go", "Truncated", oldContent)
	record.Content = oldContent[:12]
	record.ContentTruncated = true
	source := singleStructuralPageSource(projectKey, fingerprint, []structuralSymbolRecord{record})

	set, err := source.LoadStructuralChunks(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Issues) != 1 || len(set.Files) != 0 || !strings.Contains(set.Issues[0].Error(), "source changed") {
		t.Fatalf("set = %+v, want one file-scoped changed-tail issue", set)
	}
}

func TestCodemapStructuralSourceUsesProducerOrdering(t *testing.T) {
	root := t.TempDir()
	content := "first\nsecond"
	writeStructuralFixture(t, root, "same.go", content)
	projectKey, _ := structuralProjectKey(root)
	fingerprint := strings.Repeat("a", 64)
	first := validStructuralRecord(projectKey, fingerprint, "same.go", "Z", "first")
	first.FQN = "fixture.Z"
	second := validStructuralRecord(projectKey, fingerprint, "same.go", "A", content)
	second.FQN = "fixture.A"
	second.EndLine = 2
	source := singleStructuralPageSource(projectKey, fingerprint, []structuralSymbolRecord{first, second})

	set, err := source.LoadStructuralChunks(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Issues) != 0 || len(set.Files["same.go"].Chunks) != 2 {
		t.Fatalf("set = %+v, want producer-ordered same-line records", set)
	}
}

func TestStructuralRecordLessMatchesEveryProducerTieBreaker(t *testing.T) {
	base := structuralSymbolRecord{
		File: "same.go", StartLine: 1, EndLine: 1, FQN: "fixture.Same",
		Kind: "function", Symbol: "Same", Language: "go",
		Signature: "func Same()", Docstring: "same", SourceHash: strings.Repeat("a", 64),
	}
	tests := []struct {
		name  string
		left  structuralSymbolRecord
		right structuralSymbolRecord
	}{
		{
			name: "language precedes source hash",
			left: func() structuralSymbolRecord {
				r := base
				r.Language = "go"
				r.SourceHash = strings.Repeat("f", 64)
				return r
			}(),
			right: func() structuralSymbolRecord {
				r := base
				r.Language = "typescript"
				r.SourceHash = strings.Repeat("0", 64)
				return r
			}(),
		},
		{
			name: "signature precedes source hash",
			left: func() structuralSymbolRecord {
				r := base
				r.Signature = "func A()"
				r.SourceHash = strings.Repeat("f", 64)
				return r
			}(),
			right: func() structuralSymbolRecord {
				r := base
				r.Signature = "func Z()"
				r.SourceHash = strings.Repeat("0", 64)
				return r
			}(),
		},
		{
			name: "docstring precedes source hash",
			left: func() structuralSymbolRecord {
				r := base
				r.Docstring = "alpha"
				r.SourceHash = strings.Repeat("f", 64)
				return r
			}(),
			right: func() structuralSymbolRecord {
				r := base
				r.Docstring = "zeta"
				r.SourceHash = strings.Repeat("0", 64)
				return r
			}(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !structuralRecordLess(test.left, test.right) {
				t.Fatalf("producer tie-breaker did not order left before right: left=%+v right=%+v", test.left, test.right)
			}
			if structuralRecordLess(test.right, test.left) {
				t.Fatalf("producer tie-breaker ordered right before left: left=%+v right=%+v", test.left, test.right)
			}
		})
	}
}

func TestCodemapStructuralOrdinalIsAuthoritativeOverTruncatedTieBreakers(t *testing.T) {
	root := t.TempDir()
	writeStructuralFixture(t, root, "dup.go", "same")
	projectKey, _ := structuralProjectKey(root)
	fingerprint := strings.Repeat("a", 64)
	first := validStructuralRecord(projectKey, fingerprint, "dup.go", "Same", "same")
	second := first
	first.Signature = "same-truncated-prefix"
	second.Signature = "same-truncated-prefix"
	firstOrdinal, secondOrdinal := 1, 2
	first.Ordinal = &firstOrdinal
	second.Ordinal = &secondOrdinal

	// Reconstructing the producer comparator from these emitted values would
	// reject the order. Ordinals are generated before signature/doc truncation
	// and therefore remain authoritative across pagination.
	if structuralRecordLess(first, second) || structuralRecordLess(second, first) {
		t.Fatal("fixture must be indistinguishable under the legacy comparator")
	}
	set, err := singleStructuralPageSource(projectKey, fingerprint, []structuralSymbolRecord{first, second}).LoadStructuralChunks(context.Background(), root)
	if err != nil {
		t.Fatalf("ordinal-ordered stream was rejected: %v", err)
	}
	if len(set.Issues) != 1 {
		t.Fatalf("duplicate selector issues = %d, want one file-scoped issue", len(set.Issues))
	}
}

func TestCodemapStructuralOrdinalMustBeContiguous(t *testing.T) {
	root := t.TempDir()
	writeStructuralFixture(t, root, "main.go", "same")
	projectKey, _ := structuralProjectKey(root)
	fingerprint := strings.Repeat("a", 64)
	record := validStructuralRecord(projectKey, fingerprint, "main.go", "Same", "same")
	badOrdinal := 2
	record.Ordinal = &badOrdinal

	_, err := singleStructuralPageSource(projectKey, fingerprint, []structuralSymbolRecord{record}).LoadStructuralChunks(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "ordinal") {
		t.Fatalf("non-contiguous ordinal error = %v", err)
	}
}

func TestCodemapStructuralSourceTreatsDuplicateSelectorAsFileIssue(t *testing.T) {
	root := t.TempDir()
	writeStructuralFixture(t, root, "dup.go", "first\nsecond")
	writeStructuralFixture(t, root, "fresh.go", "fresh")
	projectKey, _ := structuralProjectKey(root)
	fingerprint := strings.Repeat("a", 64)
	first := validStructuralRecord(projectKey, fingerprint, "dup.go", "Same", "first")
	second := validStructuralRecord(projectKey, fingerprint, "dup.go", "Same", "first\nsecond")
	second.EndLine = 2
	fresh := validStructuralRecord(projectKey, fingerprint, "fresh.go", "Fresh", "fresh")
	source := singleStructuralPageSource(projectKey, fingerprint, []structuralSymbolRecord{first, second, fresh})

	set, err := source.LoadStructuralChunks(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Issues) != 1 || len(set.Files) != 1 || len(set.Files["fresh.go"].Chunks) != 1 {
		t.Fatalf("set = %+v, want dup.go fallback and fresh.go structural", set)
	}
}

func TestConfigureStructuralChunksRequiredNeedsBinary(t *testing.T) {
	idx := index.NewIndexer(nil, nil, index.DefaultIndexerConfig())
	err := ConfigureStructuralChunks(idx, config.CodemapConfig{
		StructuralChunks: "required",
		Bin:              filepath.Join(t.TempDir(), "missing-codemap"),
	}, "")
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("error = %v, want required binary failure", err)
	}
}

func TestCodemapFailureMessageDecodesJSONStdoutEnvelope(t *testing.T) {
	got := codemapFailureMessage([]byte(`{"ok":false,"error":"project is not indexed","code":"not_indexed","hint":"run: codemap index"}`))
	if got != "project is not indexed [not_indexed]; run: codemap index" {
		t.Fatalf("failure message = %q", got)
	}
	for _, invalid := range [][]byte{
		[]byte(`not json`),
		[]byte(`{"ok":true,"error":"ignore me"}`),
		[]byte(`{"ok":false,"records":[{"content":"do not echo source"}]}`),
	} {
		if got := codemapFailureMessage(invalid); got != "" {
			t.Fatalf("arbitrary stdout became error text: %q", got)
		}
	}
}

func TestConfigureStructuralChunksFailurePreservesExistingSource(t *testing.T) {
	idx := index.NewIndexer(nil, nil, index.DefaultIndexerConfig())
	sentinel := errors.New("sentinel structural source")
	idx.SetStructuralChunkSource(failingStructuralSource{err: sentinel}, true)
	err := ConfigureStructuralChunks(idx, config.CodemapConfig{
		StructuralChunks: "required",
		Bin:              filepath.Join(t.TempDir(), "missing-codemap"),
	}, "")
	if err == nil {
		t.Fatal("missing required binary should fail")
	}
	_, err = idx.ReindexAll(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
		t.Fatalf("source after failed configure = %v, want preserved sentinel source", err)
	}
}

func TestValidateStructuralRecordRequiresSourceHash(t *testing.T) {
	projectKey := "0123456789ab"
	fingerprint := strings.Repeat("a", 64)
	record := validStructuralRecord(projectKey, fingerprint, "fresh.go", "Fresh", "fresh")
	record.SourceHash = ""
	page := structuralExportReport{SchemaVersion: 1, Project: "fixture", ProjectKey: projectKey, IndexFingerprint: fingerprint}
	_, issue, fatal := validateStructuralRecord(record, page)
	if fatal != nil || issue == nil || !strings.Contains(issue.Error(), "source_hash") {
		t.Fatalf("issue/fatal = %v / %v, want file-scoped source_hash issue", issue, fatal)
	}
}

func TestParseStructuralChunksMode(t *testing.T) {
	for _, value := range []string{"", "auto", "off", "required", " REQUIRED "} {
		if _, err := ParseStructuralChunksMode(value); err != nil {
			t.Errorf("ParseStructuralChunksMode(%q): %v", value, err)
		}
	}
	if _, err := ParseStructuralChunksMode("sometimes"); err == nil {
		t.Fatal("invalid mode should fail")
	}
}

func TestStructuralPreviewAndEmbeddingBudgetsAreRuneSafe(t *testing.T) {
	record := structuralSymbolRecord{
		Docstring: strings.Repeat("é", 3000),
		Signature: strings.Repeat("S", 2000),
		Content:   "SOURCE_SENTINEL\n" + strings.Repeat("界", 3000),
	}
	preview := structuralPreviewText(record.Content)
	embedding := structuralEmbeddingText(record)
	if len(preview) > structuralPreviewMaxBytes || !utf8.ValidString(preview) {
		t.Fatalf("preview bytes=%d valid=%t", len(preview), utf8.ValidString(preview))
	}
	if len(embedding) > structuralEmbeddingMaxBytes || !utf8.ValidString(embedding) || !strings.Contains(embedding, "SOURCE_SENTINEL") {
		t.Fatalf("embedding bytes=%d valid=%t contains_source=%t", len(embedding), utf8.ValidString(embedding), strings.Contains(embedding, "SOURCE_SENTINEL"))
	}
}

func TestStructuralProfileChangesWithSymbol(t *testing.T) {
	root := t.TempDir()
	content := "fresh"
	writeStructuralFixture(t, root, "fresh.go", content)
	projectKey, _ := structuralProjectKey(root)
	fingerprint := strings.Repeat("a", 64)
	first := validStructuralRecord(projectKey, fingerprint, "fresh.go", "First", content)
	first.FQN = "" // SymbolName falls back to Symbol, so Symbol must invalidate.
	second := first
	second.Symbol = "Second"
	firstSet := buildStructuralChunkSet(root, projectKey, fingerprint, []structuralSymbolRecord{first}, nil)
	secondSet := buildStructuralChunkSet(root, projectKey, fingerprint, []structuralSymbolRecord{second}, nil)
	if firstSet.Files["fresh.go"].ProfileHash == secondSet.Files["fresh.go"].ProfileHash {
		t.Fatal("structural profile must change when fallback symbol name changes")
	}
}

func TestStructuralChunksPreserveLongSymbolTailAndFileGaps(t *testing.T) {
	root := t.TempDir()
	longBody := "func Long() string { return \"START_" + strings.Repeat("界", 3500) + "_TAIL_SENTINEL\" }"
	content := "package fixture\nvar Before = 1\n" + longBody + "\nvar After = 2\n"
	writeStructuralFixture(t, root, "long.go", content)
	projectKey, _ := structuralProjectKey(root)
	fingerprint := strings.Repeat("a", 64)
	record := validStructuralRecord(projectKey, fingerprint, "long.go", "Long", longBody)
	record.StartLine = 3
	record.EndLine = 3
	record.Content = longBody[:32]
	record.ContentTruncated = true
	record.ContentHash = sha256Hex(longBody)

	set := buildStructuralChunkSet(root, projectKey, fingerprint, []structuralSymbolRecord{record}, nil)
	file := set.Files["long.go"]
	var reconstructed strings.Builder
	foundTail := false
	foundBefore := false
	foundAfter := false
	for i, chunk := range file.Chunks {
		if len(chunk.Content) > structuralPreviewMaxBytes || !utf8.ValidString(chunk.Content) {
			t.Fatalf("chunk %d bytes=%d valid=%t", i, len(chunk.Content), utf8.ValidString(chunk.Content))
		}
		reconstructed.WriteString(chunk.Content)
		if chunk.SymbolName != "" && !strings.Contains(chunk.EmbeddingContent, chunk.Content) {
			t.Fatalf("chunk %d source was truncated out of its embedding payload", i)
		}
		foundTail = foundTail || strings.Contains(chunk.Content, "TAIL_SENTINEL")
		foundBefore = foundBefore || strings.Contains(chunk.Content, "var Before")
		foundAfter = foundAfter || strings.Contains(chunk.Content, "var After")
	}
	if got := reconstructed.String(); got != content {
		t.Fatalf("lossless reconstruction mismatch: got %d bytes, want %d", len(got), len(content))
	}
	if !foundTail || !foundBefore || !foundAfter {
		t.Fatalf("sentinels tail=%t before=%t after=%t", foundTail, foundBefore, foundAfter)
	}
}

func TestStructuralChunksPreserveVueContainerRegions(t *testing.T) {
	root := t.TempDir()
	content := "<template>\n  <button>Save</button>\n</template>\n<script setup lang=\"ts\">\nconst save = () => true\n</script>\n<style>\nbutton { color: red }\n</style>"
	writeStructuralFixture(t, root, "Button.vue", content)
	projectKey, _ := structuralProjectKey(root)
	fingerprint := strings.Repeat("a", 64)
	record := validStructuralRecord(projectKey, fingerprint, "Button.vue", "save", "const save = () => true")
	record.StartLine = 5
	record.EndLine = 5
	record.Language = "typescript"

	set := buildStructuralChunkSet(root, projectKey, fingerprint, []structuralSymbolRecord{record}, nil)
	var reconstructed strings.Builder
	for _, chunk := range set.Files["Button.vue"].Chunks {
		reconstructed.WriteString(chunk.Content)
	}
	if got := reconstructed.String(); got != content {
		t.Fatalf("Vue structural replacement lost a container region\n--- got ---\n%s\n--- want ---\n%s", got, content)
	}
}

func TestStructuralChunksAssignNestedLinesToNarrowestSymbol(t *testing.T) {
	root := t.TempDir()
	content := "type Outer struct {\n  field int\n  func Inner() {\n    use(field)\n  }\n}"
	writeStructuralFixture(t, root, "nested.go", content)
	projectKey, _ := structuralProjectKey(root)
	fingerprint := strings.Repeat("a", 64)
	outerContent := structuralLineRange([]byte(content), 1, 6)
	outer := validStructuralRecord(projectKey, fingerprint, "nested.go", "Outer", outerContent)
	outer.EndLine = 6
	outer.Kind = "class"
	innerContent := structuralLineRange([]byte(content), 3, 5)
	inner := validStructuralRecord(projectKey, fingerprint, "nested.go", "Inner", innerContent)
	inner.StartLine = 3
	inner.EndLine = 5

	set := buildStructuralChunkSet(root, projectKey, fingerprint, []structuralSymbolRecord{outer, inner}, nil)
	var reconstructed strings.Builder
	innerOccurrences := 0
	for _, chunk := range set.Files["nested.go"].Chunks {
		reconstructed.WriteString(chunk.Content)
		innerOccurrences += strings.Count(chunk.Content, "func Inner")
	}
	if reconstructed.String() != content {
		t.Fatalf("nested reconstruction duplicated or lost source: %q", reconstructed.String())
	}
	if innerOccurrences != 1 {
		t.Fatalf("nested function occurrences = %d, want 1", innerOccurrences)
	}
}

// TestCodemapStructuralSharedV1Fixture pins the byte-for-byte fixture copied
// from codemap and runs it through the consumer's real envelope/record guards.
func TestCodemapStructuralSharedV1Fixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "codemap_structural_export_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var page structuralExportReport
	if err := json.Unmarshal(data, &page); err != nil {
		t.Fatalf("parse shared v1 fixture: %v", err)
	}
	if err := validateStructuralPage(page, 0, 128, 262144, page.ProjectKey, "", "", -1); err != nil {
		t.Fatalf("validate shared v1 page: %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("shared v1 records = %d, want 1", len(page.Records))
	}
	if page.Records[0].Ordinal == nil || *page.Records[0].Ordinal != 1 {
		t.Fatalf("shared v1 ordinal = %v, want 1", page.Records[0].Ordinal)
	}
	if _, issue, err := validateStructuralRecord(page.Records[0], page); err != nil || issue != nil {
		t.Fatalf("validate shared v1 record: fatal=%v issue=%v", err, issue)
	}
}

func validStructuralRecord(projectKey, fingerprint, file, symbol, content string) structuralSymbolRecord {
	return structuralSymbolRecord{
		SchemaVersion:    1,
		Project:          "fixture",
		ProjectKey:       projectKey,
		IndexFingerprint: fingerprint,
		File:             file,
		StartLine:        1,
		EndLine:          1,
		Symbol:           symbol,
		FQN:              "fixture." + symbol,
		Kind:             "function",
		Language:         "go",
		SourceHash:       strings.Repeat("b", 64),
		Content:          content,
		ContentHash:      sha256Hex(content),
	}
}

func singleStructuralPageSource(projectKey, fingerprint string, records []structuralSymbolRecord) *codemapStructuralSource {
	return &codemapStructuralSource{
		bin:        "codemap",
		pageLimit:  max(1, len(records)),
		maxContent: 64,
		runPage: func(_ context.Context, _, _ string, offset, limit, maxContent int) ([]byte, error) {
			return json.Marshal(structuralExportReport{
				SchemaVersion:    1,
				Project:          "fixture",
				ProjectKey:       projectKey,
				IndexFingerprint: fingerprint,
				Offset:           offset,
				Limit:            limit,
				MaxContentBytes:  maxContent,
				TotalRecords:     len(records),
				ReturnedRecords:  len(records),
				Complete:         true,
				Records:          records,
			})
		},
	}
}

func writeStructuralFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
