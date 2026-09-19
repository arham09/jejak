package hooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arham09/jejak/internal/repository"
)

type location int

const (
	locationSafe location = iota
	locationOutside
	locationTracked
	locationSymlink
)

func (m *Manager) classify(ctx context.Context, target repository.Target, path string) (location, string, error) {
	if pathHasSymlink(path) {
		return locationSymlink, "effective hook path contains a symlink", nil
	}
	commonHooks := filepath.Join(target.Repository.CommonDir, "hooks")
	gitHooks := filepath.Join(target.Worktree.GitDir, "hooks")
	if pathWithin(commonHooks, path) || pathWithin(gitHooks, path) {
		return locationSafe, "repository Git hooks directory", nil
	}
	if pathWithin(target.Repository.Root, path) {
		tracked, err := m.client.IsTracked(ctx, target.Repository.Root, path)
		if err != nil {
			return locationTracked, "unable to prove custom hook ownership", err
		}
		if tracked {
			return locationTracked, "tracked hook path; Jejak will not replace it", nil
		}
		return locationSafe, "repository-local untracked hooks directory", nil
	}
	return locationOutside, "effective hook path is outside this repository", nil
}

func (m *Manager) writeHook(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create Git hooks directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("protect Git hooks directory: %w", err)
	}
	return writeAtomic(path, data, 0o755)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".jejak-hook-*")
	if err != nil {
		return fmt.Errorf("create temporary hook file: %w", err)
	}
	temporary := file.Name()
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(mode); err != nil {
		return fmt.Errorf("set temporary hook mode: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write temporary hook file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temporary hook file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary hook file: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace hook file %q: %w", path, err)
	}
	cleanup = false
	return os.Chmod(path, mode)
}

func fileMode(mode os.FileMode) uint32 {
	return uint32(mode.Perm() | (mode & (os.ModeSetuid | os.ModeSetgid | os.ModeSticky)))
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func removeIfMatches(path string, data []byte) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if hashBytes(existing) != hashBytes(data) {
		return nil
	}
	return os.Remove(path)
}

func pathHasSymlink(path string) bool {
	path, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, statErr := os.Lstat(current)
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
	}
}

func pathWithin(base, candidate string) bool {
	base, baseErr := filepath.Abs(base)
	candidate, candidateErr := filepath.Abs(candidate)
	if baseErr != nil || candidateErr != nil {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(base), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
