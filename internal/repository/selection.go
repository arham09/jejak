package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/arham09/jejak/internal/git"
)

type gitDiscoverer interface {
	Discover(context.Context, string) (git.RepositoryInfo, error)
}

// Resolve discovers the repository containing path and returns its stable
// repository/worktree target. An empty path uses the current directory.
func Resolve(ctx context.Context, discoverer gitDiscoverer, path string) (Target, error) {
	if path == "" {
		var err error
		path, err = os.Getwd()
		if err != nil {
			return Target{}, fmt.Errorf("resolve current directory: %w", err)
		}
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return Target{}, fmt.Errorf("resolve worktree path %q: %w", path, err)
	}
	return identifyDiscovered(ctx, discoverer, filepath.Clean(absPath))
}

func identifyDiscovered(ctx context.Context, discoverer gitDiscoverer, path string) (Target, error) {
	info, err := discoverer.Discover(ctx, path)
	if err != nil {
		return Target{}, err
	}
	target, err := Identify(info)
	if err != nil {
		return Target{}, fmt.Errorf("identify repository %q: %w", path, err)
	}
	return target, nil
}
