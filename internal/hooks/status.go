package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/arham09/jejak/internal/repository"
)

func (m *Manager) inspectOne(ctx context.Context, target repository.Target, hooksPath string, document manifest, name string) HookState {
	hookPath := filepath.Join(hooksPath, name)
	state := HookState{Name: name, Path: hookPath}
	where, detail, err := m.classify(ctx, target, hookPath)
	if err != nil {
		state.Status = StatusError
		state.Detail = err.Error()
		return state
	}
	if where != locationSafe {
		state.Status = StatusSkipped
		state.Detail = detail
		return state
	}
	entry, managed := document.Entries[manifestKey(name, hookPath)]
	info, statErr := os.Lstat(hookPath)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			if managed {
				state.Status = StatusConflict
				state.Detail = "managed dispatcher is missing"
			} else {
				state.Status = StatusNotInstalled
				state.Detail = "no hook installed"
			}
			return state
		}
		state.Status = StatusError
		state.Detail = statErr.Error()
		return state
	}
	if info.Mode()&os.ModeSymlink != 0 || pathHasSymlink(hookPath) {
		state.Status = StatusSkipped
		state.Detail = "hook path is symlinked"
		return state
	}
	if !info.Mode().IsRegular() {
		state.Status = StatusSkipped
		state.Detail = "hook path is not a regular file"
		return state
	}
	data, readErr := os.ReadFile(hookPath)
	if readErr != nil {
		state.Status = StatusError
		state.Detail = readErr.Error()
		return state
	}
	if managed {
		if sameInstalled(data, info.Mode(), entry) {
			if entry.OriginalExists {
				if backupErr := verifyBackup(entry); backupErr != nil {
					state.Status = StatusConflict
					state.Detail = backupErr.Error()
					return state
				}
			}
			state.Status = StatusInstalled
			state.Detail = fmt.Sprintf("managed by Jejak for %d worktree(s)", len(entry.Owners))
			return state
		}
		state.Status = StatusConflict
		state.Detail = "managed hook was modified after installation"
		return state
	}
	if isManagedWrapper(data) {
		state.Status = StatusConflict
		state.Detail = "managed marker found without an attributable manifest"
		return state
	}
	if info.Mode().Perm()&0o111 == 0 {
		state.Status = StatusSkipped
		state.Detail = "existing hook is not executable"
		return state
	}
	state.Status = StatusUnmanaged
	state.Detail = "existing executable hook is not managed by Jejak"
	return state
}

func sameInstalled(data []byte, mode os.FileMode, entry manifestEntry) bool {
	return hashBytes(data) == entry.InstalledSHA256 && fileMode(mode) == entry.InstalledMode && isManagedWrapper(data)
}

func isManagedWrapper(data []byte) bool {
	return bytes.HasPrefix(data, []byte("#!/bin/sh\n"+managedMarker+"\n"))
}
