package graphdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
)

// Register records a repository/worktree target without creating a
// source-derived graph. Repeated calls update observed Git metadata and keep
// the active generation untouched.
func (s *Store) Register(ctx context.Context, target repository.Target) (graph.State, error) {
	if target.Repository.ID == "" || target.Worktree.ID == "" {
		return graph.State{}, fmt.Errorf("%w: repository and worktree identities are required", ErrInvalidGeneration)
	}
	db, err := s.database()
	if err != nil {
		return graph.State{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return graph.State{}, fmt.Errorf("begin repository registration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	nowValue := timestamp(now())
	_, err = tx.ExecContext(ctx, `
		INSERT INTO repositories(
			repo_id, canonical_identity, root_path, common_dir, remote_name, remote_url, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_id) DO UPDATE SET
			canonical_identity = excluded.canonical_identity,
			remote_name = excluded.remote_name,
			remote_url = excluded.remote_url,
			updated_at = excluded.updated_at
		WHERE repositories.canonical_identity <> excluded.canonical_identity
		   OR repositories.remote_name <> excluded.remote_name
		   OR repositories.remote_url <> excluded.remote_url
	`, string(target.Repository.ID), target.Repository.CanonicalIdentity, target.Repository.Root,
		target.Repository.CommonDir, target.Repository.RemoteName, target.Repository.RemoteURL, nowValue, nowValue)
	if err != nil {
		return graph.State{}, fmt.Errorf("register repository %q: %w", target.Repository.ID, err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO worktrees(
			repo_id, worktree_id, worktree_path, git_dir, common_dir, branch, observed_head, head_known, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_id, worktree_id) DO UPDATE SET
			worktree_path = excluded.worktree_path,
			git_dir = excluded.git_dir,
			common_dir = excluded.common_dir,
			branch = excluded.branch,
			observed_head = excluded.observed_head,
			head_known = excluded.head_known,
			updated_at = excluded.updated_at
		WHERE worktrees.worktree_path <> excluded.worktree_path
		   OR worktrees.git_dir <> excluded.git_dir
		   OR worktrees.common_dir <> excluded.common_dir
		   OR worktrees.branch <> excluded.branch
		   OR worktrees.observed_head <> excluded.observed_head
		   OR worktrees.head_known <> excluded.head_known
	`, string(target.Repository.ID), string(target.Worktree.ID), target.Worktree.Path, target.Worktree.GitDir,
		target.Worktree.CommonDir, target.Worktree.Branch, string(target.Worktree.Head), boolInt(target.Worktree.HeadKnown), nowValue, nowValue)
	if err != nil {
		return graph.State{}, fmt.Errorf("register worktree %q: %w", target.Worktree.ID, err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO graph_state(repo_id, worktree_id, status, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(repo_id, worktree_id) DO NOTHING
	`, string(target.Repository.ID), string(target.Worktree.ID), string(graph.StatusUnindexed), nowValue)
	if err != nil {
		return graph.State{}, fmt.Errorf("initialize graph state: %w", err)
	}
	var indexedHead, status string
	var activeGeneration sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
		SELECT indexed_head, active_generation, status
		FROM graph_state WHERE repo_id = ? AND worktree_id = ?
	`, string(target.Repository.ID), string(target.Worktree.ID)).Scan(&indexedHead, &activeGeneration, &status); err != nil {
		return graph.State{}, fmt.Errorf("read registered graph state: %w", err)
	}
	if activeGeneration.Valid {
		observedHead := string(target.Worktree.Head)
		nextStatus := status
		switch {
		case observedHead != indexedHead:
			nextStatus = string(graph.StatusStale)
		case status == string(graph.StatusStale):
			nextStatus = string(graph.StatusReady)
		}
		if nextStatus != status {
			result, err := tx.ExecContext(ctx, `
				UPDATE graph_state SET status = ?, last_error = '', updated_at = ?
				WHERE repo_id = ? AND worktree_id = ?
			`, nextStatus, nowValue, string(target.Repository.ID), string(target.Worktree.ID))
			if err != nil {
				return graph.State{}, fmt.Errorf("refresh registered graph state: %w", err)
			}
			rows, err := result.RowsAffected()
			if err != nil {
				return graph.State{}, fmt.Errorf("check refreshed graph state: %w", err)
			}
			if rows != 1 {
				return graph.State{}, fmt.Errorf("%w: worktree %s", ErrNotFound, target.Worktree.ID)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return graph.State{}, fmt.Errorf("commit repository registration: %w", err)
	}
	return s.State(ctx, target.Repository.ID, target.Worktree.ID)
}

// ListRepositories lists metadata stored in this database. It never loads
// semantic graph rows.
func (s *Store) ListRepositories(ctx context.Context) ([]repository.CatalogEntry, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT r.repo_id, r.canonical_identity, r.root_path, r.common_dir,
		       r.remote_name, r.remote_url, COUNT(w.worktree_id), r.created_at, r.updated_at
		FROM repositories AS r
		LEFT JOIN worktrees AS w ON w.repo_id = r.repo_id
		GROUP BY r.repo_id, r.canonical_identity, r.root_path, r.common_dir,
		         r.remote_name, r.remote_url, r.created_at, r.updated_at
		ORDER BY r.canonical_identity, r.repo_id
	`)
	if err != nil {
		return nil, fmt.Errorf("list registered repositories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []repository.CatalogEntry
	for rows.Next() {
		var entry repository.CatalogEntry
		var id, created, updated string
		if err := rows.Scan(&id, &entry.CanonicalIdentity, &entry.Root, &entry.CommonDir,
			&entry.RemoteName, &entry.RemoteURL, &entry.WorktreeCount, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan repository catalog: %w", err)
		}
		entry.ID = repository.RepoID(id)
		entry.CreatedAt, err = parseTimestamp(created)
		if err != nil {
			return nil, fmt.Errorf("parse repository creation time: %w", err)
		}
		entry.UpdatedAt, err = parseTimestamp(updated)
		if err != nil {
			return nil, fmt.Errorf("parse repository update time: %w", err)
		}
		result = append(result, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate repository catalog: %w", err)
	}
	return result, nil
}

// CatalogIssue records one repository directory that could not be read while
// listing the application catalog.
type CatalogIssue struct {
	Path string
	Err  error
}

// ScanCatalog reads all repository databases below dataRoot. A corrupt or
// unreadable entry is returned as an issue while healthy entries remain
// available to the caller.
func ScanCatalog(ctx context.Context, dataRoot string) ([]repository.CatalogEntry, []CatalogIssue, error) {
	reposRoot := filepath.Join(dataRoot, "repos")
	entries, err := os.ReadDir(reposRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read repository catalog %q: %w", reposRoot, err)
	}

	var result []repository.CatalogEntry
	var issues []CatalogIssue
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dbPath := filepath.Join(reposRoot, entry.Name(), "graph.db")
		if _, statErr := os.Stat(dbPath); statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				issues = append(issues, CatalogIssue{Path: dbPath, Err: fmt.Errorf("graph database is missing")})
				continue
			}
			issues = append(issues, CatalogIssue{Path: dbPath, Err: statErr})
			continue
		}
		store, err := Open(ctx, dbPath)
		if err != nil {
			issues = append(issues, CatalogIssue{Path: dbPath, Err: err})
			continue
		}
		items, listErr := store.ListRepositories(ctx)
		closeErr := store.Close()
		if listErr != nil {
			issues = append(issues, CatalogIssue{Path: dbPath, Err: listErr})
			continue
		}
		if closeErr != nil {
			issues = append(issues, CatalogIssue{Path: dbPath, Err: closeErr})
		}
		result = append(result, items...)
	}
	return result, issues, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
