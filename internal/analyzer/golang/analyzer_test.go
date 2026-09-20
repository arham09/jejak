package golang

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arham09/jejak/internal/git"
	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
	"github.com/arham09/jejak/internal/testrepo"
)

func TestAnalyzeSingleModule(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/single\n\ngo 1.27\n")
	repo.Write(t, "lib/lib.go", "package lib\n\n// Answer returns a value.\nfunc Answer() int { return 42 }\n")
	repo.Write(t, "main.go", "package single\n\nimport \"example.com/single/lib\"\n\nfunc Use() int { return lib.Answer() }\n")
	repo.Write(t, "main_test.go", "package single\n\nimport \"testing\"\n\nfunc TestUse(t *testing.T) { if Use() != 42 { t.Fatal(\"bad\") } }\n")
	commit := repo.Commit(t, "initial")
	input, cleanup := snapshotInput(t, repo.Root, commit)
	defer cleanup()
	result, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.HasErrors() {
		t.Fatalf("Analyze() diagnostics = %#v", result.Diagnostics)
	}
	if len(result.Packages) < 2 || len(result.Files) < 3 || len(result.Symbols) < 3 {
		t.Fatalf("analysis counts = packages=%d files=%d symbols=%d packages=%#v files=%#v symbols=%#v diagnostics=%#v", len(result.Packages), len(result.Files), len(result.Symbols), result.Packages, result.Files, result.Symbols, result.Diagnostics)
	}
	if !hasEdge(result.Edges, graph.EdgeImports) || !hasEdge(result.Edges, graph.EdgeReferences) || !hasEdge(result.Edges, graph.EdgeTests) {
		t.Fatalf("analysis edges = %#v", result.Edges)
	}
	answerKey := symbolKeyByName(result.Symbols, "Answer")
	if answerKey == "" || !hasReferenceTarget(result.Edges, answerKey) {
		t.Fatalf("cross-package Answer reference missing: key=%q edges=%#v", answerKey, result.Edges)
	}
	for _, symbol := range result.Symbols {
		if symbol.Name == "Answer" && symbol.Position.Path != "lib/lib.go" {
			t.Fatalf("Answer position = %#v", symbol.Position)
		}
	}
}

func TestAnalyzeWorkspace(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.work", "go 1.27\n\nuse (\n\tservice\n\tshared\n)\n")
	repo.Write(t, "service/go.mod", "module example.com/service\n\ngo 1.27\n\nrequire example.com/shared v0.0.0\n")
	repo.Write(t, "shared/go.mod", "module example.com/shared\n\ngo 1.27\n")
	repo.Write(t, "shared/shared.go", "package shared\n\nfunc Value() int { return 7 }\n")
	repo.Write(t, "service/main.go", "package service\n\nimport \"example.com/shared\"\n\nfunc Run() int { return shared.Value() }\n")
	commit := repo.Commit(t, "workspace")
	input, cleanup := snapshotInput(t, repo.Root, commit)
	defer cleanup()
	result, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.HasErrors() {
		t.Fatalf("workspace diagnostics = %#v", result.Diagnostics)
	}
	if !hasPackage(result.Packages, "example.com/service") || !hasPackage(result.Packages, "example.com/shared") {
		t.Fatalf("workspace packages = %#v", result.Packages)
	}
}

func TestAnalyzeOfflineMissingDependencyIsDiagnostic(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/missing\n\ngo 1.27\n\nrequire example.com/not-cached v1.0.0\n")
	repo.Write(t, "main.go", "package missing\n\nimport \"example.com/not-cached\"\n\nvar _ = notcached.Value\n")
	commit := repo.Commit(t, "missing dependency")
	input, cleanup := snapshotInput(t, repo.Root, commit)
	defer cleanup()
	result, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if !result.HasErrors() {
		t.Fatalf("missing dependency diagnostics = %#v", result.Diagnostics)
	}
	joined := make([]string, 0, len(result.Diagnostics))
	for _, diagnostic := range result.Diagnostics {
		joined = append(joined, diagnostic.Message)
	}
	if !strings.Contains(strings.ToLower(strings.Join(joined, " ")), "module") {
		t.Fatalf("missing dependency diagnostics = %#v", result.Diagnostics)
	}
}

