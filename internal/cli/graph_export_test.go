package cli

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
)

func TestSymbolGraphExportIsDeterministicAndDeduplicatesEvidence(t *testing.T) {
	target := graph.Symbol{
		Key:        "symbol:target",
		Kind:       graph.NodeFunction,
		PackageKey: "go:package:example.com/fixture",
		FileKey:    "file:target.go",
		Name:       "Target",
		Signature:  "func() int",
		Position:   graph.Position{Path: "target.go", StartLine: 3, EndLine: 3},
	}
	result := graph.SymbolResult{
		Symbol: target,
		References: []graph.SymbolReference{{
			SourceKey:     "symbol:caller",
			TargetKey:     target.Key,
			SourcePackage: target.PackageKey,
			SourceFile:    "caller.go",
			Kind:          graph.EdgeReferences,
			Confidence:    graph.ConfidenceExact,
			Position:      graph.Position{Path: "caller.go", StartLine: 8, EndLine: 8},
		}},
		CalledBy: []graph.SymbolRelationship{
			{
				SourceKey:  "symbol:caller",
				TargetKey:  target.Key,
				SourceName: "Caller",
				TargetName: "Target",
				SourceKind: graph.NodeFunction,
				TargetKind: graph.NodeFunction,
				SourceFile: "caller.go",
				TargetFile: "target.go",
				Kind:       graph.EdgeCalls,
				Confidence: graph.ConfidenceExact,
				Position:   graph.Position{Path: "caller.go", StartLine: 10, EndLine: 10},
				Details:    "first evidence",
			},
			{
				SourceKey:  "symbol:caller",
				TargetKey:  target.Key,
				SourceName: "Caller",
				TargetName: "Target",
				SourceKind: graph.NodeFunction,
				TargetKind: graph.NodeFunction,
				SourceFile: "caller.go",
				TargetFile: "target.go",
				Kind:       graph.EdgeCalls,
				Confidence: graph.ConfidenceExact,
				Position:   graph.Position{Path: "caller.go", StartLine: 12, EndLine: 12},
				Details:    "second evidence",
			},
		},
		Calls: []graph.SymbolRelationship{{
			SourceKey:  target.Key,
			TargetKey:  "symbol:maybe",
			TargetName: "Maybe",
			TargetKind: graph.NodeFunction,
			TargetFile: "maybe.go",
			Kind:       graph.EdgePossibleCall,
			Confidence: graph.ConfidencePossible,
			Position:   graph.Position{Path: "target.go", StartLine: 5, EndLine: 5},
		}},
	}

	targetInfo := graphExportTestTarget()
	source := graphSource{mode: "committed"}
	generation := graph.Generation{
		RepoID:           targetInfo.Repository.ID,
		WorktreeID:       targetInfo.Worktree.ID,
		ID:               7,
		Commit:           "abc123",
		BuildFingerprint: "fingerprint",
		AnalyzerVersion:  "analyzer-v1",
		SchemaVersion:    1,
	}
	first := newSymbolGraphExport(targetInfo, generation, source, "Target", []graph.SymbolResult{result})
	reversed := result
	reversed.CalledBy = append([]graph.SymbolRelationship(nil), result.CalledBy...)
	for left, right := 0, len(reversed.CalledBy)-1; left < right; left, right = left+1, right-1 {
		reversed.CalledBy[left], reversed.CalledBy[right] = reversed.CalledBy[right], reversed.CalledBy[left]
	}
	second := newSymbolGraphExport(targetInfo, generation, source, "Target", []graph.SymbolResult{reversed})

	firstJSON := graphExportJSON(t, first)
	secondJSON := graphExportJSON(t, second)
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("export is not deterministic:\nfirst=%s\nsecond=%s", firstJSON, secondJSON)
	}

	var decoded graphExport
	if err := json.Unmarshal(firstJSON, &decoded); err != nil {
		t.Fatalf("decode graph export: %v", err)
	}
	if decoded.SchemaVersion != graphExportSchemaVersion || decoded.Query.Kind != "symbol" || decoded.Query.Value != "Target" {
		t.Fatalf("export identity = %#v", decoded)
	}
	if decoded.Repository.ID != string(targetInfo.Repository.ID) || decoded.Worktree.ID != string(targetInfo.Worktree.ID) || decoded.Generation.ID != 7 || decoded.Source.Mode != "committed" {
		t.Fatalf("export metadata = %#v", decoded)
	}
	if !sortExportNodes(decoded.Nodes) {
		t.Fatalf("nodes are not sorted: %#v", decoded.Nodes)
	}

	var calls, possible graphExportEdge
	for _, edge := range decoded.Edges {
		switch edge.Kind {
		case string(graph.EdgeCalls):
			calls = edge
		case string(graph.EdgePossibleCall):
			possible = edge
		}
	}
	if calls.Source != "symbol:caller" || calls.Target != "symbol:target" || len(calls.Locations) != 2 || len(calls.Details) != 2 {
		t.Fatalf("deduplicated call edge = %#v", calls)
	}
	if possible.Confidence != string(graph.ConfidencePossible) || possible.Source != target.Key || possible.Target != "symbol:maybe" {
		t.Fatalf("possible edge = %#v", possible)
	}
}

