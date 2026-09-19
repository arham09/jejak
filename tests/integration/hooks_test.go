package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arham09/jejak/internal/git"
	"github.com/arham09/jejak/internal/hooks"
	"github.com/arham09/jejak/internal/repository"
)

func TestInitNoHooksAndExplicitHookLifecycleCommands(t *testing.T) {
	repo := newCommittedRepository(t, "github.com/acme/hooks-cli")
	dataRoot := filepath.Join(t.TempDir(), "data")
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "Hooks: disabled (--no-hooks)") {
		t.Fatalf("init --no-hooks code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	hooksPath, err := git.NewClient("git").EffectiveHooksPath(context.Background(), repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(hooksPath, "post-commit")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("--no-hooks created post-commit: %v", err)
	}
	code, stdout, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "hooks", "install")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "post-commit") || !strings.Contains(stdout, "installed") {
		t.Fatalf("hooks install code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "hooks", "status")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "post-rewrite") || !strings.Contains(stdout, "installed") {
		t.Fatalf("hooks status code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "hooks", "uninstall")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "not-installed") {
		t.Fatalf("hooks uninstall code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(hooksPath, "post-commit")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hooks uninstall left post-commit: %v", err)
	}
}

func TestGitCommitInvokesInstalledDispatcherInTriggeringWorktree(t *testing.T) {
	repo := newCommittedRepository(t, "github.com/acme/hooks-event")
	target, err := repository.Resolve(context.Background(), git.NewClient("git"), repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	dataRoot := t.TempDir()
	fakeLog := filepath.Join(t.TempDir(), "hook-events.log")
	fakeJejak := filepath.Join(t.TempDir(), "jejak")
	fake := "#!/bin/sh\nprintf 'cwd=%s args=%s\\n' \"$PWD\" \"$*\" >> " + quoteForShell(fakeLog) + "\nexit 0\n"
	if err := os.WriteFile(fakeJejak, []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JEJAK_BIN", fakeJejak)
	manager := hooks.NewManager(git.NewClient("git"), dataRoot)
	if _, err := manager.Install(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	repo.Write(t, "main.go", "package fixture\n\nfunc Answer() int { return 43 }\n")
	repo.Commit(t, "hook event")
	data, err := os.ReadFile(fakeLog)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	canonicalRoot, err := filepath.EvalSymlinks(repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "cwd="+canonicalRoot) || !strings.Contains(text, "sync --quiet") {
		t.Fatalf("hook event log = %q", text)
	}
}

func quoteForShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
