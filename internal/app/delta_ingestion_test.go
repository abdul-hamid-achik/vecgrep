package app

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestPlanDeltaIngestionCases pins the certified selective re-ingestion
// decision: the plan applies only when a complete, error-free receipt
// certifies exactly the attested from_fingerprint and the manifest is exactly
// the to_fingerprint. Every other combination is a conservative nil (full
// ingestion path).
func TestPlanDeltaIngestionCases(t *testing.T) {
	fromFp := strings.Repeat("a", 64)
	toFp := strings.Repeat("b", 64)
	otherFp := strings.Repeat("c", 64)

	validReceipt := &IngestionReceipt{
		ScopeComplete:     true,
		IngestionComplete: true,
		IndexFingerprint:  fromFp,
	}
	validManifest := &StructuralManifestReport{
		IndexFingerprint: toFp,
		ReindexDelta: &StructuralReindexDelta{
			FromFingerprint: fromFp,
			ToFingerprint:   toFp,
			ChangedFiles:    []string{"changed.go"},
			NewFiles:        []string{"new.go"},
			DeletedFiles:    []string{"deleted.go"},
		},
	}

	plan := planDeltaIngestion(validReceipt, validManifest)
	if plan == nil {
		t.Fatal("certified receipt + matching attestation must produce a plan")
	}
	if strings.Join(plan.ChangedNew, ",") != "changed.go,new.go" {
		t.Errorf("changedNew = %v, want changed.go,new.go", plan.ChangedNew)
	}
	if strings.Join(plan.Deleted, ",") != "deleted.go" {
		t.Errorf("deleted = %v, want deleted.go", plan.Deleted)
	}

	cases := []struct {
		name     string
		receipt  *IngestionReceipt
		manifest *StructuralManifestReport
	}{
		{"nil receipt", nil, validManifest},
		{"nil manifest", validReceipt, nil},
		{"no attestation", validReceipt, &StructuralManifestReport{IndexFingerprint: toFp}},
		{
			"receipt certified a different fingerprint",
			&IngestionReceipt{ScopeComplete: true, IngestionComplete: true, IndexFingerprint: otherFp},
			validManifest,
		},
		{
			"manifest is not the to-side",
			validReceipt,
			&StructuralManifestReport{IndexFingerprint: otherFp, ReindexDelta: validManifest.ReindexDelta},
		},
		{
			"incomplete receipt",
			&IngestionReceipt{ScopeComplete: false, IngestionComplete: false, IndexFingerprint: fromFp},
			validManifest,
		},
		{
			"receipt with errors",
			&IngestionReceipt{ScopeComplete: true, IngestionComplete: true, ErrorCount: 2, IndexFingerprint: fromFp},
			validManifest,
		},
		{
			"empty delta",
			validReceipt,
			&StructuralManifestReport{IndexFingerprint: toFp, ReindexDelta: &StructuralReindexDelta{
				FromFingerprint: fromFp, ToFingerprint: toFp,
			}},
		},
	}
	for _, tc := range cases {
		if got := planDeltaIngestion(tc.receipt, tc.manifest); got != nil {
			t.Errorf("%s: expected nil plan, got %+v", tc.name, got)
		}
	}
}

