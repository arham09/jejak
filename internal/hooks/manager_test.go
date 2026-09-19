package hooks

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arham09/jejak/internal/git"
	"github.com/arham09/jejak/internal/repository"
	"github.com/arham09/jejak/internal/testrepo"
)

func TestInstallChainsOriginalContractAndUninstallRestoresBytesAndMode(t *testing.T) {
	repo := newHookRepository(t)
	target := resolveHookTarget(t, repo.Root)
	client := git.NewClient("git")
	hooksPath, err := client.EffectiveHooksPath(context.Background(), repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hooksPath, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "original.log")
	original := []byte("#!/bin/sh\nprintf 'args=%s env=%s cwd=%s stdin=' \"$*\" \"$HOOK_ENV\" \"$PWD\" >> " + shellQuote(logPath) + "\ncat >> " + shellQuote(logPath) + "\nprintf '\\n' >> " + shellQuote(logPath) + "\nexit 7\n")
	hookPath := filepath.Join(hooksPath, "post-commit")
	if err := os.WriteFile(hookPath, original, 0o751); err != nil {
		t.Fatal(err)
	}
	fakeJejak := filepath.Join(t.TempDir(), "jejak")
	jejakLog := filepath.Join(t.TempDir(), "jejak.log")
	fakeScript := "#!/bin/sh\nprintf 'args=%s cwd=%s data=%s\\n' \"$*\" \"$PWD\" \"$JEJAK_DATA_MARKER\" >> " + shellQuote(jejakLog) + "\nexit 0\n"
	if err := os.WriteFile(fakeJejak, []byte(fakeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JEJAK_DATA_MARKER", "marker")
	dataRoot := filepath.Join(t.TempDir(), "jejak data")
	manager := NewManager(client, dataRoot)
	report, err := manager.Install(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if state := reportState(report, "post-commit"); state.Status != StatusInstalled {
		t.Fatalf("post-commit install state = %#v", state)
	}
	info, err := os.Stat(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("managed hook mode = %o, want 755", info.Mode().Perm())
	}

	command := exec.Command(hookPath, "argument with spaces", "second")
	command.Dir = repo.Root
	command.Env = append(os.Environ(), "JEJAK_BIN="+fakeJejak, "HOOK_ENV=preserved")
	command.Stdin = strings.NewReader("rewrite stdin\n")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	err = command.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("dispatcher exit = %v, want 7 (stderr=%q)", err, stderr.String())
	}
	originalLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(originalLog)
	if !strings.Contains(logText, "args=argument with spaces second") || !strings.Contains(logText, "env=preserved") || !strings.Contains(logText, "stdin=rewrite stdin") {
		t.Fatalf("original hook contract log = %q", logText)
	}
	jejakLogBytes, err := os.ReadFile(jejakLog)
	if err != nil {
		t.Fatal(err)
	}
	jejakText := string(jejakLogBytes)
	canonicalRoot, err := filepath.EvalSymlinks(repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jejakText, "--data-dir "+dataRoot) || !strings.Contains(jejakText, "--worktree "+canonicalRoot) || !strings.Contains(jejakText, "sync --quiet") {
		t.Fatalf("dispatcher sync invocation log = %q", jejakText)
	}

	secondReport, err := manager.Install(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if state := reportState(secondReport, "post-commit"); state.Status != StatusInstalled {
		t.Fatalf("repeated install state = %#v", state)
	}
	manifestPath := filepath.Join(dataRoot, "repos", string(target.Repository.ID), "hooks", "manifest.json")
	manifestBefore, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	thirdReport, err := manager.Install(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if state := reportState(thirdReport, "post-commit"); state.Status != StatusInstalled {
		t.Fatalf("third install state = %#v", state)
	}
	manifestAfter, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(manifestBefore, manifestAfter) {
		t.Fatalf("idempotent installation changed manifest:\nbefore=%safter=%s", manifestBefore, manifestAfter)
	}

	uninstallReport, err := manager.Uninstall(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if state := reportState(uninstallReport, "post-commit"); state.Status != StatusNotInstalled {
		t.Fatalf("uninstall state = %#v", state)
	}
	restored, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("restored hook bytes differ: got %q want %q", restored, original)
	}
	info, err = os.Stat(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o751 {
		t.Fatalf("restored hook mode = %o, want 751", info.Mode().Perm())
	}
	if _, err := os.Stat(manifestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manifest after uninstall stat error = %v, want absent", err)
	}
}

func TestUninstallRefusesModifiedManagedHook(t *testing.T) {
	repo := newHookRepository(t)
	target := resolveHookTarget(t, repo.Root)
	dataRoot := t.TempDir()
	manager := NewManager(git.NewClient("git"), dataRoot)
	if _, err := manager.Install(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	hooksPath, err := git.NewClient("git").EffectiveHooksPath(context.Background(), repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(hooksPath, "post-commit")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nuser edit\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	report, err := manager.Uninstall(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if state := reportState(report, "post-commit"); state.Status != StatusConflict {
		t.Fatalf("modified hook uninstall state = %#v", state)
	}
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("user edit")) {
		t.Fatal("modified managed hook was overwritten")
	}
}

func TestUnsupportedConfiguredHookPathIsSkipped(t *testing.T) {
	repo := newHookRepository(t)
	outside := filepath.Join(t.TempDir(), "shared hooks")
	repo.Run(t, "config", "core.hooksPath", outside)
	target := resolveHookTarget(t, repo.Root)
	manager := NewManager(git.NewClient("git"), t.TempDir())
	report, err := manager.Install(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range report.States {
		if state.Status != StatusSkipped {
			t.Fatalf("unsupported %s state = %#v", state.Name, state)
		}
	}
	if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported hook path was created: %v", err)
	}
}

func TestTrackedAndSymlinkedHooksArePreserved(t *testing.T) {
	repo := newHookRepository(t)
	trackedDir := filepath.Join(repo.Root, "tracked-hooks")
	if err := os.MkdirAll(trackedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repo.Run(t, "config", "core.hooksPath", "tracked-hooks")
	trackedPath := filepath.Join(trackedDir, "post-commit")
	trackedData := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(trackedPath, trackedData, 0o755); err != nil {
		t.Fatal(err)
	}
	repo.Commit(t, "tracked hook")
	target := resolveHookTarget(t, repo.Root)
	manager := NewManager(git.NewClient("git"), t.TempDir())
	report, err := manager.Install(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if state := reportState(report, "post-commit"); state.Status != StatusSkipped || !strings.Contains(state.Detail, "tracked") {
		t.Fatalf("tracked hook state = %#v", state)
	}
	got, err := os.ReadFile(trackedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, trackedData) {
		t.Fatal("tracked hook was changed")
	}

	// A symlinked hook is unsafe even when its target is inside the repository.
	repo.Run(t, "config", "core.hooksPath", ".git/hooks")
	hooksPath, err := git.NewClient("git").EffectiveHooksPath(context.Background(), repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	linkTarget := filepath.Join(t.TempDir(), "target-hook")
	if err := os.WriteFile(linkTarget, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(hooksPath, "post-commit")
	if err := os.Symlink(linkTarget, symlinkPath); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(symlinkPath)
	report, err = manager.Install(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if state := reportState(report, "post-commit"); state.Status != StatusSkipped {
		t.Fatalf("symlink hook state = %#v", state)
	}
	linkInfo, err := os.Lstat(symlinkPath)
	if err != nil {
		t.Fatal(err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlinked hook was replaced")
	}
}

func TestDispatcherSyncFailureDoesNotFailGitHook(t *testing.T) {
	dir := t.TempDir()
	hookPath := filepath.Join(dir, "post-merge")
	if err := os.WriteFile(hookPath, GenerateDispatcher("post-merge", "", filepath.Join(dir, "data")), 0o755); err != nil {
		t.Fatal(err)
	}
	failingJejak := filepath.Join(dir, "jejak")
	if err := os.WriteFile(failingJejak, []byte("#!/bin/sh\nexit 23\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(hookPath, "arg")
	command.Dir = dir
	command.Env = append(os.Environ(), "JEJAK_BIN="+failingJejak)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("failed synchronization changed Git hook status: %v", err)
	}
	if !strings.Contains(stderr.String(), "post-merge sync failed") {
		t.Fatalf("missing sync failure diagnostic: %q", stderr.String())
	}
}

func TestSharedHookRemainsUntilLastWorktreeUninstalls(t *testing.T) {
	base := newHookRepository(t)
	linkedPath := filepath.Join(t.TempDir(), "linked")
	linked := base.AddWorktree(t, linkedPath, "feature")
	baseTarget := resolveHookTarget(t, base.Root)
	linkedTarget := resolveHookTarget(t, linked.Root)
	dataRoot := t.TempDir()
	manager := NewManager(git.NewClient("git"), dataRoot)
	if _, err := manager.Install(context.Background(), baseTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), linkedTarget); err != nil {
		t.Fatal(err)
	}
	hooksPath, err := git.NewClient("git").EffectiveHooksPath(context.Background(), base.Root)
	if err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(hooksPath, "post-commit")
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatal(err)
	}
	baseUninstall, err := manager.Uninstall(context.Background(), baseTarget)
	if err != nil {
		t.Fatal(err)
	}
	if state := reportState(baseUninstall, "post-commit"); state.Status != StatusInstalled {
		t.Fatalf("base uninstall state = %#v", state)
	}
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatalf("shared hook removed while linked worktree owns it: %v", err)
	}
	if _, err := manager.Uninstall(context.Background(), linkedTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hookPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("last worktree uninstall left hook: %v", err)
	}
}

func newHookRepository(t *testing.T) *testrepo.Repository {
	t.Helper()
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/hooks\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package hooks\n\nfunc Answer() int { return 42 }\n")
	repo.Commit(t, "initial")
	return repo
}

func resolveHookTarget(t *testing.T, path string) repository.Target {
	t.Helper()
	target, err := repository.Resolve(context.Background(), git.NewClient("git"), path)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func reportState(report Report, name string) HookState {
	for _, state := range report.States {
		if state.Name == name {
			return state
		}
	}
	return HookState{Name: name, Status: StatusError, Detail: "missing state"}
}
