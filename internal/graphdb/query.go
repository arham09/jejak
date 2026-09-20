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
	key, _, err := generationRef(ctx, db, repoID, worktreeID, id)
	if err != nil {
		return graph.Counts{}, err
	}
	counts, err := countGeneration(ctx, db, key)
	if err != nil {
		return graph.Counts{}, fmt.Errorf("count graph generation %d: %w", id, err)
	}
	return counts, nil
}

func countGeneration(ctx context.Context, q rowQuerier, key int64) (graph.Counts, error) {
	var counts graph.Counts
	if err := q.QueryRowContext(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM packages WHERE generation_key = ?),
		  (SELECT COUNT(*) FROM files WHERE generation_key = ?),
		  (SELECT COUNT(*) FROM symbols WHERE generation_key = ?),
		  (SELECT COUNT(*) FROM edges WHERE generation_key = ?),
		  (SELECT COUNT(*) FROM symbols WHERE generation_key = ? AND node_kind = ?)
	`, key, key, key, key, key, string(graph.NodeTest)).Scan(&counts.Packages, &counts.Files, &counts.Symbols, &counts.Edges, &counts.Tests); err != nil {
		return graph.Counts{}, err
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
		WHERE generation_key = ? AND (symbol_key = ? OR name = ?)
		ORDER BY package_key, file_key, start_line, symbol_key
	`, v.key, query, query)
	if err != nil {
		return nil, fmt.Errorf("query graph symbols: %w", err)
	}
	symbols, err := scanSymbols(rows, "")
	if err != nil {
		return nil, err
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
	file, err := v.findFile(ctx, query)
	if err != nil {
		return graph.FileResult{}, err
	}
	rows, err := v.tx.QueryContext(ctx, `
		SELECT symbol_key, node_kind, package_key, file_key, name, signature,
		       receiver, start_line, end_line, exported
		FROM symbols WHERE generation_key = ? AND file_key = ?
		ORDER BY start_line, symbol_key
	`, v.key, file.Key)
	if err != nil {
		return graph.FileResult{}, fmt.Errorf("query symbols for graph file %q: %w", file.Path, err)
	}
	symbols, err := scanSymbols(rows, file.Path)
	if err != nil {
		return graph.FileResult{}, fmt.Errorf("graph file %q: %w", file.Path, err)
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

// findFile resolves one file row by repository-relative path or file key.
func (v *View) findFile(ctx context.Context, query string) (graph.File, error) {
	var file graph.File
	if err := v.tx.QueryRowContext(ctx, `
		SELECT file_key, path, blob_sha, package_key
		FROM files
		WHERE generation_key = ? AND (path = ? OR file_key = ?)
		ORDER BY path
		LIMIT 1
	`, v.key, query, query).Scan(&file.Key, &file.Path, &file.BlobSHA, &file.PackageKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return graph.File{}, fmt.Errorf("%w: file %q", ErrNotFound, query)
		}
		return graph.File{}, fmt.Errorf("query graph file %q: %w", query, err)
	}
	file.IsTest = strings.Contains(file.PackageKey, "#")
	return file, nil
}

// scanSymbols reads symbol rows in the column order used by every symbol
// query. A non-empty path is assigned to each position; callers pass "" when
// the path must be resolved per symbol.
func scanSymbols(rows *sql.Rows, path string) ([]graph.Symbol, error) {
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
		symbol.Position.Path = path
		symbols = append(symbols, symbol)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate graph symbols: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close graph symbols: %w", err)
	}
	return symbols, nil
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

// nodeID resolves the integer id of one node key in the pinned generation.
// A missing node has no edges, which callers report as an empty result.
func (v *View) nodeID(ctx context.Context, nodeKey string) (int64, bool, error) {
	var id int64
	err := v.tx.QueryRowContext(ctx, `SELECT node_id FROM nodes WHERE generation_key = ? AND node_key = ?`, v.key, nodeKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("resolve graph node %q: %w", nodeKey, err)
	}
	return id, true, nil
}

// incidentEdgesCTE selects the semantic edges that touch one node, as the
// body of a WITH clause. SQLite cannot satisfy a single OR over source_id
// and target_id from the two edge indexes, so it would fall back to reading
// every edge in the generation. Two indexed branches joined by UNION keep
// both lookups on their index, and UNION also collapses the duplicate a
// self-referencing edge would otherwise produce.
const incidentEdgesCTE = `incident_edges AS (
			SELECT edge_id, generation_key, source_id, target_id, edge_kind, confidence, owner_package
			FROM edges
			WHERE generation_key = ? AND source_id = ?
			  AND edge_kind IN (?, ?, ?, ?, ?)
			UNION
			SELECT edge_id, generation_key, source_id, target_id, edge_kind, confidence, owner_package
			FROM edges
			WHERE generation_key = ? AND target_id = ?
			  AND edge_kind IN (?, ?, ?, ?, ?)
		)`

// relationshipColumns describes both endpoints of an incident edge. Symbol
// rows win over node rows for names and kinds, file rows supply paths, and
// a package target falls back to the package record it names.
const relationshipColumns = `
		SELECT sn.node_key, tn.node_key, e.edge_kind, e.confidence,
		       COALESCE(ss.package_key, sf.package_key, sfile.package_key, sn.package_key, e.owner_package, ''),
		       COALESCE(ts.package_key, tf.package_key, tfile.package_key, tn.package_key, tp.package_key, ''),
		       COALESCE(sf.path, sfile.path, ''), COALESCE(tf.path, tfile.path, ''),
		       COALESCE(ss.name, sn.symbol_key, ''), COALESCE(ts.name, tp.import_path, tn.symbol_key, ''),
		       COALESCE(ss.node_kind, sn.node_kind), COALESCE(ts.node_kind, tn.node_kind),
		       COALESCE(ev.source_start, 0), COALESCE(ev.source_end, 0), COALESCE(ev.details, '')`

const relationshipJoins = `
		JOIN nodes AS sn ON sn.node_id = e.source_id
		JOIN nodes AS tn ON tn.node_id = e.target_id
		LEFT JOIN symbols AS ss ON ss.generation_key = e.generation_key AND ss.symbol_key = sn.node_key
		LEFT JOIN symbols AS ts ON ts.generation_key = e.generation_key AND ts.symbol_key = tn.node_key
		LEFT JOIN files AS sf ON sf.generation_key = e.generation_key AND sf.file_key = ss.file_key
		LEFT JOIN files AS sfile ON sfile.generation_key = e.generation_key AND sfile.file_key = sn.node_key
		LEFT JOIN files AS tf ON tf.generation_key = e.generation_key AND tf.file_key = ts.file_key
		LEFT JOIN files AS tfile ON tfile.generation_key = e.generation_key AND tfile.file_key = tn.node_key
		LEFT JOIN packages AS tp ON tp.generation_key = e.generation_key AND tp.package_key = CASE WHEN tn.node_key LIKE 'package:%' THEN substr(tn.node_key, length('package:') + 1) ELSE ts.package_key END`

// relationshipQuery returns every evidence row of every incident semantic
// edge.
const relationshipQuery = `WITH ` + incidentEdgesCTE + relationshipColumns + `
		FROM incident_edges AS e` + relationshipJoins + `
		LEFT JOIN edge_evidence AS ev ON ev.edge_id = e.edge_id
		ORDER BY e.edge_kind, sn.node_key, tn.node_key,
		         COALESCE(ev.source_start, 0), COALESCE(ev.source_end, 0), ev.evidence_id
	`

// boundedRelationshipQuery first limits distinct semantic edges and only then
// joins their descriptive records. A single edge may have multiple evidence
// rows; applying LIMIT after that join would spend the fanout budget on
// duplicate evidence and hide otherwise relevant neighbors.
const boundedRelationshipQuery = `WITH ` + incidentEdgesCTE + `,
		bounded_edges AS (
			SELECT e.edge_id, e.generation_key, e.source_id, e.target_id, e.edge_kind, e.confidence, e.owner_package
			FROM incident_edges AS e
			JOIN nodes AS bs ON bs.node_id = e.source_id
			JOIN nodes AS bt ON bt.node_id = e.target_id
			ORDER BY e.edge_kind, bs.node_key, bt.node_key
			LIMIT ?
		)` + relationshipColumns + `
		FROM bounded_edges AS e` + relationshipJoins + `
		LEFT JOIN edge_evidence AS ev ON ev.evidence_id = (SELECT MIN(ev2.evidence_id) FROM edge_evidence AS ev2 WHERE ev2.edge_id = e.edge_id)
		ORDER BY e.edge_kind, sn.node_key, tn.node_key
	`

// incidentEdgeArgs binds incidentEdgesCTE for one node. Both branches take
// the same generation key, node id, and edge kinds.
func (v *View) incidentEdgeArgs(nodeID int64) []any {
	args := make([]any, 0, 14)
	for range 2 {
		args = append(args,
			v.key, nodeID,
			string(graph.EdgeCalls), string(graph.EdgePossibleCall), string(graph.EdgeUnresolvedCall), string(graph.EdgeImplements), string(graph.EdgeTests),
		)
	}
	return args
}

func (v *View) symbolRelationships(ctx context.Context, symbolKey string) ([]graph.SymbolRelationship, error) {
	return v.symbolRelationshipsLimit(ctx, symbolKey, 0)
}

func (v *View) symbolRelationshipsLimit(ctx context.Context, symbolKey string, limit int) ([]graph.SymbolRelationship, error) {
	nodeID, found, err := v.nodeID(ctx, symbolKey)
	if err != nil || !found {
		return nil, err
	}
	query, args := relationshipQuery, v.incidentEdgeArgs(nodeID)
	if limit > 0 {
		query, args = boundedRelationshipQuery, append(args, limit)
	}
	rows, err := v.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query graph relationships for %q: %w", symbolKey, err)
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
	if err := v.tx.QueryRowContext(ctx, `SELECT path FROM files WHERE generation_key = ? AND file_key = ?`, v.key, fileKey).Scan(&path); err != nil {
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

// referenceQuery returns every evidence row of every incoming reference edge
// of one target node. The target lookup seeks idx_edges_target; the source
// key comes from the joined node row.
const referenceQuery = `
		SELECT sn.node_key, e.edge_kind, e.confidence, COALESCE(s.package_key, ''), COALESCE(f.path, ''),
		       COALESCE(ev.source_start, 0), COALESCE(ev.source_end, 0)
		FROM edges AS e
		JOIN nodes AS sn ON sn.node_id = e.source_id
		LEFT JOIN symbols AS s ON s.generation_key = e.generation_key AND s.symbol_key = sn.node_key
		LEFT JOIN files AS f ON f.generation_key = e.generation_key AND f.file_key = s.file_key
		LEFT JOIN edge_evidence AS ev ON ev.edge_id = e.edge_id
		WHERE e.generation_key = ? AND e.target_id = ? AND e.edge_kind = ?
		ORDER BY sn.node_key, ev.source_start, ev.source_end, ev.evidence_id
	`

// boundedReferenceQuery limits distinct reference edges before joining one
// evidence row per edge, for the same reason as boundedRelationshipQuery.
const boundedReferenceQuery = `
		WITH bounded_edges AS (
			SELECT e.edge_id, e.generation_key, e.edge_kind, e.confidence, sn.node_key AS source_key
			FROM edges AS e
			JOIN nodes AS sn ON sn.node_id = e.source_id
			WHERE e.generation_key = ? AND e.target_id = ? AND e.edge_kind = ?
			ORDER BY sn.node_key
			LIMIT ?
		)
		SELECT e.source_key, e.edge_kind, e.confidence, COALESCE(s.package_key, ''), COALESCE(f.path, ''),
		       COALESCE(ev.source_start, 0), COALESCE(ev.source_end, 0)
		FROM bounded_edges AS e
		LEFT JOIN symbols AS s ON s.generation_key = e.generation_key AND s.symbol_key = e.source_key
		LEFT JOIN files AS f ON f.generation_key = e.generation_key AND f.file_key = s.file_key
		LEFT JOIN edge_evidence AS ev ON ev.evidence_id = (SELECT MIN(ev2.evidence_id) FROM edge_evidence AS ev2 WHERE ev2.edge_id = e.edge_id)
		ORDER BY e.source_key
	`

func (v *View) referencesLimit(ctx context.Context, target string, limit int) ([]graph.SymbolReference, error) {
	nodeID, found, err := v.nodeID(ctx, target)
	if err != nil || !found {
		return nil, err
	}
	query := referenceQuery
	args := []any{v.key, nodeID, string(graph.EdgeReferences)}
	if limit > 0 {
		query = boundedReferenceQuery
		args = append(args, limit)
	}
	rows, err := v.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query graph references: %w", err)
	}
	return scanSymbolReferences(rows, target, limit > 0)
}

// scanSymbolReferences reads reference rows. Bounded impact lookups record
// the source file as the reference position path; the unbounded inspection
// form leaves it empty, as it always has.
func scanSymbolReferences(rows *sql.Rows, target string, withPath bool) ([]graph.SymbolReference, error) {
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
		if withPath {
			reference.Position.Path = reference.SourceFile
		}
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
		LEFT JOIN packages AS p ON p.generation_key = d.generation_key AND p.package_key = d.target_package
		WHERE d.generation_key = ? AND d.source_package = ?
		ORDER BY 1
	`, v.key, packageKey)
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
