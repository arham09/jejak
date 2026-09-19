package git

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ChangeKind identifies the status reported by a committed-tree diff. The
// Git package deliberately owns this small wire representation so it does not
// depend on graph (repository imports Git for identity resolution).
type ChangeKind string

const (
	ChangeAdded      ChangeKind = "added"
	ChangeModified   ChangeKind = "modified"
	ChangeDeleted    ChangeKind = "deleted"
	ChangeRenamed    ChangeKind = "renamed"
	ChangeCopied     ChangeKind = "copied"
	ChangeTypeChange ChangeKind = "type_changed"
)

// DiffChange is a normalized old/new path change returned by Client.Diff.
// Blob and mode details are intentionally absent from name-status output.
type DiffChange struct {
	Kind    ChangeKind
	OldPath string
	NewPath string
	Score   int
}

// WorktreeChange describes one effective checkout path reported by Git. The
// index and worktree status bytes are retained independently because a file
// can be staged and then edited again. Overlay consumers use the current
// on-disk bytes; these fields are provenance only.
type WorktreeChange struct {
	IndexStatus    byte
	WorktreeStatus byte
	Kind           ChangeKind
	OldPath        string
	NewPath        string
	Tracked        bool
}

// WorktreeChanges returns the current staged/unstaged/untracked change set.
// Git's NUL-delimited porcelain format keeps unusual paths unambiguous and
// excludes ignored files by default.
func (c *Client) WorktreeChanges(ctx context.Context, root string) ([]WorktreeChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("git worktree status requires a root")
	}
	output, err := c.runBytes(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, fmt.Errorf("read Git worktree status: %w", err)
	}
	return parseWorktreeStatus(output)
}

func parseWorktreeStatus(output []byte) ([]WorktreeChange, error) {
	parts := bytes.Split(output, []byte{0})
	changes := make([]WorktreeChange, 0, len(parts))
	for index := 0; index < len(parts); {
		if len(parts[index]) == 0 {
			index++
			continue
		}
		record := parts[index]
		index++
		if len(record) < 3 || record[2] != ' ' {
			return nil, fmt.Errorf("malformed Git worktree status record %q", string(record))
		}
		indexStatus, worktreeStatus := record[0], record[1]
		path := string(record[3:])
		if path == "" {
			return nil, fmt.Errorf("git worktree status record has an empty path")
		}
		if indexStatus == '!' && worktreeStatus == '!' {
			// The command does not request ignored files, but tolerate a test
			// double or a future Git flag that emits them.
			if _, err := normalizeGitPath(path); err != nil {
				return nil, fmt.Errorf("validate ignored Git path %q: %w", path, err)
			}
			continue
		}
		oldPath, newPath := path, path
		kind := ChangeModified
		tracked := true
		if indexStatus == '?' && worktreeStatus == '?' {
			kind = ChangeAdded
			tracked = false
			oldPath = ""
		} else if indexStatus == 'R' || worktreeStatus == 'R' || indexStatus == 'C' || worktreeStatus == 'C' {
			kind = ChangeRenamed
			if indexStatus == 'C' || worktreeStatus == 'C' {
				kind = ChangeCopied
			}
			if index >= len(parts) || len(parts[index]) == 0 {
				return nil, fmt.Errorf("git worktree rename/copy record %q has no original path", string(record))
			}
			oldPath = string(parts[index])
			index++
		} else if indexStatus == 'A' || worktreeStatus == 'A' {
			kind = ChangeAdded
			oldPath = ""
		} else if indexStatus == 'D' || worktreeStatus == 'D' {
			kind = ChangeDeleted
			newPath = ""
		} else if indexStatus == 'T' || worktreeStatus == 'T' || indexStatus == 'U' || worktreeStatus == 'U' {
			kind = ChangeTypeChange
		}
		if oldPath != "" {
			normalized, err := normalizeGitPath(oldPath)
			if err != nil {
				return nil, fmt.Errorf("validate old Git worktree path %q: %w", oldPath, err)
			}
			oldPath = normalized
		}
		if newPath != "" {
			normalized, err := normalizeGitPath(newPath)
			if err != nil {
				return nil, fmt.Errorf("validate new Git worktree path %q: %w", newPath, err)
			}
			newPath = normalized
		}
		change := WorktreeChange{IndexStatus: indexStatus, WorktreeStatus: worktreeStatus, Kind: kind, OldPath: oldPath, NewPath: newPath, Tracked: tracked}
		if err := validateWorktreeChange(change); err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].OldPath != changes[j].OldPath {
			return changes[i].OldPath < changes[j].OldPath
		}
		if changes[i].NewPath != changes[j].NewPath {
			return changes[i].NewPath < changes[j].NewPath
		}
		if changes[i].Kind != changes[j].Kind {
			return changes[i].Kind < changes[j].Kind
		}
		if changes[i].IndexStatus != changes[j].IndexStatus {
			return changes[i].IndexStatus < changes[j].IndexStatus
		}
		return changes[i].WorktreeStatus < changes[j].WorktreeStatus
	})
	return changes, nil
}

