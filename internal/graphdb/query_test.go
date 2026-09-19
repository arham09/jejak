package graphdb

import (
	"context"
	"testing"

	"github.com/arham09/jejak/internal/graph"
)

func TestWriteAndReadGenerationPinnedGraph(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	generation, err := store.CreateGeneration(ctx, graph.Generation{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: graph.CommitSHA(target.Worktree.Head), BuildFingerprint: "fingerprint", AnalyzerVersion: "test-analyzer"})
	if err != nil {
		t.Fatal(err)
	}
	result := graph.AnalysisResult{
		BuildFingerprint: "fingerprint",
		AnalyzerVersion:  "test-analyzer",
		Packages:         []graph.Package{{Key: "go:package:example.com/fixture", ImportPath: "example.com/fixture", ModulePath: "example.com/fixture", Directory: ".", Variant: "production"}},
		Files:            []graph.File{{Key: "file:main.go", Path: "main.go", BlobSHA: "blob", PackageKey: "go:package:example.com/fixture"}},
		Symbols: []graph.Symbol{
			{Key: "symbol:answer", Kind: graph.NodeFunction, PackageKey: "go:package:example.com/fixture", FileKey: "file:main.go", Name: "Answer", Position: graph.Position{Path: "main.go", StartLine: 3, EndLine: 3}},
			{Key: "symbol:caller", Kind: graph.NodeFunction, PackageKey: "go:package:example.com/fixture", FileKey: "file:main.go", Name: "Caller", Position: graph.Position{Path: "main.go", StartLine: 5, EndLine: 5}},
		},
		Blobs: []graph.Blob{{SHA: "blob", ObjectFormat: "sha1", ByteSize: 10}},
		Nodes: []graph.Node{
			{Key: "repository:" + string(target.Repository.ID), Kind: graph.NodeRepository, Owned: true},
			{Key: "package:go:package:example.com/fixture", Kind: graph.NodePackage, Owned: true, PackageKey: "go:package:example.com/fixture"},
			{Key: "file:main.go", Kind: graph.NodeFile, Owned: true, PackageKey: "go:package:example.com/fixture", FileKey: "file:main.go", SourceBlob: "blob"},
			{Key: "symbol:answer", Kind: graph.NodeFunction, Owned: true, PackageKey: "go:package:example.com/fixture", FileKey: "file:main.go", SymbolKey: "symbol:answer", SourceBlob: "blob", SourceStart: 3, SourceEnd: 3},
			{Key: "symbol:caller", Kind: graph.NodeFunction, Owned: true, PackageKey: "go:package:example.com/fixture", FileKey: "file:main.go", SymbolKey: "symbol:caller", SourceBlob: "blob", SourceStart: 5, SourceEnd: 5},
		},
		Edges: []graph.Edge{
			{SourceKey: "repository:" + string(target.Repository.ID), TargetKey: "package:go:package:example.com/fixture", Kind: graph.EdgeContains},
			{SourceKey: "package:go:package:example.com/fixture", TargetKey: "file:main.go", Kind: graph.EdgeContains},
			{SourceKey: "file:main.go", TargetKey: "symbol:answer", Kind: graph.EdgeDefines},
			{SourceKey: "symbol:caller", TargetKey: "symbol:answer", Kind: graph.EdgeReferences},
		},
		Evidence: []graph.EdgeEvidence{{SourceKey: "symbol:caller", TargetKey: "symbol:answer", Kind: graph.EdgeReferences, SourceBlob: "blob", StartLine: 5, EndLine: 5}},
	}
	if err := store.WriteAnalysis(ctx, generation, result); err != nil {
		t.Fatalf("WriteAnalysis() error = %v", err)
	}
	if err := store.ValidateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID, nil); err != nil {
		t.Fatal(err)
	}
	view, err := store.OpenView(ctx, target.Repository.ID, target.Worktree.ID, generation.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	results, err := view.FindSymbols(ctx, "Answer")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Symbol.Position.Path != "main.go" {
		t.Fatalf("symbol results = %#v", results)
	}
	if len(results[0].References) != 1 || results[0].References[0].SourceKey != "symbol:caller" {
		t.Fatalf("symbol references = %#v", results[0].References)
	}
	file, err := view.FindFile(ctx, "main.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Symbols) != 2 || file.Symbols[0].Name != "Answer" {
		t.Fatalf("file result = %#v", file)
	}
}

