package graphdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
)

// Generation returns one persisted candidate or active generation.
func (s *Store) Generation(ctx context.Context, repoID repository.RepoID, worktreeID repository.WorktreeID, id graph.GenerationID) (graph.Generation, error) {
	db, err := s.database()
	if err != nil {
		return graph.Generation{}, err
	}
	var generation graph.Generation
	var commit, fingerprint, analyzerVersion, state, message, created, validated, retired string
	var schema int
	if err := db.QueryRowContext(ctx, `
		SELECT commit_sha, build_fingerprint, analyzer_version, schema_version,
		       state, error, created_at, COALESCE(validated_at, ''), COALESCE(retired_at, '')
		FROM graph_generations
		WHERE repo_id = ? AND worktree_id = ? AND generation_id = ?
	`, string(repoID), string(worktreeID), int64(id)).Scan(&commit, &fingerprint, &analyzerVersion, &schema, &state, &message, &created, &validated, &retired); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return graph.Generation{}, fmt.Errorf("%w: generation %d", ErrNotFound, id)
		}
		return graph.Generation{}, fmt.Errorf("read graph generation %d: %w", id, err)
	}
	generation = graph.Generation{RepoID: repoID, WorktreeID: worktreeID, ID: id, Commit: graph.CommitSHA(commit), BuildFingerprint: fingerprint, AnalyzerVersion: analyzerVersion, SchemaVersion: schema, State: graph.GenerationState(state), Error: message}
	generation.CreatedAt, err = parseTimestamp(created)
	if err != nil {
		return graph.Generation{}, fmt.Errorf("parse graph generation creation time: %w", err)
	}
	if validated != "" {
		value, parseErr := parseTimestamp(validated)
		if parseErr != nil {
			return graph.Generation{}, fmt.Errorf("parse graph generation validation time: %w", parseErr)
		}
		generation.ValidatedAt = &value
	}
	if retired != "" {
		value, parseErr := parseTimestamp(retired)
		if parseErr != nil {
			return graph.Generation{}, fmt.Errorf("parse graph generation retirement time: %w", parseErr)
		}
		generation.RetiredAt = &value
	}
	if err := generation.Validate(); err != nil {
		return graph.Generation{}, fmt.Errorf("validate graph generation: %w", err)
	}
	return generation, nil
}

// State returns the durable state for one repository worktree.
func (s *Store) State(ctx context.Context, repoID repository.RepoID, worktreeID repository.WorktreeID) (graph.State, error) {
	db, err := s.database()
	if err != nil {
		return graph.State{}, err
	}
	var state graph.State
	var active sql.NullInt64
	var currentHead, indexedHead, status, lastError, branch, path, updated, analyzerVersion string
	var graphSchema int
	var headKnown int
	if err := db.QueryRowContext(ctx, `
		SELECT w.worktree_path, w.branch, w.observed_head, w.head_known,
		       s.indexed_head, s.active_generation, s.status, s.last_error,
		       s.updated_at, COALESCE(g.analyzer_version, ''), COALESCE(g.schema_version, 1)
		FROM graph_state AS s
		JOIN worktrees AS w ON w.repo_id = s.repo_id AND w.worktree_id = s.worktree_id
		LEFT JOIN graph_generations AS g ON g.repo_id = s.repo_id AND g.worktree_id = s.worktree_id AND g.generation_id = s.active_generation
		WHERE s.repo_id = ? AND s.worktree_id = ?
	`, string(repoID), string(worktreeID)).Scan(&path, &branch, &currentHead, &headKnown,
		&indexedHead, &active, &status, &lastError, &updated, &analyzerVersion, &graphSchema); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return graph.State{}, fmt.Errorf("%w: worktree %s", ErrNotFound, worktreeID)
		}
		return graph.State{}, fmt.Errorf("read graph state: %w", err)
	}
	state.RepoID = repoID
	state.WorktreeID = worktreeID
	state.WorktreePath = path
	state.Branch = branch
	state.CurrentHead = graph.CommitSHA(currentHead)
	state.HeadKnown = headKnown == 1
	state.IndexedHead = graph.CommitSHA(indexedHead)
	if active.Valid {
		generation := graph.GenerationID(active.Int64)
		state.ActiveGeneration = &generation
	}
	state.Status = graph.GraphStatus(status)
	state.LastError = lastError
	state.GraphSchema = graphSchema
	if state.GraphSchema == 0 {
		state.GraphSchema = defaultSchema
	}
	state.AnalyzerVersion = analyzerVersion
	updatedAt, err := parseTimestamp(updated)
	if err != nil {
		return graph.State{}, fmt.Errorf("parse graph state timestamp: %w", err)
	}
	state.UpdatedAt = updatedAt
	if err := state.Validate(); err != nil {
		return graph.State{}, fmt.Errorf("validate graph state: %w", err)
	}
	return state, nil
}

