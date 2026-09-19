package impact

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arham09/jejak/internal/graph"
)

type fakeReader struct {
	generation graph.Generation
	symbols    map[string]graph.SymbolResult
	files      map[string]graph.FileResult
}

func (r *fakeReader) Generation() graph.Generation { return r.generation }

func (r *fakeReader) SearchSymbols(_ context.Context, terms []string, limit int) ([]graph.SymbolResult, error) {
	var result []graph.SymbolResult
	for _, symbol := range r.symbols {
		value := strings.ToLower(strings.Join([]string{symbol.Symbol.Key, symbol.Symbol.Name, symbol.Symbol.PackageKey, symbol.Symbol.Position.Path}, " "))
		matched := false
		for _, term := range terms {
			if strings.Contains(value, strings.ToLower(term)) {
				matched = true
				break
			}
		}
		if matched {
			result = append(result, graph.SymbolResult{Symbol: symbol.Symbol})
		}
	}
	if limit < len(result) {
		result = result[:limit]
	}
	return result, nil
}

func (r *fakeReader) FindSymbols(_ context.Context, query string) ([]graph.SymbolResult, error) {
	if result, ok := r.symbols[query]; ok {
		return []graph.SymbolResult{result}, nil
	}
	return nil, errors.New("symbol not found")
}

func (r *fakeReader) FindFile(_ context.Context, query string) (graph.FileResult, error) {
	if result, ok := r.files[query]; ok {
		return result, nil
	}
	return graph.FileResult{}, errors.New("file not found")
}

func TestTokenizeTask(t *testing.T) {
	got := tokenize("add retry_handling to SubmitTransaction-v2")
	want := []string{"add", "retry", "handling", "to", "submit", "transaction", "v", "2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tokenize() = %#v, want %#v", got, want)
	}
}

