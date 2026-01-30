// Package search provides semantic search functionality.
package search

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// MultiSearcher coordinates searches across multiple searcher instances (profiles).
type MultiSearcher struct {
	mu        sync.RWMutex
	searchers map[string]*Searcher
}

// NewMultiSearcher creates a new MultiSearcher.
func NewMultiSearcher() *MultiSearcher {
	return &MultiSearcher{
		searchers: make(map[string]*Searcher),
	}
}

// AddProfile adds a searcher for a profile.
func (m *MultiSearcher) AddProfile(name string, s *Searcher) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.searchers[name] = s
}

// RemoveProfile removes a searcher for a profile.
func (m *MultiSearcher) RemoveProfile(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.searchers, name)
}

// GetProfile returns a searcher for a specific profile.
func (m *MultiSearcher) GetProfile(name string) (*Searcher, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.searchers[name]
	return s, ok
}

// ListProfiles returns all registered profile names.
func (m *MultiSearcher) ListProfiles() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.searchers))
	for name := range m.searchers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// MultiSearchOptions configures multi-profile search behavior.
type MultiSearchOptions struct {
	SearchOptions

	// Profiles is the list of profile names to search.
	// If empty, searches all profiles.
	Profiles []string

	// MergeResults determines how results from different profiles are combined.
	// If true, results are merged and sorted by score.
	// If false, results are grouped by profile.
	MergeResults bool

	// MaxResultsPerProfile limits results from each profile when merging.
	MaxResultsPerProfile int
}

// MultiResult wraps a search result with its source profile.
type MultiResult struct {
	Result
	Profile string `json:"profile"`
}

// MultiSearchResult holds results from a multi-profile search.
type MultiSearchResult struct {
	// Results contains all search results.
	Results []MultiResult `json:"results"`

	// ByProfile groups results by profile name.
	ByProfile map[string][]Result `json:"by_profile,omitempty"`

	// ProfilesSearched lists all profiles that were searched.
	ProfilesSearched []string `json:"profiles_searched"`

	// Errors contains any errors that occurred during search.
	Errors map[string]error `json:"errors,omitempty"`
}

// Search performs a search across multiple profiles.
func (m *MultiSearcher) Search(ctx context.Context, query string, opts MultiSearchOptions) (*MultiSearchResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Determine which profiles to search
	profiles := opts.Profiles
	if len(profiles) == 0 {
		profiles = make([]string, 0, len(m.searchers))
		for name := range m.searchers {
			profiles = append(profiles, name)
		}
	}

	if len(profiles) == 0 {
		return nil, fmt.Errorf("no profiles available to search")
	}

	// Search all profiles in parallel
	type profileResult struct {
		profile string
		results []Result
		err     error
	}

	resultsChan := make(chan profileResult, len(profiles))
	var wg sync.WaitGroup

	for _, profileName := range profiles {
		searcher, exists := m.searchers[profileName]
		if !exists {
			resultsChan <- profileResult{
				profile: profileName,
				err:     fmt.Errorf("profile '%s' not found", profileName),
			}
			continue
		}

		wg.Add(1)
		go func(name string, s *Searcher) {
			defer wg.Done()

			searchOpts := opts.SearchOptions
			if opts.MaxResultsPerProfile > 0 {
				searchOpts.Limit = opts.MaxResultsPerProfile
			}

			results, err := s.Search(ctx, query, searchOpts)
			resultsChan <- profileResult{
				profile: name,
				results: results,
				err:     err,
			}
		}(profileName, searcher)
	}

	// Wait for all searches to complete
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Collect results
	multiResult := &MultiSearchResult{
		Results:          make([]MultiResult, 0),
		ByProfile:        make(map[string][]Result),
		ProfilesSearched: profiles,
		Errors:           make(map[string]error),
	}

	for pr := range resultsChan {
		if pr.err != nil {
			multiResult.Errors[pr.profile] = pr.err
			continue
		}

		multiResult.ByProfile[pr.profile] = pr.results

		for _, r := range pr.results {
			multiResult.Results = append(multiResult.Results, MultiResult{
				Result:  r,
				Profile: pr.profile,
			})
		}
	}

	// Sort merged results by score if requested
	if opts.MergeResults {
		sort.Slice(multiResult.Results, func(i, j int) bool {
			return multiResult.Results[i].Score > multiResult.Results[j].Score
		})

		// Apply overall limit
		if opts.Limit > 0 && len(multiResult.Results) > opts.Limit {
			multiResult.Results = multiResult.Results[:opts.Limit]
		}
	}

	return multiResult, nil
}

// SearchProfile searches a specific profile.
func (m *MultiSearcher) SearchProfile(ctx context.Context, profile, query string, opts SearchOptions) ([]Result, error) {
	m.mu.RLock()
	searcher, exists := m.searchers[profile]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("profile '%s' not found", profile)
	}

	return searcher.Search(ctx, query, opts)
}

// GetStats returns stats for all profiles.
func (m *MultiSearcher) GetStats(ctx context.Context) (map[string]map[string]any, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make(map[string]map[string]any)

	for name, searcher := range m.searchers {
		profileStats, err := searcher.GetIndexStats(ctx)
		if err != nil {
			continue
		}
		stats[name] = profileStats
	}

	return stats, nil
}
