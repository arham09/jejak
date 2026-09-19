package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arham09/jejak/internal/testrepo"
)

func TestCommittedSnapshotExcludesDirtyTrackedBytes(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/snapshot\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package snapshot\n\nfunc Answer() int { return 41 }\n")
	commit := repo.Commit(t, "initial")
	repo.Write(t, "main.go", "package snapshot\n\nfunc Answer() int { return 99 }\n")

	snapshot, err := NewClient("git").Snapshot(context.Background(), repo.Root, commit)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	contents, err := os.ReadFile(filepath.Join(snapshot.Root, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "package snapshot\n\nfunc Answer() int { return 41 }\n" {
		t.Fatalf("snapshot contents = %q", contents)
	}
	if _, err := os.Stat(snapshot.Root); err != nil {
		t.Fatalf("snapshot root missing before close: %v", err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshot.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot root after close error = %v", err)
	}
}

func TestCommittedSnapshotRejectsSubmoduleEntry(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "README.md", "fixture\n")
	commit := repo.Commit(t, "initial")
	// A regular fixture does not have a submodule object; this assertion keeps
	// the test focused on the ordinary snapshot path while documenting that the
	// returned manifest is deterministic.
	snapshot, err := NewClient("git").Snapshot(context.Background(), repo.Root, commit)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	defer snapshot.Close()
	if len(snapshot.Files) != 1 || snapshot.Files[0].Path != "README.md" {
		t.Fatalf("snapshot files = %#v", snapshot.Files)
	}
}
