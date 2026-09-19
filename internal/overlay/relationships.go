package overlay

import (
	"sort"

	"github.com/arham09/jejak/internal/graph"
)

func (v *View) symbolResult(symbol graph.Symbol) graph.SymbolResult {
	result := graph.SymbolResult{Symbol: symbol}
	for _, edge := range v.analysisEdgesByKey[symbol.Key] {
		if edge.SourceKey != symbol.Key && edge.TargetKey != symbol.Key {
			continue
		}
		relationship := v.relationship(edge)
		if edge.Kind == graph.EdgeReferences {
			if edge.TargetKey == symbol.Key {
				result.References = append(result.References, graph.SymbolReference{SourceKey: relationship.SourceKey, TargetKey: relationship.TargetKey, SourcePackage: relationship.SourcePackage, SourceFile: relationship.SourceFile, Kind: relationship.Kind, Confidence: relationship.Confidence, Position: relationship.Position, Historical: relationship.Historical})
			}
			continue
		}
		switch edge.Kind {
		case graph.EdgeCalls, graph.EdgePossibleCall, graph.EdgeUnresolvedCall:
			if edge.SourceKey == symbol.Key {
				result.Calls = append(result.Calls, relationship)
			} else {
				result.CalledBy = append(result.CalledBy, relationship)
			}
		case graph.EdgeImplements:
			if edge.SourceKey == symbol.Key {
				result.Implementations = append(result.Implementations, relationship)
			} else {
				result.ImplementedBy = append(result.ImplementedBy, relationship)
			}
		case graph.EdgeTests:
			result.Tests = append(result.Tests, relationship)
		}
	}
	sortSymbolRelationships(&result)
	return result
}

func (v *View) relationship(edge graph.Edge) graph.SymbolRelationship {
	relationship := graph.SymbolRelationship{SourceKey: edge.SourceKey, TargetKey: edge.TargetKey, Kind: edge.Kind, Confidence: edge.Confidence}
	if relationship.Confidence == "" {
		relationship.Confidence = graph.ConfidenceExact
	}
	if symbol, ok := v.analysisSymbols[edge.SourceKey]; ok {
		relationship.SourcePackage, relationship.SourceFile, relationship.SourceName, relationship.SourceKind = symbol.PackageKey, symbol.Position.Path, symbol.Name, symbol.Kind
	}
	if symbol, ok := v.analysisSymbols[edge.TargetKey]; ok {
		relationship.TargetPackage, relationship.TargetFile, relationship.TargetName, relationship.TargetKind = symbol.PackageKey, symbol.Position.Path, symbol.Name, symbol.Kind
	}
	if node, ok := v.analysisNodes[edge.SourceKey]; ok && relationship.SourceKind == "" {
		relationship.SourcePackage, relationship.SourceFile, relationship.SourceKind = node.PackageKey, node.FileKey, node.Kind
	}
	if node, ok := v.analysisNodes[edge.TargetKey]; ok && relationship.TargetKind == "" {
		relationship.TargetPackage, relationship.TargetFile, relationship.TargetKind = node.PackageKey, node.FileKey, node.Kind
	}
	if evidence, ok := v.analysisEvidence[edgeIdentity(edge.SourceKey, edge.TargetKey, edge.Kind)]; ok {
		relationship.Position = graph.Position{Path: relationship.SourceFile, StartLine: evidence.StartLine, StartColumn: evidence.StartColumn, EndLine: evidence.EndLine, EndColumn: evidence.EndColumn}
		relationship.Details = evidence.Details
	}
	if relationship.Position.Path == "" {
		relationship.Position.Path = relationship.SourceFile
	}
	return relationship
}

func edgeIdentity(source, target string, kind graph.EdgeKind) string {
	return source + "\x00" + target + "\x00" + string(kind)
}

