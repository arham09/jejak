package overlay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arham09/jejak/internal/git"
	"github.com/arham09/jejak/internal/graph"
)

// Snapshot is an external effective source tree. Unchanged files retain their
// committed Git blob identity; captured files use the namespaced synthetic
// identity returned by SyntheticBlobSHA.
type Snapshot struct {
	Root     string
	Commit   graph.CommitSHA
	Files    []graph.SnapshotFile
	Manifest Manifest

	client   *git.Client
	repoRoot string
	blobs    map[string][]byte
	base     *git.Snapshot
}

// SyntheticBlobSHA returns the stable content identity used for a captured
// working-tree blob. It is not a Git object ID and is readable only through
// this snapshot's source reader.
func SyntheticBlobSHA(contents []byte) string {
	sum := sha256.Sum256(contents)
	return "overlay:sha256:" + hex.EncodeToString(sum[:])
}

// materialize creates an external copy of baseCommit and applies the current
// on-disk manifest. It reads each changed file once and records its bytes for
// source excerpts so later checkout edits cannot alter the report.
func materialize(ctx context.Context, client *git.Client, repoRoot string, baseCommit graph.CommitSHA, manifest Manifest, parent string) (*Snapshot, error) {
	if client == nil {
		return nil, errors.New("overlay snapshot requires a Git client")
	}
	if baseCommit == "" {
		return nil, errors.New("overlay snapshot requires a committed base")
	}
	base, err := client.SnapshotIn(ctx, repoRoot, string(baseCommit), parent)
	if err != nil {
		return nil, fmt.Errorf("materialize overlay base: %w", err)
	}
	fail := func(cause error) (*Snapshot, error) {
		_ = base.Close()
		return nil, cause
	}
	files := make(map[string]graph.SnapshotFile, len(base.Files))
	for _, file := range base.Files {
		files[filepath.ToSlash(file.Path)] = graph.SnapshotFile{Path: filepath.ToSlash(file.Path), BlobSHA: file.BlobSHA, ObjectFormat: file.ObjectFormat, Mode: file.Mode, Size: file.Size}
	}
	blobs := make(map[string][]byte)
	for _, entry := range manifest.Entries {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if !entry.Eligible && !entry.Tracked {
			continue
		}
		path := filepath.ToSlash(entry.Path)
		if !entry.Present {
			if err := removeSnapshotPath(base.Root, path); err != nil {
				return fail(err)
			}
			delete(files, path)
			continue
		}
		contents, err := readCurrentForSnapshot(repoRoot, path, entry.ContentSHA)
		if err != nil {
			return fail(err)
		}
		if err := writeSnapshotPath(base.Root, path, contents, entry.Mode); err != nil {
			return fail(err)
		}
		blobSHA := SyntheticBlobSHA(contents)
		blobs[blobSHA] = append([]byte(nil), contents...)
		files[path] = graph.SnapshotFile{Path: path, BlobSHA: blobSHA, ObjectFormat: "sha256", Mode: entry.Mode, Size: int64(len(contents))}
	}
	ordered := make([]graph.SnapshotFile, 0, len(files))
	for _, file := range files {
		ordered = append(ordered, file)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	return &Snapshot{Root: base.Root, Commit: baseCommit, Files: ordered, Manifest: manifest, client: client, repoRoot: repoRoot, blobs: blobs, base: base}, nil
}

// ReadBlob reads either a captured synthetic blob or an unchanged/deleted
// committed blob. The root argument is accepted to satisfy brief's source
// reader shape; source ownership remains this snapshot.
func (s *Snapshot) ReadBlob(ctx context.Context, _ string, objectID string) ([]byte, error) {
	if s == nil {
		return nil, errors.New("overlay snapshot is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if contents, ok := s.blobs[strings.TrimSpace(objectID)]; ok {
		return append([]byte(nil), contents...), nil
	}
	if strings.HasPrefix(objectID, "overlay:") {
		return nil, fmt.Errorf("overlay blob %q is unavailable", objectID)
	}
	if s.client == nil {
		return nil, fmt.Errorf("committed blob %q is unavailable", objectID)
	}
	contents, err := s.client.ReadBlob(ctx, s.repoRoot, objectID)
	if err != nil {
		return nil, err
	}
	return contents, nil
}

// Close releases the external source tree. It is safe to call repeatedly.
func (s *Snapshot) Close() error {
	if s == nil || s.base == nil {
		return nil
	}
	err := s.base.Close()
	s.base = nil
	s.blobs = nil
	s.Files = nil
	return err
}

func readCurrentForSnapshot(root, relative, expectedSHA string) ([]byte, error) {
	relative = filepath.ToSlash(relative)
	clean := filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("overlay path %q escapes repository", relative)
	}
	path := filepath.Join(root, clean)
	if !within(root, path) {
		return nil, fmt.Errorf("overlay path %q escapes repository", relative)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat overlay source %q: %w", relative, err)
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("overlay source %q is not a regular file", relative)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read overlay source %q: %w", relative, err)
	}
	sum := sha256.Sum256(contents)
	if expectedSHA != "" && hex.EncodeToString(sum[:]) != expectedSHA {
		return nil, fmt.Errorf("overlay source %q changed during capture", relative)
	}
	return contents, nil
}

func writeSnapshotPath(root, relative string, contents []byte, mode string) error {
	clean := filepath.Clean(filepath.FromSlash(relative))
	destination := filepath.Join(root, clean)
	if !within(root, destination) {
		return fmt.Errorf("overlay destination %q escapes snapshot", relative)
	}
	if err := ensureNoSymlinkParents(root, destination); err != nil {
		return err
	}
	// A type-change can replace a committed symlink or directory at this
	// path. Remove only the temporary snapshot entry before writing the
	// captured regular file; the developer checkout is never touched.
	if err := os.RemoveAll(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("replace overlay destination %q: %w", relative, err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("create overlay directory for %q: %w", relative, err)
	}
	permissions := os.FileMode(0o644)
	if mode == "100755" {
		permissions = 0o755
	}
	if err := os.WriteFile(destination, contents, permissions); err != nil {
		return fmt.Errorf("write overlay source %q: %w", relative, err)
	}
	return nil
}

func ensureNoSymlinkParents(root, destination string) error {
	relative, err := filepath.Rel(root, destination)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("overlay destination %q escapes snapshot", destination)
	}
	parent := filepath.Dir(destination)
	for {
		info, statErr := os.Lstat(parent)
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("overlay destination parent %q is a symlink", parent)
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect overlay destination parent %q: %w", parent, statErr)
		}
		if filepath.Clean(parent) == filepath.Clean(root) {
			return nil
		}
		next := filepath.Dir(parent)
		if next == parent {
			return fmt.Errorf("overlay destination parent escapes snapshot")
		}
		parent = next
	}
}

func removeSnapshotPath(root, relative string) error {
	clean := filepath.Clean(filepath.FromSlash(relative))
	destination := filepath.Join(root, clean)
	if !within(root, destination) {
		return fmt.Errorf("overlay removal %q escapes snapshot", relative)
	}
	if err := ensureNoSymlinkParents(root, destination); err != nil {
		return err
	}
	if err := os.RemoveAll(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove overlay source %q: %w", relative, err)
	}
	return nil
}
