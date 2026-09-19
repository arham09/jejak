// Package git contains the small, cancellable adapter Jejak uses for the
// installed Git executable.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNotRepository indicates that a path is not inside a Git worktree.
var ErrNotRepository = errors.New("not a Git repository")

// ErrUnborn indicates a valid repository without a committed HEAD.
var ErrUnborn = errors.New("repository has no committed HEAD")

// Remote is one configured Git remote URL.
type Remote struct {
	Name string
	URL  string
}

// RepositoryInfo is the neutral Git metadata needed to resolve a Jejak
// repository and worktree identity.
type RepositoryInfo struct {
	Root      string
	GitDir    string
	CommonDir string
	Head      string
	HeadKnown bool
	Branch    string
	Remotes   []Remote
}

// Head returns the current committed HEAD without fetching remote metadata.
// It distinguishes an unborn repository so callers that pin a snapshot can
// detect a commit race cheaply during a long analysis.
func (c *Client) Head(ctx context.Context, root string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if strings.TrimSpace(root) == "" {
		return "", false, fmt.Errorf("git HEAD requires a repository root")
	}
	output, err := c.run(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		if isUnborn(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read Git HEAD: %w", err)
	}
	head := strings.TrimSpace(output)
	if head == "" {
		return "", false, nil
	}
	return head, true, nil
}

// Client runs the installed Git executable. It has no mutable command state
// and may be shared by independent operations.
type Client struct {
	executable string
}

// NewClient returns a Git client using executable, or "git" when executable
// is empty.
func NewClient(executable string) *Client {
	if executable == "" {
		executable = "git"
	}
	return &Client{executable: executable}
}

// CommandError describes a failed Git command while preserving its cause for
// errors.Is/errors.As callers.
type CommandError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *CommandError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("git %s: %v", strings.Join(e.Args, " "), e.Err)
	}
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.Args, " "), e.Err, strings.TrimSpace(e.Stderr))
}

// Unwrap returns the process error.
func (e *CommandError) Unwrap() error { return e.Err }

// Discover resolves repository and worktree metadata for path. It does not
// inspect or modify source files. An unborn repository is returned with
// HeadKnown=false rather than treated as a command failure.
func (c *Client) Discover(ctx context.Context, path string) (RepositoryInfo, error) {
	if err := ctx.Err(); err != nil {
		return RepositoryInfo{}, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return RepositoryInfo{}, fmt.Errorf("resolve Git path %q: %w", path, err)
	}
	if info, statErr := os.Stat(absPath); statErr != nil || !info.IsDir() {
		if statErr == nil {
			statErr = errors.New("path is not a directory")
		}
		return RepositoryInfo{}, fmt.Errorf("%w: %q: %v", ErrNotRepository, absPath, statErr)
	}

	rootOutput, err := c.run(ctx, absPath, "rev-parse", "--show-toplevel")
	if err != nil {
		return RepositoryInfo{}, fmt.Errorf("%w: %q: %v", ErrNotRepository, absPath, err)
	}
	root := strings.TrimSpace(rootOutput)
	if !filepath.IsAbs(root) {
		root = filepath.Join(absPath, root)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return RepositoryInfo{}, fmt.Errorf("resolve Git repository root: %w", err)
	}
	root = filepath.Clean(root)

	gitDirOutput, err := c.run(ctx, root, "rev-parse", "--git-dir")
	if err != nil {
		return RepositoryInfo{}, fmt.Errorf("resolve Git directory: %w", err)
	}
	commonDirOutput, err := c.run(ctx, root, "rev-parse", "--git-common-dir")
	if err != nil {
		return RepositoryInfo{}, fmt.Errorf("resolve Git common directory: %w", err)
	}

	gitDir := resolveGitPath(root, strings.TrimSpace(gitDirOutput))
	commonDir := resolveGitPath(root, strings.TrimSpace(commonDirOutput))
	result := RepositoryInfo{
		Root:      root,
		GitDir:    gitDir,
		CommonDir: commonDir,
	}

	headOutput, headErr := c.run(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if headErr == nil {
		result.Head = strings.TrimSpace(headOutput)
		result.HeadKnown = result.Head != ""
	} else if isUnborn(headErr) {
		result.HeadKnown = false
	} else {
		return RepositoryInfo{}, fmt.Errorf("resolve Git HEAD: %w", headErr)
	}

	branchOutput, branchErr := c.run(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if branchErr == nil {
		result.Branch = strings.TrimSpace(branchOutput)
	}

	remotes, err := c.remotes(ctx, root)
	if err != nil {
		return RepositoryInfo{}, err
	}
	result.Remotes = remotes
	return result, nil
}

func (c *Client) remotes(ctx context.Context, root string) ([]Remote, error) {
	output, err := c.run(ctx, root, "remote")
	if err != nil {
		return nil, fmt.Errorf("list Git remotes: %w", err)
	}
	var remotes []Remote
	for _, name := range nonEmptyLines(output) {
		urls, err := c.run(ctx, root, "remote", "get-url", "--all", name)
		if err != nil {
			return nil, fmt.Errorf("read Git remote %q: %w", name, err)
		}
		for _, remoteURL := range nonEmptyLines(urls) {
			remotes = append(remotes, Remote{Name: name, URL: remoteURL})
		}
	}
	return remotes, nil
}

func (c *Client) run(ctx context.Context, dir string, args ...string) (string, error) {
	output, err := c.runBytes(ctx, dir, args...)
	return string(output), err
}

func (c *Client) runBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.executable, args...)
	cmd.Dir = dir
	cmd.Env = commandEnvironment()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &CommandError{Args: append([]string(nil), args...), Stderr: stderr.String(), Err: err}
	}
	return stdout.Bytes(), nil
}

func commandEnvironment() []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+2)
	for _, value := range base {
		if strings.HasPrefix(value, "LC_ALL=") || strings.HasPrefix(value, "LANG=") {
			continue
		}
		env = append(env, value)
	}
	return append(env, "LC_ALL=C", "LANG=C")
}

func resolveGitPath(root, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(root, value))
}

func nonEmptyLines(value string) []string {
	lines := strings.Split(value, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result
}

func isUnborn(err error) bool {
	var commandErr *CommandError
	if !errors.As(err, &commandErr) {
		return false
	}
	message := strings.ToLower(commandErr.Stderr)
	return strings.Contains(message, "does not have any commits yet") ||
		strings.Contains(message, "needed a single revision") ||
		strings.Contains(message, "unknown revision or path not in the working tree")
}