func TestSemanticRelationshipsAreGenerationScoped(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	const packageKey = "go:package:example.com/fixture"
	const fileKey = "file:main.go"
	repoKey := "repository:" + string(target.Repository.ID)
	generation, err := store.CreateGeneration(ctx, graph.Generation{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: graph.CommitSHA(target.Worktree.Head), BuildFingerprint: "semantic-fingerprint", AnalyzerVersion: "semantic-analyzer"})
	if err != nil {
		t.Fatal(err)
	}
	result := graph.AnalysisResult{
		BuildFingerprint: "semantic-fingerprint",
		AnalyzerVersion:  "semantic-analyzer",
		Packages:         []graph.Package{{Key: packageKey, ImportPath: "example.com/fixture", ModulePath: "example.com/fixture", Directory: ".", Variant: "production"}},
		Files:            []graph.File{{Key: fileKey, Path: "main.go", BlobSHA: "semantic-blob", PackageKey: packageKey}},
		Symbols: []graph.Symbol{
			{Key: "symbol:answer", Kind: graph.NodeFunction, PackageKey: packageKey, FileKey: fileKey, Name: "Answer", Position: graph.Position{Path: "main.go", StartLine: 3, EndLine: 3}},
			{Key: "symbol:caller", Kind: graph.NodeFunction, PackageKey: packageKey, FileKey: fileKey, Name: "Caller", Position: graph.Position{Path: "main.go", StartLine: 5, EndLine: 5}},
			{Key: "symbol:runner", Kind: graph.NodeInterface, PackageKey: packageKey, FileKey: fileKey, Name: "Runner", Position: graph.Position{Path: "main.go", StartLine: 7, EndLine: 7}},
			{Key: "symbol:impl", Kind: graph.NodeStruct, PackageKey: packageKey, FileKey: fileKey, Name: "runner", Position: graph.Position{Path: "main.go", StartLine: 9, EndLine: 9}},
			{Key: "symbol:test", Kind: graph.NodeTest, PackageKey: packageKey, FileKey: fileKey, Name: "TestAnswer", Position: graph.Position{Path: "main.go", StartLine: 11, EndLine: 11}},
			{Key: "symbol:package-test", Kind: graph.NodeTest, PackageKey: packageKey, FileKey: fileKey, Name: "TestPackage", Position: graph.Position{Path: "main.go", StartLine: 13, EndLine: 13}},
		},
		Blobs: []graph.Blob{{SHA: "semantic-blob", ObjectFormat: "sha1", ByteSize: 32}},
		Nodes: []graph.Node{
			{Key: repoKey, Kind: graph.NodeRepository, Owned: true},
			{Key: "package:" + packageKey, Kind: graph.NodePackage, Owned: true, PackageKey: packageKey},
			{Key: fileKey, Kind: graph.NodeFile, Owned: true, PackageKey: packageKey, FileKey: fileKey, SourceBlob: "semantic-blob"},
			{Key: "symbol:answer", Kind: graph.NodeFunction, Owned: true, PackageKey: packageKey, FileKey: fileKey, SymbolKey: "symbol:answer", SourceBlob: "semantic-blob", SourceStart: 3, SourceEnd: 3},
			{Key: "symbol:caller", Kind: graph.NodeFunction, Owned: true, PackageKey: packageKey, FileKey: fileKey, SymbolKey: "symbol:caller", SourceBlob: "semantic-blob", SourceStart: 5, SourceEnd: 5},
			{Key: "symbol:runner", Kind: graph.NodeInterface, Owned: true, PackageKey: packageKey, FileKey: fileKey, SymbolKey: "symbol:runner", SourceBlob: "semantic-blob", SourceStart: 7, SourceEnd: 7},
			{Key: "symbol:impl", Kind: graph.NodeStruct, Owned: true, PackageKey: packageKey, FileKey: fileKey, SymbolKey: "symbol:impl", SourceBlob: "semantic-blob", SourceStart: 9, SourceEnd: 9},
			{Key: "symbol:test", Kind: graph.NodeTest, Owned: true, PackageKey: packageKey, FileKey: fileKey, SymbolKey: "symbol:test", SourceBlob: "semantic-blob", SourceStart: 11, SourceEnd: 11},
			{Key: "symbol:package-test", Kind: graph.NodeTest, Owned: true, PackageKey: packageKey, FileKey: fileKey, SymbolKey: "symbol:package-test", SourceBlob: "semantic-blob", SourceStart: 13, SourceEnd: 13},
		},
		Edges: []graph.Edge{
			{SourceKey: repoKey, TargetKey: "package:" + packageKey, Kind: graph.EdgeContains},
			{SourceKey: "package:" + packageKey, TargetKey: fileKey, Kind: graph.EdgeContains},
			{SourceKey: fileKey, TargetKey: "symbol:answer", Kind: graph.EdgeDefines},
			{SourceKey: fileKey, TargetKey: "symbol:caller", Kind: graph.EdgeDefines},
			{SourceKey: fileKey, TargetKey: "symbol:runner", Kind: graph.EdgeDefines},
			{SourceKey: fileKey, TargetKey: "symbol:impl", Kind: graph.EdgeDefines},
			{SourceKey: fileKey, TargetKey: "symbol:test", Kind: graph.EdgeDefines},
			{SourceKey: fileKey, TargetKey: "symbol:package-test", Kind: graph.EdgeDefines},
			{SourceKey: "symbol:caller", TargetKey: "symbol:answer", Kind: graph.EdgeCalls, Confidence: graph.ConfidenceExact, OwnerPackage: packageKey},
			{SourceKey: "symbol:impl", TargetKey: "symbol:runner", Kind: graph.EdgeImplements, Confidence: graph.ConfidenceExact, OwnerPackage: packageKey},
			{SourceKey: "symbol:test", TargetKey: "symbol:answer", Kind: graph.EdgeTests, Confidence: graph.ConfidenceExact, OwnerPackage: packageKey},
			{SourceKey: "symbol:package-test", TargetKey: "package:" + packageKey, Kind: graph.EdgeTests, Confidence: graph.ConfidenceInferred, OwnerPackage: packageKey},
		},
		Evidence: []graph.EdgeEvidence{
			{SourceKey: "symbol:caller", TargetKey: "symbol:answer", Kind: graph.EdgeCalls, AnalyzerSource: "semantic-analyzer", SourceBlob: "semantic-blob", StartLine: 5, EndLine: 5, Details: "direct call"},
			{SourceKey: "symbol:impl", TargetKey: "symbol:runner", Kind: graph.EdgeImplements, AnalyzerSource: "semantic-analyzer", SourceBlob: "semantic-blob", StartLine: 9, EndLine: 9, Details: "value method set"},
			{SourceKey: "symbol:test", TargetKey: "symbol:answer", Kind: graph.EdgeTests, AnalyzerSource: "semantic-analyzer", SourceBlob: "semantic-blob", StartLine: 11, EndLine: 11, Details: "direct call"},
			{SourceKey: "symbol:package-test", TargetKey: "package:" + packageKey, Kind: graph.EdgeTests, AnalyzerSource: "semantic-analyzer", SourceBlob: "semantic-blob", StartLine: 13, EndLine: 13, Details: "package fallback"},
		},
		TestRelationships: []graph.TestRelationship{
			{TestKey: "symbol:test", TargetKey: "symbol:answer", Confidence: graph.ConfidenceExact},
			{TestKey: "symbol:package-test", TargetKey: "package:" + packageKey, Confidence: graph.ConfidenceInferred},
		},
	}
	if err := store.WriteAnalysis(ctx, generation, result); err != nil {
		t.Fatalf("WriteAnalysis() error = %v", err)
	}
	if err := store.ValidateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID, nil); err != nil {
		t.Fatal(err)
	}
	view, err := store.OpenView(ctx, target.Repository.ID, target.Worktree.ID, generation.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()

	caller, err := view.FindSymbols(ctx, "Caller")
	if err != nil {
		t.Fatal(err)
	}
	if len(caller) != 1 || len(caller[0].Calls) != 1 || caller[0].Calls[0].TargetName != "Answer" || caller[0].Calls[0].Confidence != graph.ConfidenceExact {
		t.Fatalf("caller semantic relationships = %#v", caller)
	}
	answer, err := view.FindSymbols(ctx, "Answer")
	if err != nil {
		t.Fatal(err)
	}
	if len(answer) != 1 || len(answer[0].CalledBy) != 1 || answer[0].CalledBy[0].SourceName != "Caller" || len(answer[0].Tests) != 1 || answer[0].Tests[0].SourceName != "TestAnswer" {
		t.Fatalf("answer inverse relationships = %#v", answer)
	}
	runner, err := view.FindSymbols(ctx, "Runner")
	if err != nil {
		t.Fatal(err)
	}
	if len(runner) != 1 || len(runner[0].ImplementedBy) != 1 || runner[0].ImplementedBy[0].SourceName != "runner" {
		t.Fatalf("interface inverse relationships = %#v", runner)
	}
	packageTest, err := view.FindSymbols(ctx, "TestPackage")
	if err != nil {
		t.Fatal(err)
	}
	if len(packageTest) != 1 || len(packageTest[0].Tests) != 1 || packageTest[0].Tests[0].TargetName != "example.com/fixture" || packageTest[0].Tests[0].TargetKind != graph.NodePackage {
		t.Fatalf("package fallback relationship = %#v", packageTest)
	}
	file, err := view.FindFile(ctx, fileKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.SymbolResults) != len(file.Symbols) || len(file.SymbolResults[1].Calls) == 0 {
		t.Fatalf("file semantic relationships = %#v", file)
	}
}
