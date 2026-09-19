// Package hooks manages Jejak's reversible Git lifecycle dispatchers.
package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/arham09/jejak/internal/config"
	"github.com/arham09/jejak/internal/git"
	"github.com/arham09/jejak/internal/repository"
)

// SupportedHooks returns the lifecycle hooks Jejak can dispatch. The returned
// slice is a copy and can be changed by the caller safely.
func SupportedHooks() []string {
	return append([]string(nil), supportedHooks...)
}

var supportedHooks = []string{"post-commit", "post-checkout", "post-merge", "post-rewrite"}

// Status describes Jejak's ownership and capability for one hook path.
type Status string

const (
	StatusInstalled    Status = "installed"
	StatusNotInstalled Status = "not-installed"
	StatusSkipped      Status = "skipped"
	StatusUnmanaged    Status = "unmanaged"
	StatusConflict     Status = "conflict"
	StatusError        Status = "error"
)

// HookState is the inspectable result for one supported lifecycle hook.
type HookState struct {
	Name   string
	Path   string
	Status Status
	Detail string
}

// Report contains deterministic hook states and whether installation changed
// any managed filesystem or manifest content.
type Report struct {
	ManifestPath string
	States       []HookState
	Changed      bool
}

// Manager owns hook files and manifests below one external Jejak data root.
// It does not acquire a repository lock itself; callers that mutate hooks must
// hold the repository writer lease for the whole operation.
type Manager struct {
	client   *git.Client
	dataRoot string
}

// NewManager returns a hook manager using client and an external data root.
func NewManager(client *git.Client, dataRoot string) *Manager {
	return &Manager{client: client, dataRoot: dataRoot}
}

// Install installs or adopts all supported hooks in a safe effective Git
// hooks directory. Per-hook unsupported or ownership-conflict states are
// returned in the report; operational failures are joined into the error.
func (m *Manager) Install(ctx context.Context, target repository.Target) (Report, error) {
	if err := m.validate(ctx, target); err != nil {
		return Report{}, err
	}
	paths, err := m.repositoryPaths(target)
	if err != nil {
		return Report{}, err
	}
	if err := os.MkdirAll(paths.Hooks, 0o700); err != nil {
		return Report{}, fmt.Errorf("create external hook storage: %w", err)
	}
	if err := os.Chmod(paths.Hooks, 0o700); err != nil {
		return Report{}, fmt.Errorf("protect external hook storage: %w", err)
	}
	manifestPath := filepath.Join(paths.Hooks, "manifest.json")
	state := Report{ManifestPath: manifestPath}
	document, err := readManifest(manifestPath, string(target.Repository.ID))
	if err != nil {
		return state, err
	}
	hooksPath, err := m.client.EffectiveHooksPath(ctx, target.Worktree.Path)
	if err != nil {
		return state, err
	}
	var operationErr error
	for _, name := range supportedHooks {
		hookState, changed, hookErr := m.installOne(ctx, target, paths, hooksPath, &document, name)
		state.States = append(state.States, hookState)
		state.Changed = state.Changed || changed
		if hookErr != nil {
			operationErr = errors.Join(operationErr, hookErr)
		}
	}
	sortHookStates(state.States)
	return state, operationErr
}

// Status inspects supported hooks without creating files or changing Git
// configuration. The caller may hold a writer lease to serialize its view
// with installation, but no lease is acquired here.
func (m *Manager) Status(ctx context.Context, target repository.Target) (Report, error) {
	if err := m.validate(ctx, target); err != nil {
		return Report{}, err
	}
	paths, err := m.repositoryPaths(target)
	if err != nil {
		return Report{}, err
	}
	state := Report{ManifestPath: filepath.Join(paths.Hooks, "manifest.json")}
	document, err := readManifest(state.ManifestPath, string(target.Repository.ID))
	if err != nil {
		return state, err
	}
	hooksPath, err := m.client.EffectiveHooksPath(ctx, target.Worktree.Path)
	if err != nil {
		return state, err
	}
	for _, name := range supportedHooks {
		hookState := m.inspectOne(ctx, target, hooksPath, document, name)
		state.States = append(state.States, hookState)
	}
	sortHookStates(state.States)
	return state, nil
}

// Uninstall removes only dispatchers attributable to target's worktree. If a
// managed wrapper was changed after installation, it is left in place and a
// conflict state is returned. Shared wrappers remain while another worktree
// still owns them.
func (m *Manager) Uninstall(ctx context.Context, target repository.Target) (Report, error) {
	if err := m.validate(ctx, target); err != nil {
		return Report{}, err
	}
	paths, err := m.repositoryPaths(target)
	if err != nil {
		return Report{}, err
	}
	state := Report{ManifestPath: filepath.Join(paths.Hooks, "manifest.json")}
	document, err := readManifest(state.ManifestPath, string(target.Repository.ID))
	if err != nil {
		return state, err
	}
	hooksPath, err := m.client.EffectiveHooksPath(ctx, target.Worktree.Path)
	if err != nil {
		return state, err
	}
	var operationErr error
	for _, name := range supportedHooks {
		hookState, changed, hookErr := m.uninstallOne(ctx, target, paths, hooksPath, &document, name)
		state.States = append(state.States, hookState)
		state.Changed = state.Changed || changed
		if hookErr != nil {
			operationErr = errors.Join(operationErr, hookErr)
		}
	}
	sortHookStates(state.States)
	return state, operationErr
}

func (m *Manager) repositoryPaths(target repository.Target) (config.RepositoryPaths, error) {
	if m.dataRoot == "" {
		return config.RepositoryPaths{}, fmt.Errorf("resolve hook storage: data root is empty")
	}
	return config.RepositoryPathsFor(m.dataRoot, string(target.Repository.ID))
}

func (m *Manager) validate(ctx context.Context, target repository.Target) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m == nil || m.client == nil {
		return errors.New("hook manager has no Git client")
	}
	if target.Repository.ID == "" || target.Repository.Root == "" || target.Worktree.Path == "" {
		return errors.New("hook manager requires a resolved repository/worktree target")
	}
	return nil
}

func sortHookStates(states []HookState) {
	sort.Slice(states, func(i, j int) bool { return states[i].Name < states[j].Name })
}
