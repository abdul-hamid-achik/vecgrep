package app

import "errors"

var (
	ErrNoProject         = errors.New("not in a vecgrep project")
	ErrMigrationRequired = errors.New("legacy vecgrep database found without a veclite index")
	ErrProviderRequired  = errors.New("embedding provider required")
)
