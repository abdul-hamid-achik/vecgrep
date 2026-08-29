package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

// failReader simulates a scripted run where the confirm prompt cannot be read
// (e.g. stdin is /dev/null: still a char device, so the TTY check passes, but
// ReadString hits EOF immediately).
type failReader struct{}

func (failReader) Read(_ []byte) (int, error) { return 0, errors.New("EOF") }

func TestConfirmIndexPlanReaderErrorNamesYesFlag(t *testing.T) {
	preview := &index.DryRunPreview{FilesToEmbed: 3, ScannedFiles: 10, EstimatedChunks: 5}
	_, err := confirmIndexPlanReader("/tmp/repo", true, false, preview, failReader{})
	if err == nil {
		t.Fatal("expected an error when the confirmation read fails")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("error %q should suggest the --yes flag", err)
	}
	if !strings.Contains(err.Error(), "read confirmation") {
		t.Fatalf("error %q should keep the read-confirmation context", err)
	}
}

func TestConfirmIndexPlanReaderParsesAnswers(t *testing.T) {
	preview := &index.DryRunPreview{FilesToEmbed: 3, ScannedFiles: 10, EstimatedChunks: 5}
	tests := []struct {
		name    string
		input   string
		confirm bool
	}{
		{"yes lowercase", "y\n", true},
		{"yes word with spaces", "  YES  \n", true},
		{"no", "n\n", false},
		{"empty line", "\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := confirmIndexPlanReader("/tmp/repo", false, true, preview, strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tc.confirm {
				t.Fatalf("confirm = %v, want %v", ok, tc.confirm)
			}
		})
	}
}

func TestNeedsInteractiveIndexConfirm(t *testing.T) {
	small := &index.DryRunPreview{FilesToEmbed: 3, ScannedFiles: 10, EstimatedChunks: 5}
	if needsInteractiveIndexConfirm(false, false, small) {
		t.Fatal("small incremental should not confirm")
	}
	if !needsInteractiveIndexConfirm(true, false, small) {
		t.Fatal("full reindex should always confirm")
	}
	if !needsInteractiveIndexConfirm(false, true, small) {
		t.Fatal("empty index should always confirm")
	}
	large := &index.DryRunPreview{FilesToEmbed: index.ConfirmScopeFiles}
	if !needsInteractiveIndexConfirm(false, false, large) {
		t.Fatal("large plan should confirm")
	}
}

func TestFormatPlanBytes(t *testing.T) {
	if got := formatPlanBytes(512); got != "512 B" {
		t.Fatalf("got %q", got)
	}
	if got := formatPlanBytes(2048); got != "2.0 KiB" {
		t.Fatalf("got %q", got)
	}
}
