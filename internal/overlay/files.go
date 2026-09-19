package overlay

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arham09/jejak/internal/graph"
)

// FindFile returns effective file declarations/imports, delegating unaffected
// paths to the committed reader.
func (v *View) FindFile(ctx context.Context, query string) (graph.FileResult, error) {
	if v == nil {
		return graph.FileResult{}, errors.New("overlay view is nil")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return graph.FileResult{}, fmt.Errorf("file query is empty")
	}
	v.mu.RLock()
	if v.closed {
		v.mu.RUnlock()
		return graph.FileResult{}, errors.New("overlay view is closed")
	}
	path := strings.TrimPrefix(filepath.ToSlash(query), "file:")
	if file, ok := v.files[path]; ok {
		result := v.fileResultLocked(file)
		v.mu.RUnlock()
		return result, nil
	}
	base := v.base
	masked := cloneKeySet(v.maskedKeys)
	affected := clonePathSet(v.affectedPath)
	v.mu.RUnlock()
	result, err := base.FindFile(ctx, query)
	if err != nil {
		return graph.FileResult{}, err
	}
	if baseSymbolMasked(graph.Symbol{FileKey: result.File.Key, Position: graph.Position{Path: result.File.Path}}, masked, affected) || pathSetContains(affected, result.File.Path) {
		return graph.FileResult{}, fmt.Errorf("file %q is unavailable in the effective graph", query)
	}
	result.SymbolResults = sanitizeResults(result.SymbolResults, masked, affected)
	result.Symbols = make([]graph.Symbol, 0, len(result.SymbolResults))
	for _, item := range result.SymbolResults {
		result.Symbols = append(result.Symbols, item.Symbol)
	}
	return result, nil
}

// FindFileMetadata returns only effective immutable file provenance.
func (v *View) FindFileMetadata(ctx context.Context, query string) (graph.File, error) {
	if v == nil {
		return graph.File{}, errors.New("overlay view is nil")
	}
	result, err := v.FindFile(ctx, query)
	if err != nil {
		// Deleted/renamed declarations are masked from effective file
		// inspection, but impact context still needs their immutable baseline
		// blob to explain what disappeared.
		v.mu.RLock()
		defer v.mu.RUnlock()
		if v.closed {
			return graph.File{}, errors.New("overlay view is closed")
		}
		path := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(query)), "file:")
		for _, fact := range v.baseFacts {
			if fact.File.Path != path && fact.File.Key != query {
				continue
			}
			for _, historical := range v.historical {
				if historical.Symbol.FileKey == fact.File.Key {
					return fact.File, nil
				}
			}
		}
		return graph.File{}, err
	}
	return result.File, nil
}

// FindHistoricalFileMetadata returns committed file provenance for a deleted
// or renamed declaration. It is intentionally separate from FindFile so a
// deleted path cannot re-enter effective file inspection.
func (v *View) FindHistoricalFileMetadata(ctx context.Context, query string) (graph.File, error) {
	if v == nil {
		return graph.File{}, errors.New("overlay view is nil")
	}
	if err := ctx.Err(); err != nil {
		return graph.File{}, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return graph.File{}, fmt.Errorf("historical file query is empty")
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.closed {
		return graph.File{}, errors.New("overlay view is closed")
	}
	path := strings.TrimPrefix(filepath.ToSlash(query), "file:")
	for _, fact := range v.baseFacts {
		if fact.File.Path != path && fact.File.Key != query {
			continue
		}
		for _, historical := range v.historical {
			if historical.Symbol.FileKey == fact.File.Key {
				return fact.File, nil
			}
		}
	}
	return graph.File{}, fmt.Errorf("historical file %q is unavailable", query)
}

func (v *View) fileResultLocked(file graph.File) graph.FileResult {
	result := graph.FileResult{File: file, Symbols: []graph.Symbol{}, SymbolResults: []graph.SymbolResult{}, Imports: append([]string(nil), v.packageImports[file.PackageKey]...)}
	for _, item := range v.symbols {
		if item.Symbol.FileKey != file.Key {
			continue
		}
		result.Symbols = append(result.Symbols, item.Symbol)
		result.SymbolResults = append(result.SymbolResults, cloneSymbolResult(item))
	}
	sort.Slice(result.Symbols, func(i, j int) bool { return result.Symbols[i].Key < result.Symbols[j].Key })
	sort.Slice(result.SymbolResults, func(i, j int) bool { return result.SymbolResults[i].Symbol.Key < result.SymbolResults[j].Symbol.Key })
	return result
}

func (v *View) pathAffected(path string) bool {
	_, ok := v.affectedPath[path]
	return ok
}

func clonePathSet(values map[string]struct{}) map[string]struct{} {
	return cloneKeySet(values)
}

func pathSetContains(values map[string]struct{}, path string) bool {
	_, ok := values[path]
	return ok
}

func cloneFileFacts(values map[string]graph.FileResult) map[string]graph.FileResult {
	result := make(map[string]graph.FileResult, len(values))
	for key, value := range values {
		value.Symbols = append([]graph.Symbol(nil), value.Symbols...)
		value.SymbolResults = append([]graph.SymbolResult(nil), value.SymbolResults...)
		value.Imports = append([]string(nil), value.Imports...)
		result[key] = value
	}
	return result
}
