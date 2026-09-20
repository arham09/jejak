package git

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arham09/jejak/internal/testrepo"
)

func TestManifestListsCommittedTreeWithoutMaterializing(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/manifest\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package manifest\n\nfunc Answer() int { return 41 }\n")
	commit := repo.Commit(t, "initial")
	// A dirty checkout must not reach the manifest, which reads Git objects.
	repo.Write(t, "main.go", "package manifest\n\nfunc Answer() int { return 99 }\n")

	client := NewClient("git")
	manifest, err := client.Manifest(context.Background(), repo.Root, commit)
	if err != nil {
		t.Fatalf("Manifest() error = %v", err)
	}
	if manifest.Commit != commit {
		t.Fatalf("manifest commit = %q, want %q", manifest.Commit, commit)
	}
	if len(manifest.Files) != 2 {
		t.Fatalf("manifest files = %d, want 2", len(manifest.Files))
	}
	byPath := make(map[string]SnapshotFile, len(manifest.Files))
	for _, file := range manifest.Files {
		byPath[file.Path] = file
	}
	main, ok := byPath["main.go"]
	if !ok {
		t.Fatal("manifest is missing main.go")
	}
	if main.BlobSHA == "" || main.Mode != "100644" {
		t.Fatalf("main.go entry = %+v", main)
	}
	// The size must describe the committed blob, not the dirty working file.
	if want := int64(len("package manifest\n\nfunc Answer() int { return 41 }\n")); main.Size != want {
		t.Fatalf("main.go size = %d, want %d", main.Size, want)
	}
}

func TestManifestReadObjectReturnsCommittedBytes(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/manifest\n\ngo 1.27\n")
	repo.Write(t, "go.work", "go 1.27\n\nuse .\n")
	commit := repo.Commit(t, "workspace")
	repo.Write(t, "go.work", "corrupted\n")

	client := NewClient("git")
	manifest, err := client.Manifest(context.Background(), repo.Root, commit)
	if err != nil {
		t.Fatal(err)
	}
	read := func(object string) ([]byte, error) { return client.ReadBlob(context.Background(), repo.Root, object) }
	contents, found, err := manifest.ReadObject(read, "go.work")
	if err != nil || !found {
		t.Fatalf("ReadObject() = %v, found=%v, err=%v", contents, found, err)
	}
	if string(contents) != "go 1.27\n\nuse .\n" {
		t.Fatalf("go.work contents = %q", contents)
	}
	if _, found, err := manifest.ReadObject(read, "absent.go"); err != nil || found {
		t.Fatalf("absent path found=%v, err=%v", found, err)
	}
}

func TestBatchObjectReaderStreamsBinaryAndRejectsMissing(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/batch\n\ngo 1.27\n")
	// Binary content with embedded newlines and NUL bytes exercises the
	// size-delimited framing of `git cat-file --batch`.
	binary := []byte{0x00, 0x01, '\n', 0xff, '\n', '\n', 0x7f}
	if err := os.WriteFile(filepath.Join(repo.Root, "blob.bin"), binary, 0o644); err != nil {
		t.Fatal(err)
	}
	commit := repo.Commit(t, "binary")

	client := NewClient("git")
	manifest, err := client.Manifest(context.Background(), repo.Root, commit)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := client.newObjectReader(context.Background(), repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()

	// Reading several objects through one process must keep the stream aligned.
	for _, file := range manifest.Files {
		contents, readErr := reader.object(file.BlobSHA)
		if readErr != nil {
			t.Fatalf("object(%s) error = %v", file.Path, readErr)
		}
		if int64(len(contents)) != file.Size {
			t.Fatalf("%s read %d bytes, want %d", file.Path, len(contents), file.Size)
		}
		if file.Path == "blob.bin" && !bytes.Equal(contents, binary) {
			t.Fatalf("blob.bin contents = %v", contents)
		}
	}
	if _, err := reader.object("0000000000000000000000000000000000000000"); err == nil {
		t.Fatal("expected a missing object to fail")
	}
	// A protocol failure leaves the stream at an unknown offset, so the reader
	// must refuse further work rather than return misaligned bytes.
	if _, err := reader.object(manifest.Files[0].BlobSHA); err == nil {
		t.Fatal("expected a failed reader to stay unusable")
	}
}

func TestSnapshotMaterializesTheSameBytesAsTheManifestDescribes(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/agree\n\ngo 1.27\n")
	repo.Write(t, "pkg/a.go", "package pkg\n\nfunc A() int { return 1 }\n")
	repo.Write(t, "pkg/b.go", "package pkg\n\nfunc B() int { return A() }\n")
	commit := repo.Commit(t, "initial")

	client := NewClient("git")
	manifest, err := client.Manifest(context.Background(), repo.Root, commit)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.Snapshot(context.Background(), repo.Root, commit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })

	if len(manifest.Files) != len(snapshot.Files) {
		t.Fatalf("manifest listed %d files, snapshot materialized %d", len(manifest.Files), len(snapshot.Files))
	}
	for index, listed := range manifest.Files {
		materialized := snapshot.Files[index]
		if listed.Path != materialized.Path || listed.BlobSHA != materialized.BlobSHA ||
			listed.Mode != materialized.Mode || listed.Size != materialized.Size {
			t.Fatalf("entry %d: manifest %+v, snapshot %+v", index, listed, materialized)
		}
	}
}
