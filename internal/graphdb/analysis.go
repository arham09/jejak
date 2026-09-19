package graphdb

import (
	"context"
	"fmt"
	"strings"

	"github.com/arham09/jejak/internal/graph"
)

// WriteAnalysis writes all source-derived rows for an inactive building
// generation in one transaction. Candidate rows remain invisible to normal
// views until ActivateGeneration commits.
func (s *Store) WriteAnalysis(ctx context.Context, generation graph.Generation, result graph.AnalysisResult) error {
	if generation.ID <= 0 || generation.RepoID == "" || generation.WorktreeID == "" {
		return fmt.Errorf("%w: invalid generation identity", ErrInvalidGeneration)
	}
	if generation.State != graph.GenerationBuilding {
		return fmt.Errorf("%w: generation %d is not building", ErrInvalidGeneration, generation.ID)
	}
	if err := graph.ValidateAnalysis(result); err != nil {
		return fmt.Errorf("validate analysis before write: %w", err)
	}
	if strings.TrimSpace(generation.BuildFingerprint) != strings.TrimSpace(result.BuildFingerprint) {
		return fmt.Errorf("%w: generation fingerprint does not match analysis", ErrInvalidGeneration)
	}
	if strings.TrimSpace(generation.AnalyzerVersion) != strings.TrimSpace(result.AnalyzerVersion) {
		return fmt.Errorf("%w: generation analyzer version does not match analysis", ErrInvalidGeneration)
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin graph analysis write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var persistedState string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM graph_generations WHERE repo_id = ? AND worktree_id = ? AND generation_id = ?`, string(generation.RepoID), string(generation.WorktreeID), int64(generation.ID)).Scan(&persistedState); err != nil {
		return fmt.Errorf("read graph generation before write: %w", err)
	}
	if persistedState != string(graph.GenerationBuilding) {
		return fmt.Errorf("%w: generation %d is %s", ErrInvalidGeneration, generation.ID, persistedState)
	}
	result = result.Normalize()
	repoID, worktreeID, generationID := string(generation.RepoID), string(generation.WorktreeID), int64(generation.ID)
	for _, blob := range result.Blobs {
		if strings.TrimSpace(blob.SHA) == "" {
			continue
		}
		format := blob.ObjectFormat
		if format == "" {
			format = "sha1"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO blobs(repo_id, blob_sha, object_format, byte_size, created_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(repo_id, blob_sha) DO UPDATE SET object_format = excluded.object_format, byte_size = excluded.byte_size
		`, repoID, blob.SHA, format, blob.ByteSize, timestamp(now())); err != nil {
			return fmt.Errorf("insert graph blob %q: %w", blob.SHA, err)
		}
	}
	for _, item := range result.Packages {
		if _, err := tx.ExecContext(ctx, `INSERT INTO packages(repo_id, worktree_id, generation_id, package_key, import_path, module_path, directory) VALUES (?, ?, ?, ?, ?, ?, ?)`, repoID, worktreeID, generationID, item.Key, item.ImportPath, item.ModulePath, item.Directory); err != nil {
			return fmt.Errorf("insert graph package %q: %w", item.Key, err)
		}
	}
	for _, item := range result.Files {
		if _, err := tx.ExecContext(ctx, `INSERT INTO files(repo_id, worktree_id, generation_id, file_key, path, blob_sha, package_key) VALUES (?, ?, ?, ?, ?, ?, ?)`, repoID, worktreeID, generationID, item.Key, item.Path, item.BlobSHA, item.PackageKey); err != nil {
			return fmt.Errorf("insert graph file %q: %w", item.Path, err)
		}
	}
	for _, item := range result.Symbols {
		if _, err := tx.ExecContext(ctx, `INSERT INTO symbols(repo_id, worktree_id, generation_id, symbol_key, node_kind, package_key, file_key, name, signature, receiver, start_line, end_line, exported) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, repoID, worktreeID, generationID, item.Key, string(item.Kind), item.PackageKey, item.FileKey, item.Name, item.Signature, item.Receiver, item.Position.StartLine, item.Position.EndLine, boolInt(item.Exported)); err != nil {
			return fmt.Errorf("insert graph symbol %q: %w", item.Key, err)
		}
	}
	for _, item := range result.Nodes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO nodes(repo_id, worktree_id, generation_id, node_key, node_kind, owned, package_key, file_key, symbol_key, source_blob, source_start, source_end) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, repoID, worktreeID, generationID, item.Key, string(item.Kind), boolInt(item.Owned), item.PackageKey, item.FileKey, item.SymbolKey, item.SourceBlob, item.SourceStart, item.SourceEnd); err != nil {
			return fmt.Errorf("insert graph node %q: %w", item.Key, err)
		}
	}
	for _, item := range result.Edges {
		confidence := item.Confidence
		if confidence == "" {
			confidence = graph.ConfidenceExact
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO edges(repo_id, worktree_id, generation_id, source_key, target_key, edge_kind, confidence, owner_package) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, repoID, worktreeID, generationID, item.SourceKey, item.TargetKey, string(item.Kind), string(confidence), item.OwnerPackage); err != nil {
			return fmt.Errorf("insert graph edge %s -> %s: %w", item.SourceKey, item.TargetKey, err)
		}
	}
	for _, item := range result.Evidence {
		if _, err := tx.ExecContext(ctx, `INSERT INTO edge_evidence(repo_id, worktree_id, generation_id, source_key, target_key, edge_kind, analyzer_source, source_blob, source_start, source_end, details) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, repoID, worktreeID, generationID, item.SourceKey, item.TargetKey, string(item.Kind), item.AnalyzerSource, item.SourceBlob, item.StartLine, item.EndLine, item.Details); err != nil {
			return fmt.Errorf("insert graph edge evidence: %w", err)
		}
	}
	for _, item := range result.PackageDependencies {
		if _, err := tx.ExecContext(ctx, `INSERT INTO package_dependencies(repo_id, worktree_id, generation_id, source_package, target_package) VALUES (?, ?, ?, ?, ?)`, repoID, worktreeID, generationID, item.SourcePackage, item.TargetPackage); err != nil {
			return fmt.Errorf("insert graph package dependency: %w", err)
		}
	}
	for _, item := range result.TestRelationships {
		confidence := item.Confidence
		if confidence == "" {
			confidence = graph.ConfidenceInferred
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO test_relationships(repo_id, worktree_id, generation_id, test_key, target_key, confidence) VALUES (?, ?, ?, ?, ?, ?)`, repoID, worktreeID, generationID, item.TestKey, item.TargetKey, string(confidence)); err != nil {
			return fmt.Errorf("insert graph test relationship: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit graph analysis write: %w", err)
	}
	return nil
}
