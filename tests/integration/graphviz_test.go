package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/arham09/jejak/internal/testrepo"
)

func TestGraphJSONAndDOTExports(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/graphexport\n\ngo 1.27\n")
	repo.Write(t, "semantic.go", `package graphexport

type Runner interface {
	Run() int
}

type runner struct{}

func (runner) Run() int { return helper() }

func helper() int { return 41 }

func Caller() int { return helper() }
`)
	repo.Write(t, "semantic_test.go", `package graphexport

import "testing"

func TestCaller(t *testing.T) {
	if Caller() != 41 {
		t.Fatal("unexpected result")
	}
}
`)
	repo.Commit(t, "graph export baseline")
	dataRoot := t.TempDir()
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks", "--no-agent-skills"); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}

	code, output, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "--committed", "--format", "json", "symbol", "Caller")
	if code != 0 {
		t.Fatalf("committed symbol JSON code=%d stderr=%q output=%q", code, stderr, output)
	}
	var symbolExport graphExportFixture
	if err := json.Unmarshal([]byte(output), &symbolExport); err != nil {
		t.Fatalf("decode symbol JSON: %v\n%s", err, output)
	}
	if symbolExport.SchemaVersion != 1 || symbolExport.Query.Kind != "symbol" || symbolExport.Query.Value != "Caller" || symbolExport.Source.Mode != "committed" {
		t.Fatalf("symbol export metadata = %#v", symbolExport)
	}
	if !hasGraphEdge(symbolExport.Edges, "calls") {
		t.Fatalf("symbol export has no calls edge: %#v", symbolExport.Edges)
	}

	code, output, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "--committed", "--format=dot", "symbol", "Caller")
	if code != 0 {
		t.Fatalf("committed symbol DOT code=%d stderr=%q output=%q", code, stderr, output)
	}
	for _, want := range []string{"digraph jejak {", `query_kind="symbol"`, `source_mode="committed"`, `kind="calls"`, `confidence="exact"`} {
		if !strings.Contains(output, want) {
			t.Fatalf("DOT output missing %q: %s", want, output)
		}
	}

	code, output, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "--json", "graph", "--committed", "symbol", "Caller")
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(output), "{") {
		t.Fatalf("--json graph compatibility code=%d stderr=%q output=%q", code, stderr, output)
	}

	code, output, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "--committed", "--format", "json", "file", "semantic.go")
	if code != 0 {
		t.Fatalf("committed file JSON code=%d stderr=%q output=%q", code, stderr, output)
	}
	var fileExport graphExportFixture
	if err := json.Unmarshal([]byte(output), &fileExport); err != nil {
		t.Fatalf("decode file JSON: %v\n%s", err, output)
	}
	if fileExport.Query.Kind != "file" || fileExport.Query.Value != "semantic.go" || fileExport.Source.Mode != "committed" || !hasGraphEdge(fileExport.Edges, "contains") {
		t.Fatalf("file export = %#v", fileExport)
	}

	before := directoryDigest(t, dataRoot)
	repo.Write(t, "working.go", "package graphexport\n\nfunc NewWorkingSymbol() int { return 7 }\n")
	code, output, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "--format", "json", "symbol", "NewWorkingSymbol")
	if code != 0 {
		t.Fatalf("working-tree symbol JSON code=%d stderr=%q output=%q", code, stderr, output)
	}
	var workingExport graphExportFixture
	if err := json.Unmarshal([]byte(output), &workingExport); err != nil {
		t.Fatalf("decode working-tree JSON: %v\n%s", err, output)
	}
	if workingExport.Source.Mode != "working-tree" || !hasGraphNode(workingExport.Nodes, "NewWorkingSymbol") {
		t.Fatalf("working-tree export = %#v", workingExport)
	}

	code, output, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "--format", "dot", "symbol", "NewWorkingSymbol")
	if code != 0 || !strings.Contains(output, `source_mode="working-tree"`) {
		t.Fatalf("working-tree DOT code=%d stderr=%q output=%q", code, stderr, output)
	}

	code, output, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "--committed", "--format", "json", "symbol", "NewWorkingSymbol")
	if code == 0 || !strings.Contains(stderr, "symbol") {
		t.Fatalf("committed graph unexpectedly found working symbol code=%d stderr=%q output=%q", code, stderr, output)
	}
	if after := directoryDigest(t, dataRoot); before != after {
		t.Fatalf("graph exports changed durable data: before=%s after=%s", before, after)
	}
}

type graphExportFixture struct {
	SchemaVersion int `json:"schema_version"`
	Query         struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	} `json:"query"`
	Source struct {
		Mode string `json:"mode"`
	} `json:"source"`
	Nodes []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"nodes"`
	Edges []struct {
		Kind string `json:"kind"`
	} `json:"edges"`
}

func hasGraphEdge(edges []struct {
	Kind string `json:"kind"`
}, kind string) bool {
	for _, edge := range edges {
		if edge.Kind == kind {
			return true
		}
	}
	return false
}

func hasGraphNode(nodes []struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}, name string) bool {
	for _, node := range nodes {
		if node.Name == name {
			return true
		}
	}
	return false
}