func TestAnalyzeDependencyDownloadOptIn(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/downloader\n\ngo 1.27\n\nrequire example.com/remote v1.0.0\n")
	repo.Write(t, "main.go", "package downloader\n\nimport \"example.com/remote\"\n\nvar _ = remote.Value\n")
	commit := repo.Commit(t, "download dependency")
	input, cleanup := snapshotInput(t, repo.Root, commit)
	defer cleanup()

	proxy := newModuleProxy(t)
	modCache := filepath.Join(t.TempDir(), "modcache")
	t.Cleanup(func() {
		_ = filepath.Walk(modCache, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			_ = os.Chmod(path, 0o700)
			return nil
		})
	})
	t.Setenv("GOPROXY", "file://"+proxy)
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GONOSUMDB", "*")
	t.Setenv("GOMODCACHE", modCache)
	t.Setenv("GOPATH", filepath.Join(t.TempDir(), "gopath"))
	input.Build.DownloadDependencies = true
	result, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.HasErrors() {
		t.Fatalf("download opt-in diagnostics = %#v", result.Diagnostics)
	}
	if _, err := os.Stat(filepath.Join(modCache, "example.com", "remote@v1.0.0", "value.go")); err != nil {
		t.Fatalf("downloaded module is not in the isolated module cache: %v", err)
	}
}

