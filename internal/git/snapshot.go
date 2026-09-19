package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// SnapshotFile identifies one tracked file in a committed tree.
type SnapshotFile struct {
	Path         string
	BlobSHA      string
	ObjectFormat string
	Mode         string
	Size         int64
}

// Snapshot is an immutable materialization of one committed Git tree. The
// materialization lives outside the developer checkout and is removed by
// Close.
type Snapshot struct {
	Root         string
	Commit       string
	ObjectFormat string
	Files        []SnapshotFile

	close func() error
}

// Close removes the temporary tree. It is safe to call more than once.
func (s *Snapshot) Close() error {
	if s == nil || s.close == nil {
		return nil
	}
	err := s.close()
	s.close = nil
	return err
}

// Snapshot materializes commit from the worktree's Git object database into a
// fresh OS temporary directory. It never reads mutable checkout bytes.
func (c *Client) Snapshot(ctx context.Context, root, commit string) (*Snapshot, error) {
	return c.SnapshotIn(ctx, root, commit, "")
}

// MaterializeSnapshot is a descriptive alias for Snapshot.
func (c *Client) MaterializeSnapshot(ctx context.Context, root, commit string) (*Snapshot, error) {
	return c.Snapshot(ctx, root, commit)
}

// SnapshotIn is Snapshot with an optional parent directory for temporary
// storage. The parent must be outside the checkout; callers commonly pass a
// repository's external Jejak tmp directory.
func (c *Client) SnapshotIn(ctx context.Context, root, commit, parent string) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(root) == "" || strings.TrimSpace(commit) == "" {
		return nil, errors.New("git snapshot requires a root and commit")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve snapshot repository root: %w", err)
	}
	root = filepath.Clean(root)
	if parent != "" {
		parent, err = filepath.Abs(parent)
		if err != nil {
			return nil, fmt.Errorf("resolve snapshot parent: %w", err)
		}
		canonicalRoot := canonicalExistingPath(root)
		canonicalParent := canonicalExistingPath(parent)
		if pathWithin(canonicalRoot, canonicalParent) || pathWithin(canonicalParent, canonicalRoot) {
			return nil, fmt.Errorf("snapshot temporary storage %q must be outside repository %q", parent, root)
		}
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return nil, fmt.Errorf("create snapshot parent %q: %w", parent, err)
		}
	}
	workDir := parent
	if workDir == "" {
		workDir = os.TempDir()
	}
	dir, err := os.MkdirTemp(workDir, "jejak-snapshot-")
	if err != nil {
		return nil, fmt.Errorf("create git snapshot directory: %w", err)
	}
	cleanup := func() error { return os.RemoveAll(dir) }
	fail := func(cause error) (*Snapshot, error) {
		_ = cleanup()
		return nil, cause
	}

	entries, err := c.treeEntries(ctx, root, commit)
	if err != nil {
		return fail(fmt.Errorf("list git tree %s: %w", commit, err))
	}
	objectFormat := "sha1"
	if value, formatErr := c.run(ctx, root, "rev-parse", "--show-object-format"); formatErr == nil && strings.TrimSpace(value) != "" {
		objectFormat = strings.TrimSpace(value)
	}
	files := make([]SnapshotFile, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if entry.Type == "commit" || entry.Mode == "160000" {
			return fail(fmt.Errorf("unsupported git submodule at %q", entry.Path))
		}
		if entry.Type != "blob" {
			return fail(fmt.Errorf("unsupported git tree object %q (%s)", entry.Path, entry.Type))
		}
		relative := filepath.FromSlash(entry.Path)
		if filepath.IsAbs(relative) || pathEscapes(relative) {
			return fail(fmt.Errorf("unsafe git tree path %q", entry.Path))
		}
		destination := filepath.Join(dir, relative)
		if !pathWithin(dir, destination) {
			return fail(fmt.Errorf("git tree path escapes snapshot: %q", entry.Path))
		}
		if entry.Mode == "120000" {
			contents, readErr := c.object(ctx, root, entry.Object)
			if readErr != nil {
				return fail(fmt.Errorf("read git symlink %q: %w", entry.Path, readErr))
			}
			target := string(contents)
			linkDestination := filepath.Join(filepath.Dir(destination), filepath.FromSlash(target))
			if filepath.IsAbs(target) || !pathWithin(dir, filepath.Clean(linkDestination)) {
				return fail(fmt.Errorf("git symlink %q escapes committed snapshot", entry.Path))
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				return fail(fmt.Errorf("create git snapshot directory for %q: %w", entry.Path, err))
			}
			if err := os.Symlink(target, destination); err != nil {
				return fail(fmt.Errorf("materialize git symlink %q: %w", entry.Path, err))
			}
			files = append(files, SnapshotFile{Path: entry.Path, BlobSHA: entry.Object, ObjectFormat: objectFormat, Mode: entry.Mode, Size: int64(len(contents))})
			continue
		}
		contents, readErr := c.object(ctx, root, entry.Object)
		if readErr != nil {
			return fail(fmt.Errorf("read git blob %q: %w", entry.Path, readErr))
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return fail(fmt.Errorf("create git snapshot directory for %q: %w", entry.Path, err))
		}
		mode := os.FileMode(0o644)
		if entry.Mode == "100755" {
			mode = 0o755
		}
		if err := os.WriteFile(destination, contents, mode); err != nil {
			return fail(fmt.Errorf("write git snapshot file %q: %w", entry.Path, err))
		}
		files = append(files, SnapshotFile{Path: entry.Path, BlobSHA: entry.Object, ObjectFormat: objectFormat, Mode: entry.Mode, Size: int64(len(contents))})
	}
	for _, file := range files {
		if file.Mode != "120000" {
			continue
		}
		resolved, resolveErr := filepath.EvalSymlinks(filepath.Join(dir, filepath.FromSlash(file.Path)))
		if resolveErr == nil && !pathWithin(dir, resolved) {
			return fail(fmt.Errorf("git symlink %q escapes committed snapshot", file.Path))
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return &Snapshot{Root: dir, Commit: commit, ObjectFormat: objectFormat, Files: files, close: cleanup}, nil
}

type treeEntry struct {
	Mode   string
	Type   string
	Object string
	Path   string
}

func (c *Client) treeEntries(ctx context.Context, root, commit string) ([]treeEntry, error) {
	output, err := c.runRaw(ctx, root, "ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	parts := bytes.Split(output, []byte{0})
	entries := make([]treeEntry, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		space := bytes.IndexByte(part, ' ')
		tab := bytes.IndexByte(part, '\t')
		if space <= 0 || tab <= space+1 || tab == len(part)-1 {
			return nil, fmt.Errorf("malformed Git tree entry %q", string(part))
		}
		objectAndType := strings.Fields(string(part[space+1 : tab]))
		if len(objectAndType) != 2 {
			return nil, fmt.Errorf("malformed Git tree object entry %q", string(part))
		}
		if _, err := strconv.ParseInt(string(part[:space]), 8, 32); err != nil {
			return nil, fmt.Errorf("malformed Git tree mode %q: %w", string(part[:space]), err)
		}
		entries = append(entries, treeEntry{Mode: string(part[:space]), Type: objectAndType[0], Object: objectAndType[1], Path: string(part[tab+1:])})
	}
	return entries, nil
}

func (c *Client) object(ctx context.Context, root, object string) ([]byte, error) {
	return c.runRaw(ctx, root, "cat-file", "blob", object)
}

func (c *Client) runRaw(ctx context.Context, dir string, args ...string) ([]byte, error) {
	value, err := c.runBytes(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	return value, nil
}

func pathEscapes(path string) bool {
	clean := filepath.Clean(path)
	return clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func pathWithin(base, candidate string) bool {
	rel, err := filepath.Rel(base, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func canonicalExistingPath(path string) string {
	path = filepath.Clean(path)
	for current := path; ; current = filepath.Dir(current) {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			if absolute, absErr := filepath.Abs(resolved); absErr == nil {
				return filepath.Clean(absolute)
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
	}
}
