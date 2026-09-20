package golang

import (
	"context"
	"testing"

	"github.com/arham09/jejak/internal/git"
	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
	"github.com/arham09/jejak/internal/testrepo"
)

// manifestInput lists a committed tree the way the CLI wiring does.
func manifestInput(t *testing.T, root, commit string, build graph.BuildConfig) graph.ManifestInput {
	t.Helper()
	client := git.NewClient("git")
	target, err := repository.Resolve(context.Background(), client, root)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := client.Manifest(context.Background(), root, commit)
	if err != nil {
		t.Fatal(err)
	}
	files := make([]graph.SnapshotFile, 0, len(listed.Files))
	for _, file := range listed.Files {
		files = append(files, graph.SnapshotFile{Path: file.Path, BlobSHA: file.BlobSHA, ObjectFormat: file.ObjectFormat, Mode: file.Mode, Size: file.Size})
	}
	read := func(path string) ([]byte, error) {
		contents, found, readErr := listed.ReadObject(func(object string) ([]byte, error) {
			return client.ReadBlob(context.Background(), root, object)
		}, path)
		if readErr != nil || !found {
			return nil, readErr
		}
		return contents, nil
	}
	manifest := &graph.Manifest{Commit: graph.CommitSHA(listed.Commit), ObjectFormat: listed.ObjectFormat, Files: files, ReadFile: read}
	return graph.ManifestInput{Repository: target.Repository.ID, Worktree: target.Worktree.ID, Commit: graph.CommitSHA(commit), Manifest: manifest, Build: build}
}

// The manager decides to reuse a generation from a listing but records the
// fingerprint of a materialized tree. If the two ever disagree, every command
// would rebuild the graph, so this equality is the load-bearing invariant of
// the cheap reuse path.
func TestManifestFingerprintMatchesSnapshotFingerprint(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, repo *testrepo.Repository)
		build graph.BuildConfig
	}{
		{
			name: "single module",
			setup: func(t *testing.T, repo *testrepo.Repository) {
				repo.Write(t, "go.mod", "module example.com/fp\n\ngo 1.27\n")
				repo.Write(t, "pkg/a.go", "package pkg\n\nfunc A() int { return 1 }\n")
			},
		},
		{
			name: "workspace",
			setup: func(t *testing.T, repo *testrepo.Repository) {
				repo.Write(t, "go.mod", "module example.com/fp\n\ngo 1.27\n")
				repo.Write(t, "go.work", "go 1.27\n\nuse .\n")
				repo.Write(t, "pkg/a.go", "package pkg\n\nfunc A() int { return 1 }\n")
			},
		},
		{
			name: "non-go assets and tests",
			setup: func(t *testing.T, repo *testrepo.Repository) {
				repo.Write(t, "go.mod", "module example.com/fp\n\ngo 1.27\n")
				repo.Write(t, "pkg/a.go", "package pkg\n\nfunc A() int { return 1 }\n")
				repo.Write(t, "pkg/a_test.go", "package pkg\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { _ = A() }\n")
				repo.Write(t, "assets/data.json", "{\"key\":\"value\"}\n")
			},
			build: graph.BuildConfig{IncludeTests: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := testrepo.New(t)
			test.setup(t, repo)
			commit := repo.Commit(t, "fingerprint")

			snapshot, cleanup := snapshotInput(t, repo.Root, commit)
			defer cleanup()
			snapshot.Build = test.build

			analyzer := New()
			fromSnapshot, err := analyzer.BuildFingerprint(context.Background(), snapshot)
			if err != nil {
				t.Fatal(err)
			}
			fromManifest, err := analyzer.ManifestFingerprint(context.Background(), manifestInput(t, repo.Root, commit, test.build))
			if err != nil {
				t.Fatal(err)
			}
			if fromSnapshot != fromManifest {
				t.Fatalf("snapshot fingerprint %s != manifest fingerprint %s", fromSnapshot, fromManifest)
			}
		})
	}
}

func TestManifestFingerprintChangesWithCommittedSource(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/fp\n\ngo 1.27\n")
	repo.Write(t, "pkg/a.go", "package pkg\n\nfunc A() int { return 1 }\n")
	first := repo.Commit(t, "first")
	repo.Write(t, "pkg/a.go", "package pkg\n\nfunc A() int { return 2 }\n")
	second := repo.Commit(t, "second")

	analyzer := New()
	before, err := analyzer.ManifestFingerprint(context.Background(), manifestInput(t, repo.Root, first, graph.BuildConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	after, err := analyzer.ManifestFingerprint(context.Background(), manifestInput(t, repo.Root, second, graph.BuildConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("changed committed source produced an unchanged fingerprint")
	}
}

func TestManifestFingerprintIgnoresDirtyWorkingTree(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/fp\n\ngo 1.27\n")
	repo.Write(t, "pkg/a.go", "package pkg\n\nfunc A() int { return 1 }\n")
	commit := repo.Commit(t, "committed")

	analyzer := New()
	clean, err := analyzer.ManifestFingerprint(context.Background(), manifestInput(t, repo.Root, commit, graph.BuildConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	repo.Write(t, "pkg/a.go", "package pkg\n\nfunc A() int { return 999 }\n")
	dirty, err := analyzer.ManifestFingerprint(context.Background(), manifestInput(t, repo.Root, commit, graph.BuildConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	if clean != dirty {
		t.Fatal("uncommitted edits changed the committed build fingerprint")
	}
}

func TestManifestFingerprintIncludesBuildSelection(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/fp\n\ngo 1.27\n")
	repo.Write(t, "pkg/a.go", "package pkg\n\nfunc A() int { return 1 }\n")
	commit := repo.Commit(t, "committed")

	analyzer := New()
	base, err := analyzer.ManifestFingerprint(context.Background(), manifestInput(t, repo.Root, commit, graph.BuildConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	tagged, err := analyzer.ManifestFingerprint(context.Background(), manifestInput(t, repo.Root, commit, graph.BuildConfig{Tags: []string{"integration"}}))
	if err != nil {
		t.Fatal(err)
	}
	if base == tagged {
		t.Fatal("build tags did not change the fingerprint")
	}
	withTests, err := analyzer.ManifestFingerprint(context.Background(), manifestInput(t, repo.Root, commit, graph.BuildConfig{IncludeTests: true}))
	if err != nil {
		t.Fatal(err)
	}
	if base == withTests {
		t.Fatal("--include-tests did not change the fingerprint")
	}
}
