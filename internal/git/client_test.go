package git

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/arham09/jejak/internal/testrepo"
)

func TestDiscoverCommittedRepository(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "README.md", "fixture\n")
	commit := repo.Commit(t, "initial")
	repo.AddRemote(t, "origin", "git@github.com:company/fixture.git")

	info, err := NewClient("git").Discover(context.Background(), repo.Root)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	wantRoot, err := filepath.EvalSymlinks(repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Root != wantRoot || !info.HeadKnown || info.Head != commit {
		t.Fatalf("repository info = %#v", info)
	}
	if len(info.Remotes) != 1 || info.Remotes[0].Name != "origin" {
		t.Fatalf("remotes = %#v", info.Remotes)
	}
}

func TestDiscoverUnbornRepository(t *testing.T) {
	repo := testrepo.New(t)
	info, err := NewClient("git").Discover(context.Background(), repo.Root)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if info.HeadKnown || info.Head != "" {
		t.Fatalf("unborn repository info = %#v", info)
	}
}

func TestDiscoverRejectsNonRepository(t *testing.T) {
	path := t.TempDir()
	_, err := NewClient("git").Discover(context.Background(), path)
	if !errors.Is(err, ErrNotRepository) {
		t.Fatalf("Discover() error = %v, want ErrNotRepository", err)
	}
}