func TestBuildFingerprintIncludesBuildSelection(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/fingerprint\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package fingerprint\n\nfunc Value() int { return 1 }\n")
	commit := repo.Commit(t, "fingerprint")
	input, cleanup := snapshotInput(t, repo.Root, commit)
	defer cleanup()
	analyzer := New()
	first, err := analyzer.BuildFingerprint(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.Build.Tags = []string{"integration"}
	second, err := analyzer.BuildFingerprint(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("build tag did not change fingerprint: %s", first)
	}
	repo.Write(t, "templates/value.txt", "embedded\n")
	secondCommit := repo.Commit(t, "embedded input")
	thirdInput, thirdCleanup := snapshotInput(t, repo.Root, secondCommit)
	defer thirdCleanup()
	thirdInput.Build.Tags = []string{"integration"}
	third, err := analyzer.BuildFingerprint(context.Background(), thirdInput)
	if err != nil {
		t.Fatal(err)
	}
	if second == third {
		t.Fatalf("tracked non-Go input did not change fingerprint: %s", second)
	}
}

func TestControlledEnvironmentEnforcesOfflineDefault(t *testing.T) {
	t.Setenv("GOPROXY", "https://proxy.example.invalid")
	t.Setenv("GOWORK", "/outside/go.work")
	inheritedFlags := os.Getenv("GOFLAGS")
	env := controlledEnvironment(graph.BuildConfig{}, loadPlan{Root: t.TempDir()})
	if value := envValue(env, "GOPROXY"); value != "off" {
		t.Fatalf("offline GOPROXY = %q", value)
	}
	if value := envValue(env, "GOWORK"); value != "off" {
		t.Fatalf("single-module GOWORK = %q", value)
	}
	if value := envValue(env, "GOFLAGS"); value != appendModuleMode(inheritedFlags, "readonly") {
		t.Fatalf("offline GOFLAGS = %q", value)
	}
	if value := envValue(controlledEnvironment(graph.BuildConfig{DownloadDependencies: true}, loadPlan{}), "GOPROXY"); value != "https://proxy.example.invalid" {
		t.Fatalf("opt-in GOPROXY = %q", value)
	}
	if value := envValue(controlledEnvironment(graph.BuildConfig{DownloadDependencies: true}, loadPlan{}), "GOFLAGS"); value != appendModuleMode(inheritedFlags, "mod") {
		t.Fatalf("opt-in GOFLAGS = %q", value)
	}
	taggedFlags := envValue(controlledEnvironment(graph.BuildConfig{Tags: []string{"integration"}}, loadPlan{}), "GOFLAGS")
	if !strings.Contains(taggedFlags, "-mod=readonly") || !strings.Contains(taggedFlags, "-tags=integration") {
		t.Fatalf("tagged offline GOFLAGS = %q", taggedFlags)
	}
}

func TestAnalyzeBuildTagsSelectFiles(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/tags\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package tags\n\nfunc Base() {}\n")
	repo.Write(t, "special.go", "//go:build special\n\npackage tags\n\nfunc Special() {}\n")
	commit := repo.Commit(t, "tags")
	input, cleanup := snapshotInput(t, repo.Root, commit)
	defer cleanup()
	without, err := New().Analyze(context.Background(), input)
	if err != nil || without.HasErrors() {
		t.Fatalf("default tag analysis error=%v diagnostics=%#v", err, without.Diagnostics)
	}
	if hasSymbol(without.Symbols, "Special") {
		t.Fatalf("default analysis included tagged symbol: %#v", without.Symbols)
	}
	input.Build.Tags = []string{"special"}
	with, err := New().Analyze(context.Background(), input)
	if err != nil || with.HasErrors() {
		t.Fatalf("tagged analysis error=%v diagnostics=%#v", err, with.Diagnostics)
	}
	if !hasSymbol(with.Symbols, "Special") {
		t.Fatalf("tagged analysis omitted Special: %#v", with.Symbols)
	}
}

func TestAnalyzeSemanticCallsAndInterfaces(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/semantic\n\ngo 1.27\n")
	repo.Write(t, "semantic.go", `package semantic

type Runner interface {
	Run(int) int
}

type EmbeddedRunner interface {
	Runner
}

type valueRunner struct{}

func (valueRunner) Run(value int) int { return value }

type pointerRunner struct{}

func (*pointerRunner) Run(value int) int { return value }

type AliasRunner = valueRunner

type genericRunner[T any] struct{}

func (genericRunner[T]) Run(value int) int { return value }

type GenericRunner[T any] interface {
	RunGeneric(T) T
}

type genericImplementation struct{}

func (genericImplementation) RunGeneric(value int) int { return value }

func helper(value int) int { return value }

func Direct() int {
	helper(1)
	return helper(1)
}

func InterfaceCall(r Runner) int { return r.Run(2) }

func ClosureCall() int { return func() int { return helper(3) }() }

func Async() {
	go helper(4)
	defer helper(5)
}

func Conversion(value int) int { return int(value) }

func Builtin(values []int) int { return len(values) }

func Unknown(function func()) { function() }

func UseRunner() int {
	var runner Runner = valueRunner{}
	return runner.Run(6)
}

func GenericCall() int {
	var runner Runner = genericRunner[int]{}
	return runner.Run(7)
}

func GenericInterfaceUse(runner GenericRunner[int]) int { return runner.RunGeneric(10) }

func MethodExpression(value valueRunner) int { return valueRunner.Run(value, 8) }

func MethodValue(value valueRunner) int {
	run := value.Run
	return run(9)
}
`)
	repo.Write(t, "semantic_test.go", `package semantic

import "testing"

func TestDirect(t *testing.T) {
	if Direct() != 1 {
		t.Fatal("unexpected result")
	}
}

func TestFallback(t *testing.T) {
	t.Helper()
}
`)
	repo.Write(t, "external_test.go", `package semantic_test

import (
	"testing"
	"example.com/semantic"
)

func TestInterfaceCall(t *testing.T) {
	if semantic.Direct() != 1 {
		t.Fatal("unexpected result")
	}
}
`)
	commit := repo.Commit(t, "semantic graph")
	input, cleanup := snapshotInput(t, repo.Root, commit)
	defer cleanup()
	result, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.HasErrors() {
		t.Fatalf("semantic diagnostics = %#v", result.Diagnostics)
	}
	repeated, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("repeated Analyze() error = %v", err)
	}
	if !reflect.DeepEqual(result.Normalize(), repeated.Normalize()) {
		t.Fatalf("semantic analysis is not deterministic")
	}
	if !hasSemanticEdge(result, graph.EdgeCalls, "Direct", "helper", graph.ConfidenceExact) {
		t.Fatalf("direct call edge missing: %#v", result.Edges)
	}
	if !hasSemanticEdge(result, graph.EdgeCalls, "InterfaceCall", "Run", graph.ConfidenceExact) {
		t.Fatalf("interface static call edge missing: %#v", result.Edges)
	}
	if !hasSemanticEdge(result, graph.EdgePossibleCall, "InterfaceCall", "Run", graph.ConfidencePossible) {
		t.Fatalf("interface possible dispatch edge missing: %#v", result.Edges)
	}
	if !hasAnonymousCallTarget(result, "helper") {
		t.Fatalf("closure call edge missing: %#v", result.Edges)
	}
	if !hasSemanticEdge(result, graph.EdgeCalls, "Async", "helper", graph.ConfidenceExact) {
		t.Fatalf("go/defer call edge missing: %#v", result.Edges)
	}
	if !hasSemanticEdge(result, graph.EdgeUnresolvedCall, "Unknown", "", graph.ConfidencePossible) {
		t.Fatalf("unresolved function-value edge missing: %#v", result.Edges)
	}
	if !hasSemanticEdge(result, graph.EdgeUnresolvedCall, "MethodValue", "", graph.ConfidencePossible) {
		t.Fatalf("method-value unresolved edge missing: %#v", result.Edges)
	}
	if hasSemanticEdge(result, graph.EdgeCalls, "Conversion", "", graph.ConfidenceExact) || hasSemanticEdge(result, graph.EdgeCalls, "Builtin", "", graph.ConfidenceExact) {
		t.Fatalf("conversion/builtin unexpectedly emitted call edge: %#v", result.Edges)
	}
	if !hasImplementation(result, "valueRunner", "Runner") || !hasImplementation(result, "pointerRunner", "Runner") || !hasImplementation(result, "valueRunner", "EmbeddedRunner") || !hasImplementation(result, "genericRunner", "Runner") {
		t.Fatalf("interface implementation edges missing: %#v", result.Edges)
	}
	if !hasImplementation(result, "genericImplementation", "GenericRunner") {
		t.Fatalf("instantiated generic interface implementation edge missing: %#v", result.Edges)
	}
	if !hasSemanticEdge(result, graph.EdgeCalls, "GenericInterfaceUse", "RunGeneric", graph.ConfidenceExact) || !hasSemanticEdge(result, graph.EdgePossibleCall, "GenericInterfaceUse", "RunGeneric", graph.ConfidencePossible) {
		t.Fatalf("generic interface dispatch edges missing: %#v", result.Edges)
	}
	if !hasSemanticEdge(result, graph.EdgeCalls, "MethodExpression", "Run", graph.ConfidenceExact) {
		t.Fatalf("method-expression call edge missing: %#v", result.Edges)
	}
	if countSemanticEdges(result, graph.EdgeCalls, "Direct", "helper") != 1 || countEvidence(result, graph.EdgeCalls, "Direct", "helper") < 2 {
		t.Fatalf("repeated call evidence was not normalized: edges=%#v evidence=%#v", result.Edges, result.Evidence)
	}
	if !hasTestRelationship(result, "TestDirect") {
		t.Fatalf("direct test relationship missing: %#v", result.TestRelationships)
	}
	if !hasTestPackageFallback(result, "TestFallback") {
		t.Fatalf("package test fallback missing: %#v", result.TestRelationships)
	}
	if !hasEvidenceDetail(result, graph.EdgeCalls, "go") || !hasEvidenceDetail(result, graph.EdgeCalls, "defer") {
		t.Fatalf("go/defer call evidence missing: %#v", result.Evidence)
	}
}