func validateWorktreeChange(change WorktreeChange) error {
	switch change.Kind {
	case ChangeAdded:
		if change.NewPath == "" {
			return fmt.Errorf("git worktree added change has no new path")
		}
	case ChangeDeleted:
		if change.OldPath == "" {
			return fmt.Errorf("git worktree deleted change has no old path")
		}
	case ChangeRenamed, ChangeCopied, ChangeModified, ChangeTypeChange:
		if change.OldPath == "" || change.NewPath == "" {
			return fmt.Errorf("git worktree %s change requires old and new paths", change.Kind)
		}
	default:
		return fmt.Errorf("unknown git worktree change kind %q", change.Kind)
	}
	return nil
}

// Diff returns normalized committed-tree path changes between two commits.
// Git's NUL-delimited name/status format keeps whitespace, tabs, and newlines
// in paths unambiguous. Rename and copy scores are retained when Git reports
// them.
func (c *Client) Diff(ctx context.Context, root, oldCommit, newCommit string) ([]DiffChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(root) == "" || strings.TrimSpace(oldCommit) == "" || strings.TrimSpace(newCommit) == "" {
		return nil, fmt.Errorf("git diff requires a root, old commit, and new commit")
	}
	if oldCommit == newCommit {
		return nil, nil
	}
	output, err := c.runBytes(ctx, root, "diff", "--no-ext-diff", "--no-textconv", "--name-status", "-z", "-M", oldCommit, newCommit)
	if err != nil {
		return nil, fmt.Errorf("diff Git commits %s..%s: %w", oldCommit, newCommit, err)
	}
	changes, err := parseNameStatusDiff(output)
	if err != nil {
		return nil, fmt.Errorf("parse Git diff %s..%s: %w", oldCommit, newCommit, err)
	}
	return changes, nil
}

// Changes is a descriptive alias for Diff.
func (c *Client) Changes(ctx context.Context, root, oldCommit, newCommit string) ([]DiffChange, error) {
	return c.Diff(ctx, root, oldCommit, newCommit)
}

