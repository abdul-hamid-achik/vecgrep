package search

import (
	"context"
	"testing"
)

func TestNewMultiSearcher(t *testing.T) {
	ms := NewMultiSearcher()
	if ms == nil {
		t.Fatal("expected non-nil MultiSearcher")
	}
	if ms.searchers == nil {
		t.Error("searchers map should be initialized")
	}
}

func TestMultiSearcher_AddRemoveProfile(t *testing.T) {
	ms := NewMultiSearcher()

	// Add profile
	ms.AddProfile("test", nil)

	profiles := ms.ListProfiles()
	if len(profiles) != 1 {
		t.Errorf("ListProfiles() length = %d, want 1", len(profiles))
	}
	if profiles[0] != "test" {
		t.Errorf("profile name = %s, want test", profiles[0])
	}

	// Remove profile
	ms.RemoveProfile("test")
	profiles = ms.ListProfiles()
	if len(profiles) != 0 {
		t.Errorf("ListProfiles() length = %d, want 0", len(profiles))
	}
}

func TestMultiSearcher_GetProfile(t *testing.T) {
	ms := NewMultiSearcher()

	// Get non-existent profile
	_, found := ms.GetProfile("nonexistent")
	if found {
		t.Error("expected not found for non-existent profile")
	}

	// Add and get profile
	ms.AddProfile("test", nil)
	_, found = ms.GetProfile("test")
	if !found {
		t.Error("expected found for existing profile")
	}
}

func TestMultiSearcher_ListProfiles(t *testing.T) {
	ms := NewMultiSearcher()

	ms.AddProfile("alpha", nil)
	ms.AddProfile("beta", nil)
	ms.AddProfile("gamma", nil)

	profiles := ms.ListProfiles()
	if len(profiles) != 3 {
		t.Errorf("ListProfiles() length = %d, want 3", len(profiles))
	}

	// Should be sorted alphabetically
	expected := []string{"alpha", "beta", "gamma"}
	for i, name := range profiles {
		if name != expected[i] {
			t.Errorf("profile[%d] = %s, want %s", i, name, expected[i])
		}
	}
}

func TestMultiSearchOptions_Defaults(t *testing.T) {
	opts := MultiSearchOptions{}

	// MergeResults default
	if opts.MergeResults != false {
		t.Error("MergeResults should default to false")
	}

	// Profiles default (empty)
	if len(opts.Profiles) != 0 {
		t.Error("Profiles should default to empty")
	}
}

func TestMultiSearcher_SearchNoProfiles(t *testing.T) {
	ms := NewMultiSearcher()
	ctx := context.Background()

	_, err := ms.Search(ctx, "test query", MultiSearchOptions{})
	if err == nil {
		t.Error("expected error when no profiles available")
	}
}

func TestMultiResult(t *testing.T) {
	result := MultiResult{
		Result: Result{
			ChunkID:      1,
			FilePath:     "/test/file.go",
			RelativePath: "file.go",
			Content:      "test content",
			Score:        0.95,
		},
		Profile: "code",
	}

	if result.Profile != "code" {
		t.Errorf("Profile = %s, want code", result.Profile)
	}
	if result.Score != 0.95 {
		t.Errorf("Score = %f, want 0.95", result.Score)
	}
}

func TestMultiSearchResult(t *testing.T) {
	result := &MultiSearchResult{
		Results:          make([]MultiResult, 0),
		ByProfile:        make(map[string][]Result),
		ProfilesSearched: []string{"profile1", "profile2"},
		Errors:           make(map[string]error),
	}

	if len(result.ProfilesSearched) != 2 {
		t.Errorf("ProfilesSearched length = %d, want 2", len(result.ProfilesSearched))
	}
}
