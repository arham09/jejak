package git

import (
	"context"
	"reflect"
	"testing"

	"github.com/arham09/jejak/internal/testrepo"
)

func TestGitDiffParsesNULChanges(t *testing.T) {
	output := []byte("M\x00dir/with spaces.go\x00A\x00tab\tname.go\x00D\x00old.go\x00R087\x00before.go\x00after.go\x00T\x00mode.go\x00")
	changes, err := parseNameStatusDiff(output)
	if err != nil {
		t.Fatal(err)
	}
	want := []DiffChange{
		{Kind: ChangeAdded, NewPath: "tab\tname.go"},
		{Kind: ChangeRenamed, OldPath: "before.go", NewPath: "after.go", Score: 87},
		{Kind: ChangeModified, OldPath: "dir/with spaces.go", NewPath: "dir/with spaces.go"},
		{Kind: ChangeTypeChange, OldPath: "mode.go", NewPath: "mode.go"},
		{Kind: ChangeDeleted, OldPath: "old.go"},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %#v, want %#v", changes, want)
	}
}

func TestParseNameStatusDiffSupportsHistoricInlineTab(t *testing.T) {
	changes, err := parseNameStatusDiff([]byte("M\tfile.go\x00"))
	if err != nil {
		t.Fatal(err)
	}
	want := []DiffChange{{Kind: ChangeModified, OldPath: "file.go", NewPath: "file.go"}}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %#v, want %#v", changes, want)
	}
}

func TestClientDiffReportsCommittedTreeChanges(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "keep.go", "package fixture\n\nfunc Keep() {}\n")
	repo.Write(t, "delete.go", "package fixture\n")
	repo.Write(t, "rename.go", "package fixture\n\nfunc Rename() {}\n")
	first := repo.Commit(t, "first")
	repo.Write(t, "keep.go", "package fixture\n\nfunc KeepChanged() {}\n")
	repo.Write(t, "new file.go", "package fixture\n\nvar notTheSame = true\n")
	repo.Run(t, "mv", "rename.go", "renamed.go")
	repo.Run(t, "rm", "delete.go")
	second := repo.Commit(t, "second")

	changes, err := NewClient("git").Diff(context.Background(), repo.Root, first, second)
	if err != nil {
		t.Fatal(err)
	}
	byKind := make(map[ChangeKind]int)
	for _, change := range changes {
		byKind[change.Kind]++
	}
	if byKind[ChangeModified] != 1 || byKind[ChangeAdded] != 1 || byKind[ChangeDeleted] != 1 || byKind[ChangeRenamed] != 1 {
		t.Fatalf("change kinds = %#v, changes=%#v", byKind, changes)
	}
	if changes[0].OldPath == "" && changes[0].NewPath == "" {
		t.Fatalf("change has no path: %#v", changes[0])
	}
}

func TestParseWorktreeStatusPreservesIndexAndCheckoutStates(t *testing.T) {
	output := []byte("MM partial.go\x00AD staged-then-deleted.go\x00R  new.go\x00old.go\x00?? untracked.go\x00 D deleted.go\x00!! ignored.go\x00")
	changes, err := parseWorktreeStatus(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 5 {
		t.Fatalf("changes = %#v, want five non-ignored records", changes)
	}
	byPath := make(map[string]WorktreeChange, len(changes))
	for _, change := range changes {
		path := change.NewPath
		if path == "" {
			path = change.OldPath
		}
		byPath[path] = change
	}
	partial := byPath["partial.go"]
	if partial.Kind != ChangeModified || partial.IndexStatus != 'M' || partial.WorktreeStatus != 'M' || !partial.Tracked {
		t.Fatalf("partial status = %#v", partial)
	}
	stagedDelete := byPath["staged-then-deleted.go"]
	if stagedDelete.Kind != ChangeAdded || stagedDelete.IndexStatus != 'A' || stagedDelete.WorktreeStatus != 'D' || !stagedDelete.Tracked {
		t.Fatalf("staged-then-deleted status = %#v", stagedDelete)
	}
	rename := byPath["new.go"]
	if rename.Kind != ChangeRenamed || rename.OldPath != "old.go" || rename.IndexStatus != 'R' {
		t.Fatalf("rename status = %#v", rename)
	}
	untracked := byPath["untracked.go"]
	if untracked.Kind != ChangeAdded || untracked.OldPath != "" || untracked.NewPath != "untracked.go" || untracked.Tracked {
		t.Fatalf("untracked status = %#v", untracked)
	}
}

func TestClientWorktreeChangesExcludesIgnoredFiles(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/worktree\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package worktree\n\nfunc Answer() int { return 42 }\n")
	repo.Commit(t, "baseline")
	repo.Write(t, "main.go", "package worktree\n\nfunc Answer() int { return 43 }\n")
	repo.Write(t, "new.go", "package worktree\n\nfunc New() {}\n")
	repo.Write(t, ".gitignore", "ignored.go\n")
	repo.Write(t, "ignored.go", "package worktree\n\nfunc Ignored() {}\n")
	changes, err := NewClient("git").WorktreeChanges(context.Background(), repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range changes {
		path := change.NewPath
		if path == "" {
			path = change.OldPath
		}
		if path == "ignored.go" {
			t.Fatalf("ignored file appeared in worktree changes: %#v", changes)
		}
	}
	if len(changes) != 3 {
		t.Fatalf("worktree changes = %#v, want main.go, new.go, .gitignore", changes)
	}
}

func TestClientHeadDistinguishesUnbornRepository(t *testing.T) {
	repo := testrepo.New(t)
	head, known, err := NewClient("git").Head(context.Background(), repo.Root)
	if err != nil || known || head != "" {
		t.Fatalf("unborn HEAD = %q known=%t err=%v", head, known, err)
	}
	repo.Write(t, "main.go", "package fixture\n")
	repo.Commit(t, "head")
	head, known, err = NewClient("git").Head(context.Background(), repo.Root)
	if err != nil || !known || head == "" {
		t.Fatalf("committed HEAD = %q known=%t err=%v", head, known, err)
	}
}
