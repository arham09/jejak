package graphdb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/arham09/jejak/internal/graph"
)

// WriteAnalysis writes all source-derived rows for an inactive building
// generation in one transaction. Candidate rows remain invisible to normal
// views until ActivateGeneration commits.
//
// Rows are keyed by the generation's integer storage key, and edges and
// evidence reference their endpoints by integer id, so the text keys of a
// node are stored once per generation rather than once per relationship.
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
	key, persistedState, err := generationRef(ctx, tx, generation.RepoID, generation.WorktreeID, generation.ID)
	if err != nil {
		return fmt.Errorf("read graph generation before write: %w", err)
	}
	if persistedState != graph.GenerationBuilding {
		return fmt.Errorf("%w: generation %d is %s", ErrInvalidGeneration, generation.ID, persistedState)
	}
	result = result.Normalize()
	repoID := string(generation.RepoID)
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO packages(generation_key, package_key, import_path, module_path, directory) VALUES (?, ?, ?, ?, ?)`, key, item.Key, item.ImportPath, item.ModulePath, item.Directory); err != nil {
			return fmt.Errorf("insert graph package %q: %w", item.Key, err)
		}
	}
	for _, item := range result.Files {
		if _, err := tx.ExecContext(ctx, `INSERT INTO files(generation_key, file_key, path, blob_sha, package_key) VALUES (?, ?, ?, ?, ?)`, key, item.Key, item.Path, item.BlobSHA, item.PackageKey); err != nil {
			return fmt.Errorf("insert graph file %q: %w", item.Path, err)
		}
	}
	if err := insertRows(ctx, tx, `INSERT INTO symbols(generation_key, symbol_key, node_kind, package_key, file_key, name, signature, receiver, start_line, end_line, exported) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, len(result.Symbols), func(index int) ([]any, string) {
		item := result.Symbols[index]
		return []any{key, item.Key, string(item.Kind), item.PackageKey, item.FileKey, item.Name, item.Signature, item.Receiver, item.Position.StartLine, item.Position.EndLine, boolInt(item.Exported)}, "symbol " + item.Key
	}, nil); err != nil {
		return err
	}
	nodeIDs := make(map[string]int64, len(result.Nodes))
	if err := insertRows(ctx, tx, `INSERT INTO nodes(generation_key, node_key, node_kind, owned, package_key, file_key, symbol_key, source_blob, source_start, source_end) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, len(result.Nodes), func(index int) ([]any, string) {
		item := result.Nodes[index]
		return []any{key, item.Key, string(item.Kind), boolInt(item.Owned), item.PackageKey, item.FileKey, item.SymbolKey, item.SourceBlob, item.SourceStart, item.SourceEnd}, "node " + item.Key
	}, func(index int, id int64) { nodeIDs[result.Nodes[index].Key] = id }); err != nil {
		return err
	}
	edgeIDs := make(map[string]int64, len(result.Edges))
	for _, item := range result.Edges {
		if _, ok := nodeIDs[item.SourceKey]; !ok {
			return fmt.Errorf("%w: edge source %q has no node", ErrInvalidGeneration, item.SourceKey)
		}
		if _, ok := nodeIDs[item.TargetKey]; !ok {
			return fmt.Errorf("%w: edge target %q has no node", ErrInvalidGeneration, item.TargetKey)
		}
	}
	if err := insertRows(ctx, tx, `INSERT INTO edges(generation_key, source_id, target_id, edge_kind, confidence, owner_package) VALUES (?, ?, ?, ?, ?, ?)`, len(result.Edges), func(index int) ([]any, string) {
		item := result.Edges[index]
		confidence := item.Confidence
		if confidence == "" {
			confidence = graph.ConfidenceExact
		}
		return []any{key, nodeIDs[item.SourceKey], nodeIDs[item.TargetKey], string(item.Kind), string(confidence), item.OwnerPackage}, fmt.Sprintf("edge %s -> %s", item.SourceKey, item.TargetKey)
	}, func(index int, id int64) {
		edgeIDs[edgeIdentity(result.Edges[index].SourceKey, result.Edges[index].TargetKey, result.Edges[index].Kind)] = id
	}); err != nil {
		return err
	}
	for _, item := range result.Evidence {
		if _, ok := edgeIDs[edgeIdentity(item.SourceKey, item.TargetKey, item.Kind)]; !ok {
			return fmt.Errorf("%w: evidence for %s -> %s (%s) has no edge", ErrInvalidGeneration, item.SourceKey, item.TargetKey, item.Kind)
		}
	}
	if err := insertRows(ctx, tx, `INSERT INTO edge_evidence(edge_id, analyzer_source, source_blob, source_start, source_end, details) VALUES (?, ?, ?, ?, ?, ?)`, len(result.Evidence), func(index int) ([]any, string) {
		item := result.Evidence[index]
		return []any{edgeIDs[edgeIdentity(item.SourceKey, item.TargetKey, item.Kind)], item.AnalyzerSource, item.SourceBlob, item.StartLine, item.EndLine, item.Details}, "edge evidence"
	}, nil); err != nil {
		return err
	}
	for _, item := range result.PackageDependencies {
		if _, err := tx.ExecContext(ctx, `INSERT INTO package_dependencies(generation_key, source_package, target_package) VALUES (?, ?, ?)`, key, item.SourcePackage, item.TargetPackage); err != nil {
			return fmt.Errorf("insert graph package dependency: %w", err)
		}
	}
	for _, item := range result.TestRelationships {
		confidence := item.Confidence
		if confidence == "" {
			confidence = graph.ConfidenceInferred
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO test_relationships(generation_key, test_key, target_key, confidence) VALUES (?, ?, ?, ?)`, key, item.TestKey, item.TargetKey, string(confidence)); err != nil {
			return fmt.Errorf("insert graph test relationship: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit graph analysis write: %w", err)
	}
	return nil
}

// insertRows runs one prepared INSERT for count rows. bind returns the
// arguments and a short description of row index for error messages;
// assigned, when non-nil, receives each row's generated integer id.
func insertRows(ctx context.Context, tx *sql.Tx, query string, count int, bind func(index int) ([]any, string), assigned func(index int, id int64)) error {
	if count == 0 {
		return nil
	}
	statement, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return fmt.Errorf("prepare graph insert: %w", err)
	}
	defer func() { _ = statement.Close() }()
	for index := 0; index < count; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		args, description := bind(index)
		outcome, err := statement.ExecContext(ctx, args...)
		if err != nil {
			return fmt.Errorf("insert graph %s: %w", description, err)
		}
		if assigned != nil {
			id, err := outcome.LastInsertId()
			if err != nil {
				return fmt.Errorf("read inserted graph %s id: %w", description, err)
			}
			assigned(index, id)
		}
	}
	return nil
}

func edgeIdentity(sourceKey, targetKey string, kind graph.EdgeKind) string {
	return sourceKey + "\x00" + targetKey + "\x00" + string(kind)
}
