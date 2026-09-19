package integration

import (
	"strings"
	"testing"

	"github.com/arham09/jejak/internal/testrepo"
)

func TestResetRecoveryUsesEnsureGraphAfterHistoryMovesBack(t *testing.T) {
	repo := newLifecycleRepository(t)
	first := repo.Run(t, "rev-parse", "HEAD")
	dataRoot := t.TempDir()
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init"); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}
	repo.Write(t, "main.go", "package fixture\n\nfunc Answer() int { return 43 }\n")
	second := repo.Commit(t, "second")
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "sync", "--quiet"); code != 0 {
		t.Fatalf("sync code=%d stderr=%q", code, stderr)
	}
	if second == first {
		t.Fatal("fixture commit did not advance HEAD")
	}
	repo.Run(t, "reset", "--hard", first)
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "symbol", "Answer")
	if code != 0 {
		t.Fatalf("reset recovery query code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "Commit "+first) || strings.Contains(stdout, "Commit "+second) {
		t.Fatalf("reset recovery output = %q", stdout)
	}
}

func TestMergeRecoveryUsesCurrentMergedHead(t *testing.T) {
	repo := newLifecycleRepository(t)
	dataRoot := t.TempDir()
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init"); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}
	repo.Run(t, "checkout", "-q", "-b", "feature")
	repo.Write(t, "feature.go", "package fixture\n\nfunc Feature() int { return 1 }\n")
	repo.Commit(t, "feature")
	repo.Run(t, "checkout", "-q", "main")
	repo.Run(t, "merge", "--no-ff", "--no-edit", "feature")
	mergeHead := repo.Run(t, "rev-parse", "HEAD")
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "symbol", "Feature")
	if code != 0 {
		t.Fatalf("merge recovery query code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "Commit "+mergeHead) || !strings.Contains(stdout, "Symbol Feature") {
		t.Fatalf("merge recovery output = %q", stdout)
	}
}

func TestRebaseRecoveryUsesRewrittenHead(t *testing.T) {
	repo := newLifecycleRepository(t)
	dataRoot := t.TempDir()
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init"); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}
	repo.Run(t, "checkout", "-q", "-b", "feature")
	repo.Write(t, "feature.go", "package fixture\n\nfunc Feature() int { return 1 }\n")
	repo.Commit(t, "feature")
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "sync", "--quiet"); code != 0 {
		t.Fatalf("feature sync code=%d stderr=%q", code, stderr)
	}
	repo.Run(t, "checkout", "-q", "main")
	repo.Write(t, "main.go", "package fixture\n\nfunc Answer() int { return 43 }\n")
	repo.Commit(t, "main change")
	repo.Run(t, "checkout", "-q", "feature")
	repo.Run(t, "rebase", "main")
	rebasedHead := repo.Run(t, "rev-parse", "HEAD")
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "symbol", "Feature")
	if code != 0 {
		t.Fatalf("rebase recovery query code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "Commit "+rebasedHead) || !strings.Contains(stdout, "Symbol Feature") {
		t.Fatalf("rebase recovery output = %q", stdout)
	}
}

func newLifecycleRepository(t *testing.T) *testrepo.Repository {
	t.Helper()
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/lifecycle\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package fixture\n\nfunc Answer() int { return 42 }\n")
	repo.Commit(t, "initial")
	return repo
}
