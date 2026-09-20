package overlay

import (
	"context"
	"fmt"
	"testing"

	"github.com/arham09/jejak/internal/graph"
)

type viewTestReader struct {
	gen     graph.Generation
	symbols map[string][]graph.SymbolResult
	files   map[string]graph.FileResult
}

func (r *viewTestReader) Generation() graph.Generation { return r.gen }

func (r *viewTestReader) SearchSymbols(_ context.Context, _ []string, _ int) ([]graph.SymbolResult, error) {
	return nil, nil
}

func (r *viewTestReader) FindSymbols(_ context.Context, query string) ([]graph.SymbolResult, error) {
	return append([]graph.SymbolResult(nil), r.symbols[query]...), nil
}

func (r *viewTestReader) FindFile(_ context.Context, query string) (graph.FileResult, error) {
	result, ok := r.files[query]
	if !ok {
		return graph.FileResult{}, fmt.Errorf("file %q not found", query)
	}
	return result, nil
}

func TestViewMasksChangedBaseRelationshipsAndKeepsDeletedHistory(t *testing.T) {
	ctx := context.Background()
	gen := graph.Generation{RepoID: "repo", WorktreeID: "worktree", ID: 1, Commit: "commit", BuildFingerprint: "base"}
	baseSymbol := graph.Symbol{Key: "symbol:old", Kind: graph.NodeFunction, PackageKey: "go:package:fixture", FileKey: "file:changed.go", Name: "Old", Position: graph.Position{Path: "changed.go", StartLine: 3, EndLine: 3}}
	currentSymbol := graph.Symbol{Key: "symbol:new", Kind: graph.NodeFunction, PackageKey: "go:package:fixture", FileKey: "file:changed.go", Name: "New", Position: graph.Position{Path: "changed.go", StartLine: 3, EndLine: 3}}
	staleTarget := graph.Symbol{Key: "symbol:stale", Kind: graph.NodeFunction, PackageKey: "go:package:fixture", FileKey: "file:changed.go", Name: "Stale", Position: graph.Position{Path: "changed.go", StartLine: 5, EndLine: 5}}
	caller := graph.Symbol{Key: "symbol:caller", Kind: graph.NodeFunction, PackageKey: "go:package:fixture", FileKey: "file:caller.go", Name: "Caller", Position: graph.Position{Path: "caller.go", StartLine: 3, EndLine: 3}}
	base := &viewTestReader{
		gen: gen,
		symbols: map[string][]graph.SymbolResult{
			baseSymbol.Key:  {{Symbol: baseSymbol, Calls: []graph.SymbolRelationship{{SourceKey: baseSymbol.Key, TargetKey: staleTarget.Key, Kind: graph.EdgeCalls, TargetName: staleTarget.Name}}}},
			staleTarget.Key: {{Symbol: staleTarget}},
		},
		files: map[string]graph.FileResult{
			"changed.go": {File: graph.File{Key: "file:changed.go", Path: "changed.go", BlobSHA: "base-blob", PackageKey: "go:package:fixture"}},
			"caller.go":  {File: graph.File{Key: "file:caller.go", Path: "caller.go", BlobSHA: "caller-blob", PackageKey: "go:package:fixture"}, SymbolResults: []graph.SymbolResult{{Symbol: caller, Calls: []graph.SymbolRelationship{{SourceKey: caller.Key, TargetKey: baseSymbol.Key, Kind: graph.EdgeCalls, TargetName: baseSymbol.Name}}}}},
		},
	}
	manifest := Manifest{ID: "manifest", Changes: []graph.FileChange{{Kind: graph.ChangeModified, OldPath: "changed.go", NewPath: "changed.go"}}}
	snapshot := &Snapshot{Root: t.TempDir(), Files: []graph.SnapshotFile{{Path: "changed.go", BlobSHA: "overlay:sha256:new", ObjectFormat: "sha256"}}, blobs: map[string][]byte{"overlay:sha256:new": []byte("package fixture\n\nfunc New() {}\n")}}
	analysis := graph.AnalysisResult{
		BuildFingerprint: "effective",
		Files:            []graph.File{{Key: "file:changed.go", Path: "changed.go", BlobSHA: "overlay:sha256:new", PackageKey: "go:package:fixture"}, {Key: "file:caller.go", Path: "caller.go", BlobSHA: "caller-blob", PackageKey: "go:package:fixture"}},
		Symbols:          []graph.Symbol{currentSymbol, caller},
		Edges:            []graph.Edge{{SourceKey: currentSymbol.Key, TargetKey: staleTarget.Key, Kind: graph.EdgeCalls, Confidence: graph.ConfidenceExact}, {SourceKey: caller.Key, TargetKey: currentSymbol.Key, Kind: graph.EdgeCalls, Confidence: graph.ConfidenceExact}},
	}
	changes := []SymbolChange{{Kind: SymbolModified, Before: baseSymbol, After: currentSymbol}}
	view, err := newView(base, snapshot, manifest, analysis, map[string]graph.FileResult{"changed.go": base.files["changed.go"]}, changes)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = view.Close() }()
	result, err := view.FindSymbols(ctx, currentSymbol.Key)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || len(result[0].Calls) != 1 || result[0].Calls[0].TargetKey != staleTarget.Key {
		t.Fatalf("effective replacement result = %#v", result)
	}
	if _, err := view.FindSymbols(ctx, baseSymbol.Key); err == nil {
		t.Fatal("modified baseline symbol unexpectedly remained queryable")
	}
	if _, err := view.FindSymbols(ctx, staleTarget.Key); err == nil {
		t.Fatal("changed-file stale target unexpectedly remained queryable")
	}
	callerFile, err := view.FindFile(ctx, "caller.go")
	if err != nil || len(callerFile.SymbolResults) != 1 || len(callerFile.SymbolResults[0].Calls) != 1 || callerFile.SymbolResults[0].Calls[0].TargetKey != currentSymbol.Key {
		t.Fatalf("unaffected file did not receive effective edge: %#v err=%v", callerFile, err)
	}
}