func TestSeedAmbiguityAndNoMatch(t *testing.T) {
	reader := newImpactFixture()
	report, err := Analyze(context.Background(), reader, Request{Task: "helper"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Seeds.Status != SeedAmbiguous || len(report.Seeds.Selected) != 2 {
		t.Fatalf("ambiguous seeds = %#v", report.Seeds)
	}
	noMatch, err := Analyze(context.Background(), reader, Request{Task: "missing operation"})
	if err != nil {
		t.Fatal(err)
	}
	if noMatch.Seeds.Status != SeedNoMatch || len(noMatch.Context.Items) != 0 || len(noMatch.Implementation.Items) != 0 {
		t.Fatalf("no-match report = %#v", noMatch)
	}
}

func TestImpactTraversalAndRadiusSeparation(t *testing.T) {
	reader := newImpactFixture()
	report, err := Analyze(context.Background(), reader, Request{Task: "add retry handling to SubmitTransaction", MaxDepth: 3})
	if err != nil {
		t.Fatal(err)
	}
	if report.Seeds.Status != SeedMatched || len(report.Seeds.Selected) != 1 || report.Seeds.Selected[0].Name != "SubmitTransaction" {
		t.Fatalf("seeds = %#v", report.Seeds)
	}
	if !containsItem(report.Context.Items, "symbol:submit") || !containsItem(report.Context.Items, "symbol:caller") || !containsItem(report.Context.Items, "symbol:test") {
		t.Fatalf("context items = %#v", report.Context.Items)
	}
	if !containsItem(report.Implementation.Items, "symbol:submit") || containsItem(report.Implementation.Items, "symbol:test") {
		t.Fatalf("implementation items = %#v", report.Implementation.Items)
	}
	if !containsItem(report.Validation.Items, "symbol:test") || !containsItem(report.Validation.Items, "package:go:package:example.com/fixture") {
		t.Fatalf("validation items = %#v", report.Validation.Items)
	}
	for _, item := range report.Implementation.Items {
		if len(item.Evidence) == 0 {
			t.Fatalf("implementation item lacks evidence: %#v", item)
		}
		if item.Key != "symbol:submit" && len(item.Evidence[0].Path) == 0 {
			t.Fatalf("related item lacks path evidence: %#v", item)
		}
	}
}

func TestImpactTraversalTerminatesOnCycleAndReportsLimits(t *testing.T) {
	reader := newImpactFixture()
	reader.symbols["symbol:submit"] = addRelationship(reader.symbols["symbol:submit"], graph.SymbolRelationship{SourceKey: "symbol:submit", TargetKey: "symbol:cycle", SourceKind: graph.NodeFunction, TargetKind: graph.NodeFunction, Kind: graph.EdgeCalls, Confidence: graph.ConfidenceExact})
	reader.symbols["symbol:cycle"] = graph.SymbolResult{Symbol: symbol("symbol:cycle", "Cycle", graph.NodeFunction, "cycle.go", 2), Calls: []graph.SymbolRelationship{{SourceKey: "symbol:cycle", TargetKey: "symbol:submit", SourceKind: graph.NodeFunction, TargetKind: graph.NodeFunction, Kind: graph.EdgeCalls, Confidence: graph.ConfidenceExact}}}
	report, err := Analyze(context.Background(), reader, Request{Task: "SubmitTransaction", MaxDepth: 1, MaxFanout: 1, ContextLimit: 1, ImplementationLimit: 1, ValidationLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Context.Items) > 1 || len(report.Implementation.Items) > 1 || len(report.Validation.Items) > 1 {
		t.Fatalf("limits exceeded: context=%d implementation=%d validation=%d", len(report.Context.Items), len(report.Implementation.Items), len(report.Validation.Items))
	}
	if !containsItem(report.Context.Items, "symbol:submit") || !containsItem(report.Implementation.Items, "symbol:submit") {
		t.Fatalf("direct seed was not preserved under radius limits: context=%#v implementation=%#v", report.Context.Items, report.Implementation.Items)
	}
	if !report.Truncated {
		t.Fatalf("expected truncation diagnostics: %#v", report)
	}
}

func TestImpactTraversalIncludesReferences(t *testing.T) {
	reader := newImpactFixture()
	reader.symbols["symbol:referrer"] = graph.SymbolResult{Symbol: symbol("symbol:referrer", "Referrer", graph.NodeFunction, "transaction/referrer.go", 4)}
	reader.files["file:transaction/referrer.go"] = graph.FileResult{File: graph.File{Key: "file:transaction/referrer.go", Path: "transaction/referrer.go", BlobSHA: "symbol:referrer-blob", PackageKey: "go:package:example.com/fixture"}}
	result := reader.symbols["symbol:submit"]
	result.References = []graph.SymbolReference{{SourceKey: "symbol:referrer", TargetKey: "symbol:submit", SourcePackage: "go:package:example.com/fixture", SourceFile: "transaction/referrer.go", Kind: graph.EdgeReferences, Confidence: graph.ConfidenceExact, Position: graph.Position{Path: "transaction/referrer.go", StartLine: 4, EndLine: 4}}}
	reader.symbols["symbol:submit"] = result
	report, err := Analyze(context.Background(), reader, Request{Task: "SubmitTransaction", MaxDepth: 1, MaxFanout: 8})
	if err != nil {
		t.Fatal(err)
	}
	if !containsItem(report.Context.Items, "symbol:referrer") {
		t.Fatalf("reference-only dependent missing from context: %#v", report.Context.Items)
	}
}

func TestImpactTraversalRetainsExternalPackageRisk(t *testing.T) {
	reader := newImpactFixture()
	result := reader.symbols["symbol:submit"]
	result.Calls = append(result.Calls, graph.SymbolRelationship{SourceKey: "symbol:submit", TargetKey: "external:symbol:fmt:func:Errorf", TargetPackage: "external:package:fmt", SourceKind: graph.NodeFunction, TargetKind: graph.NodeSymbol, Kind: graph.EdgeCalls, Confidence: graph.ConfidenceExact})
	reader.symbols["symbol:submit"] = result
	report, err := Analyze(context.Background(), reader, Request{Task: "SubmitTransaction", MaxDepth: 1, MaxFanout: 8})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, item := range report.Validation.Items {
		if item.Label == LabelExternal && item.Package == "external:package:fmt" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("external package risk missing from validation: %#v", report.Validation.Items)
	}
}

func TestQualifiedSeedOverride(t *testing.T) {
	reader := newImpactFixture()
	report, err := Analyze(context.Background(), reader, Request{Task: "helper", SeedKeys: []string{"symbol:helper-two"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Seeds.Selected) != 1 || report.Seeds.Selected[0].Key != "symbol:helper-two" || report.Seeds.Selected[0].Match != "qualified override" {
		t.Fatalf("override seeds = %#v", report.Seeds)
	}
}

func containsItem(items []Item, key string) bool {
	for _, item := range items {
		if item.Key == key {
			return true
		}
	}
	return false
}

func addRelationship(result graph.SymbolResult, relationship graph.SymbolRelationship) graph.SymbolResult {
	result.Calls = append(result.Calls, relationship)
	return result
}

func newImpactFixture() *fakeReader {
	reader := &fakeReader{generation: graph.Generation{RepoID: "repo", WorktreeID: "worktree", ID: 7, Commit: "commit", BuildFingerprint: "fingerprint", AnalyzerVersion: "analyzer", SchemaVersion: 1}, symbols: make(map[string]graph.SymbolResult), files: make(map[string]graph.FileResult)}
	add := func(key, name string, kind graph.NodeKind, path string, line int) {
		symbol := symbol(key, name, kind, path, line)
		reader.symbols[key] = graph.SymbolResult{Symbol: symbol}
		reader.files[symbol.FileKey] = graph.FileResult{File: graph.File{Key: symbol.FileKey, Path: path, BlobSHA: key + "-blob", PackageKey: symbol.PackageKey}}
	}
	add("symbol:submit", "SubmitTransaction", graph.NodeFunction, "transaction/service.go", 10)
	add("symbol:caller", "Caller", graph.NodeFunction, "transaction/handler.go", 5)
	add("symbol:reserve", "Reserve", graph.NodeFunction, "ledger/service.go", 20)
	add("symbol:test", "TestSubmitTransaction", graph.NodeTest, "transaction/service_test.go", 8)
	add("symbol:helper-one", "Helper", graph.NodeFunction, "one/helper.go", 3)
	add("symbol:helper-two", "Helper", graph.NodeFunction, "two/helper.go", 3)
	reader.symbols["symbol:submit"] = graph.SymbolResult{Symbol: reader.symbols["symbol:submit"].Symbol, Calls: []graph.SymbolRelationship{{SourceKey: "symbol:submit", TargetKey: "symbol:reserve", SourceKind: graph.NodeFunction, TargetKind: graph.NodeFunction, Kind: graph.EdgeCalls, Confidence: graph.ConfidenceExact}, {SourceKey: "symbol:caller", TargetKey: "symbol:submit", SourceKind: graph.NodeFunction, TargetKind: graph.NodeFunction, Kind: graph.EdgeCalls, Confidence: graph.ConfidenceExact}}, Tests: []graph.SymbolRelationship{{SourceKey: "symbol:test", TargetKey: "symbol:submit", SourceKind: graph.NodeTest, TargetKind: graph.NodeFunction, Kind: graph.EdgeTests, Confidence: graph.ConfidenceExact}}}
	reader.symbols["symbol:reserve"] = graph.SymbolResult{Symbol: reader.symbols["symbol:reserve"].Symbol, CalledBy: []graph.SymbolRelationship{{SourceKey: "symbol:submit", TargetKey: "symbol:reserve", SourceKind: graph.NodeFunction, TargetKind: graph.NodeFunction, Kind: graph.EdgeCalls, Confidence: graph.ConfidenceExact}}}
	reader.symbols["symbol:caller"] = graph.SymbolResult{Symbol: reader.symbols["symbol:caller"].Symbol, Calls: []graph.SymbolRelationship{{SourceKey: "symbol:caller", TargetKey: "symbol:submit", SourceKind: graph.NodeFunction, TargetKind: graph.NodeFunction, Kind: graph.EdgeCalls, Confidence: graph.ConfidenceExact}}}
	reader.symbols["symbol:test"] = graph.SymbolResult{Symbol: reader.symbols["symbol:test"].Symbol, Tests: []graph.SymbolRelationship{{SourceKey: "symbol:test", TargetKey: "symbol:submit", SourceKind: graph.NodeTest, TargetKind: graph.NodeFunction, Kind: graph.EdgeTests, Confidence: graph.ConfidenceExact}}}
	return reader
}

func symbol(key, name string, kind graph.NodeKind, path string, line int) graph.Symbol {
	return graph.Symbol{Key: key, Name: name, Kind: kind, PackageKey: "go:package:example.com/fixture", FileKey: "file:" + path, Position: graph.Position{Path: path, StartLine: line, EndLine: line + 2}}
}
