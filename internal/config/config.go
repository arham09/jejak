// Package config resolves and validates Jejak's process configuration.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrInvalidDataRoot indicates that a Jejak data directory is unusable or
// would place durable state inside the repository being indexed.
var ErrInvalidDataRoot = errors.New("invalid Jejak data root")

// RepositoryPaths contains the durable and temporary paths owned by one
// logical repository.
type RepositoryPaths struct {
	Root  string
	DB    string
	Cache string
	Tmp   string
	Hooks string
	Locks string
	Logs  string
}

// ResolveDataRoot returns the canonical application data directory. An
// explicit override takes precedence over JEJAK_DATA_DIR; when both are
// absent, the platform's application data convention is used. Overrides are
// intentionally not suffixed with another "jejak" component.
func ResolveDataRoot(override, repositoryRoot string) (string, error) {
	candidate := override
	if candidate == "" {
		candidate = os.Getenv("JEJAK_DATA_DIR")
	}
	if candidate == "" {
		var err error
		candidate, err = defaultDataRoot()
		if err != nil {
			return "", err
		}
	}

	resolved, err := canonicalPath(candidate)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidDataRoot, err)
	}
	if repositoryRoot != "" {
		repoPath, err := canonicalPath(repositoryRoot)
		if err != nil {
			return "", fmt.Errorf("%w: resolve repository root: %v", ErrInvalidDataRoot, err)
		}
		if pathWithin(repoPath, resolved) {
			return "", fmt.Errorf("%w: %q is inside repository %q", ErrInvalidDataRoot, resolved, repoPath)
		}
	}
	return resolved, nil
}

// EnsureDataRoot creates the application directory and its repository
// catalog directory with private permissions.
func EnsureDataRoot(root string) error {
	if root == "" || !filepath.IsAbs(root) {
		return fmt.Errorf("%w: path must be absolute", ErrInvalidDataRoot)
	}
	if err := os.MkdirAll(filepath.Join(root, "repos"), 0o700); err != nil {
		return fmt.Errorf("create data root %q: %w", root, err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return fmt.Errorf("protect data root %q: %w", root, err)
	}
	if err := os.Chmod(filepath.Join(root, "repos"), 0o700); err != nil {
		return fmt.Errorf("protect repository catalog %q: %w", filepath.Join(root, "repos"), err)
	}
	return nil
}

// RepositoryPathsFor returns the paths Jejak owns for a repository ID. The
// ID is validated before it is used as a path component.
func RepositoryPathsFor(dataRoot, repositoryID string) (RepositoryPaths, error) {
	if dataRoot == "" || !filepath.IsAbs(dataRoot) {
		return RepositoryPaths{}, fmt.Errorf("%w: data root must be absolute", ErrInvalidDataRoot)
	}
	if repositoryID == "" || repositoryID == "." || repositoryID == ".." ||
		filepath.Base(repositoryID) != repositoryID ||
		strings.ContainsAny(repositoryID, `/\\`) {
		return RepositoryPaths{}, fmt.Errorf("%w: invalid repository ID %q", ErrInvalidDataRoot, repositoryID)
	}
	root := filepath.Join(dataRoot, "repos", repositoryID)
	return RepositoryPaths{
		Root:  root,
		DB:    filepath.Join(root, "graph.db"),
		Cache: filepath.Join(root, "cache"),
		Tmp:   filepath.Join(root, "tmp"),
		Hooks: filepath.Join(root, "hooks"),
		Locks: filepath.Join(root, "locks"),
		Logs:  filepath.Join(root, "logs"),
	}, nil
}

func defaultDataRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "jejak"), nil
	case "linux":
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			if !filepath.IsAbs(xdg) {
				return "", fmt.Errorf("%w: XDG_DATA_HOME must be absolute", ErrInvalidDataRoot)
			}
			return filepath.Join(xdg, "jejak"), nil
		}
		return filepath.Join(home, ".local", "share", "jejak"), nil
	default:
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("resolve application data directory: %w", err)
		}
		return filepath.Join(base, "jejak"), nil
	}
}

func canonicalPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)

	current := abs
	var suffix []string
	for {
		_, statErr := os.Lstat(current)
		if statErr == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(statErr, fs.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return abs, nil
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func pathWithin(base, candidate string) bool {
	rel, err := filepath.Rel(base, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
