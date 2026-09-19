package graphdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/arham09/jejak/internal/graph"
)

// FindSymbolForImpact returns one symbol with bounded semantic relationships.
// It avoids loading the complete relationship fanout used by the interactive
// graph inspection command, which keeps large repositories responsive to
// bounded impact requests.
func (v *View) FindSymbolForImpact(ctx context.Context, key string, limit int) (graph.SymbolResult, error) {
	if v == nil || v.tx == nil {
		return graph.SymbolResult{}, ErrStoreClosed
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return graph.SymbolResult{}, fmt.Errorf("%w: symbol key is empty", ErrNotFound)
	}
	var symbol graph.Symbol
	var kind string
	var exported int
	if err := v.tx.QueryRowContext(ctx, `
		SELECT s.symbol_key, s.node_kind, s.package_key, s.file_key, s.name,
		       s.signature, s.receiver, s.start_line, s.end_line, s.exported,
		       COALESCE(f.path, '')
		FROM symbols AS s
		LEFT JOIN files AS f ON f.repo_id = s.repo_id AND f.worktree_id = s.worktree_id
		  AND f.generation_id = s.generation_id AND f.file_key = s.file_key
		WHERE s.repo_id = ? AND s.worktree_id = ? AND s.generation_id = ? AND s.symbol_key = ?
	`, string(v.generation.RepoID), string(v.generation.WorktreeID), int64(v.generation.ID), key).Scan(
		&symbol.Key, &kind, &symbol.PackageKey, &symbol.FileKey, &symbol.Name,
		&symbol.Signature, &symbol.Receiver, &symbol.Position.StartLine,
		&symbol.Position.EndLine, &exported, &symbol.Position.Path); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return graph.SymbolResult{}, fmt.Errorf("%w: symbol %q", ErrNotFound, key)
		}
		return graph.SymbolResult{}, fmt.Errorf("query impact symbol %q: %w", key, err)
	}
	symbol.Kind = graph.NodeKind(kind)
	symbol.Exported = exported == 1
	relationships, err := v.symbolRelationshipsLimit(ctx, key, limit)
	if err != nil {
		return graph.SymbolResult{}, err
	}
	references, err := v.referencesLimit(ctx, key, limit)
	if err != nil {
		return graph.SymbolResult{}, err
	}
	return decorateSymbol(symbol, references, relationships), nil
}

// SearchSymbols returns bounded symbol candidates whose indexed fields contain
// at least one of terms. The search is pinned to the view's repository,
// worktree, and generation; callers perform ranking in their own domain
// package so SQLite remains a storage concern.
func (v *View) SearchSymbols(ctx context.Context, terms []string, limit int) ([]graph.SymbolResult, error) {
	if v == nil || v.tx == nil {
		return nil, ErrStoreClosed
	}
	terms = normalizeSearchTerms(terms)
	if len(terms) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 256
	}
	if limit > 2048 {
		limit = 2048
	}
	clauses := make([]string, 0, len(terms))
	args := make([]any, 0, len(terms)*8+4)
	for range terms {
		clauses = append(clauses, `(instr(lower(s.symbol_key), ?) > 0 OR instr(lower(s.name), ?) > 0 OR instr(lower(s.receiver), ?) > 0 OR instr(lower(s.signature), ?) > 0 OR instr(lower(s.package_key), ?) > 0 OR instr(lower(s.file_key), ?) > 0 OR instr(lower(COALESCE(f.path, '')), ?) > 0 OR instr(lower(COALESCE(p.import_path, '')), ?) > 0)`)
	}
	args = append(args, string(v.generation.RepoID), string(v.generation.WorktreeID), int64(v.generation.ID))
	for _, term := range terms {
		for range 8 {
			args = append(args, term)
		}
	}
	args = append(args, limit)
	query := `
		SELECT s.symbol_key, s.node_kind, s.package_key, s.file_key, s.name,
		       s.signature, s.receiver, s.start_line, s.end_line, s.exported,
		       COALESCE(f.path, '')
		FROM symbols AS s
		LEFT JOIN files AS f ON f.repo_id = s.repo_id AND f.worktree_id = s.worktree_id
		  AND f.generation_id = s.generation_id AND f.file_key = s.file_key
		LEFT JOIN packages AS p ON p.repo_id = s.repo_id AND p.worktree_id = s.worktree_id
		  AND p.generation_id = s.generation_id AND p.package_key = s.package_key
		WHERE s.repo_id = ? AND s.worktree_id = ? AND s.generation_id = ?
		  AND (` + strings.Join(clauses, " OR ") + `)
		ORDER BY s.package_key, COALESCE(f.path, ''), s.start_line, s.symbol_key
		LIMIT ?`
	rows, err := v.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search graph symbols: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []graph.SymbolResult
	for rows.Next() {
		var symbol graph.Symbol
		var kind string
		var exported int
		if err := rows.Scan(&symbol.Key, &kind, &symbol.PackageKey, &symbol.FileKey,
			&symbol.Name, &symbol.Signature, &symbol.Receiver, &symbol.Position.StartLine,
			&symbol.Position.EndLine, &exported, &symbol.Position.Path); err != nil {
			return nil, fmt.Errorf("scan searched graph symbol: %w", err)
		}
		symbol.Kind = graph.NodeKind(kind)
		symbol.Exported = exported == 1
		result = append(result, graph.SymbolResult{Symbol: symbol})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate searched graph symbols: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close searched graph symbols: %w", err)
	}
	return result, nil
}

func normalizeSearchTerms(terms []string) []string {
	seen := make(map[string]struct{}, len(terms))
	result := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" {
			continue
		}
		if _, exists := seen[term]; exists {
			continue
		}
		seen[term] = struct{}{}
		result = append(result, term)
	}
	return result
}

// FindFileMetadata returns only immutable file provenance for a generation.
// Impact traversal uses this bounded form instead of loading every symbol and
// relationship declared by the file.
func (v *View) FindFileMetadata(ctx context.Context, query string) (graph.File, error) {
	if v == nil || v.tx == nil {
		return graph.File{}, ErrStoreClosed
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return graph.File{}, fmt.Errorf("%w: file query is empty", ErrNotFound)
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
			return graph.File{}, fmt.Errorf("%w: file %q", ErrNotFound, query)
		}
		return graph.File{}, fmt.Errorf("query graph file metadata %q: %w", query, err)
	}
	file.IsTest = strings.Contains(file.PackageKey, "#")
	return file, nil
}
