package benchmarks

import (
	"fmt"
	"testing"

	"github.com/arham09/jejak/internal/graph"
)

// BenchmarkNormalizeAnalysis supplies a small deterministic graph-shaped
// corpus. It measures the normalization boundary used before SQLite writes,
// not an end-to-end index and therefore does not make repository performance
// claims by itself.
func BenchmarkNormalizeAnalysis(b *testing.B) {
	result := benchmarkAnalysis(512)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		_ = result.Normalize()
	}
}

func benchmarkAnalysis(size int) graph.AnalysisResult {
	result := graph.AnalysisResult{AnalyzerVersion: "benchmark", BuildFingerprint: "benchmark"}
	for index := 0; index < size; index++ {
		packageKey := fmt.Sprintf("package:%04d", index)
		fileKey := fmt.Sprintf("file:%04d.go", index)
		symbolKey := fmt.Sprintf("symbol:%04d", index)
		result.Packages = append(result.Packages, graph.Package{Key: packageKey, ImportPath: packageKey})
		result.Files = append(result.Files, graph.File{Key: fileKey, Path: fmt.Sprintf("pkg/%04d.go", index), PackageKey: packageKey})
		result.Symbols = append(result.Symbols, graph.Symbol{Key: symbolKey, Name: fmt.Sprintf("Function%04d", index), PackageKey: packageKey, FileKey: fileKey, Kind: graph.NodeFunction})
		result.Nodes = append(result.Nodes, graph.Node{Key: symbolKey, Kind: graph.NodeFunction, Owned: true, PackageKey: packageKey, FileKey: fileKey, SymbolKey: symbolKey})
	}
	return result
}
