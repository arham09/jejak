package graphdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
)

// rowQuerier is the subset of *sql.DB and *sql.Tx used to locate one row.
type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// generationRef resolves the integer storage key and lifecycle state of one
// generation. Generation-scoped tables hang off the key rather than the
// public repository/worktree/generation identity.
func generationRef(ctx context.Context, q rowQuerier, repoID repository.RepoID, worktreeID repository.WorktreeID, id graph.GenerationID) (int64, graph.GenerationState, error) {
	var key int64
	var state string
	err := q.QueryRowContext(ctx, `
		SELECT generation_key, state FROM graph_generations
		WHERE repo_id = ? AND worktree_id = ? AND generation_id = ?
	`, string(repoID), string(worktreeID), int64(id)).Scan(&key, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", fmt.Errorf("%w: generation %d", ErrNotFound, id)
	}
	if err != nil {
		return 0, "", fmt.Errorf("locate graph generation %d: %w", id, err)
	}
	return key, graph.GenerationState(state), nil
}

// PruneGenerations deletes every generation of one worktree that is not
// active and has no open in-process reader, and returns how many it removed.
// Packages, files, symbols, nodes, edges, evidence, dependencies, and test
// relationships cascade with their generation. Blob and parse-cache rows are
// repository-scoped, rebuildable optimization state and are left for GC.
//
// Callers must hold the repository writer lock. Readers in other processes
// keep their SQLite snapshot until their transaction ends, so a concurrent
// query never observes a half-deleted generation.
func (s *Store) PruneGenerations(ctx context.Context, repoID repository.RepoID, worktreeID repository.WorktreeID) (int, error) {
	if repoID == "" || worktreeID == "" {
		return 0, fmt.Errorf("%w: repository and worktree identities are required", ErrInvalidGeneration)
	}
	db, err := s.database()
	if err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin graph generation pruning: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
		SELECT generation_id FROM graph_generations
		WHERE repo_id = ? AND worktree_id = ? AND state <> ?
		ORDER BY generation_id
	`, string(repoID), string(worktreeID), string(graph.GenerationActive))
	if err != nil {
		return 0, fmt.Errorf("list prunable graph generations: %w", err)
	}
	candidates := make([]graph.GenerationID, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan prunable graph generation: %w", err)
		}
		candidates = append(candidates, graph.GenerationID(id))
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("iterate prunable graph generations: %w", err)
	}
	_ = rows.Close()
	pruned := 0
	for _, id := range candidates {
		if s.ReaderCount(repoID, worktreeID, id) > 0 {
			continue
		}
		result, err := tx.ExecContext(ctx, `
			DELETE FROM graph_generations
			WHERE repo_id = ? AND worktree_id = ? AND generation_id = ? AND state <> ?
		`, string(repoID), string(worktreeID), int64(id), string(graph.GenerationActive))
		if err != nil {
			return 0, fmt.Errorf("prune graph generation %d: %w", id, err)
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("check pruned graph generation %d: %w", id, err)
		}
		pruned += int(deleted)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit graph generation pruning: %w", err)
	}
	if pruned > 0 {
		// Return freed pages to the filesystem when the database uses
		// incremental auto-vacuum. On an older file this is a no-op: the
		// pages stay in the freelist for reuse, and gc's compaction reclaims
		// them. Either way the failure of this optimization must not fail a
		// synchronization whose generation is already active.
		_, _ = db.ExecContext(ctx, `PRAGMA incremental_vacuum`)
	}
	return pruned, nil
}

// Compact rebuilds the database file so pages freed by dropped or pruned
// generations return to the filesystem, and switches the file to incremental
// auto-vacuum so later pruning releases pages without a full rebuild. It
// returns the number of bytes the main database file shrank by. Callers must
// hold the repository writer lock and must not hold open views.
func (s *Store) Compact(ctx context.Context) (int64, error) {
	db, err := s.database()
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	before := fileSize(s.dbPath)
	if _, err := db.ExecContext(ctx, `PRAGMA auto_vacuum = INCREMENTAL`); err != nil {
		return 0, fmt.Errorf("enable incremental auto-vacuum: %w", err)
	}
	if _, err := db.ExecContext(ctx, `VACUUM`); err != nil {
		return 0, fmt.Errorf("vacuum graph database: %w", err)
	}
	// VACUUM in WAL mode writes the rebuilt content through the log; a
	// truncating checkpoint folds it into the main file so the measured size
	// reflects the compaction and the sidecar does not keep the old bytes.
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return 0, fmt.Errorf("checkpoint graph database after vacuum: %w", err)
	}
	after := fileSize(s.dbPath)
	if after >= before {
		return 0, nil
	}
	return before - after, nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