func TestAnalyzeRejectsExternalWorkspaceMember(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.work", "go 1.27\n\nuse (\n\t../outside\n)\n")
	commit := repo.Commit(t, "external workspace")
	input, cleanup := snapshotInput(t, repo.Root, commit)
	defer cleanup()
	result, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.HasErrors() || len(result.Diagnostics) == 0 || !strings.Contains(result.Diagnostics[0].Message, "outside") {
		t.Fatalf("external workspace diagnostics = %#v", result.Diagnostics)
	}
}

func TestSyntaxCacheEntriesAreVersionedAndDeterministic(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/syntax\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package syntax\n\nimport \"fmt\"\n\nconst Answer = 42\n\nfunc Print() { fmt.Println(Answer) }\n")
	repo.Write(t, "README.md", "syntax\n")
	commit := repo.Commit(t, "syntax cache")
	input, cleanup := snapshotInput(t, repo.Root, commit)
	defer cleanup()
	entries, err := New().SyntaxCacheEntries(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ParserVersion != syntaxParserVersion || entries[0].ObjectFormat == "" {
		t.Fatalf("syntax cache entries = %#v", entries)
	}
	var summary syntaxSummary
	if err := json.Unmarshal(entries[0].SyntaxJSON, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Package != "syntax" || len(summary.Imports) != 1 || summary.Imports[0] != "fmt" || len(summary.Declarations) != 2 {
		t.Fatalf("syntax summary = %#v", summary)
	}
	input.Files = append(input.Files, input.Files[1])
	duplicated, err := New().SyntaxCacheEntries(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(duplicated) != 1 {
		t.Fatalf("duplicate blob produced %d cache entries", len(duplicated))
	}
	cachedInput := input
	cachedInput.Root = filepath.Join(t.TempDir(), "missing-snapshot")
	cachedInput.CachedSyntax = entries
	cached, err := New().SyntaxCacheEntries(context.Background(), cachedInput)
	if err != nil {
		t.Fatalf("cached syntax summary should not reread the source: %v", err)
	}
	if !reflect.DeepEqual(cached, entries) {
		t.Fatalf("cached syntax entries = %#v, want %#v", cached, entries)
	}
	unsafeInput := input
	unsafeInput.Files = []graph.SnapshotFile{{Path: "../outside.go", BlobSHA: "outside"}}
	if _, err := New().SyntaxCacheEntries(context.Background(), unsafeInput); err == nil {
		t.Fatal("unsafe syntax cache path unexpectedly succeeded")
	}
}

func snapshotInput(t *testing.T, root, commit string) (graph.AnalyzeInput, func()) {
	t.Helper()
	target, err := repository.Resolve(context.Background(), git.NewClient("git"), root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := git.NewClient("git").Snapshot(context.Background(), root, commit)
	if err != nil {
		t.Fatal(err)
	}
	files := make([]graph.SnapshotFile, 0, len(snapshot.Files))
	for _, file := range snapshot.Files {
		files = append(files, graph.SnapshotFile{Path: file.Path, BlobSHA: file.BlobSHA, ObjectFormat: file.ObjectFormat, Mode: file.Mode, Size: file.Size})
	}
	// The fixtures exercise test-variant extraction, so they opt into the
	// test packages that production indexing leaves out by default.
	input := graph.AnalyzeInput{Repository: target.Repository.ID, Worktree: target.Worktree.ID, Commit: graph.CommitSHA(commit), Root: snapshot.Root, Files: files, Build: graph.BuildConfig{IncludeTests: true}}
	return input, func() { _ = snapshot.Close() }
}

func hasEdge(edges []graph.Edge, kind graph.EdgeKind) bool {
	for _, edge := range edges {
		if edge.Kind == kind {
			return true
		}
	}
	return false
}

func hasPackage(packages []graph.Package, importPath string) bool {
	for _, item := range packages {
		if item.ImportPath == importPath && item.Variant == "production" {
			return true
		}
	}
	return false
}

func hasSymbol(symbols []graph.Symbol, name string) bool {
	for _, symbol := range symbols {
		if symbol.Name == name {
			return true
		}
	}
	return false
}

func symbolKeyByName(symbols []graph.Symbol, name string) string {
	for _, symbol := range symbols {
		if symbol.Name == name {
			return symbol.Key
		}
	}
	return ""
}

func hasReferenceTarget(edges []graph.Edge, target string) bool {
	for _, edge := range edges {
		if edge.Kind == graph.EdgeReferences && edge.TargetKey == target {
			return true
		}
	}
	return false
}

func hasSemanticEdge(result graph.AnalysisResult, kind graph.EdgeKind, sourceName, targetName string, confidence graph.Confidence) bool {
	for _, edge := range result.Edges {
		if edge.Kind != kind || edge.Confidence != confidence {
			continue
		}
		var sourceKey string
		var targetKeys []string
		for _, symbol := range result.Symbols {
			if symbol.Name == sourceName {
				sourceKey = symbol.Key
			}
			if targetName != "" && symbol.Name == targetName {
				targetKeys = append(targetKeys, symbol.Key)
			}
		}
		if sourceName == "" || sourceKey == "" || edge.SourceKey != sourceKey {
			continue
		}
		if targetName == "" {
			return true
		}
		for _, targetKey := range targetKeys {
			if edge.TargetKey == targetKey {
				return true
			}
		}
	}
	return false
}

func hasAnonymousCallTarget(result graph.AnalysisResult, targetName string) bool {
	var anonymousKeys, targetKeys []string
	for _, symbol := range result.Symbols {
		if strings.HasPrefix(symbol.Name, "$anon@") {
			anonymousKeys = append(anonymousKeys, symbol.Key)
		}
		if symbol.Name == targetName {
			targetKeys = append(targetKeys, symbol.Key)
		}
	}
	for _, edge := range result.Edges {
		if edge.Kind != graph.EdgeCalls || edge.Confidence != graph.ConfidenceExact {
			continue
		}
		if containsString(anonymousKeys, edge.SourceKey) && containsString(targetKeys, edge.TargetKey) {
			return true
		}
	}
	return false
}

func countSemanticEdges(result graph.AnalysisResult, kind graph.EdgeKind, sourceName, targetName string) int {
	sourceKey := symbolKeyByName(result.Symbols, sourceName)
	targetKey := symbolKeyByName(result.Symbols, targetName)
	count := 0
	for _, edge := range result.Edges {
		if edge.Kind == kind && edge.SourceKey == sourceKey && edge.TargetKey == targetKey {
			count++
		}
	}
	return count
}

func countEvidence(result graph.AnalysisResult, kind graph.EdgeKind, sourceName, targetName string) int {
	sourceKey := symbolKeyByName(result.Symbols, sourceName)
	targetKey := symbolKeyByName(result.Symbols, targetName)
	count := 0
	for _, evidence := range result.Evidence {
		if evidence.Kind == kind && evidence.SourceKey == sourceKey && evidence.TargetKey == targetKey {
			count++
		}
	}
	return count
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func hasImplementation(result graph.AnalysisResult, concreteName, interfaceName string) bool {
	var concreteKey, interfaceKey string
	for _, symbol := range result.Symbols {
		if symbol.Name == concreteName {
			concreteKey = symbol.Key
		}
		if symbol.Name == interfaceName {
			interfaceKey = symbol.Key
		}
	}
	for _, edge := range result.Edges {
		if edge.Kind == graph.EdgeImplements && edge.SourceKey == concreteKey && edge.TargetKey == interfaceKey {
			return true
		}
	}
	return false
}

func hasTestRelationship(result graph.AnalysisResult, testName string) bool {
	for _, symbol := range result.Symbols {
		if symbol.Name != testName || symbol.Kind != graph.NodeTest {
			continue
		}
		for _, relationship := range result.TestRelationships {
			if relationship.TestKey == symbol.Key && relationship.TargetKey != "" {
				return true
			}
		}
	}
	return false
}

func hasTestPackageFallback(result graph.AnalysisResult, testName string) bool {
	for _, symbol := range result.Symbols {
		if symbol.Name != testName || symbol.Kind != graph.NodeTest {
			continue
		}
		for _, relationship := range result.TestRelationships {
			if relationship.TestKey == symbol.Key && strings.HasPrefix(relationship.TargetKey, "package:go:package:") {
				return true
			}
		}
	}
	return false
}

func hasEvidenceDetail(result graph.AnalysisResult, kind graph.EdgeKind, detail string) bool {
	for _, evidence := range result.Evidence {
		if evidence.Kind == kind && strings.Contains(evidence.Details, detail) {
			return true
		}
	}
	return false
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, value := range env {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return ""
}

func newModuleProxy(t *testing.T) string {
	t.Helper()
	proxy := filepath.Join(t.TempDir(), "proxy")
	versionDirectory := filepath.Join(proxy, "example.com", "remote", "@v")
	if err := os.MkdirAll(versionDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	info := fmt.Sprintf("{\"Version\":\"v1.0.0\",\"Time\":%q}\n", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(versionDirectory, "v1.0.0.info"), []byte(info), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versionDirectory, "v1.0.0.mod"), []byte("module example.com/remote\n\ngo 1.20\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versionDirectory, "v1.0.0.zip"), moduleZip(t), 0o644); err != nil {
		t.Fatal(err)
	}
	return proxy
}

func moduleZip(t *testing.T) []byte {
	t.Helper()
	var contents bytes.Buffer
	writer := zip.NewWriter(&contents)
	entry, err := writer.Create("example.com/remote@v1.0.0/value.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("package remote\n\nfunc Value() int { return 1 }\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return contents.Bytes()
}

func TestAnalyzeExcludesTestPackagesUnlessRequested(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/notests\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package notests\n\nfunc Answer() int { return 42 }\n")
	repo.Write(t, "main_test.go", "package notests\n\nimport \"testing\"\n\nfunc TestAnswer(t *testing.T) { if Answer() != 42 { t.Fatal(\"bad\") } }\n")
	repo.Write(t, "external_test.go", "package notests_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/notests\"\n)\n\nfunc TestExternal(t *testing.T) { if notests.Answer() != 42 { t.Fatal(\"bad\") } }\n")
	commit := repo.Commit(t, "initial")
	input, cleanup := snapshotInput(t, repo.Root, commit)
	defer cleanup()
	input.Build = graph.BuildConfig{}
	result, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.HasErrors() {
		t.Fatalf("Analyze() diagnostics = %#v", result.Diagnostics)
	}
	if len(result.Packages) != 1 || result.Packages[0].Variant != "production" {
		t.Fatalf("packages without tests = %#v, want the production package only", result.Packages)
	}
	for _, file := range result.Files {
		if file.IsTest || strings.HasSuffix(file.Path, "_test.go") {
			t.Fatalf("test file indexed without --include-tests: %#v", file)
		}
	}
	if result.Count().Tests != 0 || len(result.TestRelationships) != 0 || hasEdge(result.Edges, graph.EdgeTests) {
		t.Fatalf("test facts recorded without --include-tests: tests=%d relationships=%#v", result.Count().Tests, result.TestRelationships)
	}
	if symbolKeyByName(result.Symbols, "Answer") == "" {
		t.Fatalf("production symbol missing: %#v", result.Symbols)
	}
	excludedFingerprint, err := New().BuildFingerprint(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.Build = graph.BuildConfig{IncludeTests: true}
	includedFingerprint, err := New().BuildFingerprint(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if excludedFingerprint == includedFingerprint {
		t.Fatal("build fingerprint ignores test inclusion, so toggling it would reuse a stale graph")
	}
	included, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if included.Count().Tests != 2 || len(included.Packages) < 3 {
		t.Fatalf("tests included: count=%d packages=%#v", included.Count().Tests, included.Packages)
	}
}
