package graphdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
)

// View is a read-only transaction pinned to one active generation. It must be
// closed before its owning Store is closed.
type View struct {
	tx         *sql.Tx
	generation graph.Generation
	release    func()
	closeOnce  sync.Once
	closeErr   error
}

var _ graph.QueryView = (*View)(nil)

// OpenView acquires a consistent read transaction for an active generation.
func (s *Store) OpenView(ctx context.Context, repoID repository.RepoID, worktreeID repository.WorktreeID, id graph.GenerationID) (*View, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	if id <= 0 {
		return nil, fmt.Errorf("%w: generation ID must be positive", ErrInvalidGeneration)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin graph view: %w", err)
	}
	generation, err := scanGeneration(ctx, tx, repoID, worktreeID, id)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if generation.State != graph.GenerationActive {
		_ = tx.Rollback()
		return nil, fmt.Errorf("%w: generation %d is %s", ErrStaleGeneration, id, generation.State)
	}
	var active sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT active_generation FROM graph_state WHERE repo_id = ? AND worktree_id = ?`, string(repoID), string(worktreeID)).Scan(&active); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("read active graph generation: %w", err)
	}
	if !active.Valid || active.Int64 != int64(id) {
		_ = tx.Rollback()
		return nil, fmt.Errorf("%w: generation %d is not active", ErrStaleGeneration, id)
	}
	s.retainReader(repoID, worktreeID, id)
	return &View{
		tx:         tx,
		generation: generation,
		release: func() {
			s.releaseReader(repoID, worktreeID, id)
		},
	}, nil
}

func scanGeneration(ctx context.Context, tx *sql.Tx, repoID repository.RepoID, worktreeID repository.WorktreeID, id graph.GenerationID) (graph.Generation, error) {
	var commit, fingerprint, analyzerVersion, state, message, created, validated, retired string
	var schema int
	if err := tx.QueryRowContext(ctx, `
		SELECT commit_sha, build_fingerprint, analyzer_version, schema_version,
		       state, error, created_at, COALESCE(validated_at, ''), COALESCE(retired_at, '')
		FROM graph_generations
		WHERE repo_id = ? AND worktree_id = ? AND generation_id = ?
	`, string(repoID), string(worktreeID), int64(id)).Scan(&commit, &fingerprint, &analyzerVersion, &schema, &state, &message, &created, &validated, &retired); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return graph.Generation{}, fmt.Errorf("%w: generation %d", ErrNotFound, id)
		}
		return graph.Generation{}, fmt.Errorf("read graph view generation %d: %w", id, err)
	}
	generation := graph.Generation{RepoID: repoID, WorktreeID: worktreeID, ID: id, Commit: graph.CommitSHA(commit), BuildFingerprint: fingerprint, AnalyzerVersion: analyzerVersion, SchemaVersion: schema, State: graph.GenerationState(state), Error: message}
	createdAt, err := parseTimestamp(created)
	if err != nil {
		return graph.Generation{}, fmt.Errorf("parse graph view creation time: %w", err)
	}
	generation.CreatedAt = createdAt
	if validated != "" {
		value, parseErr := parseTimestamp(validated)
		if parseErr != nil {
			return graph.Generation{}, fmt.Errorf("parse graph view validation time: %w", parseErr)
		}
		generation.ValidatedAt = &value
	}
	if retired != "" {
		value, parseErr := parseTimestamp(retired)
		if parseErr != nil {
			return graph.Generation{}, fmt.Errorf("parse graph view retirement time: %w", parseErr)
		}
		generation.RetiredAt = &value
	}
	return generation, nil
}

// Generation returns the generation pinned by this view.
func (v *View) Generation() graph.Generation {
	if v == nil {
		return graph.Generation{}
	}
	return v.generation
}

// Close releases the read transaction. It is safe to call repeatedly.
func (v *View) Close() error {
	if v == nil {
		return nil
	}
	v.closeOnce.Do(func() {
		if v.tx != nil {
			err := v.tx.Rollback()
			v.tx = nil
			if err != nil && !errors.Is(err, sql.ErrTxDone) {
				v.closeErr = fmt.Errorf("close graph view: %w", err)
			}
		}
		if v.release != nil {
			v.release()
			v.release = nil
		}
	})
	return v.closeErr
}
