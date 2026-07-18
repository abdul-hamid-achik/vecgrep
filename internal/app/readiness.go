package app

import (
	"context"
	"encoding/json"
	"fmt"
)

// ReadinessState is the primary machine label for index searchability.
// Booleans on Readiness remain the source of truth; State is derived.
type ReadinessState string

const (
	ReadinessEmpty           ReadinessState = "empty"
	ReadinessProfileMismatch ReadinessState = "profile_mismatch"
	ReadinessUnknown         ReadinessState = "unknown"
	ReadinessStale           ReadinessState = "stale"
	ReadinessReady           ReadinessState = "ready"
)

// Stable MCP/CLI action strings. Agents should treat these as exact next steps.
const (
	ActionIndex      = "vecgrep_index"
	ActionIndexForce = "vecgrep_index force:true"
)

// Readiness is the shared envelope for CLI and MCP consumers.
//
// Priority for State (first match wins):
// empty → profile_mismatch → unknown → stale → ready
type Readiness struct {
	State           ReadinessState `json:"state"`
	Indexed         bool           `json:"indexed"`
	Fresh           bool           `json:"fresh"`
	Chunks          int            `json:"chunks"`
	ProfileMatches  bool           `json:"profile_matches"`
	Action          string         `json:"action,omitempty"`
	Reason          string         `json:"reason,omitempty"`
	StoredProfileID string         `json:"stored_profile_id,omitempty"`
	ActiveProfileID string         `json:"active_profile_id,omitempty"`
}

// BlocksSearch reports whether MCP search should fail with IsError instead of
// returning hits or a false "No results found." Empty and profile mismatch
// block; stale/unknown/ready do not.
func (r Readiness) BlocksSearch() bool {
	return r.State == ReadinessEmpty || r.State == ReadinessProfileMismatch
}

// JSON returns a compact JSON object for agent-parseable tool text.
func (r Readiness) JSON() string {
	b, err := json.Marshal(r)
	if err != nil {
		return `{"state":"unknown","reason":"marshal readiness failed"}`
	}
	return string(b)
}

// DeriveReadiness maps facts into a Readiness value without I/O.
// freshness may be nil when the index is empty or freshness could not be loaded.
func DeriveReadiness(
	indexed bool,
	fresh bool,
	chunks int,
	profileMatches bool,
	profileStatus string,
	freshness *IndexFreshnessReport,
	storedID, activeID string,
) Readiness {
	r := Readiness{
		Indexed:         indexed,
		Fresh:           fresh,
		Chunks:          chunks,
		ProfileMatches:  profileMatches,
		StoredProfileID: storedID,
		ActiveProfileID: activeID,
	}

	// 1. Empty / never indexed wins over missing profile or freshness noise.
	if !indexed {
		r.State = ReadinessEmpty
		r.Fresh = false
		r.ProfileMatches = true // no vectors to mismatch against
		r.Action = ActionIndex
		r.Reason = "index is empty; run vecgrep_index before search"
		r.StoredProfileID = ""
		r.ActiveProfileID = ""
		return r
	}

	// 2. Profile mismatch (including missing profile on a non-empty index).
	if !profileMatches {
		r.State = ReadinessProfileMismatch
		r.Action = ActionIndexForce
		switch profileStatus {
		case "missing":
			r.Reason = "embedding profile is missing for an existing index; call vecgrep_index with force:true to rebuild"
		case "mismatch":
			if storedID != "" && activeID != "" {
				r.Reason = fmt.Sprintf("stored embedding profile does not match active configuration: stored %q, active %q; call vecgrep_index with force:true to rebuild",
					storedID, activeID)
			} else {
				r.Reason = "stored embedding profile does not match active configuration; call vecgrep_index with force:true to rebuild"
			}
		default:
			if profileStatus != "" {
				r.Reason = profileStatus
			} else {
				r.Reason = "embedding profile does not match active configuration; call vecgrep_index with force:true to rebuild"
			}
		}
		return r
	}

	// 3. Freshness unknown (profile OK).
	if freshness != nil && freshness.State == IndexFreshnessUnknown {
		r.State = ReadinessUnknown
		r.Fresh = false
		r.Action = ActionIndexForce
		r.Reason = freshness.Reason
		if r.Reason == "" {
			r.Reason = "freshness unknown; call vecgrep_index with force:true to rebuild trusted metadata"
		}
		return r
	}

	// 4. Stale (profile OK, not fresh).
	if !fresh || (freshness != nil && freshness.State == IndexFreshnessStale) {
		r.State = ReadinessStale
		r.Fresh = false
		r.Action = ActionIndex
		if freshness != nil && freshness.Reason != "" {
			r.Reason = freshness.Reason
		} else {
			r.Reason = "index is stale; run vecgrep_index to update"
		}
		return r
	}

	// 5. Ready.
	r.State = ReadinessReady
	r.Fresh = true
	r.Action = ""
	r.Reason = ""
	return r
}

// Readiness computes the shared readiness envelope for the active session.
func (s *Service) Readiness(ctx context.Context) (Readiness, error) {
	if s == nil || s.session == nil {
		return Readiness{}, fmt.Errorf("service not initialized")
	}

	indexed, fresh, chunks, err := s.IndexMeta(ctx)
	if err != nil {
		return Readiness{}, err
	}

	current := CurrentEmbeddingProfile(s.session.Config)
	activeID := current.ProfileID
	stored, profileErr := LoadEmbeddingProfile(s.session.DB, s.session.Config.DataDir)
	profileMatches := true
	profileStatus := "ok"
	storedID := ""
	if profileErr != nil {
		profileMatches = false
		profileStatus = profileErr.Error()
	} else if stored == nil {
		if indexed {
			profileMatches = false
			profileStatus = "missing"
		} else {
			profileStatus = "not written yet"
		}
	} else {
		storedID = stored.ProfileID
		if !stored.Matches(current) {
			profileMatches = false
			profileStatus = "mismatch"
		}
	}

	var freshness *IndexFreshnessReport
	if indexed {
		// Prefer a full freshness report so unknown vs stale is distinguishable.
		// IndexMeta already collapsed unknown to fresh=false.
		report, _, freshnessErr := s.IndexFreshness(ctx)
		if freshnessErr == nil {
			freshness = report
			if report != nil {
				fresh = report.IsFresh()
			}
		} else if fresh {
			// Could not load freshness but IndexMeta claimed fresh — fail closed.
			fresh = false
			freshness = &IndexFreshnessReport{
				State:  IndexFreshnessUnknown,
				Reason: freshnessErr.Error(),
			}
		} else {
			// Indexed but not fresh without a report: treat as stale (conservative).
			freshness = &IndexFreshnessReport{
				State:  IndexFreshnessStale,
				Reason: "index is not fresh",
			}
		}
	}

	return DeriveReadiness(indexed, fresh, chunks, profileMatches, profileStatus, freshness, storedID, activeID), nil
}
