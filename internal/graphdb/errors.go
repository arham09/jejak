package graphdb

import (
	"errors"

	"github.com/arham09/jejak/internal/graph"
)

// Persistence errors callers can classify without depending on SQLite
// driver-specific values.
var (
	ErrStoreClosed       = errors.New("graph store is closed")
	ErrNotFound          = errors.New("graph record not found")
	ErrStaleGeneration   = errors.New("graph generation is stale")
	ErrStaleState        = graph.ErrStaleState
	ErrInvalidGeneration = errors.New("invalid graph generation")
)
