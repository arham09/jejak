package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arham09/jejak/internal/testrepo"
)

func TestInitialBuildAndGraphInspection(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/initial\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package initial\n\nfunc Answer() int { return 42 }\n")
	commit := repo.Commit(t, "initial")
	dataRoot := t.TempDir()
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init")
	if code != 0 {
		t.Fatalf("init code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Graph: ready") || !strings.Contains(stdout, "Packages") || !strings.Contains(stdout, "Graph ready.") {
		t.Fatalf("init output = %q", stdout)
	}
	if !strings.Contains(stdout, commit) {
		t.Fatalf("init output does not identify HEAD %s: %q", commit, stdout)
	}
	canonicalSkill := filepath.Join(repo.Root, ".claude", "skills", "jejak")
	contents, err := os.ReadFile(filepath.Join(canonicalSkill, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "name: jejak") {
		t.Fatalf("canonical skill content = %q", contents)
	}
	if info, err := os.Lstat(canonicalSkill); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("Claude skill path = %#v err=%v", info, err)
	}
	linkPath := filepath.Join(repo.Root, ".agents", "skills", "jejak")
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("Codex skill path is not a symlink")
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(target) {
		t.Fatalf("Codex skill target %q is absolute", target)
	}
	canonicalRoot, err := filepath.EvalSymlinks(repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	canonicalSkill = filepath.Join(canonicalRoot, ".claude", "skills", "jejak")
	if resolved != canonicalSkill {
		t.Fatalf("Codex resolves to %q, want %q", resolved, canonicalSkill)
	}
	if _, err := os.Stat(filepath.Join(repo.Root, ".jejak")); !os.IsNotExist(err) {
		t.Fatalf("legacy .jejak path exists: %v", err)
	}
	repo.Write(t, "main.go", "package initial\n\nfunc Answer() int { return 99 }\n")
	code, stdout, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "symbol", "Answer")
	if code != 0 {
		t.Fatalf("graph symbol code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Symbol Answer") || !strings.Contains(stdout, "func() int") {
		t.Fatalf("graph symbol output = %q", stdout)
	}
	code, stdout, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "file", "main.go")
	if code != 0 {
		t.Fatalf("graph file code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "File main.go") || !strings.Contains(stdout, "Answer") {
		t.Fatalf("graph file output = %q", stdout)
	}
	code, stdout, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "init")
	if code != 0 {
		t.Fatalf("repeated init code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Packages       1") {
		t.Fatalf("repeated init counts = %q", stdout)
	}
	if !strings.Contains(stdout, "unchanged") {
		t.Fatalf("repeated init did not report idempotent skill integration: %q", stdout)
	}
}

func TestSemanticGraphInspectionShowsCallsImplementationsAndTests(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/semanticcli\n\ngo 1.27\n")
	repo.Write(t, "semantic.go", `package semanticcli

type Runner interface {
	Run() int
}

type runner struct{}

func (runner) Run() int { return helper() }

func helper() int { return 42 }

func Caller() int { return helper() }
`)
	repo.Write(t, "semantic_test.go", `package semanticcli

import "testing"

func TestCaller(t *testing.T) {
	t.Helper()
	if Caller() != 42 {
		t.Fatal("unexpected result")
	}
}
`)
	repo.Commit(t, "semantic graph")
	dataRoot := t.TempDir()
	if code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--include-tests"); code != 0 {
		t.Fatalf("semantic init code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "--include-tests", "graph", "symbol", "Caller")
	if code != 0 {
		t.Fatalf("caller graph code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "calls") || !strings.Contains(stdout, "helper calls exact") || !strings.Contains(stdout, "tests") || !strings.Contains(stdout, "TestCaller tests exact") {
		t.Fatalf("caller semantic output = %q", stdout)
	}

	code, stdout, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "--include-tests", "graph", "symbol", "Runner")
	if code != 0 {
		t.Fatalf("interface graph code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "implemented by") || !strings.Contains(stdout, "runner implements exact") {
		t.Fatalf("interface semantic output = %q", stdout)
	}

	code, stdout, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "--include-tests", "graph", "symbol", "helper")
	if code != 0 {
		t.Fatalf("helper graph code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "called by") || !strings.Contains(stdout, "Caller calls exact") {
		t.Fatalf("helper inverse semantic output = %q", stdout)
	}
}

func TestFailedAnalysisRetainsPreviousGeneration(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/failure\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package failure\n\nfunc Answer() int { return 42 }\n")
	firstCommit := repo.Commit(t, "initial")
	dataRoot := t.TempDir()
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init"); code != 0 {
		t.Fatalf("initial init code=%d stderr=%q", code, stderr)
	}
	repo.Write(t, "main.go", "package failure\n\nfunc Answer( int { return 42 }\n")
	secondCommit := repo.Commit(t, "invalid source")
	if secondCommit == firstCommit {
		t.Fatal("invalid-source fixture did not advance HEAD")
	}
	code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init")
	if code == 0 || !strings.Contains(strings.ToLower(stderr), "analysis") {
		t.Fatalf("failed init code=%d stderr=%q", code, stderr)
	}
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "status")
	if code != 0 {
		t.Fatalf("status after failed init code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "indexed HEAD "+firstCommit) || !strings.Contains(stdout, "status      failed") || !strings.Contains(stdout, "generation  1") {
		t.Fatalf("status after failed init = %q", stdout)
	}
}
