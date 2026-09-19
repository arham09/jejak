package integration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arham09/jejak/internal/cli"
	"github.com/arham09/jejak/internal/git"
	"github.com/arham09/jejak/internal/repository"
	"github.com/arham09/jejak/internal/testrepo"
)

func TestRepositoryIsolationAndSelection(t *testing.T) {
	alpha := newCommittedRepository(t, "github.com/acme/alpha")
	beta := newCommittedRepository(t, "github.com/acme/beta")
	dataRoot := filepath.Join(t.TempDir(), "jejak data")

	before := alpha.Run(t, "status", "--porcelain", "--untracked-files=all")
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", alpha.Root, "init", "--no-agent-skills")
	if code != 0 {
		t.Fatalf("alpha init code=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "Graph: ready") || !strings.Contains(stdout, "github.com/acme/alpha") {
		t.Fatalf("alpha init output = %q", stdout)
	}
	if !strings.Contains(stdout, "Agent skills: disabled") {
		t.Fatalf("alpha init did not report disabled agent skills: %q", stdout)
	}
	if after := alpha.Run(t, "status", "--porcelain", "--untracked-files=all"); after != before {
		t.Fatalf("init changed alpha working tree: before=%q after=%q", before, after)
	}

	code, stdout, stderr = runCLI("--data-dir", dataRoot, "-C", beta.Root, "init")
	if code != 0 {
		t.Fatalf("beta init code=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "github.com/acme/beta") {
		t.Fatalf("beta init output = %q", stdout)
	}

	code, stdout, stderr = runCLI("--data-dir", dataRoot, "repos", "list")
	if code != 0 {
		t.Fatalf("repos list code=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "github.com/acme/alpha") || !strings.Contains(stdout, "github.com/acme/beta") {
		t.Fatalf("catalog output = %q", stdout)
	}

	code, stdout, stderr = runCLI("--data-dir", dataRoot, "-C", beta.Root, "status")
	if code != 0 {
		t.Fatalf("beta status code=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "Identity    github.com/acme/beta") || !strings.Contains(stdout, "status      ready") {
		t.Fatalf("beta status output = %q", stdout)
	}

	entries, err := os.ReadDir(filepath.Join(dataRoot, "repos"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("repository storage entries = %d, want 2", len(entries))
	}
}

func TestLinkedWorktreesHaveIndependentState(t *testing.T) {
	base := newCommittedRepository(t, "github.com/acme/worktree")
	linkedPath := filepath.Join(t.TempDir(), "linked worktree")
	linked := base.AddWorktree(t, linkedPath, "feature")
	dataRoot := filepath.Join(t.TempDir(), "data")

	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", base.Root, "init"); code != 0 {
		t.Fatalf("base init code=%d stderr=%q", code, stderr)
	}
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", linked.Root, "init"); code != 0 {
		t.Fatalf("linked init code=%d stderr=%q", code, stderr)
	}

	baseTarget, err := repository.Resolve(context.Background(), git.NewClient("git"), base.Root)
	if err != nil {
		t.Fatal(err)
	}
	linkedTarget, err := repository.Resolve(context.Background(), git.NewClient("git"), linked.Root)
	if err != nil {
		t.Fatal(err)
	}
	if baseTarget.Repository.ID != linkedTarget.Repository.ID {
		t.Fatalf("linked worktrees have different repository IDs: %s vs %s", baseTarget.Repository.ID, linkedTarget.Repository.ID)
	}
	if baseTarget.Worktree.ID == linkedTarget.Worktree.ID {
		t.Fatalf("linked worktrees share worktree ID %s", baseTarget.Worktree.ID)
	}

	code, stdout, stderr := runCLI("--data-dir", dataRoot, "repos", "list")
	if code != 0 {
		t.Fatalf("repos list code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "worktrees  2") {
		t.Fatalf("catalog output = %q", stdout)
	}
}

func TestSameRemoteIndependentClonesShareRepositoryButNotWorktreeState(t *testing.T) {
	first := newCommittedRepository(t, "github.com/acme/shared")
	second := newCommittedRepository(t, "github.com/acme/shared")
	dataRoot := filepath.Join(t.TempDir(), "data")

	for _, root := range []string{first.Root, second.Root} {
		if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", root, "init"); code != 0 {
			t.Fatalf("init %s code=%d stderr=%q", root, code, stderr)
		}
	}
	firstTarget, err := repository.Resolve(context.Background(), git.NewClient("git"), first.Root)
	if err != nil {
		t.Fatal(err)
	}
	secondTarget, err := repository.Resolve(context.Background(), git.NewClient("git"), second.Root)
	if err != nil {
		t.Fatal(err)
	}
	if firstTarget.Repository.ID != secondTarget.Repository.ID {
		t.Fatalf("same remote produced different repository IDs: %s vs %s", firstTarget.Repository.ID, secondTarget.Repository.ID)
	}
	if firstTarget.Worktree.ID == secondTarget.Worktree.ID {
		t.Fatalf("independent clones produced the same worktree ID: %s", firstTarget.Worktree.ID)
	}

	code, stdout, stderr := runCLI("--data-dir", dataRoot, "repos", "list")
	if code != 0 {
		t.Fatalf("repos list code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "worktrees  2") {
		t.Fatalf("catalog output = %q", stdout)
	}
}

func TestUnbornRepositoryIsExplicitlyUnindexed(t *testing.T) {
	repo := testrepo.New(t)
	dataRoot := filepath.Join(t.TempDir(), "data")
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init")
	if code != 0 {
		t.Fatalf("unborn init code=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "HEAD: (unborn)") || !strings.Contains(stdout, "Graph: unindexed") {
		t.Fatalf("unborn init output = %q", stdout)
	}
	code, stdout, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "status")
	if code != 0 {
		t.Fatalf("unborn status code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "HEAD        (unborn)") || !strings.Contains(stdout, "status      unindexed") {
		t.Fatalf("unborn status output = %q", stdout)
	}
}

func TestDataRootInsideRepositoryIsRejected(t *testing.T) {
	repo := newCommittedRepository(t, "github.com/acme/invalid")
	inside := filepath.Join(repo.Root, ".jejak")
	code, _, stderr := runCLI("--data-dir", inside, "-C", repo.Root, "init")
	if code != 1 || !strings.Contains(stderr, "invalid Jejak data root") {
		t.Fatalf("invalid data root code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Fatalf("rejected data root was created: stat error=%v", err)
	}
}

func TestStatusDoesNotCreateAnUnregisteredRepositoryStore(t *testing.T) {
	repo := newCommittedRepository(t, "github.com/acme/status")
	dataRoot := filepath.Join(t.TempDir(), "data")
	code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "status")
	if code != 1 || !strings.Contains(stderr, "not registered") {
		t.Fatalf("status code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "repos")); !os.IsNotExist(err) {
		t.Fatalf("status created data root: stat error=%v", err)
	}
}

func TestStatusReportsCurrentGitHeadAfterACommit(t *testing.T) {
	repo := newCommittedRepository(t, "github.com/acme/status-head")
	firstHead := repo.Run(t, "rev-parse", "HEAD")
	dataRoot := filepath.Join(t.TempDir(), "data")
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks"); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}
	repo.Write(t, "main.go", "package fixture\n\nfunc Answer() int { return 43 }\n")
	secondHead := repo.Commit(t, "second")
	if secondHead == firstHead {
		t.Fatal("fixture commit did not advance HEAD")
	}
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "status")
	if code != 0 {
		t.Fatalf("status code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "HEAD        "+secondHead) || !strings.Contains(stdout, "status      stale") {
		t.Fatalf("status output = %q", stdout)
	}
}

func TestCatalogReportsUnreadableEntryWithoutHidingHealthyEntries(t *testing.T) {
	repo := newCommittedRepository(t, "github.com/acme/healthy")
	dataRoot := filepath.Join(t.TempDir(), "data")
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init"); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}
	if err := os.MkdirAll(filepath.Join(dataRoot, "repos", "broken-entry"), 0o700); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "repos", "list")
	if code != 1 {
		t.Fatalf("catalog code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "github.com/acme/healthy") || !strings.Contains(stderr, "unreadable repository store") {
		t.Fatalf("catalog output stdout=%q stderr=%q", stdout, stderr)
	}
}

func newCommittedRepository(t *testing.T, remote string) *testrepo.Repository {
	t.Helper()
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/fixture\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package fixture\n\nfunc Answer() int { return 42 }\n")
	repo.Commit(t, "initial")
	repo.AddRemote(t, "origin", "https://"+remote+".git")
	return repo
}

func runCLI(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}
