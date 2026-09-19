package golang

import (
	"sort"
	"strings"

	"github.com/arham09/jejak/internal/graph"
)

// collectTests associates tests with the strongest source-backed fact that is
// available. Direct calls/references are exact, the conventional Test*/
// Benchmark*/Example* name match is inferred, and a package node remains as a
// useful fallback when a test only reaches production code through helpers.
func (e *extractor) collectTests() {
	production := make(map[string]map[string]string)
	for _, symbol := range e.result.Symbols {
		if symbol.Kind == graph.NodeTest || strings.Contains(symbol.PackageKey, "#") {
			continue
		}
		if production[symbol.PackageKey] == nil {
			production[symbol.PackageKey] = make(map[string]string)
		}
		production[symbol.PackageKey][symbol.Name] = symbol.Key
	}

	tests := make([]graph.Symbol, 0)
	for _, symbol := range e.result.Symbols {
		if symbol.Kind == graph.NodeTest {
			tests = append(tests, symbol)
		}
	}
	sort.Slice(tests, func(i, j int) bool { return tests[i].Key < tests[j].Key })
	for _, test := range tests {
		info := e.packages[test.PackageKey]
		if info == nil {
			continue
		}
		basePackage := packageKey(info.basePath, "production")
		related := make(map[string]struct{})
		for _, edge := range e.result.Edges {
			if edge.SourceKey != test.Key || (edge.Kind != graph.EdgeCalls && edge.Kind != graph.EdgeReferences) {
				continue
			}
			target, exists := e.symbolsByKey[edge.TargetKey]
			if !exists || target.Kind == graph.NodeTest || strings.Contains(target.PackageKey, "#") {
				continue
			}
			if _, exists := related[target.Key]; exists {
				continue
			}
			related[target.Key] = struct{}{}
			e.addTestRelationship(test, target.Key, graph.ConfidenceExact, "direct "+string(edge.Kind))
		}

		name := test.Name
		for _, prefix := range []string{"Test", "Benchmark", "Example"} {
			if strings.HasPrefix(name, prefix) {
				name = strings.TrimPrefix(name, prefix)
				break
			}
		}
		name = strings.SplitN(name, "_", 2)[0]
		if target := production[basePackage][name]; target != "" {
			if _, exists := related[target]; !exists {
				e.addTestRelationship(test, target, graph.ConfidenceInferred, "name convention")
				related[target] = struct{}{}
			}
		}

		// Keep a package-level validation target when direct symbol identity is
		// not enough to explain what the test exercises. The package node is a
		// deliberate relationship endpoint, not a fabricated production symbol.
		if packageNode := packageNodeKey(basePackage); len(related) == 0 {
			if _, exists := e.nodes[packageNode]; !exists {
				continue
			}
			e.addTestRelationship(test, packageNode, graph.ConfidenceInferred, "package fallback")
		}
	}
}

func (e *extractor) addTestRelationship(test graph.Symbol, target string, confidence graph.Confidence, details string) {
	if test.Key == "" || target == "" {
		return
	}
	key := test.Key + "\x00" + target
	for _, relationship := range e.result.TestRelationships {
		if relationship.TestKey+"\x00"+relationship.TargetKey == key {
			return
		}
	}
	edge := graph.Edge{SourceKey: test.Key, TargetKey: target, Kind: graph.EdgeTests, Confidence: confidence, OwnerPackage: test.PackageKey}
	e.addEdge(edge)
	e.result.TestRelationships = append(e.result.TestRelationships, graph.TestRelationship{TestKey: test.Key, TargetKey: target, Confidence: confidence})
	e.result.Evidence = append(e.result.Evidence, graph.EdgeEvidence{
		SourceKey: edge.SourceKey, TargetKey: edge.TargetKey, Kind: edge.Kind,
		AnalyzerSource: analyzerVersion, SourceBlob: e.blobForFile(test.FileKey),
		StartLine: test.Position.StartLine, StartColumn: test.Position.StartColumn,
		EndLine: test.Position.EndLine, EndColumn: test.Position.EndColumn, Details: details,
	})
}
