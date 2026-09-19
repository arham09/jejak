package graph

import "reflect"

// EqualAnalysis compares source-derived graph contents after deterministic
// normalization. Generation IDs, database row IDs, and timestamps are not part
// of AnalysisResult and therefore cannot make equivalent builds differ.
func EqualAnalysis(left, right AnalysisResult) bool {
	return reflect.DeepEqual(normalizeForComparison(left), normalizeForComparison(right))
}

func normalizeForComparison(result AnalysisResult) AnalysisResult {
	result = result.Normalize()
	// Treat nil and empty collections as the same semantic result. This avoids
	// making a comparison depend on whether an analyzer had zero records or
	// explicitly allocated an empty buffer.
	if result.Packages == nil {
		result.Packages = []Package{}
	}
	if result.Files == nil {
		result.Files = []File{}
	}
	if result.Symbols == nil {
		result.Symbols = []Symbol{}
	}
	if result.Nodes == nil {
		result.Nodes = []Node{}
	}
	if result.Edges == nil {
		result.Edges = []Edge{}
	}
	if result.Evidence == nil {
		result.Evidence = []EdgeEvidence{}
	}
	if result.Blobs == nil {
		result.Blobs = []Blob{}
	}
	if result.PackageDependencies == nil {
		result.PackageDependencies = []PackageDependency{}
	}
	if result.TestRelationships == nil {
		result.TestRelationships = []TestRelationship{}
	}
	if result.Diagnostics == nil {
		result.Diagnostics = []Diagnostic{}
	}
	return result
}
