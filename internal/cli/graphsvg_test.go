package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func svgTestExport() graphExport {
	return graphExport{
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
			{Source: "symbol:caller", Target: "symbol:target", Kind: "calls", Confidence: "exact", Historical: true},
			{Source: "symbol:target", Target: "symbol:possible", Kind: "possible_call", Confidence: "possible"},
		},
	}
}

func TestGraphSVGRendersWithoutAnInstalledGraphviz(t *testing.T) {
	var output bytes.Buffer
	if err := writeGraphSVG(context.Background(), &output, svgTestExport()); err != nil {
		t.Fatal(err)
	}
	svg := output.String()
	for _, want := range []string{"<svg", "</svg>", "Target", "Caller"} {
		if !strings.Contains(svg, want) {
			t.Fatalf("rendered SVG is missing %q: %s", want, truncateForTest(svg))
		}
	}
}

func TestGraphSVGRendersDeterministically(t *testing.T) {
	export := svgTestExport()
	var first, second bytes.Buffer
	if err := writeGraphSVG(context.Background(), &first, export); err != nil {
		t.Fatal(err)
	}
	if err := writeGraphSVG(context.Background(), &second, export); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("two renders of one export produced different SVG bytes")
	}
}

func TestGraphSVGHonorsCancellationAndWritesNothing(t *testing.T) {
	// Callers pipe this output straight into a file, so a cancelled render
	// must fail before it writes any part of an SVG.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	err := writeGraphSVG(ctx, &output, svgTestExport())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if output.Len() != 0 {
		t.Fatalf("cancelled render wrote %d bytes: %s", output.Len(), truncateForTest(output.String()))
	}
}

func TestParseGraphOutputFormatAcceptsSVG(t *testing.T) {
	format, err := parseGraphOutputFormat("SVG")
	if err != nil {
		t.Fatal(err)
	}
	if format != graphOutputSVG {
		t.Fatalf("format = %q, want %q", format, graphOutputSVG)
	}
	if _, err := parseGraphOutputFormat("png"); err == nil {
		t.Fatal("expected an unsupported format to be rejected")
	}
}

func TestRenderGraphExportRoutesSVG(t *testing.T) {
	var output bytes.Buffer
	if err := renderGraphExport(context.Background(), &output, graphOutputSVG, svgTestExport()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "<svg") {
		t.Fatalf("renderGraphExport did not produce SVG: %s", truncateForTest(output.String()))
	}
}

func truncateForTest(value string) string {
	const limit = 200
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
