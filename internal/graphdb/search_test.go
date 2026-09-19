package graphdb

import (
	"context"
	"testing"

	"github.com/arham09/jejak/internal/graph"
)

func TestSearchSymbolsMatchesIndexedFieldsAndScopesGeneration(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateGeneration(ctx, graph.Generation{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: graph.CommitSHA(target.Worktree.Head), BuildFingerprint: "a", AnalyzerVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteAnalysis(ctx, first, searchAnalysis("symbol:answer-a", "Answer", "main/answer.go", "func Answer() int")); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateGeneration(ctx, target.Repository.ID, target.Worktree.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateGeneration(ctx, target.Repository.ID, target.Worktree.ID, first.ID, nil); err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateGeneration(ctx, graph.Generation{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: "search-b", BuildFingerprint: "a", AnalyzerVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteAnalysis(ctx, second, searchAnalysis("symbol:other-b", "Other", "other/receiver.go", "func (Receiver) Other()")); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateGeneration(ctx, target.Repository.ID, target.Worktree.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	view, err := store.OpenView(ctx, target.Repository.ID, target.Worktree.ID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	for _, terms := range [][]string{{"answer"}, {"receiver"}, {"main/answer.go"}, {"func", "answer"}} {
		results, searchErr := view.SearchSymbols(ctx, terms, 10)
		if searchErr != nil {
			t.Fatalf("SearchSymbols(%v): %v", terms, searchErr)
		}
		if len(results) != 1 || results[0].Symbol.Key != "symbol:answer-a" {
			t.Fatalf("SearchSymbols(%v) = %#v", terms, results)
		}
	}
	results, err := view.SearchSymbols(ctx, []string{"other"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("generation-scoped search returned inactive records: %#v", results)
	}
}

func searchAnalysis(symbolKey, name, path, signature string) graph.AnalysisResult {
	packageKey := "go:package:example.com/search"
	fileKey := "file:" + path
	return graph.AnalysisResult{
		AnalyzerVersion:  "test",
		BuildFingerprint: "a",
		Packages:         []graph.Package{{Key: packageKey, ImportPath: "example.com/search", ModulePath: "example.com/search", Directory: "."}},
		Files:            []graph.File{{Key: fileKey, Path: path, BlobSHA: symbolKey + "-blob", PackageKey: packageKey}},
		Symbols:          []graph.Symbol{{Key: symbolKey, Kind: graph.NodeFunction, PackageKey: packageKey, FileKey: fileKey, Name: name, Signature: signature, Receiver: "Receiver", Position: graph.Position{Path: path, StartLine: 4, EndLine: 8}}},
		Nodes:            []graph.Node{{Key: "package:" + packageKey, Kind: graph.NodePackage, Owned: true, PackageKey: packageKey}, {Key: fileKey, Kind: graph.NodeFile, Owned: true, PackageKey: packageKey, FileKey: fileKey, SourceBlob: symbolKey + "-blob"}, {Key: symbolKey, Kind: graph.NodeFunction, Owned: true, PackageKey: packageKey, FileKey: fileKey, SymbolKey: symbolKey, SourceBlob: symbolKey + "-blob", SourceStart: 4, SourceEnd: 8}},
		Edges:            []graph.Edge{{SourceKey: "package:" + packageKey, TargetKey: fileKey, Kind: graph.EdgeContains}, {SourceKey: fileKey, TargetKey: symbolKey, Kind: graph.EdgeDefines}},
		Blobs:            []graph.Blob{{SHA: symbolKey + "-blob", ObjectFormat: "sha1", ByteSize: 10}},
	}
}
