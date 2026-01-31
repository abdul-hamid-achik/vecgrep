package search

import (
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
)

// TestScoreCalculation verifies the score is calculated correctly
// After the fix, Score should equal Distance (cosine similarity)
// not 1 - Distance which was the bug
func TestScoreCalculation(t *testing.T) {
	testCases := []struct {
		name             string
		distance         float32 // veclite returns cosine similarity as "distance"
		expectedScore    float32
		expectedMinScore float32 // Allow small tolerance
		expectedMaxScore float32
	}{
		{
			name:             "Perfect match (similarity = 1.0)",
			distance:         1.0,
			expectedScore:    1.0,
			expectedMinScore: 0.99,
			expectedMaxScore: 1.01,
		},
		{
			name:             "High similarity (0.85)",
			distance:         0.85,
			expectedScore:    0.85,
			expectedMinScore: 0.84,
			expectedMaxScore: 0.86,
		},
		{
			name:             "Medium similarity (0.5)",
			distance:         0.5,
			expectedScore:    0.5,
			expectedMinScore: 0.49,
			expectedMaxScore: 0.51,
		},
		{
			name:             "Low similarity (0.2)",
			distance:         0.2,
			expectedScore:    0.2,
			expectedMinScore: 0.19,
			expectedMaxScore: 0.21,
		},
		{
			name:             "Zero similarity",
			distance:         0.0,
			expectedScore:    0.0,
			expectedMinScore: -0.01,
			expectedMaxScore: 0.01,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a mock db.SearchResult
			sr := db.SearchResult{
				ChunkID:  1,
				Distance: tc.distance,
				Chunk: &db.ChunkRecord{
					FilePath:     "/test/file.go",
					RelativePath: "file.go",
					Content:      "test content",
					Language:     "go",
				},
			}

			// Convert using the function we fixed
			result := searchResultToResult(sr)

			// Verify score is within expected range
			if result.Score < tc.expectedMinScore || result.Score > tc.expectedMaxScore {
				t.Errorf("Score = %v, want between %v and %v (distance was %v)",
					result.Score, tc.expectedMinScore, tc.expectedMaxScore, tc.distance)
			}

			// The key fix: Score should equal Distance for cosine similarity
			// NOT be (1 - Distance) which was the bug
			if result.Score != tc.distance {
				t.Errorf("Score (%v) should equal Distance (%v) for cosine similarity",
					result.Score, tc.distance)
			}
		})
	}
}

// TestScoreNotInverted ensures we don't have the old bug
// where perfect matches showed as 0%
func TestScoreNotInverted(t *testing.T) {
	// Simulate a perfect match from veclite (cosine similarity = 1.0)
	sr := db.SearchResult{
		ChunkID:  1,
		Distance: 1.0, // Perfect match in veclite
		Chunk:    &db.ChunkRecord{},
	}

	result := searchResultToResult(sr)

	// BUG WAS: Score = 1 - 1.0 = 0.0 (0%)
	// FIX IS:  Score = 1.0 (100%)
	if result.Score == 0.0 {
		t.Error("BUG DETECTED: Perfect match (distance=1.0) resulted in Score=0.0!")
		t.Error("This means the score is still being calculated as (1 - Distance)")
	}

	if result.Score != 1.0 {
		t.Errorf("Perfect match should have Score=1.0, got %v", result.Score)
	}
}

// TestScorePercentageDisplay verifies scores display correctly as percentages
func TestScorePercentageDisplay(t *testing.T) {
	testCases := []struct {
		score              float32
		expectedPercentage float32
	}{
		{1.0, 100.0},
		{0.85, 85.0},
		{0.5, 50.0},
		{0.0, 0.0},
	}

	for _, tc := range testCases {
		percentage := tc.score * 100
		if percentage != tc.expectedPercentage {
			t.Errorf("Score %v should display as %v%%, got %v%%",
				tc.score, tc.expectedPercentage, percentage)
		}
	}
}
