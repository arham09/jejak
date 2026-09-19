package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/arham09/jejak/internal/graph"
)

func TestDotQuoteEscapesGraphvizStrings(t *testing.T) {
	quoted := dotQuote("quote \" slash \\ line\ncarriage\r")
	if !strings.HasPrefix(quoted, `"`) || !strings.HasSuffix(quoted, `"`) {
		t.Fatalf("quoted value = %q", quoted)
	}
	if !strings.Contains(quoted, `\"`) {
		t.Fatalf("quote was not escaped: %q", quoted)
	}
	if !strings.Contains(quoted, `\\`) {
		t.Fatalf("backslash was not escaped: %q", quoted)
	}
	if !strings.Contains(quoted, `\n`) || !strings.Contains(quoted, `\r`) {
		t.Fatalf("line breaks were not escaped: %q", quoted)
	}
}

func TestGraphDOTPreservesDirectionConfidenceAndHistory(t *testing.T) {
	export := graphExport{
		SchemaVersion: 1,
		Query:         graphExportQuery{Kind: "symbol", Value: "Target"},
		Repository:    graphExportRepository{ID: "repo"},
		Worktree:      graphExportWorktree{ID: "worktree"},
		Generation:    graphExportGeneration{ID: 3, Commit: "commit"},
		Source:        graphExportSource{Mode: "working-tree", OverlayID: "overlay"},
		Nodes: []graphExportNode{
			{ID: "symbol:target", Kind: "function", Name: "Target"},
			{ID: "symbol:caller", Kind: "function", Name: "Caller", Historical: true, ChangeKind: "deleted"},
		},
		Edges: []graphExportEdge{
			{Source: "symbol:caller", Target: "symbol:target", Kind: "calls", Confidence: "exact", Historical: true, Details: []string{"old call"}},
			{Source: "symbol:target", Target: "symbol:inferred", Kind: "calls", Confidence: "inferred"},
			{Source: "symbol:target", Target: "symbol:possible", Kind: "possible_call", Confidence: "possible"},
			{Source: "symbol:target", Target: "symbol:unresolved", Kind: "unresolved_call", Confidence: "unspecified"},
		},
	}

	var output bytes.Buffer
	if err := writeGraphDOT(&output, export); err != nil {
		t.Fatal(err)
	}
	dot := output.String()
	for _, want := range []string{
		"digraph jejak {",
		`query_kind="symbol"`,
		`source_mode="working-tree"`,
		`"symbol:caller" -> "symbol:target"`,
		`kind="calls"`,
		`confidence="exact"`,
		`historical="true"`,
		`kind="possible_call"`,
		`confidence="possible"`,
		`kind="unresolved_call"`,
		`confidence="unspecified"`,
		`details="old call"`,
		`change_kind="deleted"`,
		"}\n",
	} {
		if !strings.Contains(dot, want) {
			t.Errorf("DOT output missing %q:\n%s", want, dot)
		}
	}
}

func TestGraphDOTOutputIsDeterministic(t *testing.T) {
	export := newSymbolGraphExport(
		graphExportTestTarget(),
		graph.Generation{ID: 1, Commit: "commit"},
		graphSource{mode: "committed"},
		"Target",
		[]graph.SymbolResult{{
			Symbol: graph.Symbol{Key: "symbol:Target", Kind: graph.NodeFunction, Name: "Target", Position: graph.Position{Path: "target.go", StartLine: 2}},
			CalledBy: []graph.SymbolRelationship{{
				SourceKey: "symbol:Caller", TargetKey: "symbol:Target", SourceName: "Caller", TargetName: "Target", Kind: graph.EdgeCalls, Confidence: graph.ConfidenceExact,
				Position: graph.Position{Path: "caller.go", StartLine: 4},
			}},
		}},
	)
	var first, second bytes.Buffer
	if err := writeGraphDOT(&first, export); err != nil {
		t.Fatal(err)
	}
	if err := writeGraphDOT(&second, export); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatalf("DOT output changed between renders:\nfirst=%s\nsecond=%s", first.String(), second.String())
	}
}
