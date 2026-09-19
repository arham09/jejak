package integration

import (
	"strings"
	"testing"

	"github.com/arham09/jejak/internal/testrepo"
)

func TestStaleEdge(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/sync\n\ngo 1.27\n")
	repo.Write(t, "main.go", `package sync

func Answer() int { return 42 }

func Caller() int { return Answer() }
`)
	repo.Write(t, "one.go", "package sync\n\nfunc One() {}\n")
	repo.Write(t, "two.go", "package sync\n\nfunc Two() {}\n")
	repo.Write(t, "three.go", "package sync\n\nfunc Three() {}\n")
	repo.Write(t, "four.go", "package sync\n\nfunc Four() {}\n")
	first := repo.Commit(t, "initial graph")
	dataRoot := t.TempDir()
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks", "--no-agent-skills"); code != 0 {
		t.Fatalf("initial init code=%d stderr=%q", code, stderr)
	}
	repo.Write(t, "main.go", `package sync

func Answer() int { return 43 }
`)
	second := repo.Commit(t, "remove caller")
	if second == first {
		t.Fatal("sync fixture did not advance HEAD")
	}

	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "sync")
	if code != 0 {
		t.Fatalf("sync code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Graph synchronized.") || !strings.Contains(stdout, "Mode: incremental") || !strings.Contains(stdout, second) {
		t.Fatalf("sync output = %q", stdout)
	}
	code, stdout, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "symbol", "Answer")
	if code != 0 {
		t.Fatalf("answer query code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(stdout, "Caller calls") || strings.Contains(stdout, "Caller called") {
		t.Fatalf("stale Caller relationship survived sync: %q", stdout)
	}

	code, stdout, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "sync")
	if code != 0 || !strings.Contains(stdout, "Graph already synchronized.") || stderr != "" {
		t.Fatalf("no-op sync code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "sync", "--quiet")
	if code != 0 || stdout != "" {
		t.Fatalf("quiet sync code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "rebuild")
	if code != 0 {
		t.Fatalf("rebuild code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Graph rebuilt.") || !strings.Contains(stdout, "Mode: rebuild") {
		t.Fatalf("rebuild output = %q", stdout)
	}
}

func TestIncrementalMatchesRebuild(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/equal\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package equal\n\nfunc Answer() int { return 42 }\n\nfunc Caller() int { return Answer() }\n")
	for index := 0; index < 4; index++ {
		repo.Write(t, "extra"+string(rune('a'+index))+".go", "package equal\n")
	}
	repo.Commit(t, "initial graph")
	dataRoot := t.TempDir()
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks", "--no-agent-skills"); code != 0 {
		t.Fatalf("initial init code=%d stderr=%q", code, stderr)
	}
	repo.Write(t, "main.go", "package equal\n\nfunc Answer() int { return 43 }\n\nfunc Caller() int { return Answer() }\n")
	repo.Commit(t, "small source delta")
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "sync")
	if code != 0 || !strings.Contains(stdout, "Mode: incremental") {
		t.Fatalf("incremental sync code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, incrementalOutput, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "--committed", "symbol", "Answer")
	if code != 0 {
		t.Fatalf("incremental query code=%d stderr=%q", code, stderr)
	}
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "rebuild"); code != 0 {
		t.Fatalf("forced rebuild code=%d stderr=%q", code, stderr)
	}
	code, rebuildOutput, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "--committed", "symbol", "Answer")
	if code != 0 {
		t.Fatalf("rebuild query code=%d stderr=%q", code, stderr)
	}
	if normalizeGenerationOutput(incrementalOutput) != normalizeGenerationOutput(rebuildOutput) {
		t.Fatalf("incremental and rebuild outputs differ:\nincremental=%q\nrebuild=%q", incrementalOutput, rebuildOutput)
	}
}

func TestImplementationInvalidation(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/implementation\n\ngo 1.27\n")
	repo.Write(t, "main.go", `package implementation

type Runner interface { Run() int }
type runner struct{}
func (runner) Run() int { return 1 }
`)
	for index := 0; index < 4; index++ {
		repo.Write(t, "extra"+string(rune('a'+index))+".go", "package implementation\n")
	}
	repo.Commit(t, "implementation")
	dataRoot := t.TempDir()
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks", "--no-agent-skills"); code != 0 {
		t.Fatalf("initial init code=%d stderr=%q", code, stderr)
	}
	repo.Write(t, "main.go", "package implementation\n\ntype Runner interface { Run() int }\ntype runner struct{}\n")
	repo.Commit(t, "remove implementation")
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "sync")
	if code != 0 || !strings.Contains(stdout, "Mode: incremental") {
		t.Fatalf("implementation sync code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "symbol", "Runner")
	if code != 0 {
		t.Fatalf("Runner query code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(stdout, "runner implements") {
		t.Fatalf("stale implementation survived sync: %q", stdout)
	}
}

func TestSyncFailureRetainsLastActiveGeneration(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/syncfailure\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package syncfailure\n\nfunc Answer() int { return 42 }\n")
	first := repo.Commit(t, "initial graph")
	dataRoot := t.TempDir()
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks", "--no-agent-skills"); code != 0 {
		t.Fatalf("initial init code=%d stderr=%q", code, stderr)
	}
	repo.Write(t, "main.go", "package syncfailure\n\nfunc Answer( int { return 42 }\n")
	second := repo.Commit(t, "invalid source")
	code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "sync")
	if code == 0 || !strings.Contains(strings.ToLower(stderr), "analysis") {
		t.Fatalf("failed sync code=%d stderr=%q", code, stderr)
	}
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "status")
	if code != 0 {
		t.Fatalf("status code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "indexed HEAD "+first) || strings.Contains(stdout, "indexed HEAD "+second) || !strings.Contains(stdout, "status      failed") || !strings.Contains(stdout, "generation  1") {
		t.Fatalf("status after failed sync = %q", stdout)
	}
}

func TestSyncBuildInputChangeSelectsRebuild(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/syncbuild\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package syncbuild\n\nfunc Answer() int { return 42 }\n")
	repo.Commit(t, "initial graph")
	dataRoot := t.TempDir()
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks", "--no-agent-skills"); code != 0 {
		t.Fatalf("initial init code=%d stderr=%q", code, stderr)
	}
	repo.Write(t, "go.mod", "module example.com/syncbuild\n\ngo 1.27\n\n// build metadata changed\n")
	repo.Commit(t, "module metadata")
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "sync")
	if code != 0 || !strings.Contains(stdout, "Mode: rebuild") {
		t.Fatalf("build-input sync code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestMissedHook(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/missedhook\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package missedhook\n\nfunc Answer() int { return 42 }\n")
	for index := 0; index < 4; index++ {
		repo.Write(t, "extra"+string(rune('a'+index))+".go", "package missedhook\n")
	}
	repo.Commit(t, "initial")
	dataRoot := t.TempDir()
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks", "--no-agent-skills"); code != 0 {
		t.Fatalf("initial init code=%d stderr=%q", code, stderr)
	}
	repo.Write(t, "main.go", "package missedhook\n\nfunc Answer() int { return 43 }\n")
	second := repo.Commit(t, "commit without hook")
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "symbol", "Answer")
	if code != 0 {
		t.Fatalf("missed-hook query code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Commit "+second) || !strings.Contains(stdout, "Symbol Answer") {
		t.Fatalf("missed-hook query output = %q", stdout)
	}
}

func normalizeGenerationOutput(output string) string {
	lines := strings.Split(output, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(line, "Generation ") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
