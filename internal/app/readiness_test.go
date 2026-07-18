package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
)

func TestDeriveReadiness_Priority(t *testing.T) {
	tests := []struct {
		name           string
		indexed        bool
		fresh          bool
		chunks         int
		profileMatches bool
		profileStatus  string
		freshness      *IndexFreshnessReport
		storedID       string
		activeID       string
		wantState      ReadinessState
		wantAction     string
		wantBlocks     bool
		wantProfileOK  bool // ProfileMatches after derive
	}{
		{
			name:       "empty",
			indexed:    false,
			fresh:      false,
			chunks:     0,
			wantState:  ReadinessEmpty,
			wantAction: ActionIndex,
			wantBlocks: true,
		},
		{
			name:           "empty beats mismatch noise",
			indexed:        false,
			chunks:         0,
			profileMatches: false,
			profileStatus:  "mismatch",
			wantState:      ReadinessEmpty,
			wantAction:     ActionIndex,
			wantBlocks:     true,
			wantProfileOK:  true, // cleared for empty
		},
		{
			name:           "profile mismatch",
			indexed:        true,
			fresh:          true,
			chunks:         10,
			profileMatches: false,
			profileStatus:  "mismatch",
			storedID:       "stored-id",
			activeID:       "active-id",
			wantState:      ReadinessProfileMismatch,
			wantAction:     ActionIndexForce,
			wantBlocks:     true,
		},
		{
			name:           "mismatch beats stale",
			indexed:        true,
			fresh:          false,
			chunks:         10,
			profileMatches: false,
			profileStatus:  "mismatch",
			freshness:      &IndexFreshnessReport{State: IndexFreshnessStale, Reason: "pending"},
			storedID:       "a",
			activeID:       "b",
			wantState:      ReadinessProfileMismatch,
			wantAction:     ActionIndexForce,
			wantBlocks:     true,
		},
		{
			name:           "unknown freshness",
			indexed:        true,
			fresh:          false,
			chunks:         5,
			profileMatches: true,
			freshness: &IndexFreshnessReport{
				State:  IndexFreshnessUnknown,
				Reason: "raw_source_hashes_incomplete",
			},
			wantState:  ReadinessUnknown,
			wantAction: ActionIndexForce,
			wantBlocks: false,
		},
		{
			name:           "stale",
			indexed:        true,
			fresh:          false,
			chunks:         5,
			profileMatches: true,
			freshness: &IndexFreshnessReport{
				State:  IndexFreshnessStale,
				Reason: "pending changes",
			},
			wantState:  ReadinessStale,
			wantAction: ActionIndex,
			wantBlocks: false,
		},
		{
			name:           "ready",
			indexed:        true,
			fresh:          true,
			chunks:         5,
			profileMatches: true,
			freshness:      &IndexFreshnessReport{State: IndexFreshnessFresh, Reason: "fresh"},
			wantState:      ReadinessReady,
			wantAction:     "",
			wantBlocks:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Default profileMatches true unless test set false intentionally
			// via the field; for empty-beats we pass false explicitly.
			r := DeriveReadiness(tc.indexed, tc.fresh, tc.chunks, tc.profileMatches, tc.profileStatus, tc.freshness, tc.storedID, tc.activeID)
			if r.State != tc.wantState {
				t.Fatalf("state = %q, want %q", r.State, tc.wantState)
			}
			if r.Action != tc.wantAction {
				t.Fatalf("action = %q, want %q", r.Action, tc.wantAction)
			}
			if r.BlocksSearch() != tc.wantBlocks {
				t.Fatalf("BlocksSearch = %v, want %v", r.BlocksSearch(), tc.wantBlocks)
			}
			if tc.name == "profile mismatch" {
				if r.StoredProfileID != "stored-id" || r.ActiveProfileID != "active-id" {
					t.Fatalf("profile IDs = %q/%q", r.StoredProfileID, r.ActiveProfileID)
				}
				if r.Reason == "" || r.Reason == "force for freshness" {
					t.Fatalf("reason = %q, want profile wording", r.Reason)
				}
			}
		})
	}
}