func TestFileGraphExportIncludesSymbolsAndImports(t *testing.T) {
	target := graphExportTestTarget()
	file := graph.FileResult{
		File: graph.File{Key: "file:main.go", Path: "main.go", BlobSHA: "blob", PackageKey: "go:package:example.com/fixture"},
		Symbols: []graph.Symbol{
			{Key: "symbol:Zed", Kind: graph.NodeFunction, Name: "Zed", Position: graph.Position{Path: "main.go", StartLine: 10, EndLine: 10}},
			{Key: "symbol:Answer", Kind: graph.NodeFunction, Name: "Answer", Position: graph.Position{Path: "main.go", StartLine: 3, EndLine: 3}},
		},
		SymbolResults: []graph.SymbolResult{{
			Symbol: graph.Symbol{Key: "symbol:Answer", Kind: graph.NodeFunction, Name: "Answer", Position: graph.Position{Path: "main.go", StartLine: 3, EndLine: 3}},
			Calls: []graph.SymbolRelationship{{
				SourceKey: "symbol:Answer", TargetKey: "symbol:Zed", Kind: graph.EdgeCalls, Confidence: graph.ConfidenceExact,
				SourceName: "Answer", TargetName: "Zed", SourceKind: graph.NodeFunction, TargetKind: graph.NodeFunction,
				Position: graph.Position{Path: "main.go", StartLine: 4, EndLine: 4},
			}},
		}},
		Imports: []string{"z.example/imported", "a.example/imported", "a.example/imported"},
	}
	export := newFileGraphExport(target, graph.Generation{ID: 2, Commit: "commit"}, graphSource{mode: "working-tree", overlayID: "overlay", manifest: "manifest"}, "main.go", file)

	if export.Query.Kind != "file" || export.Query.Value != "main.go" || export.Source.Mode != "working-tree" {
		t.Fatalf("file export identity = %#v", export)
	}
	if findExportNode(export.Nodes, "file:main.go").Kind != string(graph.NodeFile) {
		t.Fatalf("file node missing: %#v", export.Nodes)
	}
	if countExportEdges(export.Edges, string(graph.EdgeContains)) != 2 {
		t.Fatalf("contains edges = %#v", export.Edges)
	}
	if countExportEdges(export.Edges, string(graph.EdgeImports)) != 2 {
		t.Fatalf("import edges = %#v", export.Edges)
	}
	if countExportEdges(export.Edges, string(graph.EdgeCalls)) != 1 {
		t.Fatalf("call edges = %#v", export.Edges)
	}
	if findExportNode(export.Nodes, "import:a.example/imported").Name != "a.example/imported" {
		t.Fatalf("import node missing: %#v", export.Nodes)
	}
	for _, edge := range export.Edges {
		if edge.Locations == nil || edge.Details == nil {
			t.Fatalf("edge collections must be non-nil: %#v", edge)
		}
	}
	encoded := string(graphExportJSON(t, export))
	if strings.Contains(encoded, `"locations": null`) || strings.Contains(encoded, `"details": null`) {
		t.Fatalf("empty edge collections were encoded as null: %s", encoded)
	}
}

func TestEmptyGraphExportUsesEmptyCollections(t *testing.T) {
	export := newSymbolGraphExport(graphExportTestTarget(), graph.Generation{ID: 1}, graphSource{mode: "committed"}, "missing", nil)
	encoded := string(graphExportJSON(t, export))
	if !strings.Contains(encoded, `"nodes": []`) || !strings.Contains(encoded, `"edges": []`) {
		t.Fatalf("empty collections were not encoded as arrays: %s", encoded)
	}
}

func graphExportJSON(t *testing.T, export graphExport) []byte {
	t.Helper()
	var output bytes.Buffer
	if err := writeGraphJSON(&output, export); err != nil {
		t.Fatalf("write graph JSON: %v", err)
	}
	return output.Bytes()
}

func graphExportTestTarget() repository.Target {
	return repository.Target{
		Repository: repository.Descriptor{ID: "repo-id", CanonicalIdentity: "https://example.com/repo.git"},
		Worktree:   repository.Worktree{ID: "worktree-id", Path: "/tmp/repo", Branch: "main"},
	}
}

func findExportNode(nodes []graphExportNode, id string) graphExportNode {
	for _, node := range nodes {
		if node.ID == id {
			return node
		}
	}
	return graphExportNode{}
}

func countExportEdges(edges []graphExportEdge, kind string) int {
	count := 0
	for _, edge := range edges {
		if edge.Kind == kind {
			count++
		}
	}
	return count
}

func sortExportNodes(nodes []graphExportNode) bool {
	return sort.SliceIsSorted(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
}
