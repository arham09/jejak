package hooks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/arham09/jejak/internal/config"
	"github.com/arham09/jejak/internal/repository"
)

func (m *Manager) uninstallOne(ctx context.Context, target repository.Target, paths config.RepositoryPaths, hooksPath string, document *manifest, name string) (HookState, bool, error) {
	hookPath := filepath.Join(hooksPath, name)
	state := HookState{Name: name, Path: hookPath}
	key := manifestKey(name, hookPath)
	entry, managed := document.Entries[key]
	if !managed {
		state = m.inspectOne(ctx, target, hooksPath, *document, name)
		if state.Status == StatusNotInstalled {
			state.Detail = "no Jejak dispatcher to uninstall"
		}
		return state, false, nil
	}
	if entry.Path != hookPath || entry.Name != name {
		state.Status = StatusConflict
		state.Detail = "manifest ownership does not match the effective hook path"
		return state, false, nil
	}
	removedOwner := removeOwner(&entry, string(target.Worktree.ID))
	if len(entry.Owners) > 0 {
		document.Entries[key] = entry
		if removedOwner {
			if err := writeManifest(filepath.Join(paths.Hooks, "manifest.json"), *document); err != nil {
				state.Status = StatusError
				state.Detail = err.Error()
				return state, false, fmt.Errorf("record %s hook owner removal: %w", name, err)
			}
		}
		state.Status = StatusInstalled
		state.Detail = fmt.Sprintf("dispatcher retained for %d other worktree(s)", len(entry.Owners))
		return state, removedOwner, nil
	}
	info, statErr := os.Lstat(hookPath)
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		state.Status = StatusError
		state.Detail = statErr.Error()
		return state, false, fmt.Errorf("inspect %s hook for uninstall: %w", name, statErr)
	}
	var current []byte
	if statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || pathHasSymlink(hookPath) {
			state.Status = StatusConflict
			state.Detail = "managed hook was replaced by a symlink"
			return state, false, nil
		}
		if !info.Mode().IsRegular() {
			state.Status = StatusConflict
			state.Detail = "managed hook was replaced by a non-regular file"
			return state, false, nil
		}
		var readErr error
		current, readErr = os.ReadFile(hookPath)
		if readErr != nil {
			state.Status = StatusError
			state.Detail = readErr.Error()
			return state, false, fmt.Errorf("read %s hook for uninstall: %w", name, readErr)
		}
		if !sameInstalled(current, info.Mode(), entry) {
			state.Status = StatusConflict
			state.Detail = "managed hook was modified after installation"
			return state, false, nil
		}
	}
	if entry.OriginalExists {
		backup, readErr := os.ReadFile(entry.BackupPath)
		if readErr != nil {
			state.Status = StatusError
			state.Detail = readErr.Error()
			return state, false, fmt.Errorf("read %s hook backup: %w", name, readErr)
		}
		if hashBytes(backup) != entry.OriginalSHA256 {
			state.Status = StatusConflict
			state.Detail = "external original backup fingerprint does not match"
			return state, false, nil
		}
		if err := writeAtomic(hookPath, backup, os.FileMode(entry.OriginalMode)); err != nil {
			state.Status = StatusError
			state.Detail = err.Error()
			return state, false, fmt.Errorf("restore %s hook: %w", name, err)
		}
	} else if statErr == nil {
		if err := os.Remove(hookPath); err != nil {
			state.Status = StatusError
			state.Detail = err.Error()
			return state, false, fmt.Errorf("remove %s hook: %w", name, err)
		}
	}
	delete(document.Entries, key)
	manifestPath := filepath.Join(paths.Hooks, "manifest.json")
	if err := writeManifest(manifestPath, *document); err != nil {
		// Best-effort rollback keeps an attributable wrapper in place if the
		// manifest could not be committed.
		if len(current) > 0 {
			_ = writeAtomic(hookPath, current, os.FileMode(entry.InstalledMode))
		}
		state.Status = StatusError
		state.Detail = err.Error()
		return state, false, fmt.Errorf("record %s hook uninstall: %w", name, err)
	}
	if entry.BackupPath != "" {
		if err := os.Remove(entry.BackupPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			state.Status = StatusError
			state.Detail = err.Error()
			return state, true, fmt.Errorf("remove %s hook backup: %w", name, err)
		}
	}
	state.Status = StatusNotInstalled
	state.Detail = "Jejak dispatcher uninstalled and original restored"
	return state, true, nil
}