func TestViewProvidesHistoricalFileMetadataForDeletedSymbols(t *testing.T) {
	ctx := context.Background()
	gen := graph.Generation{RepoID: "repo", WorktreeID: "worktree", ID: 1, Commit: "commit"}
	deleted := graph.Symbol{Key: "symbol:deleted", Kind: graph.NodeFunction, PackageKey: "go:package:fixture", FileKey: "file:old.go", Name: "Deleted", Position: graph.Position{Path: "old.go", StartLine: 3, EndLine: 3}}
	base := &viewTestReader{gen: gen}
	snapshot := &Snapshot{Root: t.TempDir(), blobs: map[string][]byte{"base-blob": []byte("package fixture\n\nfunc Deleted() {}\n")}}
	view, err := newView(base, snapshot, Manifest{ID: "manifest", Changes: []graph.FileChange{{Kind: graph.ChangeDeleted, OldPath: "old.go"}}}, graph.AnalysisResult{}, map[string]graph.FileResult{
		"old.go": {File: graph.File{Key: "file:old.go", Path: "old.go", BlobSHA: "base-blob", PackageKey: "go:package:fixture"}, SymbolResults: []graph.SymbolResult{{Symbol: deleted}}},
	}, []SymbolChange{{Kind: SymbolDeleted, Before: deleted}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = view.Close() }()
	file, err := view.FindHistoricalFileMetadata(ctx, deleted.FileKey)
	if err != nil {
		t.Fatal(err)
	}
	if file.BlobSHA != "base-blob" || file.Path != "old.go" {
		t.Fatalf("historical file = %#v", file)
	}
	if _, err := view.FindFile(ctx, "old.go"); err == nil {
		t.Fatal("deleted file unexpectedly remained in effective file query")
	}
}

// rankingReader hands the overlay every base match in alphabetical order. It
// ignores the limit on purpose: the store applies its own bound, and this
// fixture isolates the second bound that the overlay applies to the merged
// set, where the weakest matches sort first.
type rankingReader struct {
	gen     graph.Generation
	results []graph.SymbolResult
}

func (r *rankingReader) Generation() graph.Generation { return r.gen }

func (r *rankingReader) SearchSymbols(context.Context, []string, int) ([]graph.SymbolResult, error) {
	return append([]graph.SymbolResult(nil), r.results...), nil
}

func (r *rankingReader) FindSymbols(context.Context, string) ([]graph.SymbolResult, error) {
	return nil, nil
}

func (r *rankingReader) FindFile(_ context.Context, query string) (graph.FileResult, error) {
	return graph.FileResult{}, fmt.Errorf("file %q not found", query)
}

// The overlay bounds its merged result too, so it must rank before truncating.
// Ordering by path alone dropped the symbol that matched every term.
func TestOverlaySearchKeepsBestMatchesWhenTruncating(t *testing.T) {
	ctx := context.Background()
	gen := graph.Generation{RepoID: "repo", WorktreeID: "worktree", ID: 1, Commit: "commit", BuildFingerprint: "base"}
	packageKey := "go:package:fixture"
	results := make([]graph.SymbolResult, 0, 21)
	for index := range 20 {
		path := fmt.Sprintf("a%03d/weak.go", index)
		name := fmt.Sprintf("CreateThing%03d", index)
		results = append(results, graph.SymbolResult{Symbol: graph.Symbol{Key: "symbol:" + name, Kind: graph.NodeFunction, PackageKey: packageKey, FileKey: "file:" + path, Name: name, Position: graph.Position{Path: path, StartLine: 1, EndLine: 1}}})
	}
	strongPath := "z999/draft.go"
	results = append(results, graph.SymbolResult{Symbol: graph.Symbol{Key: "symbol:CreateDraftTransaction", Kind: graph.NodeFunction, PackageKey: packageKey, FileKey: "file:" + strongPath, Name: "CreateDraftTransaction", Position: graph.Position{Path: strongPath, StartLine: 1, EndLine: 1}}})

	base := &rankingReader{gen: gen, results: results}
	snapshot := &Snapshot{Root: t.TempDir()}
	view, err := newView(base, snapshot, Manifest{ID: "manifest"}, graph.AnalysisResult{BuildFingerprint: "effective"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = view.Close() }()

	found, err := view.SearchSymbols(ctx, []string{"create", "draft", "transaction"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 3 {
		t.Fatalf("results = %d, want 3", len(found))
	}
	if found[0].Symbol.Name != "CreateDraftTransaction" {
		names := make([]string, 0, len(found))
		for _, item := range found {
			names = append(names, item.Symbol.Name)
		}
		t.Fatalf("first result = %q, want CreateDraftTransaction; got %v", found[0].Symbol.Name, names)
	}
}
