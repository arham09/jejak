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

func (m *Manager) installOne(ctx context.Context, target repository.Target, paths config.RepositoryPaths, hooksPath string, document *manifest, name string) (HookState, bool, error) {
	hookPath := filepath.Join(hooksPath, name)
	base := HookState{Name: name, Path: hookPath}
	where, detail, err := m.classify(ctx, target, hookPath)
	if err != nil {
		base.Status = StatusError
		base.Detail = err.Error()
		return base, false, fmt.Errorf("classify %s: %w", name, err)
	}
	if where != locationSafe {
		base.Status = StatusSkipped
		base.Detail = detail
		return base, false, nil
	}
	key := manifestKey(name, hookPath)
	entry, managed := document.Entries[key]
	info, statErr := os.Lstat(hookPath)
	exists := statErr == nil
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		base.Status = StatusError
		base.Detail = statErr.Error()
		return base, false, fmt.Errorf("inspect %s hook: %w", name, statErr)
	}
	if managed {
		if entry.Path != hookPath || entry.Name != name {
			base.Status = StatusConflict
			base.Detail = "manifest ownership does not match the effective hook path"
			return base, false, nil
		}
		if exists {
			if info.Mode()&os.ModeSymlink != 0 {
				base.Status = StatusConflict
				base.Detail = "managed hook was replaced by a symlink"
				return base, false, nil
			}
			if !info.Mode().IsRegular() {
				base.Status = StatusConflict
				base.Detail = "managed hook was replaced by a non-regular file"
				return base, false, nil
			}
			data, readErr := os.ReadFile(hookPath)
			if readErr != nil {
				base.Status = StatusError
				base.Detail = readErr.Error()
				return base, false, fmt.Errorf("read managed %s hook: %w", name, readErr)
			}
			if !sameInstalled(data, info.Mode(), entry) {
				base.Status = StatusConflict
				base.Detail = "managed hook was modified after installation"
				return base, false, nil
			}
			if entry.OriginalExists {
				if backupErr := verifyBackup(entry); backupErr != nil {
					base.Status = StatusConflict
					base.Detail = backupErr.Error()
					return base, false, nil
				}
			}
			changed := addOwner(&entry, string(target.Worktree.ID))
			if changed {
				document.Entries[key] = entry
				if writeErr := writeManifest(filepath.Join(paths.Hooks, "manifest.json"), *document); writeErr != nil {
					base.Status = StatusError
					base.Detail = writeErr.Error()
					return base, false, fmt.Errorf("record %s hook owner: %w", name, writeErr)
				}
			}
			base.Status = StatusInstalled
			base.Detail = "already managed; existing dispatcher preserved"
			return base, changed, nil
		}
		if entry.OriginalExists && entry.BackupPath == "" {
			base.Status = StatusConflict
			base.Detail = "managed hook backup is missing from the manifest"
			return base, false, nil
		}
		if entry.OriginalExists {
			if backupErr := verifyBackup(entry); backupErr != nil {
				base.Status = StatusConflict
				base.Detail = backupErr.Error()
				return base, false, nil
			}
		}
		wrapper := GenerateDispatcher(name, entry.BackupPath, m.dataRoot)
		if writeErr := m.writeHook(hookPath, wrapper); writeErr != nil {
			base.Status = StatusError
			base.Detail = writeErr.Error()
			return base, false, fmt.Errorf("reinstall %s hook: %w", name, writeErr)
		}
		entry.InstalledMode = uint32(0o755)
		entry.InstalledSHA256 = hashBytes(wrapper)
		addOwner(&entry, string(target.Worktree.ID))
		document.Entries[key] = entry
		if writeErr := writeManifest(filepath.Join(paths.Hooks, "manifest.json"), *document); writeErr != nil {
			_ = m.restoreAfterManifestFailure(hookPath, entry)
			base.Status = StatusError
			base.Detail = writeErr.Error()
			return base, false, fmt.Errorf("record reinstalled %s hook: %w", name, writeErr)
		}
		base.Status = StatusInstalled
		base.Detail = "dispatcher reinstalled; original remains externally backed up"
		return base, true, nil
	}

	var original []byte
	var originalMode uint32
	if exists {
		if info.Mode()&os.ModeSymlink != 0 || pathHasSymlink(hookPath) {
			base.Status = StatusSkipped
			base.Detail = "existing hook is symlinked; Jejak will not replace it"
			return base, false, nil
		}
		if !info.Mode().IsRegular() {
			base.Status = StatusSkipped
			base.Detail = "existing hook is not a regular file"
			return base, false, nil
		}
		if info.Mode().Perm()&0o111 == 0 {
			base.Status = StatusSkipped
			base.Detail = "existing hook is not executable"
			return base, false, nil
		}
		original, err = os.ReadFile(hookPath)
		if err != nil {
			base.Status = StatusError
			base.Detail = err.Error()
			return base, false, fmt.Errorf("read existing %s hook: %w", name, err)
		}
		if isManagedWrapper(original) {
			base.Status = StatusConflict
			base.Detail = "managed marker found without an attributable manifest"
			return base, false, nil
		}
		originalMode = fileMode(info.Mode())
	}

	var backupPath string
	if exists {
		backupPath = makeBackupPath(paths.Hooks, name, hookPath)
		if backupErr := ensureBackup(backupPath, original, originalMode); backupErr != nil {
			base.Status = StatusError
			base.Detail = backupErr.Error()
			return base, false, fmt.Errorf("back up existing %s hook: %w", name, backupErr)
		}
	}
	wrapper := GenerateDispatcher(name, backupPath, m.dataRoot)
	if err := m.writeHook(hookPath, wrapper); err != nil {
		if backupPath != "" {
			_ = removeIfMatches(backupPath, original)
		}
		base.Status = StatusError
		base.Detail = err.Error()
		return base, false, fmt.Errorf("install %s hook: %w", name, err)
	}
	entry = manifestEntry{
		Name:            name,
		Path:            hookPath,
		BackupPath:      backupPath,
		OriginalExists:  exists,
		OriginalMode:    originalMode,
		OriginalSHA256:  hashBytes(original),
		InstalledMode:   uint32(0o755),
		InstalledSHA256: hashBytes(wrapper),
		Owners:          []string{string(target.Worktree.ID)},
	}
	document.Entries[key] = entry
	if err := writeManifest(filepath.Join(paths.Hooks, "manifest.json"), *document); err != nil {
		_ = m.restoreAfterManifestFailure(hookPath, entry)
		if backupPath != "" {
			_ = removeIfMatches(backupPath, original)
		}
		delete(document.Entries, key)
		base.Status = StatusError
		base.Detail = err.Error()
		return base, false, fmt.Errorf("record %s hook installation: %w", name, err)
	}
	base.Status = StatusInstalled
	if exists {
		base.Detail = "dispatcher installed; original backed up externally"
	} else {
		base.Detail = "dispatcher installed; no prior hook"
	}
	return base, true, nil
}