func markHistorical(result *graph.SymbolResult) {
	for index := range result.References {
		result.References[index].Historical = true
	}
	for index := range result.Calls {
		result.Calls[index].Historical = true
	}
	for index := range result.CalledBy {
		result.CalledBy[index].Historical = true
	}
	for index := range result.Implementations {
		result.Implementations[index].Historical = true
	}
	for index := range result.ImplementedBy {
		result.ImplementedBy[index].Historical = true
	}
	for index := range result.Tests {
		result.Tests[index].Historical = true
	}
}

func sanitizeResults(values []graph.SymbolResult, masked map[string]struct{}, affected map[string]struct{}) []graph.SymbolResult {
	result := make([]graph.SymbolResult, 0, len(values))
	for _, value := range values {
		if baseSymbolMasked(value.Symbol, masked, affected) {
			continue
		}
		result = append(result, sanitizeResult(value, masked, affected))
	}
	return result
}

func sanitizeResult(value graph.SymbolResult, masked map[string]struct{}, affected map[string]struct{}) graph.SymbolResult {
	value.References = filterReferences(value.References, masked, affected)
	value.Calls = filterRelationships(value.Calls, masked, affected)
	value.CalledBy = filterRelationships(value.CalledBy, masked, affected)
	value.Implementations = filterRelationships(value.Implementations, masked, affected)
	value.ImplementedBy = filterRelationships(value.ImplementedBy, masked, affected)
	value.Tests = filterRelationships(value.Tests, masked, affected)
	return value
}

func filterReferences(values []graph.SymbolReference, masked map[string]struct{}, affected map[string]struct{}) []graph.SymbolReference {
	result := values[:0]
	for _, value := range values {
		if _, ok := masked[value.TargetKey]; ok || pathSetContains(affected, value.SourceFile) {
			continue
		}
		result = append(result, value)
	}
	return result
}

func filterRelationships(values []graph.SymbolRelationship, masked map[string]struct{}, affected map[string]struct{}) []graph.SymbolRelationship {
	result := values[:0]
	for _, value := range values {
		if _, ok := masked[value.SourceKey]; ok || value.SourceFile != "" && pathSetContains(affected, value.SourceFile) {
			continue
		}
		if _, ok := masked[value.TargetKey]; ok || value.TargetFile != "" && pathSetContains(affected, value.TargetFile) {
			continue
		}
		result = append(result, value)
	}
	return result
}

func limitResult(result graph.SymbolResult, limit int) graph.SymbolResult {
	if limit <= 0 {
		return result
	}
	result.References = limitReferences(result.References, limit)
	result.Calls = limitRelationships(result.Calls, limit)
	result.CalledBy = limitRelationships(result.CalledBy, limit)
	result.Implementations = limitRelationships(result.Implementations, limit)
	result.ImplementedBy = limitRelationships(result.ImplementedBy, limit)
	result.Tests = limitRelationships(result.Tests, limit)
	return result
}

func limitReferences(values []graph.SymbolReference, limit int) []graph.SymbolReference {
	if len(values) > limit {
		values = values[:limit]
	}
	return append([]graph.SymbolReference(nil), values...)
}

func limitRelationships(values []graph.SymbolRelationship, limit int) []graph.SymbolRelationship {
	if len(values) > limit {
		values = values[:limit]
	}
	return append([]graph.SymbolRelationship(nil), values...)
}

func sortSymbolRelationships(result *graph.SymbolResult) {
	for _, values := range []*[]graph.SymbolRelationship{&result.Calls, &result.CalledBy, &result.Implementations, &result.ImplementedBy, &result.Tests} {
		sort.SliceStable(*values, func(i, j int) bool {
			left, right := (*values)[i], (*values)[j]
			if left.Kind != right.Kind {
				return left.Kind < right.Kind
			}
			if left.SourceKey != right.SourceKey {
				return left.SourceKey < right.SourceKey
			}
			return left.TargetKey < right.TargetKey
		})
	}
	sort.SliceStable(result.References, func(i, j int) bool {
		if result.References[i].SourceKey != result.References[j].SourceKey {
			return result.References[i].SourceKey < result.References[j].SourceKey
		}
		return result.References[i].TargetKey < result.References[j].TargetKey
	})
}
