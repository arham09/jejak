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

// boundedTestReader records which base lookup the overlay chose. The unbounded
// calls return a different, larger payload so a test can tell them apart.
type boundedTestReader struct {
	gen            graph.Generation
	symbol         graph.Symbol
	file           graph.File
	boundedSymbol  int
	unboundedSym   int
	boundedFile    int
	unboundedFile  int
	boundedRelated []graph.SymbolRelationship
	fullRelated    []graph.SymbolRelationship
}

func (r *boundedTestReader) Generation() graph.Generation { return r.gen }

func (r *boundedTestReader) SearchSymbols(context.Context, []string, int) ([]graph.SymbolResult, error) {
	return nil, nil
}

func (r *boundedTestReader) FindSymbols(_ context.Context, query string) ([]graph.SymbolResult, error) {
	r.unboundedSym++
	if query != r.symbol.Key {
		return nil, fmt.Errorf("symbol %q not found", query)
	}
	return []graph.SymbolResult{{Symbol: r.symbol, Calls: append([]graph.SymbolRelationship(nil), r.fullRelated...)}}, nil
}

func (r *boundedTestReader) FindSymbolForImpact(_ context.Context, key string, _ int) (graph.SymbolResult, error) {
	r.boundedSymbol++
	if key != r.symbol.Key {
		return graph.SymbolResult{}, fmt.Errorf("symbol %q not found", key)
	}
	return graph.SymbolResult{Symbol: r.symbol, Calls: append([]graph.SymbolRelationship(nil), r.boundedRelated...)}, nil
}

func (r *boundedTestReader) FindFile(_ context.Context, query string) (graph.FileResult, error) {
	r.unboundedFile++
	if query != r.file.Path && query != r.file.Key {
		return graph.FileResult{}, fmt.Errorf("file %q not found", query)
	}
	return graph.FileResult{File: r.file, SymbolResults: []graph.SymbolResult{{Symbol: r.symbol, Calls: append([]graph.SymbolRelationship(nil), r.fullRelated...)}}}, nil
}

func (r *boundedTestReader) FindFileMetadata(_ context.Context, query string) (graph.File, error) {
	r.boundedFile++
	if query != r.file.Path && query != r.file.Key {
		return graph.File{}, fmt.Errorf("file %q not found", query)
	}
	return r.file, nil
}

func newBoundedTestReader() *boundedTestReader {
	packageKey := "go:package:fixture"
	file := graph.File{Key: "file:base.go", Path: "base.go", BlobSHA: "base-blob", PackageKey: packageKey}
	symbol := graph.Symbol{Key: "symbol:Base", Kind: graph.NodeFunction, PackageKey: packageKey, FileKey: file.Key, Name: "Base", Position: graph.Position{Path: file.Path, StartLine: 1, EndLine: 1}}
	return &boundedTestReader{
		gen:            graph.Generation{RepoID: "repo", WorktreeID: "worktree", ID: 1, Commit: "commit", BuildFingerprint: "base"},
		symbol:         symbol,
		file:           file,
		boundedRelated: []graph.SymbolRelationship{{SourceKey: symbol.Key, TargetKey: "symbol:Bounded", Kind: graph.EdgeCalls, TargetName: "Bounded"}},
		fullRelated: []graph.SymbolRelationship{
			{SourceKey: symbol.Key, TargetKey: "symbol:Duplicate", Kind: graph.EdgeCalls, TargetName: "Duplicate"},
			{SourceKey: symbol.Key, TargetKey: "symbol:Duplicate", Kind: graph.EdgeCalls, TargetName: "Duplicate"},
			{SourceKey: symbol.Key, TargetKey: "symbol:Bounded", Kind: graph.EdgeCalls, TargetName: "Bounded"},
		},
	}
}

func newBoundedTestView(t *testing.T, base *boundedTestReader) *View {
	t.Helper()
	view, err := newView(base, &Snapshot{Root: t.TempDir()}, Manifest{ID: "manifest"}, graph.AnalysisResult{BuildFingerprint: "effective"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = view.Close() })
	return view
}

// Impact traversal reads many unchanged base symbols. Loading each one's full
// relationship fanout and trimming afterwards spends the bound on duplicate
// evidence, which both costs time and hides real neighbours, so the overlay
// must delegate to the base's bounded lookup.
func TestOverlayImpactLookupUsesTheBaseBoundedQuery(t *testing.T) {
	base := newBoundedTestReader()
	view := newBoundedTestView(t, base)

	result, err := view.FindSymbolForImpact(context.Background(), base.symbol.Key, 8)
	if err != nil {
		t.Fatal(err)
	}
	if base.boundedSymbol != 1 {
		t.Fatalf("bounded base lookups = %d, want 1", base.boundedSymbol)
	}
	if base.unboundedSym != 0 {
		t.Fatalf("unbounded base lookups = %d, want 0", base.unboundedSym)
	}
	if len(result.Calls) != 1 || result.Calls[0].TargetKey != "symbol:Bounded" {
		t.Fatalf("relationships = %#v, want the bounded set", result.Calls)
	}
}

// File provenance must not drag in every declaration the file contains.
func TestOverlayFileMetadataUsesTheBaseMetadataQuery(t *testing.T) {
	base := newBoundedTestReader()
	view := newBoundedTestView(t, base)

	file, err := view.FindFileMetadata(context.Background(), base.file.Path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Key != base.file.Key || file.BlobSHA != base.file.BlobSHA {
		t.Fatalf("file = %#v, want %#v", file, base.file)
	}
	if base.boundedFile != 1 {
		t.Fatalf("metadata lookups = %d, want 1", base.boundedFile)
	}
	if base.unboundedFile != 0 {
		t.Fatalf("full file lookups = %d, want 0", base.unboundedFile)
	}
}

// A reader without the bounded lookups must still work.
func TestOverlayFallsBackWhenTheBaseHasNoBoundedLookups(t *testing.T) {
	gen := graph.Generation{RepoID: "repo", WorktreeID: "worktree", ID: 1, Commit: "commit"}
	symbol := graph.Symbol{Key: "symbol:Base", Kind: graph.NodeFunction, PackageKey: "go:package:fixture", FileKey: "file:base.go", Name: "Base", Position: graph.Position{Path: "base.go", StartLine: 1, EndLine: 1}}
	base := &viewTestReader{
		gen:     gen,
		symbols: map[string][]graph.SymbolResult{symbol.Key: {{Symbol: symbol}}},
		files:   map[string]graph.FileResult{"base.go": {File: graph.File{Key: "file:base.go", Path: "base.go", BlobSHA: "base-blob", PackageKey: "go:package:fixture"}}},
	}
	view, err := newView(base, &Snapshot{Root: t.TempDir()}, Manifest{ID: "manifest"}, graph.AnalysisResult{BuildFingerprint: "effective"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = view.Close() }()

	if _, err := view.FindSymbolForImpact(context.Background(), symbol.Key, 8); err != nil {
		t.Fatalf("symbol fallback failed: %v", err)
	}
	file, err := view.FindFileMetadata(context.Background(), "base.go")
	if err != nil {
		t.Fatalf("file fallback failed: %v", err)
	}
	if file.BlobSHA != "base-blob" {
		t.Fatalf("file = %#v", file)
	}
}
