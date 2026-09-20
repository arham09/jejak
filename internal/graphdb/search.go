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
		LEFT JOIN files AS f ON f.generation_key = s.generation_key AND f.file_key = s.file_key
		WHERE s.generation_key = ? AND s.symbol_key = ?
	`, v.key, key).Scan(
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
// at least one of terms, keeping the rows that match the most terms.
//
// The result is bounded, so the bound must not decide relevance by accident.
// Ordering the matches by package path and truncating afterwards discards a
// symbol that matches every term whenever enough weaker matches sort ahead of
// it alphabetically, and the caller then ranks a set the best answer never
// reached. Counting matched terms in SQL keeps the truncation on the strongest
// candidates. Final relevance still belongs to the caller's domain package;
// this count only decides what survives the limit.
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
	const termMatch = `CASE WHEN instr(lower(s.symbol_key), ?) > 0 OR instr(lower(s.name), ?) > 0 OR instr(lower(s.receiver), ?) > 0 OR instr(lower(s.signature), ?) > 0 OR instr(lower(s.package_key), ?) > 0 OR instr(lower(s.file_key), ?) > 0 OR instr(lower(COALESCE(f.path, '')), ?) > 0 OR instr(lower(COALESCE(p.import_path, '')), ?) > 0 THEN 1 ELSE 0 END`
	scores := make([]string, 0, len(terms))
	for range terms {
		scores = append(scores, termMatch)
	}
	// Placeholders bind in the order they appear in the statement, and the
	// scoring expression precedes the generation predicate.
	args := make([]any, 0, len(terms)*8+2)
	for _, term := range terms {
		for range 8 {
			args = append(args, term)
		}
	}
	args = append(args, v.key, limit)
	query := `
		WITH scored AS (
			SELECT s.symbol_key AS symbol_key, s.node_kind AS node_kind,
			       s.package_key AS package_key, s.file_key AS file_key,
			       s.name AS name, s.signature AS signature,
			       s.receiver AS receiver, s.start_line AS start_line,
			       s.end_line AS end_line, s.exported AS exported,
			       COALESCE(f.path, '') AS path,
			       ` + strings.Join(scores, " + ") + ` AS matched_terms
			FROM symbols AS s
			LEFT JOIN files AS f ON f.generation_key = s.generation_key AND f.file_key = s.file_key
			LEFT JOIN packages AS p ON p.generation_key = s.generation_key AND p.package_key = s.package_key
			WHERE s.generation_key = ?
		)
		SELECT symbol_key, node_kind, package_key, file_key, name, signature,
		       receiver, start_line, end_line, exported, path
		FROM scored
		WHERE matched_terms > 0
		ORDER BY matched_terms DESC, package_key, path, start_line, symbol_key
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
	return v.findFile(ctx, query)
}
