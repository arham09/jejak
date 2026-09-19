package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arham09/jejak/internal/testrepo"
)

func TestEffectiveHooksPathHonorsConfiguredAndLinkedPaths(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "main.go", "package fixture\n\nfunc Answer() int { return 42 }\n")
	repo.Commit(t, "initial")
	client := NewClient("git")

	defaultPath, err := client.EffectiveHooksPath(context.Background(), repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot, err := filepath.EvalSymlinks(repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	wantDefault := filepath.Join(wantRoot, ".git", "hooks")
	if defaultPath != wantDefault {
		t.Fatalf("default hooks path = %q, want %q", defaultPath, wantDefault)
	}

	custom := filepath.Join(wantRoot, "custom hooks")
	relativeCustom, err := filepath.Rel(wantRoot, custom)
	if err != nil {
		t.Fatal(err)
	}
	repo.Run(t, "config", "core.hooksPath", relativeCustom)
	configuredPath, err := client.EffectiveHooksPath(context.Background(), repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	if configuredPath != custom {
		t.Fatalf("configured hooks path = %q, want %q", configuredPath, custom)
	}

	linkedPath := filepath.Join(t.TempDir(), "linked")
	linked := repo.AddWorktree(t, linkedPath, "feature")
	linkedPath, err = client.EffectiveHooksPath(context.Background(), linked.Root)
	if err != nil {
		t.Fatal(err)
	}
	linkedRoot, err := filepath.EvalSymlinks(linked.Root)
	if err != nil {
		t.Fatal(err)
	}
	wantLinked := filepath.Join(linkedRoot, "custom hooks")
	if linkedPath != wantLinked {
		t.Fatalf("linked configured hooks path = %q, want %q", linkedPath, wantLinked)
	}
}

func TestIsTrackedDistinguishesRepositoryFilesAndExternalPaths(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "tracked-hook", "#!/bin/sh\n")
	repo.Commit(t, "tracked")
	client := NewClient("git")

	tracked, err := client.IsTracked(context.Background(), repo.Root, filepath.Join(repo.Root, "tracked-hook"))
	if err != nil {
		t.Fatal(err)
	}
	if !tracked {
		t.Fatal("tracked file reported as untracked")
	}
	untrackedPath := filepath.Join(repo.Root, "untracked-hook")
	if err := os.WriteFile(untrackedPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	untracked, err := client.IsTracked(context.Background(), repo.Root, untrackedPath)
	if err != nil {
		t.Fatal(err)
	}
	if untracked {
		t.Fatal("untracked file reported as tracked")
	}
	external, err := client.IsTracked(context.Background(), repo.Root, filepath.Join(t.TempDir(), "outside"))
	if err != nil {
		t.Fatal(err)
	}
	if external {
		t.Fatal("external path reported as tracked")
	}
}
