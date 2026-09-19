package overlay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/arham09/jejak/internal/git"
	"github.com/arham09/jejak/internal/graph"
)

type manifestAnalyzer struct{}

func (manifestAnalyzer) Supports(path string) bool {
	return filepath.Ext(path) == ".go" || filepath.Base(path) == "go.mod"
}

func (manifestAnalyzer) Analyze(context.Context, graph.AnalyzeInput) (graph.AnalysisResult, error) {
	return graph.AnalysisResult{}, nil
}

func TestCaptureManifestUsesCurrentBytesAndRetainsStatusProvenance(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	statuses := []git.WorktreeChange{
		{IndexStatus: 'M', WorktreeStatus: 'M', Kind: git.ChangeModified, OldPath: "main.go", NewPath: "main.go", Tracked: true},
		{IndexStatus: 'A', WorktreeStatus: 'D', Kind: git.ChangeAdded, NewPath: "gone.go", Tracked: true},
		{IndexStatus: '?', WorktreeStatus: '?', Kind: git.ChangeAdded, NewPath: "notes.txt", Tracked: false},
	}
	manifest, err := captureManifest(context.Background(), root, statuses, manifestAnalyzer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 2 || len(manifest.Changes) != 2 {
		t.Fatalf("manifest = %#v", manifest)
	}
	entries := make(map[string]ManifestEntry, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		entries[entry.Path] = entry
	}
	mainEntry := entries["main.go"]
	if mainEntry.Path != "main.go" || mainEntry.IndexStatus != 'M' || mainEntry.WorktreeStatus != 'M' || !mainEntry.Present || !mainEntry.Eligible {
		t.Fatalf("main entry = %#v", mainEntry)
	}
	sum := sha256.Sum256([]byte("package fixture\n"))
	if mainEntry.ContentSHA != hex.EncodeToString(sum[:]) {
		t.Fatalf("main content hash = %q", mainEntry.ContentSHA)
	}
	goneEntry := entries["gone.go"]
	if goneEntry.Path != "gone.go" || goneEntry.Present || goneEntry.WorktreeStatus != 'D' {
		t.Fatalf("gone entry = %#v", goneEntry)
	}
	if manifest.ID == "" || manifest.ID != manifestHash(manifest) {
		t.Fatalf("manifest identity = %q", manifest.ID)
	}
}

func TestCaptureManifestRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	_, err := captureManifest(context.Background(), root, []git.WorktreeChange{{IndexStatus: '?', WorktreeStatus: '?', Kind: git.ChangeAdded, NewPath: "../escape.go", Tracked: false}}, manifestAnalyzer{})
	if err == nil {
		t.Fatal("unsafe manifest path unexpectedly succeeded")
	}
}

func TestSyntheticBlobSHAIsNamespacedAndDeterministic(t *testing.T) {
	first := SyntheticBlobSHA([]byte("same"))
	second := SyntheticBlobSHA([]byte("same"))
	if first != second || first[:len("overlay:sha256:")] != "overlay:sha256:" {
		t.Fatalf("synthetic blob IDs = %q, %q", first, second)
	}
	if first == SyntheticBlobSHA([]byte("different")) {
		t.Fatal("different bytes share synthetic blob ID")
	}
}
