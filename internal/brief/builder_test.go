package brief

import (
	"context"
	"errors"
	"testing"

	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/impact"
)

type fakeSourceReader struct {
	blobs  map[string][]byte
	called []string
}

func (r *fakeSourceReader) ReadBlob(_ context.Context, _ string, objectID string) ([]byte, error) {
	r.called = append(r.called, objectID)
	contents, ok := r.blobs[objectID]
	if !ok {
		return nil, errors.New("blob missing")
	}
	return append([]byte(nil), contents...), nil
}

func TestContextSourceVersionAndBounds(t *testing.T) {
	source := &fakeSourceReader{blobs: map[string][]byte{"blob-a": []byte("package fixture\n\nfunc Answer() int {\n\treturn 42\n}\n")}}
	builder, err := NewBuilder(source, "/repo", Options{MaxLines: 2, MaxBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	report, err := builder.Build(context.Background(), impact.Report{Metadata: impact.Metadata{Commit: "commit-a", GenerationID: 4}, Context: impact.Radius{Items: []impact.Item{{Key: "symbol:answer", Name: "Answer", Kind: graph.NodeFunction, Path: "main.go", Symbol: graph.Symbol{Position: graph.Position{StartLine: 3, EndLine: 5}}, Source: impact.SourceRef{Path: "main.go", BlobSHA: "blob-a", Position: graph.Position{Path: "main.go", StartLine: 3, EndLine: 5}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(source.called) != 1 || source.called[0] != "blob-a" {
		t.Fatalf("blob calls = %#v", source.called)
	}
	if len(report.Excerpts) != 1 {
		t.Fatalf("excerpts = %#v", report.Excerpts)
	}
	excerpt := report.Excerpts[0]
	if excerpt.Commit != "commit-a" || excerpt.GenerationID != 4 || excerpt.BlobSHA != "blob-a" || excerpt.StartLine != 3 || excerpt.EndLine != 4 || !excerpt.Truncated {
		t.Fatalf("excerpt provenance/bounds = %#v", excerpt)
	}
	if excerpt.Text != "func Answer() int {\n\treturn 42" {
		t.Fatalf("excerpt text = %q", excerpt.Text)
	}
}

func TestContextReportsUnavailableBlob(t *testing.T) {
	builder, err := NewBuilder(&fakeSourceReader{blobs: map[string][]byte{}}, "/repo", Options{})
	if err != nil {
		t.Fatal(err)
	}
	report, err := builder.Build(context.Background(), impact.Report{Context: impact.Radius{Items: []impact.Item{{Key: "symbol:missing", Name: "Missing", Kind: graph.NodeFunction, Source: impact.SourceRef{Path: "missing.go", BlobSHA: "missing"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Excerpts) != 0 || len(report.Diagnostics) != 1 || report.Diagnostics[0].Code != "source-unavailable" {
		t.Fatalf("unavailable report = %#v", report)
	}
}

func TestContextSkipsPackageSourceWithoutBlob(t *testing.T) {
	builder, err := NewBuilder(&fakeSourceReader{blobs: map[string][]byte{}}, "/repo", Options{})
	if err != nil {
		t.Fatal(err)
	}
	report, err := builder.Build(context.Background(), impact.Report{Context: impact.Radius{Items: []impact.Item{{Key: "package:fixture", Name: "fixture", Kind: graph.NodePackage}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Excerpts) != 0 || len(report.Diagnostics) != 1 {
		t.Fatalf("package context report = %#v", report)
	}
}