// CreateGeneration stores an inactive building generation. If ID is zero, the
// next repository/worktree-local generation number is allocated.
func (s *Store) CreateGeneration(ctx context.Context, generation graph.Generation) (graph.Generation, error) {
	if generation.RepoID == "" || generation.WorktreeID == "" || generation.Commit == "" {
		return graph.Generation{}, fmt.Errorf("%w: repository, worktree, and commit are required", ErrInvalidGeneration)
	}
	if generation.State == "" {
		generation.State = graph.GenerationBuilding
	}
	if generation.State != graph.GenerationBuilding {
		return graph.Generation{}, fmt.Errorf("%w: new generation must be building", ErrInvalidGeneration)
	}
	if generation.SchemaVersion == 0 {
		generation.SchemaVersion = defaultSchema
	}
	if generation.CreatedAt.IsZero() {
		generation.CreatedAt = now()
	}
	db, err := s.database()
	if err != nil {
		return graph.Generation{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return graph.Generation{}, fmt.Errorf("begin graph generation write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if generation.ID == 0 {
		var next int64
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(generation_id), 0) + 1
			FROM graph_generations WHERE repo_id = ? AND worktree_id = ?
		`, string(generation.RepoID), string(generation.WorktreeID)).Scan(&next); err != nil {
			return graph.Generation{}, fmt.Errorf("allocate graph generation: %w", err)
		}
		generation.ID = graph.GenerationID(next)
	}
	if err := generation.Validate(); err != nil {
		return graph.Generation{}, fmt.Errorf("%w: %v", ErrInvalidGeneration, err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO graph_generations(
			repo_id, worktree_id, generation_id, commit_sha, build_fingerprint,
			analyzer_version, schema_version, state, error, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, string(generation.RepoID), string(generation.WorktreeID), int64(generation.ID), string(generation.Commit),
		generation.BuildFingerprint, generation.AnalyzerVersion, generation.SchemaVersion,
		string(generation.State), generation.Error, timestamp(generation.CreatedAt))
	if err != nil {
		return graph.Generation{}, fmt.Errorf("insert graph generation %d: %w", generation.ID, err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE graph_state SET status = ?, last_error = '', updated_at = ?
		WHERE repo_id = ? AND worktree_id = ?
	`, string(graph.StatusBuilding), timestamp(now()), string(generation.RepoID), string(generation.WorktreeID))
	if err != nil {
		return graph.Generation{}, fmt.Errorf("mark graph state building: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return graph.Generation{}, fmt.Errorf("check graph state building: %w", err)
	}
	if rows != 1 {
		return graph.Generation{}, fmt.Errorf("%w: worktree %s", ErrNotFound, generation.WorktreeID)
	}
	if err := tx.Commit(); err != nil {
		return graph.Generation{}, fmt.Errorf("commit graph generation write: %w", err)
	}
	return generation, nil
}

// ValidateGeneration marks a building candidate ready for activation.
func (s *Store) ValidateGeneration(ctx context.Context, repoID repository.RepoID, worktreeID repository.WorktreeID, id graph.GenerationID) error {
	if id <= 0 {
		return fmt.Errorf("%w: generation ID must be positive", ErrInvalidGeneration)
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `
		UPDATE graph_generations
		SET state = ?, validated_at = ?
		WHERE repo_id = ? AND worktree_id = ? AND generation_id = ? AND state = ?
	`, string(graph.GenerationValidated), timestamp(now()), string(repoID), string(worktreeID), int64(id), string(graph.GenerationBuilding))
	if err != nil {
		return fmt.Errorf("validate graph generation %d: %w", id, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check graph generation validation: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("%w: generation %d is missing or not building", ErrNotFound, id)
	}
	return nil
}

// ActivateGeneration atomically changes the active generation and indexed
// HEAD after checking the expected prior generation and observed worktree
// HEAD. The candidate must already be validated.
func (s *Store) ActivateGeneration(ctx context.Context, repoID repository.RepoID, worktreeID repository.WorktreeID, id graph.GenerationID, expected *graph.GenerationID) error {
	if id <= 0 {
		return fmt.Errorf("%w: generation ID must be positive", ErrInvalidGeneration)
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin graph activation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var commit, candidateState, observedHead string
	if err := tx.QueryRowContext(ctx, `
		SELECT g.commit_sha, g.state, w.observed_head
		FROM graph_generations AS g
		JOIN worktrees AS w ON w.repo_id = g.repo_id AND w.worktree_id = g.worktree_id
		WHERE g.repo_id = ? AND g.worktree_id = ? AND g.generation_id = ?
	`, string(repoID), string(worktreeID), int64(id)).Scan(&commit, &candidateState, &observedHead); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: generation %d", ErrNotFound, id)
		}
		return fmt.Errorf("read graph activation candidate: %w", err)
	}
	if candidateState != string(graph.GenerationValidated) {
		return fmt.Errorf("%w: generation %d is %s", ErrInvalidGeneration, id, candidateState)
	}
	if observedHead == "" || observedHead != commit {
		return fmt.Errorf("%w: observed HEAD %q does not match candidate %q", ErrStaleState, observedHead, commit)
	}

	var active sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
		SELECT active_generation FROM graph_state WHERE repo_id = ? AND worktree_id = ?
	`, string(repoID), string(worktreeID)).Scan(&active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: worktree state", ErrNotFound)
		}
		return fmt.Errorf("read active graph generation: %w", err)
	}
	if expected == nil {
		if active.Valid {
			return fmt.Errorf("%w: expected no active generation, found %d", ErrStaleGeneration, active.Int64)
		}
	} else if !active.Valid || active.Int64 != int64(*expected) {
		actual := "none"
		if active.Valid {
			actual = fmt.Sprintf("%d", active.Int64)
		}
		return fmt.Errorf("%w: expected generation %d, found %s", ErrStaleGeneration, *expected, actual)
	}

	if active.Valid && active.Int64 != int64(id) {
		if _, err := tx.ExecContext(ctx, `
			UPDATE graph_generations SET state = ?, retired_at = ?
			WHERE repo_id = ? AND worktree_id = ? AND generation_id = ?
		`, string(graph.GenerationRetired), timestamp(now()), string(repoID), string(worktreeID), active.Int64); err != nil {
			return fmt.Errorf("retire active graph generation: %w", err)
		}
	}
	candidateResult, err := tx.ExecContext(ctx, `
		UPDATE graph_generations SET state = ?
		WHERE repo_id = ? AND worktree_id = ? AND generation_id = ?
	`, string(graph.GenerationActive), string(repoID), string(worktreeID), int64(id))
	if err != nil {
		return fmt.Errorf("activate graph generation: %w", err)
	}
	candidateRows, err := candidateResult.RowsAffected()
	if err != nil {
		return fmt.Errorf("check activated graph generation: %w", err)
	}
	if candidateRows != 1 {
		return fmt.Errorf("%w: generation %d", ErrNotFound, id)
	}
	stateResult, err := tx.ExecContext(ctx, `
		UPDATE graph_state
		SET active_generation = ?, indexed_head = ?, status = ?, last_error = '', updated_at = ?
		WHERE repo_id = ? AND worktree_id = ?
	`, int64(id), commit, string(graph.StatusReady), timestamp(now()), string(repoID), string(worktreeID))
	if err != nil {
		return fmt.Errorf("update active graph state: %w", err)
	}
	stateRows, err := stateResult.RowsAffected()
	if err != nil {
		return fmt.Errorf("check active graph state: %w", err)
	}
	if stateRows != 1 {
		return fmt.Errorf("%w: worktree %s", ErrNotFound, worktreeID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit graph activation: %w", err)
	}
	return nil
}

// FailGeneration records a failed candidate while retaining any previously
// active generation for diagnosis and possible retry.
func (s *Store) FailGeneration(ctx context.Context, repoID repository.RepoID, worktreeID repository.WorktreeID, id graph.GenerationID, cause error) error {
	if cause == nil {
		cause = errors.New("graph build failed")
	}
	message := cause.Error()
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin graph failure update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE graph_generations SET state = ?, error = ?
		WHERE repo_id = ? AND worktree_id = ? AND generation_id = ?
		  AND state IN (?, ?)
	`, string(graph.GenerationFailed), message, string(repoID), string(worktreeID), int64(id),
		string(graph.GenerationBuilding), string(graph.GenerationValidated))
	if err != nil {
		return fmt.Errorf("record failed graph generation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check failed graph generation: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("%w: generation %d", ErrNotFound, id)
	}
	stateResult, err := tx.ExecContext(ctx, `
		UPDATE graph_state SET status = ?, last_error = ?, updated_at = ?
		WHERE repo_id = ? AND worktree_id = ?
	`, string(graph.StatusFailed), message, timestamp(now()), string(repoID), string(worktreeID))
	if err != nil {
		return fmt.Errorf("record graph failure state: %w", err)
	}
	stateRows, err := stateResult.RowsAffected()
	if err != nil {
		return fmt.Errorf("check graph failure state: %w", err)
	}
	if stateRows != 1 {
		return fmt.Errorf("%w: worktree %s", ErrNotFound, worktreeID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit graph failure state: %w", err)
	}
	return nil
}
