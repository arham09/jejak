package graph

import "testing"

func TestAnalysisResultsEqualAfterNormalization(t *testing.T) {
	left := AnalysisResult{AnalyzerVersion: "v1", BuildFingerprint: "f", Packages: []Package{{Key: "b"}, {Key: "a"}}}
	right := AnalysisResult{AnalyzerVersion: "v1", BuildFingerprint: "f", Packages: []Package{{Key: "a"}, {Key: "b"}}, Files: []File{}, Symbols: []Symbol{}, Nodes: []Node{}, Edges: []Edge{}, Evidence: []EdgeEvidence{}, Blobs: []Blob{}, PackageDependencies: []PackageDependency{}, TestRelationships: []TestRelationship{}, Diagnostics: []Diagnostic{}}
	if !EqualAnalysis(left, right) {
		t.Fatalf("normalized results differ: %#v vs %#v", left.Normalize(), right.Normalize())
	}
	right.BuildFingerprint = "different"
	if EqualAnalysis(left, right) {
		t.Fatal("different build fingerprints compared equal")
	}
}
