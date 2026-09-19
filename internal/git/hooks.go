package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// EffectiveHooksPath returns the absolute directory Git uses for hooks when
// commands run in root. It honors core.hooksPath and linked-worktree rules
// through Git itself rather than reimplementing Git configuration lookup.
func (c *Client) EffectiveHooksPath(ctx context.Context, root string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve Git repository root: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(rootAbs); resolveErr == nil {
		rootAbs = resolved
	}
	output, err := c.run(ctx, rootAbs, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err != nil {
		// Older Git versions do not understand --path-format. Git still emits a
		// path relative to the worktree for the fallback form, so resolve it here.
		output, err = c.run(ctx, rootAbs, "rev-parse", "--git-path", "hooks")
		if err != nil {
			return "", fmt.Errorf("resolve effective Git hooks path: %w", err)
		}
	}
	path := strings.TrimSpace(output)
	if path == "" {
		return "", errors.New("git returned an empty hooks path")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(rootAbs, path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve effective Git hooks path %q: %w", path, err)
	}
	return filepath.Clean(absPath), nil
}

// IsTracked reports whether path is tracked by the Git worktree rooted at
// root. Paths outside root are necessarily untracked and return false.
func (c *Client) IsTracked(ctx context.Context, root, path string) (bool, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false, fmt.Errorf("resolve Git repository root: %w", err)
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Errorf("resolve tracked path %q: %w", path, err)
	}
	relative, err := filepath.Rel(filepath.Clean(rootAbs), filepath.Clean(pathAbs))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false, nil
	}
	_, err = c.run(ctx, rootAbs, "ls-files", "--error-unmatch", "--", filepath.ToSlash(relative))
	if err == nil {
		return true, nil
	}
	var commandErr *CommandError
	if errors.As(err, &commandErr) {
		message := strings.ToLower(commandErr.Stderr)
		if strings.Contains(message, "did not match any files") || strings.Contains(message, "pathspec") {
			return false, nil
		}
	}
	return false, fmt.Errorf("check whether %q is tracked: %w", pathAbs, err)
}
