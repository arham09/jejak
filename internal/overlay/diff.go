// Package overlay captures and reconciles a repository's current working
// tree without changing the durable committed graph.
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

// ManifestEntry is one status path and its current on-disk identity. Index
// and worktree status are provenance; ContentSHA always describes bytes read
// from the checkout at capture time.
type ManifestEntry struct {
	Path           string
	OldPath        string
	IndexStatus    byte
	WorktreeStatus byte
	ContentSHA     string
	Mode           string
	Present        bool
	Tracked        bool
	Eligible       bool
}

// Manifest is a deterministic description of one dirty source state.
type Manifest struct {
	ID      string
	Entries []ManifestEntry
	Changes []graph.FileChange
}

// Dirty reports whether the manifest contains any Git-reported changes.
func (m Manifest) Dirty() bool { return len(m.Entries) > 0 || len(m.Changes) > 0 }

// CaptureManifest reads Git status and hashes current checkout bytes. It does
// not read staged blob bytes: the working tree is the effective source for an
// overlay, while the index fields remain available for diagnostics.
func CaptureManifest(ctx context.Context, client *git.Client, root string, analyzer graph.Analyzer) (Manifest, error) {
	if client == nil {
		return Manifest{}, errors.New("overlay manifest requires a Git client")
	}
	if strings.TrimSpace(root) == "" {
		return Manifest{}, errors.New("overlay manifest requires a repository root")
	}
	statuses, err := client.WorktreeChanges(ctx, root)
	if err != nil {
		return Manifest{}, err
	}
	return captureManifest(ctx, root, statuses, analyzer)
}

func captureManifest(ctx context.Context, root string, statuses []git.WorktreeChange, analyzer graph.Analyzer) (Manifest, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve overlay repository root: %w", err)
	}
	root = filepath.Clean(root)
	entries := make([]ManifestEntry, 0, len(statuses)*2)
	changes := make([]graph.FileChange, 0, len(statuses))
	for _, status := range statuses {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		change, err := graphChange(status)
		if err != nil {
			return Manifest{}, err
		}
		primaryPath := status.NewPath
		if primaryPath == "" {
			primaryPath = status.OldPath
		}
		// Untracked non-source artifacts are deliberately outside the overlay
		// boundary. Tracked non-Go files remain eligible because go:embed and
		// module/workspace inputs can make them semantically relevant.
		if !status.Tracked && !eligiblePath(primaryPath, analyzer) {
			continue
		}
		changes = append(changes, change)
		// A rename/copy has an absent old path and a current new path. For
		// ordinary changes the path is whichever side is present in the
		// checkout. Keeping both records makes deletion masks explicit.
		if status.OldPath != "" && status.OldPath != status.NewPath {
			entries = append(entries, ManifestEntry{Path: status.OldPath, OldPath: status.OldPath, IndexStatus: status.IndexStatus, WorktreeStatus: status.WorktreeStatus, Tracked: status.Tracked, Eligible: eligiblePath(status.OldPath, analyzer), Present: false})
		}
		path := status.NewPath
		if path == "" {
			path = status.OldPath
		}
		if path == "" {
			return Manifest{}, fmt.Errorf("overlay status has no path")
		}
		eligible := eligiblePath(path, analyzer)
		entry := ManifestEntry{Path: path, OldPath: status.OldPath, IndexStatus: status.IndexStatus, WorktreeStatus: status.WorktreeStatus, Tracked: status.Tracked, Eligible: eligible}
		present, contentSHA, mode, readErr := currentFileIdentity(root, path)
		if readErr != nil {
			return Manifest{}, readErr
		}
		entry.Present, entry.ContentSHA, entry.Mode = present, contentSHA, mode
		entries = append(entries, entry)
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].OldPath != changes[j].OldPath {
			return changes[i].OldPath < changes[j].OldPath
		}
		if changes[i].NewPath != changes[j].NewPath {
			return changes[i].NewPath < changes[j].NewPath
		}
		return changes[i].Kind < changes[j].Kind
	})
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Path != entries[j].Path {
			return entries[i].Path < entries[j].Path
		}
		if entries[i].OldPath != entries[j].OldPath {
			return entries[i].OldPath < entries[j].OldPath
		}
		return entries[i].ContentSHA < entries[j].ContentSHA
	})
	manifest := Manifest{Entries: entries, Changes: changes}
	manifest.ID = manifestHash(manifest)
	return manifest, nil
}

func eligiblePath(path string, analyzer graph.Analyzer) bool {
	if analyzer != nil && analyzer.Supports(path) {
		return true
	}
	base := filepath.Base(filepath.FromSlash(path))
	if base == "go.mod" || base == "go.work" || base == "go.sum" || base == "go.work.sum" {
		return true
	}
	// Vendor and embed inputs can affect package loading. Tracked files are
	// allowed through; untracked callers still use the analyzer boundary.
	return false
}

func currentFileIdentity(root, relative string) (bool, string, string, error) {
	relative = filepath.ToSlash(relative)
	clean := filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return false, "", "", fmt.Errorf("overlay path %q escapes repository", relative)
	}
	path := filepath.Join(root, clean)
	if !within(root, path) {
		return false, "", "", fmt.Errorf("overlay path %q escapes repository", relative)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, "", "", nil
	}
	if err != nil {
		return false, "", "", fmt.Errorf("stat overlay path %q: %w", relative, err)
	}
	if info.IsDir() {
		return false, "", "", fmt.Errorf("overlay path %q is a directory", relative)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, "", "", fmt.Errorf("overlay path %q is a symlink; symlinked working-tree inputs are unsupported", relative)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return false, "", "", fmt.Errorf("read overlay path %q: %w", relative, err)
	}
	sum := sha256.Sum256(contents)
	mode := "100644"
	if info.Mode()&0o111 != 0 {
		mode = "100755"
	}
	return true, hex.EncodeToString(sum[:]), mode, nil
}

func graphChange(status git.WorktreeChange) (graph.FileChange, error) {
	kind := graph.ChangeModified
	switch status.Kind {
	case git.ChangeAdded:
		kind = graph.ChangeAdded
	case git.ChangeModified:
		kind = graph.ChangeModified
	case git.ChangeDeleted:
		kind = graph.ChangeDeleted
	case git.ChangeRenamed:
		kind = graph.ChangeRenamed
	case git.ChangeCopied:
		kind = graph.ChangeCopied
	case git.ChangeTypeChange:
		kind = graph.ChangeTypeChange
	default:
		return graph.FileChange{}, fmt.Errorf("unsupported Git worktree change kind %q", status.Kind)
	}
	change := graph.FileChange{Kind: kind, OldPath: status.OldPath, NewPath: status.NewPath}
	if !status.Tracked && change.Kind == graph.ChangeAdded {
		change.OldPath = ""
	}
	if err := change.Validate(); err != nil {
		return graph.FileChange{}, fmt.Errorf("validate overlay change: %w", err)
	}
	return change, nil
}

func manifestHash(manifest Manifest) string {
	hash := sha256.New()
	for _, change := range manifest.Changes {
		fmt.Fprintf(hash, "change\x00%s\x00%s\x00%s\x00%d\x00", change.Kind, change.OldPath, change.NewPath, change.Score)
	}
	for _, entry := range manifest.Entries {
		fmt.Fprintf(hash, "entry\x00%s\x00%s\x00%c\x00%c\x00%s\x00%s\x00%t\x00%t\x00%t\x00", entry.Path, entry.OldPath, entry.IndexStatus, entry.WorktreeStatus, entry.ContentSHA, entry.Mode, entry.Present, entry.Tracked, entry.Eligible)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func within(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
