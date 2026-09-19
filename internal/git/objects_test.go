package git

import (
	"context"
	"testing"

	"github.com/arham09/jejak/internal/testrepo"
)

func TestObjectAvailabilityChecksAreReadOnlyAndTypeAware(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "README.md", "fixture\n")
	commit := repo.Commit(t, "initial")
	blob := repo.Run(t, "rev-parse", "HEAD:README.md")
	client := NewClient("git")
	ctx := context.Background()

	if exists, err := client.CommitExists(ctx, repo.Root, commit); err != nil || !exists {
		t.Fatalf("CommitExists(existing) = %t, %v", exists, err)
	}
	if exists, err := client.CommitExists(ctx, repo.Root, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"); err != nil || exists {
		t.Fatalf("CommitExists(missing) = %t, %v", exists, err)
	}
	if exists, err := client.BlobExists(ctx, repo.Root, blob); err != nil || !exists {
		t.Fatalf("BlobExists(existing) = %t, %v", exists, err)
	}
	if exists, err := client.BlobExists(ctx, repo.Root, commit); err != nil || exists {
		t.Fatalf("BlobExists(commit object) = %t, %v", exists, err)
	}
	if exists, err := client.BlobExists(ctx, repo.Root, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"); err != nil || exists {
		t.Fatalf("BlobExists(missing) = %t, %v", exists, err)
	}
}
