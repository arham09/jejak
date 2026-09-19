package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDataRootKeepsExplicitPathExact(t *testing.T) {
	repoRoot := t.TempDir()
	override := filepath.Join(t.TempDir(), "jejak data")
	got, err := ResolveDataRoot(override, repoRoot)
	if err != nil {
		t.Fatalf("ResolveDataRoot() error = %v", err)
	}
	want, err := filepath.Abs(override)
	if err != nil {
		t.Fatal(err)
	}
	// ResolveDataRoot canonicalizes existing ancestors so symlink aliases do
	// not bypass the repository/data-root containment check.
	if resolved, resolveErr := filepath.EvalSymlinks(filepath.Dir(want)); resolveErr == nil {
		want = filepath.Join(resolved, filepath.Base(want))
	}
	if got != filepath.Clean(want) {
		t.Fatalf("ResolveDataRoot() = %q, want %q", got, want)
	}
	if filepath.Base(got) != "jejak data" {
		t.Fatalf("explicit path unexpectedly gained a suffix: %q", got)
	}
}

func TestResolveDataRootUsesEnvironmentOnlyWhenNoOverride(t *testing.T) {
	repoRoot := t.TempDir()
	envRoot := filepath.Join(t.TempDir(), "from-env")
	explicitRoot := filepath.Join(t.TempDir(), "explicit")
	t.Setenv("JEJAK_DATA_DIR", envRoot)
	got, err := ResolveDataRoot("", repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(envRoot)
	if resolved, resolveErr := filepath.EvalSymlinks(filepath.Dir(want)); resolveErr == nil {
		want = filepath.Join(resolved, filepath.Base(want))
	}
	if got != filepath.Clean(want) {
		t.Fatalf("environment root = %q, want %q", got, want)
	}
	got, err = ResolveDataRoot(explicitRoot, repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	want, _ = filepath.Abs(explicitRoot)
	if resolved, resolveErr := filepath.EvalSymlinks(filepath.Dir(want)); resolveErr == nil {
		want = filepath.Join(resolved, filepath.Base(want))
	}
	if got != filepath.Clean(want) {
		t.Fatalf("explicit root = %q, want %q", got, want)
	}
}

func TestResolveDataRootRejectsRepositoryAndSymlinkAlias(t *testing.T) {
	repoRoot := t.TempDir()
	inside := filepath.Join(repoRoot, ".jejak")
	if _, err := ResolveDataRoot(inside, repoRoot); !errors.Is(err, ErrInvalidDataRoot) {
		t.Fatalf("inside root error = %v, want ErrInvalidDataRoot", err)
	}

	aliasParent := t.TempDir()
	alias := filepath.Join(aliasParent, "repo-alias")
	if err := os.Symlink(repoRoot, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ResolveDataRoot(filepath.Join(alias, "state"), repoRoot); !errors.Is(err, ErrInvalidDataRoot) {
		t.Fatalf("symlinked inside root error = %v, want ErrInvalidDataRoot", err)
	}
}

func TestRepositoryPathsForValidatesID(t *testing.T) {
	root := t.TempDir()
	paths, err := RepositoryPathsFor(root, "repo-123")
	if err != nil {
		t.Fatalf("RepositoryPathsFor() error = %v", err)
	}
	if got, want := paths.DB, filepath.Join(root, "repos", "repo-123", "graph.db"); got != want {
		t.Fatalf("DB path = %q, want %q", got, want)
	}
	for _, id := range []string{"", ".", "..", "../escape", "repo/id"} {
		if _, err := RepositoryPathsFor(root, id); !errors.Is(err, ErrInvalidDataRoot) {
			t.Errorf("RepositoryPathsFor(%q) error = %v, want ErrInvalidDataRoot", id, err)
		}
	}
}
