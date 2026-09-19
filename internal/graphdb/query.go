package graphdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
)

// GenerationCounts returns deterministic record counts for one generation.
func (s *Store) GenerationCounts(ctx context.Context, repoID repository.RepoID, worktreeID repository.WorktreeID, id graph.GenerationID) (graph.Counts, error) {
	db, err := s.database()
	if err != nil {
		return graph.Counts{}, err
	}
	var counts graph.Counts
	if err := db.QueryRowContext(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM packages WHERE repo_id = ? AND worktree_id = ? AND generation_id = ?),
		  (SELECT COUNT(*) FROM files WHERE repo_id = ? AND worktree_id = ? AND generation_id = ?),
		  (SELECT COUNT(*) FROM symbols WHERE repo_id = ? AND worktree_id = ? AND generation_id = ?),
		  (SELECT COUNT(*) FROM edges WHERE repo_id = ? AND worktree_id = ? AND generation_id = ?),
		  (SELECT COUNT(*) FROM symbols WHERE repo_id = ? AND worktree_id = ? AND generation_id = ? AND node_kind = ?)
	`, string(repoID), string(worktreeID), int64(id), string(repoID), string(worktreeID), int64(id), string(repoID), string(worktreeID), int64(id), string(repoID), string(worktreeID), int64(id), string(repoID), string(worktreeID), int64(id), string(graph.NodeTest)).Scan(&counts.Packages, &counts.Files, &counts.Symbols, &counts.Edges, &counts.Tests); err != nil {
		return graph.Counts{}, fmt.Errorf("count graph generation %d: %w", id, err)
	}
	return counts, nil
}

// FindSymbols returns deterministic declaration matches by canonical key or
// display name. References are loaded from the same read transaction.
func (v *View) FindSymbols(ctx context.Context, query string) ([]graph.SymbolResult, error) {
	if v == nil || v.tx == nil {
		return nil, ErrStoreClosed
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("%w: symbol query is empty", ErrNotFound)
	}
	rows, err := v.tx.QueryContext(ctx, `
		SELECT symbol_key, node_kind, package_key, file_key, name, signature,
		       receiver, start_line, end_line, exported
		FROM symbols
		WHERE repo_id = ? AND worktree_id = ? AND generation_id = ? AND (symbol_key = ? OR name = ?)
		ORDER BY package_key, file_key, start_line, symbol_key
	`, string(v.generation.RepoID), string(v.generation.WorktreeID), int64(v.generation.ID), query, query)
	if err != nil {
		return nil, fmt.Errorf("query graph symbols: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var symbols []graph.Symbol
	for rows.Next() {
		var symbol graph.Symbol
		var kind string
		var exported int
		if err := rows.Scan(&symbol.Key, &kind, &symbol.PackageKey, &symbol.FileKey, &symbol.Name, &symbol.Signature, &symbol.Receiver, &symbol.Position.StartLine, &symbol.Position.EndLine, &exported); err != nil {
			return nil, fmt.Errorf("scan graph symbol: %w", err)
		}
		symbol.Kind = graph.NodeKind(kind)
		symbol.Exported = exported == 1
		symbols = append(symbols, symbol)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate graph symbols: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close graph symbols: %w", err)
	}
	if len(symbols) == 0 {
		return nil, fmt.Errorf("%w: symbol %q", ErrNotFound, query)
	}
	result := make([]graph.SymbolResult, 0, len(symbols))
	for _, symbol := range symbols {
		path, err := v.filePath(ctx, symbol.FileKey)
		if err != nil {
			return nil, err
		}
		symbol.Position.Path = path
		references, err := v.references(ctx, symbol.Key)
		if err != nil {
			return nil, err
		}
		relationships, err := v.symbolRelationships(ctx, symbol.Key)
		if err != nil {
			return nil, err
		}
		result = append(result, decorateSymbol(symbol, references, relationships))
	}
	return result, nil
}

// Symbols is a concise alias for FindSymbols for callers that already own a
// pinned view.
func (v *View) Symbols(ctx context.Context, query string) ([]graph.SymbolResult, error) {
	return v.FindSymbols(ctx, query)
}

// FindFile returns one generation-pinned file by repository-relative path or
// canonical file node key.
func (v *View) FindFile(ctx context.Context, query string) (graph.FileResult, error) {
	if v == nil || v.tx == nil {
		return graph.FileResult{}, ErrStoreClosed
	}
	query = filepath.ToSlash(strings.TrimSpace(query))
	if query == "" {
		return graph.FileResult{}, fmt.Errorf("%w: file query is empty", ErrNotFound)
	}
	var file graph.File
	if err := v.tx.QueryRowContext(ctx, `
		SELECT file_key, path, blob_sha, package_key
		FROM files
		WHERE repo_id = ? AND worktree_id = ? AND generation_id = ? AND (path = ? OR file_key = ?)
		ORDER BY path
		LIMIT 1
	`, string(v.generation.RepoID), string(v.generation.WorktreeID), int64(v.generation.ID), query, query).Scan(&file.Key, &file.Path, &file.BlobSHA, &file.PackageKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return graph.FileResult{}, fmt.Errorf("%w: file %q", ErrNotFound, query)
		}
		return graph.FileResult{}, fmt.Errorf("query graph file %q: %w", query, err)
	}
	file.IsTest = strings.Contains(file.PackageKey, "#")
	rows, err := v.tx.QueryContext(ctx, `
		SELECT symbol_key, node_kind, package_key, file_key, name, signature,
		       receiver, start_line, end_line, exported
		FROM symbols WHERE repo_id = ? AND worktree_id = ? AND generation_id = ? AND file_key = ?
		ORDER BY start_line, symbol_key
	`, string(v.generation.RepoID), string(v.generation.WorktreeID), int64(v.generation.ID), file.Key)
	if err != nil {
		return graph.FileResult{}, fmt.Errorf("query symbols for graph file %q: %w", file.Path, err)
	}
	defer func() { _ = rows.Close() }()
	var symbols []graph.Symbol
	for rows.Next() {
		var symbol graph.Symbol
		var kind string
		var exported int
		if err := rows.Scan(&symbol.Key, &kind, &symbol.PackageKey, &symbol.FileKey, &symbol.Name, &symbol.Signature, &symbol.Receiver, &symbol.Position.StartLine, &symbol.Position.EndLine, &exported); err != nil {
			return graph.FileResult{}, fmt.Errorf("scan symbols for graph file: %w", err)
		}
		symbol.Kind = graph.NodeKind(kind)
		symbol.Exported = exported == 1
		symbol.Position.Path = file.Path
		symbols = append(symbols, symbol)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return graph.FileResult{}, fmt.Errorf("iterate symbols for graph file: %w", err)
	}
	if err := rows.Close(); err != nil {
		return graph.FileResult{}, fmt.Errorf("close symbols for graph file: %w", err)
	}
	symbolResults := make([]graph.SymbolResult, 0, len(symbols))
	for _, symbol := range symbols {
		references, referenceErr := v.references(ctx, symbol.Key)
		if referenceErr != nil {
			return graph.FileResult{}, referenceErr
		}
		relationships, relationshipErr := v.symbolRelationships(ctx, symbol.Key)
		if relationshipErr != nil {
			return graph.FileResult{}, relationshipErr
		}
		symbolResults = append(symbolResults, decorateSymbol(symbol, references, relationships))
	}
	imports, err := v.fileImports(ctx, file.PackageKey)
	if err != nil {
		return graph.FileResult{}, err
	}
	return graph.FileResult{File: file, Symbols: symbols, SymbolResults: symbolResults, Imports: imports}, nil
}

// File is a concise alias for FindFile.
func (v *View) File(ctx context.Context, query string) (graph.FileResult, error) {
	return v.FindFile(ctx, query)
}

func decorateSymbol(symbol graph.Symbol, references []graph.SymbolReference, relationships []graph.SymbolRelationship) graph.SymbolResult {
	result := graph.SymbolResult{Symbol: symbol, References: references}
	for _, relationship := range relationships {
		switch relationship.Kind {
		case graph.EdgeCalls, graph.EdgePossibleCall, graph.EdgeUnresolvedCall:
			if relationship.SourceKey == symbol.Key {
				result.Calls = append(result.Calls, relationship)
			} else if relationship.TargetKey == symbol.Key {
				result.CalledBy = append(result.CalledBy, relationship)
			}
		case graph.EdgeImplements:
			if relationship.SourceKey == symbol.Key {
				result.Implementations = append(result.Implementations, relationship)
			} else if relationship.TargetKey == symbol.Key {
				result.ImplementedBy = append(result.ImplementedBy, relationship)
			}
		case graph.EdgeTests:
			result.Tests = append(result.Tests, relationship)
		}
	}
	return result
}

func (v *View) symbolRelationships(ctx context.Context, symbolKey string) ([]graph.SymbolRelationship, error) {
	return v.symbolRelationshipsLimit(ctx, symbolKey, 0)
}

func (v *View) symbolRelationshipsLimit(ctx context.Context, symbolKey string, limit int) ([]graph.SymbolRelationship, error) {
	if limit > 0 {
		return v.boundedSymbolRelationships(ctx, symbolKey, limit)
	}
	query := `
		SELECT e.source_key, e.target_key, e.edge_kind, e.confidence,
		       COALESCE(ss.package_key, sf.package_key, sfile.package_key, sn.package_key, e.owner_package, ''),
		       COALESCE(ts.package_key, tf.package_key, tfile.package_key, tn.package_key, tp.package_key, ''),
		       COALESCE(sf.path, sfile.path, ''), COALESCE(tf.path, tfile.path, ''),
		       COALESCE(ss.name, sn.symbol_key, ''), COALESCE(ts.name, tp.import_path, tn.symbol_key, ''),
		       COALESCE(ss.node_kind, sn.node_kind, CASE WHEN sfile.file_key IS NULL THEN '' ELSE 'file' END), COALESCE(ts.node_kind, tn.node_kind, CASE WHEN tp.package_key IS NULL THEN '' ELSE 'package' END),
		       COALESCE(ev.source_start, 0), COALESCE(ev.source_end, 0), COALESCE(ev.details, '')
		FROM edges AS e
		LEFT JOIN symbols AS ss ON ss.repo_id = e.repo_id AND ss.worktree_id = e.worktree_id AND ss.generation_id = e.generation_id AND ss.symbol_key = e.source_key
		LEFT JOIN symbols AS ts ON ts.repo_id = e.repo_id AND ts.worktree_id = e.worktree_id AND ts.generation_id = e.generation_id AND ts.symbol_key = e.target_key
		LEFT JOIN nodes AS sn ON sn.repo_id = e.repo_id AND sn.worktree_id = e.worktree_id AND sn.generation_id = e.generation_id AND sn.node_key = e.source_key
		LEFT JOIN nodes AS tn ON tn.repo_id = e.repo_id AND tn.worktree_id = e.worktree_id AND tn.generation_id = e.generation_id AND tn.node_key = e.target_key
		LEFT JOIN files AS sf ON sf.repo_id = e.repo_id AND sf.worktree_id = e.worktree_id AND sf.generation_id = e.generation_id AND sf.file_key = ss.file_key
		LEFT JOIN files AS sfile ON sfile.repo_id = e.repo_id AND sfile.worktree_id = e.worktree_id AND sfile.generation_id = e.generation_id AND sfile.file_key = e.source_key
		LEFT JOIN files AS tf ON tf.repo_id = e.repo_id AND tf.worktree_id = e.worktree_id AND tf.generation_id = e.generation_id AND tf.file_key = ts.file_key
		LEFT JOIN files AS tfile ON tfile.repo_id = e.repo_id AND tfile.worktree_id = e.worktree_id AND tfile.generation_id = e.generation_id AND tfile.file_key = e.target_key
		LEFT JOIN packages AS tp ON tp.repo_id = e.repo_id AND tp.worktree_id = e.worktree_id AND tp.generation_id = e.generation_id AND tp.package_key = CASE WHEN e.target_key LIKE 'package:%' THEN substr(e.target_key, length('package:') + 1) ELSE ts.package_key END
		LEFT JOIN edge_evidence AS ev ON ev.repo_id = e.repo_id AND ev.worktree_id = e.worktree_id AND ev.generation_id = e.generation_id AND ev.source_key = e.source_key AND ev.target_key = e.target_key AND ev.edge_kind = e.edge_kind
		WHERE e.repo_id = ? AND e.worktree_id = ? AND e.generation_id = ?
		  AND (e.source_key = ? OR e.target_key = ?)
		  AND e.edge_kind IN (?, ?, ?, ?, ?)
		ORDER BY e.edge_kind, e.source_key, e.target_key,
		         COALESCE(ev.source_start, 0), COALESCE(ev.source_end, 0), ev.evidence_id
	`
	args := []any{
		string(v.generation.RepoID), string(v.generation.WorktreeID), int64(v.generation.ID), symbolKey, symbolKey,
		string(graph.EdgeCalls), string(graph.EdgePossibleCall), string(graph.EdgeUnresolvedCall), string(graph.EdgeImplements), string(graph.EdgeTests),
	}
	rows, err := v.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query graph relationships for %q: %w", symbolKey, err)
	}
	defer func() { _ = rows.Close() }()
	var result []graph.SymbolRelationship
	for rows.Next() {
		var relationship graph.SymbolRelationship
		var kind, confidence, sourceKind, targetKind string
		if err := rows.Scan(&relationship.SourceKey, &relationship.TargetKey, &kind, &confidence,
			&relationship.SourcePackage, &relationship.TargetPackage, &relationship.SourceFile, &relationship.TargetFile,
			&relationship.SourceName, &relationship.TargetName, &sourceKind, &targetKind,
			&relationship.Position.StartLine, &relationship.Position.EndLine, &relationship.Details); err != nil {
			return nil, fmt.Errorf("scan graph relationship for %q: %w", symbolKey, err)
		}
		relationship.Kind = graph.EdgeKind(kind)
		relationship.Confidence = graph.Confidence(confidence)
		relationship.SourceKind = graph.NodeKind(sourceKind)
		relationship.TargetKind = graph.NodeKind(targetKind)
		relationship.Position.Path = relationship.SourceFile
		result = append(result, relationship)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate graph relationships for %q: %w", symbolKey, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close graph relationships for %q: %w", symbolKey, err)
	}
	return result, nil
}

// boundedSymbolRelationships first limits distinct semantic edges and only
// then joins their descriptive records. A single edge may have multiple
// evidence rows; applying LIMIT after that join would spend the fanout budget
// on duplicate evidence and hide otherwise relevant neighbors.
func (v *View) boundedSymbolRelationships(ctx context.Context, symbolKey string, limit int) ([]graph.SymbolRelationship, error) {
	query := `
		WITH bounded_edges AS (
			SELECT e.repo_id, e.worktree_id, e.generation_id, e.source_key,
			       e.target_key, e.edge_kind, e.confidence, e.owner_package
			FROM edges AS e
			WHERE e.repo_id = ? AND e.worktree_id = ? AND e.generation_id = ?
			  AND (e.source_key = ? OR e.target_key = ?)
			  AND e.edge_kind IN (?, ?, ?, ?, ?)
			ORDER BY e.edge_kind, e.source_key, e.target_key
			LIMIT ?
		)
		SELECT e.source_key, e.target_key, e.edge_kind, e.confidence,
		       COALESCE(ss.package_key, sf.package_key, sfile.package_key, sn.package_key, e.owner_package, ''),
		       COALESCE(ts.package_key, tf.package_key, tfile.package_key, tn.package_key, tp.package_key, ''),
		       COALESCE(sf.path, sfile.path, ''), COALESCE(tf.path, tfile.path, ''),
		       COALESCE(ss.name, sn.symbol_key, ''), COALESCE(ts.name, tp.import_path, tn.symbol_key, ''),
		       COALESCE(ss.node_kind, sn.node_kind, CASE WHEN sfile.file_key IS NULL THEN '' ELSE 'file' END), COALESCE(ts.node_kind, tn.node_kind, CASE WHEN tp.package_key IS NULL THEN '' ELSE 'package' END),
		       COALESCE(ev.source_start, 0), COALESCE(ev.source_end, 0), COALESCE(ev.details, '')
		FROM bounded_edges AS e
		LEFT JOIN symbols AS ss ON ss.repo_id = e.repo_id AND ss.worktree_id = e.worktree_id AND ss.generation_id = e.generation_id AND ss.symbol_key = e.source_key
		LEFT JOIN symbols AS ts ON ts.repo_id = e.repo_id AND ts.worktree_id = e.worktree_id AND ts.generation_id = e.generation_id AND ts.symbol_key = e.target_key
		LEFT JOIN nodes AS sn ON sn.repo_id = e.repo_id AND sn.worktree_id = e.worktree_id AND sn.generation_id = e.generation_id AND sn.node_key = e.source_key
		LEFT JOIN nodes AS tn ON tn.repo_id = e.repo_id AND tn.worktree_id = e.worktree_id AND tn.generation_id = e.generation_id AND tn.node_key = e.target_key
		LEFT JOIN files AS sf ON sf.repo_id = e.repo_id AND sf.worktree_id = e.worktree_id AND sf.generation_id = e.generation_id AND sf.file_key = ss.file_key
		LEFT JOIN files AS sfile ON sfile.repo_id = e.repo_id AND sfile.worktree_id = e.worktree_id AND sfile.generation_id = e.generation_id AND sfile.file_key = e.source_key
		LEFT JOIN files AS tf ON tf.repo_id = e.repo_id AND tf.worktree_id = e.worktree_id AND tf.generation_id = e.generation_id AND tf.file_key = ts.file_key
		LEFT JOIN files AS tfile ON tfile.repo_id = e.repo_id AND tfile.worktree_id = e.worktree_id AND tfile.generation_id = e.generation_id AND tfile.file_key = e.target_key
		LEFT JOIN packages AS tp ON tp.repo_id = e.repo_id AND tp.worktree_id = e.worktree_id AND tp.generation_id = e.generation_id AND tp.package_key = CASE WHEN e.target_key LIKE 'package:%' THEN substr(e.target_key, length('package:') + 1) ELSE ts.package_key END
		LEFT JOIN edge_evidence AS ev ON ev.repo_id = e.repo_id AND ev.worktree_id = e.worktree_id AND ev.generation_id = e.generation_id AND ev.source_key = e.source_key AND ev.target_key = e.target_key AND ev.edge_kind = e.edge_kind
		  AND ev.evidence_id = (SELECT MIN(ev2.evidence_id) FROM edge_evidence AS ev2 WHERE ev2.repo_id = e.repo_id AND ev2.worktree_id = e.worktree_id AND ev2.generation_id = e.generation_id AND ev2.source_key = e.source_key AND ev2.target_key = e.target_key AND ev2.edge_kind = e.edge_kind)
		ORDER BY e.edge_kind, e.source_key, e.target_key
	`
	args := []any{
		string(v.generation.RepoID), string(v.generation.WorktreeID), int64(v.generation.ID), symbolKey, symbolKey,
		string(graph.EdgeCalls), string(graph.EdgePossibleCall), string(graph.EdgeUnresolvedCall), string(graph.EdgeImplements), string(graph.EdgeTests), limit,
	}
	rows, err := v.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query bounded graph relationships for %q: %w", symbolKey, err)
	}
	return scanSymbolRelationships(rows, symbolKey)
}

func scanSymbolRelationships(rows *sql.Rows, symbolKey string) ([]graph.SymbolRelationship, error) {
	defer func() { _ = rows.Close() }()
	var result []graph.SymbolRelationship
	for rows.Next() {
		var relationship graph.SymbolRelationship
		var kind, confidence, sourceKind, targetKind string
		if err := rows.Scan(&relationship.SourceKey, &relationship.TargetKey, &kind, &confidence,
			&relationship.SourcePackage, &relationship.TargetPackage, &relationship.SourceFile, &relationship.TargetFile,
			&relationship.SourceName, &relationship.TargetName, &sourceKind, &targetKind,
			&relationship.Position.StartLine, &relationship.Position.EndLine, &relationship.Details); err != nil {
			return nil, fmt.Errorf("scan graph relationship for %q: %w", symbolKey, err)
		}
		relationship.Kind = graph.EdgeKind(kind)
		relationship.Confidence = graph.Confidence(confidence)
		relationship.SourceKind = graph.NodeKind(sourceKind)
		relationship.TargetKind = graph.NodeKind(targetKind)
		relationship.Position.Path = relationship.SourceFile
		result = append(result, relationship)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate graph relationships for %q: %w", symbolKey, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close graph relationships for %q: %w", symbolKey, err)
	}
	return result, nil
}

func (v *View) filePath(ctx context.Context, fileKey string) (string, error) {
	var path string
	if err := v.tx.QueryRowContext(ctx, `SELECT path FROM files WHERE repo_id = ? AND worktree_id = ? AND generation_id = ? AND file_key = ?`, string(v.generation.RepoID), string(v.generation.WorktreeID), int64(v.generation.ID), fileKey).Scan(&path); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("%w: file %s", ErrNotFound, fileKey)
		}
		return "", fmt.Errorf("read graph symbol file path: %w", err)
	}
	return path, nil
}

func (v *View) references(ctx context.Context, target string) ([]graph.SymbolReference, error) {
	return v.referencesLimit(ctx, target, 0)
}

func (v *View) referencesLimit(ctx context.Context, target string, limit int) ([]graph.SymbolReference, error) {
	if limit > 0 {
		return v.boundedReferences(ctx, target, limit)
	}
	query := `
		SELECT e.source_key, e.edge_kind, e.confidence, COALESCE(s.package_key, ''), COALESCE(f.path, ''),
		       COALESCE(ev.source_start, 0), COALESCE(ev.source_end, 0)
		FROM edges AS e
		LEFT JOIN symbols AS s ON s.repo_id = e.repo_id AND s.worktree_id = e.worktree_id AND s.generation_id = e.generation_id AND s.symbol_key = e.source_key
		LEFT JOIN files AS f ON f.repo_id = e.repo_id AND f.worktree_id = e.worktree_id AND f.generation_id = e.generation_id AND f.file_key = s.file_key
		LEFT JOIN edge_evidence AS ev ON ev.repo_id = e.repo_id AND ev.worktree_id = e.worktree_id AND ev.generation_id = e.generation_id AND ev.source_key = e.source_key AND ev.target_key = e.target_key AND ev.edge_kind = e.edge_kind
		WHERE e.repo_id = ? AND e.worktree_id = ? AND e.generation_id = ? AND e.target_key = ? AND e.edge_kind = ?
		ORDER BY e.source_key, ev.source_start, ev.source_end, ev.evidence_id
	`
	args := []any{string(v.generation.RepoID), string(v.generation.WorktreeID), int64(v.generation.ID), target, string(graph.EdgeReferences)}
	rows, err := v.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query graph references: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []graph.SymbolReference
	for rows.Next() {
		var reference graph.SymbolReference
		var kind, confidence string
		if err := rows.Scan(&reference.SourceKey, &kind, &confidence, &reference.SourcePackage, &reference.SourceFile, &reference.Position.StartLine, &reference.Position.EndLine); err != nil {
			return nil, fmt.Errorf("scan graph reference: %w", err)
		}
		reference.TargetKey = target
		reference.Kind = graph.EdgeKind(kind)
		reference.Confidence = graph.Confidence(confidence)
		result = append(result, reference)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate graph references: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close graph references: %w", err)
	}
	return result, nil
}

func (v *View) boundedReferences(ctx context.Context, target string, limit int) ([]graph.SymbolReference, error) {
	query := `
		WITH bounded_edges AS (
			SELECT e.repo_id, e.worktree_id, e.generation_id, e.source_key,
			       e.target_key, e.edge_kind, e.confidence
			FROM edges AS e
			WHERE e.repo_id = ? AND e.worktree_id = ? AND e.generation_id = ?
			  AND e.target_key = ? AND e.edge_kind = ?
			ORDER BY e.source_key
			LIMIT ?
		)
		SELECT e.source_key, e.edge_kind, e.confidence, COALESCE(s.package_key, ''), COALESCE(f.path, ''),
		       COALESCE(ev.source_start, 0), COALESCE(ev.source_end, 0)
		FROM bounded_edges AS e
		LEFT JOIN symbols AS s ON s.repo_id = e.repo_id AND s.worktree_id = e.worktree_id AND s.generation_id = e.generation_id AND s.symbol_key = e.source_key
		LEFT JOIN files AS f ON f.repo_id = e.repo_id AND f.worktree_id = e.worktree_id AND f.generation_id = e.generation_id AND f.file_key = s.file_key
		LEFT JOIN edge_evidence AS ev ON ev.repo_id = e.repo_id AND ev.worktree_id = e.worktree_id AND ev.generation_id = e.generation_id AND ev.source_key = e.source_key AND ev.target_key = e.target_key AND ev.edge_kind = e.edge_kind
		  AND ev.evidence_id = (SELECT MIN(ev2.evidence_id) FROM edge_evidence AS ev2 WHERE ev2.repo_id = e.repo_id AND ev2.worktree_id = e.worktree_id AND ev2.generation_id = e.generation_id AND ev2.source_key = e.source_key AND ev2.target_key = e.target_key AND ev2.edge_kind = e.edge_kind)
		ORDER BY e.source_key
	`
	args := []any{string(v.generation.RepoID), string(v.generation.WorktreeID), int64(v.generation.ID), target, string(graph.EdgeReferences), limit}
	rows, err := v.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query bounded graph references: %w", err)
	}
	return scanSymbolReferences(rows, target)
}

func scanSymbolReferences(rows *sql.Rows, target string) ([]graph.SymbolReference, error) {
	defer func() { _ = rows.Close() }()
	var result []graph.SymbolReference
	for rows.Next() {
		var reference graph.SymbolReference
		var kind, confidence string
		if err := rows.Scan(&reference.SourceKey, &kind, &confidence, &reference.SourcePackage, &reference.SourceFile, &reference.Position.StartLine, &reference.Position.EndLine); err != nil {
			return nil, fmt.Errorf("scan graph reference: %w", err)
		}
		reference.TargetKey = target
		reference.Kind = graph.EdgeKind(kind)
		reference.Confidence = graph.Confidence(confidence)
		reference.Position.Path = reference.SourceFile
		result = append(result, reference)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate graph references: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close graph references: %w", err)
	}
	return result, nil
}

func (v *View) fileImports(ctx context.Context, packageKey string) ([]string, error) {
	rows, err := v.tx.QueryContext(ctx, `
		SELECT COALESCE(p.import_path, CASE WHEN d.target_package LIKE 'external:package:%' THEN substr(d.target_package, length('external:package:') + 1) ELSE d.target_package END)
		FROM package_dependencies AS d
		LEFT JOIN packages AS p ON p.repo_id = d.repo_id AND p.worktree_id = d.worktree_id AND p.generation_id = d.generation_id AND p.package_key = d.target_package
		WHERE d.repo_id = ? AND d.worktree_id = ? AND d.generation_id = ? AND d.source_package = ?
		ORDER BY 1
	`, string(v.generation.RepoID), string(v.generation.WorktreeID), int64(v.generation.ID), packageKey)
	if err != nil {
		return nil, fmt.Errorf("query graph imports: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan graph import: %w", err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate graph imports: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close graph imports: %w", err)
	}
	return result, nil
}
