// Package testrepo provides disposable real-Git repositories for integration
// tests. It is test infrastructure and must not be imported by production
// packages.
package testrepo

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Repository is a temporary Git repository created for one test.
type Repository struct {
	Root string
}

// New creates an initialized repository with isolated author configuration.
func New(t *testing.T) *Repository {
	t.Helper()
	repo := &Repository{Root: t.TempDir()}
	repo.run(t, "init", "-q", "-b", "main")
	repo.run(t, "config", "user.name", "Jejak Test")
	repo.run(t, "config", "user.email", "jejak-test@example.invalid")
	repo.run(t, "config", "commit.gpgSign", "false")
	t.Cleanup(func() { _ = os.RemoveAll(repo.Root) })
	return repo
}

// Write writes a repository-relative file, creating parent directories.
func (r *Repository) Write(t *testing.T, relativePath, contents string) {
	t.Helper()
	path := filepath.Join(r.Root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
}

// Commit stages all changes and returns the new commit SHA.
func (r *Repository) Commit(t *testing.T, message string) string {
	t.Helper()
	r.run(t, "add", "--all")
	r.run(t, "commit", "--no-gpg-sign", "-m", message)
	return r.run(t, "rev-parse", "HEAD")
}

// AddRemote adds a named remote URL.
func (r *Repository) AddRemote(t *testing.T, name, remoteURL string) {
	t.Helper()
	r.run(t, "remote", "add", name, remoteURL)
}

// AddWorktree creates a linked worktree at path and returns it as a fixture
// repository handle.
func (r *Repository) AddWorktree(t *testing.T, path, branch string) *Repository {
	t.Helper()
	r.run(t, "worktree", "add", "-q", "-b", branch, path, "HEAD")
	worktree := &Repository{Root: path}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	return worktree
}

// Run executes Git in this repository and returns trimmed stdout.
func (r *Repository) Run(t *testing.T, args ...string) string {
	t.Helper()
	return r.run(t, args...)
}

func (r *Repository) run(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, stderr.String())
	}
	return string(bytes.TrimSpace(stdout.Bytes()))
}
