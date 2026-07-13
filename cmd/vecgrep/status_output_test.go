package main

import (
	"encoding/json"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

func TestStatusOutputIncludesConservativeFreshnessReport(t *testing.T) {
	status := &app.StatusResponse{
		ProjectRoot: "/projects/example",
		IndexFresh:  false,
		Stats:       map[string]int64{"files": 1, "chunks": 2},
		PendingChanges: &index.PendingChanges{
			ModifiedFiles: 1,
			TotalPending:  1,
		},
		Freshness: &app.IndexFreshnessReport{
			State:             app.IndexFreshnessUnknown,
			Reason:            "structural_manifest_mismatch",
			RawSourceComplete: true,
			ReceiptVerified:   true,
			ManifestRequired:  true,
		},
	}

	output := statusOutputFromResponse(status)
	if output.IndexFresh || output.Freshness == nil || output.Freshness.Reason != "structural_manifest_mismatch" {
		t.Fatalf("status output = %+v", output)
	}
	if output.PendingChanges == nil || output.PendingChanges.ModifiedFiles != 1 {
		t.Fatalf("pending output = %+v", output.PendingChanges)
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	freshness, ok := document["freshness"].(map[string]any)
	if !ok || freshness["state"] != "unknown" || freshness["reason"] != "structural_manifest_mismatch" {
		t.Fatalf("freshness JSON = %#v", document["freshness"])
	}
}