func TestServiceReadiness_EmptyIndex(t *testing.T) {
	_, service := createTestSession(t)
	r, err := service.Readiness(context.Background())
	if err != nil {
		t.Fatalf("Readiness: %v", err)
	}
	if r.State != ReadinessEmpty {
		t.Fatalf("state = %q, want empty", r.State)
	}
	if r.Action != ActionIndex {
		t.Fatalf("action = %q, want %q", r.Action, ActionIndex)
	}
	if !r.BlocksSearch() {
		t.Fatal("BlocksSearch = false, want true")
	}
	if r.Indexed || r.Chunks != 0 {
		t.Fatalf("indexed/chunks = %v/%d", r.Indexed, r.Chunks)
	}
}

func TestServiceReadiness_ProfileMismatch(t *testing.T) {
	session, service := createTestSession(t)

	chunk := db.NewChunkRecord(
		filepath.Join(session.ProjectRoot, "main.go"),
		"main.go",
		"hash",
		64,
		"go",
		"func LoadConfig() error { return nil }",
		1, 1, 0, 36,
		"function",
		"LoadConfig",
		session.ProjectRoot,
	)
	if _, err := session.DB.InsertChunk(chunk, make([]float32, session.Config.Embedding.Dimensions)); err != nil {
		t.Fatalf("InsertChunk: %v", err)
	}

	// Store a profile that cannot match the active configuration.
	stored := CurrentEmbeddingProfile(session.Config)
	stored.Preprocessor = "code-chunker-v1"
	stored.ProfileID = "ollama:nomic-embed-text:768:cosine:code-chunker-v1"
	if err := SaveEmbeddingProfile(session.DB, session.Config.DataDir, stored); err != nil {
		t.Fatalf("SaveEmbeddingProfile: %v", err)
	}

	r, err := service.Readiness(context.Background())
	if err != nil {
		t.Fatalf("Readiness: %v", err)
	}
	if r.State != ReadinessProfileMismatch {
		t.Fatalf("state = %q, want profile_mismatch (got reason %q)", r.State, r.Reason)
	}
	if r.Action != ActionIndexForce {
		t.Fatalf("action = %q, want %q", r.Action, ActionIndexForce)
	}
	if !r.BlocksSearch() {
		t.Fatal("BlocksSearch = false, want true")
	}
	if r.ProfileMatches {
		t.Fatal("ProfileMatches = true, want false")
	}
	if r.StoredProfileID == "" || r.ActiveProfileID == "" {
		t.Fatalf("missing profile IDs: stored=%q active=%q", r.StoredProfileID, r.ActiveProfileID)
	}
	if r.StoredProfileID == r.ActiveProfileID {
		t.Fatal("stored and active profile IDs should differ")
	}
}

func TestServiceReadiness_StaleAllowsSearch(t *testing.T) {
	session, service := createTestSession(t)

	chunk := db.NewChunkRecord(
		filepath.Join(session.ProjectRoot, "main.go"),
		"main.go",
		"hash",
		64,
		"go",
		"func LoadConfig() error { return nil }",
		1, 1, 0, 36,
		"function",
		"LoadConfig",
		session.ProjectRoot,
	)
	if _, err := session.DB.InsertChunk(chunk, make([]float32, session.Config.Embedding.Dimensions)); err != nil {
		t.Fatalf("InsertChunk: %v", err)
	}
	if err := service.saveCurrentEmbeddingProfile(); err != nil {
		t.Fatalf("save profile: %v", err)
	}

	r, err := service.Readiness(context.Background())
	if err != nil {
		t.Fatalf("Readiness: %v", err)
	}
	// Source file absent from disk → pending deletion → not ready/fresh.
	if r.State == ReadinessEmpty || r.State == ReadinessProfileMismatch {
		t.Fatalf("state = %q, want stale/unknown (searchable)", r.State)
	}
	if r.BlocksSearch() {
		t.Fatalf("BlocksSearch = true for state %q, want false", r.State)
	}
	if !r.Indexed || r.Chunks != 1 {
		t.Fatalf("indexed/chunks = %v/%d", r.Indexed, r.Chunks)
	}
	if r.ProfileMatches != true {
		t.Fatal("ProfileMatches = false, want true")
	}
}

func TestReadinessJSON_Parseable(t *testing.T) {
	r := DeriveReadiness(false, false, 0, true, "", nil, "", "")
	js := r.JSON()
	if js == "" {
		t.Fatal("empty JSON")
	}
	for _, part := range []string{`"state":"empty"`, `"action":"vecgrep_index"`, `"indexed":false`} {
		if !strings.Contains(js, part) {
			t.Fatalf("JSON missing %q: %s", part, js)
		}
	}
}
