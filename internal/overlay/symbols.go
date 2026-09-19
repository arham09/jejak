package overlay

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/arham09/jejak/internal/graph"
)

// SearchSymbols searches effective local declarations and unaffected base
// declarations in deterministic scope/order.
func (v *View) SearchSymbols(ctx context.Context, terms []string, limit int) ([]graph.SymbolResult, error) {
	if v == nil {
		return nil, errors.New("overlay view is nil")
	}
	terms = normalizeTerms(terms)
	if len(terms) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 256
	}
	if limit > 2048 {
		limit = 2048
	}
	v.mu.RLock()
	if v.closed {
		v.mu.RUnlock()
		return nil, errors.New("overlay view is closed")
	}
	result := make([]graph.SymbolResult, 0)
	seen := make(map[string]struct{})
	for key, item := range v.symbols {
		if symbolMatches(item.Symbol, terms) {
			result = append(result, cloneSymbolResult(item))
			seen[key] = struct{}{}
		}
	}
	base := v.base
	masked := cloneKeySet(v.maskedKeys)
	affected := clonePathSet(v.affectedPath)
	v.mu.RUnlock()
	baseResults, err := base.SearchSymbols(ctx, terms, limit*2)
	if err != nil {
		return nil, err
	}
	for _, item := range baseResults {
		if _, exists := seen[item.Symbol.Key]; exists || baseSymbolMasked(item.Symbol, masked, affected) {
			continue
		}
		item = sanitizeResult(item, masked, affected)
		result = append(result, item)
		seen[item.Symbol.Key] = struct{}{}
	}
	sort.SliceStable(result, func(i, j int) bool { return symbolResultLess(result[i], result[j]) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// FindSymbols returns effective exact-key/name matches. Deleted baseline keys
// are returned only as historical records so actual-change impact can load
// them explicitly.
func (v *View) FindSymbols(ctx context.Context, query string) ([]graph.SymbolResult, error) {
	if v == nil {
		return nil, errors.New("overlay view is nil")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("symbol query is empty")
	}
	v.mu.RLock()
	if v.closed {
		v.mu.RUnlock()
		return nil, errors.New("overlay view is closed")
	}
	if item, ok := v.symbols[query]; ok {
		v.mu.RUnlock()
		return []graph.SymbolResult{cloneSymbolResult(item)}, nil
	}
	if item, ok := v.historical[query]; ok {
		v.mu.RUnlock()
		return []graph.SymbolResult{cloneSymbolResult(item)}, nil
	}
	local := cloneSymbolResults(v.symbolsByName[query])
	local = append(local, cloneSymbolResults(v.historicalByName[query])...)
	base := v.base
	masked := cloneKeySet(v.maskedKeys)
	affected := clonePathSet(v.affectedPath)
	v.mu.RUnlock()
	if len(local) > 0 {
		return local, nil
	}
	results, err := base.FindSymbols(ctx, query)
	if err != nil {
		return nil, err
	}
	filtered := make([]graph.SymbolResult, 0, len(results))
	for _, item := range results {
		if baseSymbolMasked(item.Symbol, masked, affected) {
			continue
		}
		filtered = append(filtered, sanitizeResult(item, masked, affected))
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("symbol %q is unavailable in the effective graph", query)
	}
	return filtered, nil
}

// FindSymbolForImpact returns one effective or historical symbol with bounded
// relationships for the Phase 6 traversal path.
func (v *View) FindSymbolForImpact(ctx context.Context, key string, limit int) (graph.SymbolResult, error) {
	if v == nil {
		return graph.SymbolResult{}, errors.New("overlay view is nil")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return graph.SymbolResult{}, fmt.Errorf("symbol key is empty")
	}
	v.mu.RLock()
	if v.closed {
		v.mu.RUnlock()
		return graph.SymbolResult{}, errors.New("overlay view is closed")
	}
	if item, ok := v.symbols[key]; ok {
		v.mu.RUnlock()
		return limitResult(cloneSymbolResult(item), limit), nil
	}
	if item, ok := v.historical[key]; ok {
		v.mu.RUnlock()
		return limitResult(cloneSymbolResult(item), limit), nil
	}
	base := v.base
	masked := cloneKeySet(v.maskedKeys)
	affected := clonePathSet(v.affectedPath)
	v.mu.RUnlock()
	item, err := base.FindSymbols(ctx, key)
	if err != nil {
		return graph.SymbolResult{}, err
	}
	for _, candidate := range item {
		if candidate.Symbol.Key != key || baseSymbolMasked(candidate.Symbol, masked, affected) {
			continue
		}
		return limitResult(sanitizeResult(candidate, masked, affected), limit), nil
	}
	return graph.SymbolResult{}, fmt.Errorf("symbol %q is unavailable in the effective graph", key)
}

func (v *View) buildEffectiveSymbols() {
	changeKinds := make(map[string]string)
	for _, change := range v.changes {
		if change.After.Key != "" {
			changeKinds[change.After.Key] = string(change.Kind)
		}
	}
	for _, symbol := range v.analysis.Symbols {
		result := v.symbolResult(symbol)
		result.ChangeKind = changeKinds[symbol.Key]
		v.symbols[symbol.Key] = result
		v.symbolsByName[symbol.Name] = append(v.symbolsByName[symbol.Name], result)
	}
	for name := range v.symbolsByName {
		sort.SliceStable(v.symbolsByName[name], func(i, j int) bool { return symbolResultLess(v.symbolsByName[name][i], v.symbolsByName[name][j]) })
	}
}

func (v *View) buildHistoricalSymbols() {
	for _, change := range v.changes {
		if (change.Kind != SymbolDeleted && change.Kind != SymbolRenamed) || change.Before.Key == "" {
			continue
		}
		for _, fact := range v.baseFacts {
			items := fact.SymbolResults
			if len(items) == 0 {
				for _, symbol := range fact.Symbols {
					items = append(items, graph.SymbolResult{Symbol: symbol})
				}
			}
			for _, item := range items {
				if item.Symbol.Key != change.Before.Key {
					continue
				}
				item.Historical = true
				item.ChangeKind = string(change.Kind)
				markHistorical(&item)
				v.historical[item.Symbol.Key] = item
				v.historicalByName[item.Symbol.Name] = append(v.historicalByName[item.Symbol.Name], item)
			}
		}
	}
	for name := range v.historicalByName {
		sort.SliceStable(v.historicalByName[name], func(i, j int) bool { return symbolResultLess(v.historicalByName[name][i], v.historicalByName[name][j]) })
	}
}

func baseSymbolMasked(symbol graph.Symbol, masked map[string]struct{}, affected map[string]struct{}) bool {
	if _, ok := masked[symbol.Key]; ok {
		return true
	}
	return pathSetContains(affected, symbol.Position.Path)
}

func symbolMatches(symbol graph.Symbol, terms []string) bool {
	fields := strings.ToLower(strings.Join([]string{symbol.Key, symbol.Name, symbol.Receiver, symbol.Signature, symbol.PackageKey, symbol.FileKey, symbol.Position.Path}, " "))
	for _, term := range terms {
		if strings.Contains(fields, term) {
			return true
		}
	}
	return false
}

func normalizeTerms(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func symbolResultLess(left, right graph.SymbolResult) bool {
	if left.Symbol.PackageKey != right.Symbol.PackageKey {
		return left.Symbol.PackageKey < right.Symbol.PackageKey
	}
	if left.Symbol.Position.Path != right.Symbol.Position.Path {
		return left.Symbol.Position.Path < right.Symbol.Position.Path
	}
	if left.Symbol.Position.StartLine != right.Symbol.Position.StartLine {
		return left.Symbol.Position.StartLine < right.Symbol.Position.StartLine
	}
	return left.Symbol.Key < right.Symbol.Key
}

func cloneSymbolResults(values []graph.SymbolResult) []graph.SymbolResult {
	result := make([]graph.SymbolResult, 0, len(values))
	for _, value := range values {
		result = append(result, cloneSymbolResult(value))
	}
	return result
}

func cloneSymbolResult(value graph.SymbolResult) graph.SymbolResult {
	value.References = append([]graph.SymbolReference(nil), value.References...)
	value.Calls = append([]graph.SymbolRelationship(nil), value.Calls...)
	value.CalledBy = append([]graph.SymbolRelationship(nil), value.CalledBy...)
	value.Implementations = append([]graph.SymbolRelationship(nil), value.Implementations...)
	value.ImplementedBy = append([]graph.SymbolRelationship(nil), value.ImplementedBy...)
	value.Tests = append([]graph.SymbolRelationship(nil), value.Tests...)
	return value
}
