package graphdb

import (
	"context"
	"fmt"
	"strings"
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

// rankingAnalysis builds many symbols in one generation. Paths are numbered so
// the alphabetical order the store uses as a tiebreak is predictable.
func rankingAnalysis(weak int, strongPath, strongName string) graph.AnalysisResult {
	packageKey := "go:package:example.com/search"
	result := graph.AnalysisResult{
		AnalyzerVersion:  "test",
		BuildFingerprint: "a",
		Packages:         []graph.Package{{Key: packageKey, ImportPath: "example.com/search", ModulePath: "example.com/search", Directory: "."}},
		Nodes:            []graph.Node{{Key: "package:" + packageKey, Kind: graph.NodePackage, Owned: true, PackageKey: packageKey}},
		Blobs:            []graph.Blob{{SHA: "rank-blob", ObjectFormat: "sha1", ByteSize: 10}},
	}
	add := func(path, name string) {
		fileKey := "file:" + path
		result.Files = append(result.Files, graph.File{Key: fileKey, Path: path, BlobSHA: "rank-blob", PackageKey: packageKey})
		result.Symbols = append(result.Symbols, graph.Symbol{Key: "symbol:" + name + ":" + path, Kind: graph.NodeFunction, PackageKey: packageKey, FileKey: fileKey, Name: name, Position: graph.Position{Path: path, StartLine: 1, EndLine: 2}})
		result.Nodes = append(result.Nodes,
			graph.Node{Key: fileKey, Kind: graph.NodeFile, Owned: true, PackageKey: packageKey, FileKey: fileKey, SourceBlob: "rank-blob"},
			graph.Node{Key: "symbol:" + name + ":" + path, Kind: graph.NodeFunction, Owned: true, PackageKey: packageKey, FileKey: fileKey, SymbolKey: "symbol:" + name + ":" + path, SourceBlob: "rank-blob", SourceStart: 1, SourceEnd: 2})
	}
	// Weak matches hit one term only and sort first by path.
	for index := range weak {
		add(fmt.Sprintf("a%03d/weak.go", index), fmt.Sprintf("CreateThing%03d", index))
	}
	// The strong match hits every term but sorts last by path.
	add(strongPath, strongName)
	return result
}

// A bounded search must not let the bound decide relevance. Ordering by path
// and truncating afterwards dropped the only symbol that matched every term.
func TestSearchSymbolsKeepsBestMatchesWhenTruncating(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	generation, err := store.CreateGeneration(ctx, graph.Generation{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: graph.CommitSHA(target.Worktree.Head), BuildFingerprint: "a", AnalyzerVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteAnalysis(ctx, generation, rankingAnalysis(40, "z999/draft.go", "CreateDraftTransaction")); err != nil {
		t.Fatal(err)
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

	// The limit is far below the number of one-term matches, so the symbol
	// that matches all three terms only survives if ranking precedes the cut.
	results, err := view.SearchSymbols(ctx, []string{"create", "draft", "transaction"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Fatalf("results = %d, want 5", len(results))
	}
	if results[0].Symbol.Name != "CreateDraftTransaction" {
		names := make([]string, 0, len(results))
		for _, item := range results {
			names = append(names, item.Symbol.Name)
		}
		t.Fatalf("first result = %q, want CreateDraftTransaction; got %v", results[0].Symbol.Name, names)
	}
}

// Equal relevance must still order deterministically, because reports and
// agent workflows compare runs.
func TestSearchSymbolsOrdersEqualMatchesDeterministically(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	generation, err := store.CreateGeneration(ctx, graph.Generation{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: graph.CommitSHA(target.Worktree.Head), BuildFingerprint: "a", AnalyzerVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteAnalysis(ctx, generation, rankingAnalysis(12, "z999/draft.go", "CreateDraftTransaction")); err != nil {
		t.Fatal(err)
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
	var previous []string
	for range 3 {
		results, searchErr := view.SearchSymbols(ctx, []string{"create"}, 8)
		if searchErr != nil {
			t.Fatal(searchErr)
		}
		order := make([]string, 0, len(results))
		for _, item := range results {
			order = append(order, item.Symbol.Key)
		}
		if previous != nil && strings.Join(order, "|") != strings.Join(previous, "|") {
			t.Fatalf("search order changed between runs: %v then %v", previous, order)
		}
		previous = order
	}
}
