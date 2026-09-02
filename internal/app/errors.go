package app

import "errors"

var (
	ErrNoProject                = errors.New("not in a vecgrep project")
	ErrMigrationRequired        = errors.New("legacy vecgrep database found without a veclite index")
	ErrProviderRequired         = errors.New("embedding provider required")
	ErrEmbeddingProfileMismatch = errors.New("embedding profile mismatch")
)

// ErrIndexNotBuilt is returned when a project is registered but has never been
// indexed: the veclite store does not exist yet. It is distinct from an empty
// index (store exists, zero chunks) and from a stale/old-version store, so
// callers can point at `vecgrep index` instead of the destructive `reset`.
var ErrIndexNotBuilt = errors.New("index not built")