// TestFilteredStructuralSourceLoadsV2 exercises the v2 filtered loader against
// a stubbed producer: it must serve the filtered pages, verify the echoed
// filter fingerprint, scope totals to the slice, and clean up the temp list.
func TestFilteredStructuralSourceLoadsV2(t *testing.T) {
	root := t.TempDir()
	source, err := newFilteredCodemapStructuralSource("codemap", []string{"./a.go", "b.go", "a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if source.filesFromPath == "" {
		t.Fatal("filtered source must carry a filter list file")
	}
	// The list is canonicalized (./a.go deduped into a.go) and sorted.
	raw, err := os.ReadFile(source.filesFromPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(nonEmptyLines(string(raw)), ",") != "a.go,b.go" {
		t.Fatalf("filter list = %q, want a.go,b.go", strings.TrimSpace(string(raw)))
	}

	projectKey, err := structuralProjectKey(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(root+"/"+f, []byte("package x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint := strings.Repeat("d", 64)
	mkRecord := func(file, symbol string) structuralSymbolRecord {
		rec := validStructuralRecord(projectKey, fingerprint, file, symbol, "package x")
		rec.SchemaVersion = structuralExportFilteredSchemaVersion
		return rec
	}
	source.runFiltered = func(_ context.Context, _, _, filesFrom string, offset, limit, maxContent int) ([]byte, error) {
		if filesFrom != source.filesFromPath {
			t.Errorf("filtered page got list %q, want %q", filesFrom, source.filesFromPath)
		}
		records := []structuralSymbolRecord{mkRecord("a.go", "A"), mkRecord("b.go", "B")}
		end := min(offset+limit, len(records))
		page := structuralExportReport{
			SchemaVersion:          structuralExportFilteredSchemaVersion,
			Project:                "fixture",
			ProjectKey:             projectKey,
			IndexFingerprint:       fingerprint,
			FilesFilter:            []string{"a.go", "b.go"},
			FilesFilterFingerprint: source.expectedFilterFingerprint,
			Offset:                 offset,
			Limit:                  limit,
			MaxContentBytes:        maxContent,
			TotalRecords:           len(records),
			ReturnedRecords:        end - offset,
			Complete:               end == len(records),
			Records:                records[offset:end],
		}
		if !page.Complete {
			page.NextOffset = end
		}
		return json.Marshal(page)
	}

	set, err := source.LoadStructuralChunks(context.Background(), root)
	if err != nil {
		t.Fatalf("LoadStructuralChunks: %v", err)
	}
	if len(set.Files) != 2 {
		t.Fatalf("structural files = %d, want 2", len(set.Files))
	}
	if _, ok := set.Files["a.go"]; !ok {
		t.Error("a.go missing from filtered set")
	}
	if set.IndexFingerprint != fingerprint {
		t.Errorf("set fingerprint = %q, want the full-index fingerprint", set.IndexFingerprint)
	}
	if _, err := os.Stat(source.filesFromPath); !os.IsNotExist(err) {
		t.Errorf("filter list file should be removed after load (stat err = %v)", err)
	}
}

// TestFilteredStructuralSourceRejectsForeignFingerprint fails the load closed
// when the producer echoes a filter fingerprint that is not the one requested.
func TestFilteredStructuralSourceRejectsForeignFingerprint(t *testing.T) {
	root := t.TempDir()
	source, err := newFilteredCodemapStructuralSource("codemap", []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	rec := validStructuralRecord("0123456789abcdef0123456789abcdef", strings.Repeat("d", 64), "a.go", "A", "package x")
	rec.SchemaVersion = structuralExportFilteredSchemaVersion
	source.runFiltered = func(_ context.Context, _, _, _ string, offset, limit, maxContent int) ([]byte, error) {
		page := structuralExportReport{
			SchemaVersion:          structuralExportFilteredSchemaVersion,
			Project:                "fixture",
			ProjectKey:             "0123456789abcdef0123456789abcdef",
			IndexFingerprint:       strings.Repeat("d", 64),
			FilesFilter:            []string{"a.go"},
			FilesFilterFingerprint: strings.Repeat("e", 64),
			Offset:                 offset,
			Limit:                  limit,
			MaxContentBytes:        maxContent,
			TotalRecords:           1,
			ReturnedRecords:        1,
			Complete:               true,
			Records:                []structuralSymbolRecord{rec},
		}
		return json.Marshal(page)
	}
	if _, err := source.LoadStructuralChunks(context.Background(), root); err == nil {
		t.Fatal("a foreign filter fingerprint must fail the load")
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