func parseNameStatusDiff(output []byte) ([]DiffChange, error) {
	parts := bytes.Split(output, []byte{0})
	changes := make([]DiffChange, 0, len(parts)/2)
	for index := 0; index < len(parts); {
		if len(parts[index]) == 0 {
			index++
			continue
		}
		statusToken := string(parts[index])
		index++
		// Be tolerant of Git versions or test doubles that retain the historic
		// tab between status and the first path even with -z. Keep the embedded
		// path local so a malformed record cannot reset the parser's cursor.
		var inlinePath string
		inlinePathSet := false
		if tab := strings.IndexByte(statusToken, '\t'); tab >= 0 {
			inlinePathSet = true
			inlinePath = statusToken[tab+1:]
			statusToken = statusToken[:tab]
		}
		if statusToken == "" {
			continue
		}
		status := statusToken[0]
		score := 0
		if len(statusToken) > 1 {
			parsed, parseErr := strconv.Atoi(statusToken[1:])
			if parseErr != nil || parsed < 0 || parsed > 100 {
				return nil, fmt.Errorf("invalid change status %q", statusToken)
			}
			score = parsed
		}
		pathCount := 1
		if status == 'R' || status == 'C' {
			pathCount = 2
		}
		if inlinePathSet {
			pathCount--
		}
		if index+pathCount > len(parts) {
			return nil, fmt.Errorf("status %q has %d path(s), output ended early", statusToken, pathCount)
		}
		paths := make([]string, 0, pathCount+1)
		if inlinePathSet {
			paths = append(paths, inlinePath)
		}
		for pathIndex := 0; pathIndex < pathCount; pathIndex++ {
			paths = append(paths, string(parts[index+pathIndex]))
		}
		index += pathCount
		for pathIndex, path := range paths {
			normalized, normalizeErr := normalizeGitPath(path)
			if normalizeErr != nil {
				return nil, fmt.Errorf("invalid path %q: %w", path, normalizeErr)
			}
			paths[pathIndex] = normalized
		}
		change := DiffChange{Score: score}
		switch status {
		case 'A':
			change.Kind, change.NewPath = ChangeAdded, paths[0]
		case 'M':
			change.Kind, change.OldPath, change.NewPath = ChangeModified, paths[0], paths[0]
		case 'D':
			change.Kind, change.OldPath = ChangeDeleted, paths[0]
		case 'R':
			change.Kind, change.OldPath, change.NewPath = ChangeRenamed, paths[0], paths[1]
		case 'C':
			change.Kind, change.OldPath, change.NewPath = ChangeCopied, paths[0], paths[1]
		case 'T':
			change.Kind, change.OldPath, change.NewPath = ChangeTypeChange, paths[0], paths[0]
		default:
			// Unmerged/unknown statuses are not safe to treat as a narrow source
			// delta. Keep the path and let the graph planner select rebuild.
			change.Kind, change.OldPath, change.NewPath = ChangeTypeChange, paths[0], paths[0]
		}
		if err := validateDiffChange(change); err != nil {
			return nil, fmt.Errorf("validate change %q: %w", statusToken, err)
		}
		changes = append(changes, change)
	}
	return normalizeDiffChanges(changes), nil
}

func validateDiffChange(change DiffChange) error {
	switch change.Kind {
	case ChangeAdded:
		if change.NewPath == "" {
			return fmt.Errorf("added change has no new path")
		}
	case ChangeDeleted:
		if change.OldPath == "" {
			return fmt.Errorf("deleted change has no old path")
		}
	case ChangeRenamed, ChangeCopied, ChangeModified, ChangeTypeChange:
		if change.OldPath == "" || change.NewPath == "" {
			return fmt.Errorf("%s change requires old and new paths", change.Kind)
		}
	default:
		if change.OldPath == "" && change.NewPath == "" {
			return fmt.Errorf("change has no path")
		}
	}
	if change.Score < 0 || change.Score > 100 {
		return fmt.Errorf("score %d is outside 0..100", change.Score)
	}
	return nil
}

func normalizeDiffChanges(changes []DiffChange) []DiffChange {
	seen := make(map[string]struct{}, len(changes))
	result := make([]DiffChange, 0, len(changes))
	for _, change := range changes {
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%d", change.Kind, change.OldPath, change.NewPath, change.Score)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, change)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].OldPath != result[j].OldPath {
			return result[i].OldPath < result[j].OldPath
		}
		if result[i].NewPath != result[j].NewPath {
			return result[i].NewPath < result[j].NewPath
		}
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Score < result[j].Score
	})
	return result
}

// normalizeGitPath keeps Git paths slash-separated and rejects traversal before
// a change crosses the Git/graph package boundary. filepath.Clean catches
// traversal while ToSlash keeps graph keys platform-independent.
func normalizeGitPath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	path = filepath.ToSlash(path)
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if filepath.IsAbs(filepath.FromSlash(clean)) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path %q escapes repository", path)
	}
	return clean, nil
}
