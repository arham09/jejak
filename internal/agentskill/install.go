package agentskill

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	canonicalRelativePath       = ".claude/skills/jejak"
	legacyCanonicalRelativePath = ".jejak/skills/jejak"
	canonicalFilename           = "SKILL.md"
)

// Status describes the result for one project skill path.
type Status string

const (
	StatusInstalled Status = "installed"
	StatusUnchanged Status = "unchanged"
	StatusSkipped   Status = "skipped"
	StatusConflict  Status = "conflict"
	StatusError     Status = "error"
	StatusMigrated  Status = "migrated"
)

// State is the inspectable installation result for one canonical or discovery
// path.
type State struct {
	Name   string
	Path   string
	Status Status
	Detail string
}

// Report contains deterministic project skill installation states.
type Report struct {
	CanonicalPath string
	States        []State
	Changed       bool
}

type discoveryLink struct {
	name     string
	relative string
}

var codexLink = discoveryLink{name: "Codex", relative: ".agents/skills/jejak"}

// Install writes one embedded Jejak skill into the project-local Claude
// discovery directory and exposes that same directory to Codex through a
// relative symlink. Existing content is adopted only when it already matches;
// conflicting user paths are preserved. A legacy project-local canonical copy
// is removed only when it is an exact, otherwise-empty copy produced by an
// older release.
func Install(projectRoot string) (Report, error) {
	root, err := canonicalDirectory(projectRoot)
	if err != nil {
		return Report{}, err
	}
	canonicalPath := filepath.Join(root, filepath.FromSlash(canonicalRelativePath))
	report := Report{CanonicalPath: canonicalPath}

	legacyPath := filepath.Join(root, filepath.FromSlash(legacyCanonicalRelativePath))
	legacyManaged := legacyCanonicalManaged(legacyPath)
	canonicalState, canonicalChanged, canonicalReady, canonicalErr := installCanonicalSkill(root, canonicalPath, legacyPath, legacyManaged)
	report.States = append(report.States, canonicalState)
	report.Changed = canonicalChanged
	if !canonicalReady {
		report.States = append(report.States, State{
			Name:   codexLink.name,
			Path:   filepath.Join(root, filepath.FromSlash(codexLink.relative)),
			Status: StatusSkipped,
			Detail: "canonical Claude skill is unavailable",
		})
		return report, canonicalErr
	}

	linkState, linkChanged, linkErr := installCodexLink(root, canonicalPath, legacyPath, legacyManaged)
	report.States = append(report.States, linkState)
	report.Changed = report.Changed || linkChanged
	if linkErr != nil {
		canonicalErr = errors.Join(canonicalErr, linkErr)
	}
	if legacyManaged && !legacyCanonicalInUse(root, legacyPath) {
		removed, removeErr := removeLegacyCanonical(root, legacyPath)
		if removeErr != nil {
			canonicalErr = errors.Join(canonicalErr, removeErr)
			report.States = append(report.States, State{
				Name:   "Legacy",
				Path:   legacyPath,
				Status: StatusError,
				Detail: removeErr.Error(),
			})
		} else if removed {
			report.Changed = true
			report.States = append(report.States, State{
				Name:   "Legacy",
				Path:   legacyPath,
				Status: StatusMigrated,
				Detail: "removed the old project-local Jejak skill",
			})
		}
	}
	return report, canonicalErr
}

func canonicalDirectory(projectRoot string) (string, error) {
	if strings.TrimSpace(projectRoot) == "" {
		return "", errors.New("agent skill installation requires a project root")
	}
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve project root symlinks: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect project root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("agent skill project root %q is not a directory", resolved)
	}
	return filepath.Clean(resolved), nil
}

func installCanonicalSkill(root, canonicalPath, legacyPath string, legacyManaged bool) (State, bool, bool, error) {
	state := State{Name: "Claude", Path: canonicalPath, Status: StatusInstalled}
	parentChanged, conflict, err := ensureDirectories(root, []string{".claude", "skills"})
	if err != nil {
		state.Status = StatusError
		state.Detail = err.Error()
		return state, parentChanged, false, err
	}
	if conflict != "" {
		state.Status = StatusConflict
		state.Detail = conflict
		return state, parentChanged, false, nil
	}

	directoryCreated := false
	info, err := os.Lstat(canonicalPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if mkdirErr := os.Mkdir(canonicalPath, 0o755); mkdirErr != nil {
			wrapped := fmt.Errorf("create Claude skill directory %q: %w", canonicalPath, mkdirErr)
			state.Status = StatusError
			state.Detail = wrapped.Error()
			return state, parentChanged, false, wrapped
		}
		parentChanged = true
		directoryCreated = true
	case err != nil:
		wrapped := fmt.Errorf("inspect Claude skill directory %q: %w", canonicalPath, err)
		state.Status = StatusError
		state.Detail = wrapped.Error()
		return state, parentChanged, false, wrapped
	case info.Mode()&os.ModeSymlink != 0:
		target, readErr := os.Readlink(canonicalPath)
		if readErr != nil {
			wrapped := fmt.Errorf("read Claude skill symlink %q: %w", canonicalPath, readErr)
			state.Status = StatusError
			state.Detail = wrapped.Error()
			return state, parentChanged, false, wrapped
		}
		if !legacyManaged || resolveLinkTarget(canonicalPath, target) != legacyPath {
			state.Status = StatusConflict
			state.Detail = fmt.Sprintf("existing path is a symlink to %q; preserved", target)
			return state, parentChanged, false, nil
		}
		if removeErr := os.Remove(canonicalPath); removeErr != nil {
			wrapped := fmt.Errorf("remove legacy Claude skill symlink %q: %w", canonicalPath, removeErr)
			state.Status = StatusError
			state.Detail = wrapped.Error()
			return state, parentChanged, false, wrapped
		}
		if mkdirErr := os.Mkdir(canonicalPath, 0o755); mkdirErr != nil {
			wrapped := fmt.Errorf("recreate Claude skill directory %q: %w", canonicalPath, mkdirErr)
			state.Status = StatusError
			state.Detail = wrapped.Error()
			return state, true, false, wrapped
		}
		parentChanged = true
		directoryCreated = true
		state.Status = StatusMigrated
		state.Detail = "replaced the old project-local skill link"
	case !info.IsDir():
		state.Status = StatusConflict
		state.Detail = "existing Claude skill path is not a directory; preserved"
		return state, parentChanged, false, nil
	}

	skillPath := filepath.Join(canonicalPath, canonicalFilename)
	skillInfo, skillErr := os.Lstat(skillPath)
	switch {
	case errors.Is(skillErr, fs.ErrNotExist):
		if !directoryCreated {
			empty, emptyErr := directoryIsEmpty(canonicalPath)
			if emptyErr != nil {
				state.Status = StatusError
				state.Detail = emptyErr.Error()
				return state, parentChanged, false, emptyErr
			}
			if !empty {
				state.Status = StatusConflict
				state.Detail = "existing Claude skill directory has no SKILL.md; preserved"
				return state, parentChanged, false, nil
			}
		}
		if writeErr := writeFileAtomic(skillPath, canonicalSkill, 0o644); writeErr != nil {
			state.Status = StatusError
			state.Detail = writeErr.Error()
			return state, parentChanged, false, writeErr
		}
		if state.Status != StatusMigrated {
			state.Detail = "materialized the bundled Jejak skill"
		}
		return state, true, true, nil
	case skillErr != nil:
		wrapped := fmt.Errorf("inspect Claude skill %q: %w", skillPath, skillErr)
		state.Status = StatusError
		state.Detail = wrapped.Error()
		return state, parentChanged, false, wrapped
	case skillInfo.Mode()&os.ModeSymlink != 0 || !skillInfo.Mode().IsRegular():
		state.Status = StatusConflict
		state.Detail = "existing Claude SKILL.md is not a regular file; preserved"
		return state, parentChanged, false, nil
	}

	contents, readErr := os.ReadFile(skillPath)
	if readErr != nil {
		wrapped := fmt.Errorf("read Claude skill %q: %w", skillPath, readErr)
		state.Status = StatusError
		state.Detail = wrapped.Error()
		return state, parentChanged, false, wrapped
	}
	if !bytes.Equal(contents, canonicalSkill) {
		state.Status = StatusConflict
		state.Detail = "existing Claude SKILL.md differs from the bundled Jejak skill; preserved"
		return state, parentChanged, false, nil
	}
	if state.Status != StatusMigrated {
		state.Status = StatusUnchanged
		state.Detail = "Claude skill already matches"
	}
	return state, parentChanged, true, nil
}

func installCodexLink(root, canonicalPath, legacyPath string, legacyManaged bool) (State, bool, error) {
	linkPath := filepath.Join(root, filepath.FromSlash(codexLink.relative))
	state := State{Name: codexLink.name, Path: linkPath}
	parts := strings.Split(filepath.ToSlash(filepath.Dir(codexLink.relative)), "/")
	changed, conflict, err := ensureDirectories(root, parts)
	if err != nil {
		state.Status = StatusError
		state.Detail = err.Error()
		return state, changed, err
	}
	if conflict != "" {
		state.Status = StatusConflict
		state.Detail = conflict
		return state, changed, nil
	}

	info, err := os.Lstat(linkPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		target, linkErr := linkCodexSkill(linkPath, canonicalPath)
		if linkErr != nil {
			state.Status = StatusError
			state.Detail = linkErr.Error()
			return state, changed, linkErr
		}
		state.Status = StatusInstalled
		state.Detail = fmt.Sprintf("linked to %s", target)
		return state, true, nil
	case err != nil:
		wrapped := fmt.Errorf("inspect Codex skill path %q: %w", linkPath, err)
		state.Status = StatusError
		state.Detail = wrapped.Error()
		return state, changed, wrapped
	case info.Mode()&os.ModeSymlink == 0:
		adopted, adoptErr := adoptEmptyDirectory(linkPath, info)
		if adoptErr != nil {
			state.Status = StatusError
			state.Detail = adoptErr.Error()
			return state, changed, adoptErr
		}
		if !adopted {
			state.Status = StatusConflict
			state.Detail = "existing Codex skill path is not a symlink; preserved"
			return state, changed, nil
		}
		target, linkErr := linkCodexSkill(linkPath, canonicalPath)
		if linkErr != nil {
			state.Status = StatusError
			state.Detail = linkErr.Error()
			return state, true, linkErr
		}
		state.Status = StatusInstalled
		state.Detail = fmt.Sprintf("linked to %s", target)
		return state, true, nil
	}

	target, err := os.Readlink(linkPath)
	if err != nil {
		wrapped := fmt.Errorf("read Codex skill symlink %q: %w", linkPath, err)
		state.Status = StatusError
		state.Detail = wrapped.Error()
		return state, changed, wrapped
	}
	resolvedTarget := resolveLinkTarget(linkPath, target)
	if resolvedTarget == canonicalPath {
		state.Status = StatusUnchanged
		state.Detail = "Codex skill symlink already matches"
		return state, changed, nil
	}
	if legacyManaged && resolvedTarget == legacyPath {
		if removeErr := os.Remove(linkPath); removeErr != nil {
			wrapped := fmt.Errorf("remove legacy Codex skill symlink %q: %w", linkPath, removeErr)
			state.Status = StatusError
			state.Detail = wrapped.Error()
			return state, changed, wrapped
		}
		newTarget, relErr := filepath.Rel(filepath.Dir(linkPath), canonicalPath)
		if relErr != nil {
			wrapped := fmt.Errorf("resolve Codex skill symlink target: %w", relErr)
			state.Status = StatusError
			state.Detail = wrapped.Error()
			return state, true, wrapped
		}
		if symlinkErr := os.Symlink(newTarget, linkPath); symlinkErr != nil {
			wrapped := fmt.Errorf("recreate Codex skill symlink %q: %w", linkPath, symlinkErr)
			state.Status = StatusError
			state.Detail = wrapped.Error()
			return state, true, wrapped
		}
		state.Status = StatusMigrated
		state.Detail = fmt.Sprintf("migrated to %s", filepath.ToSlash(newTarget))
		return state, true, nil
	}
	state.Status = StatusConflict
	state.Detail = fmt.Sprintf("existing Codex symlink points to %q; preserved", target)
	return state, changed, nil
}

// linkCodexSkill points the Codex discovery path at the canonical skill
// directory through a relative symlink and reports the recorded target.
func linkCodexSkill(linkPath, canonicalPath string) (string, error) {
	target, err := filepath.Rel(filepath.Dir(linkPath), canonicalPath)
	if err != nil {
		return "", fmt.Errorf("resolve Codex skill symlink target: %w", err)
	}
	if err := os.Symlink(target, linkPath); err != nil {
		return "", fmt.Errorf("create Codex skill symlink %q: %w", linkPath, err)
	}
	return filepath.ToSlash(target), nil
}

// directoryIsEmpty reports whether the directory holds no entries. An empty
// managed path carries no user content, so installation may adopt it.
func directoryIsEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, fmt.Errorf("inspect agent skill directory %q: %w", path, err)
	}
	return len(entries) == 0, nil
}

// adoptEmptyDirectory removes path when it is an empty directory. Any other
// content stays in place and remains a reported conflict.
func adoptEmptyDirectory(path string, info fs.FileInfo) (bool, error) {
	if !info.IsDir() {
		return false, nil
	}
	empty, err := directoryIsEmpty(path)
	if err != nil || !empty {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		return false, fmt.Errorf("remove empty agent skill directory %q: %w", path, err)
	}
	return true, nil
}

func resolveLinkTarget(linkPath, target string) string {
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(linkPath), target)
	}
	return filepath.Clean(target)
}

func ensureDirectories(root string, parts []string) (bool, string, error) {
	current := root
	changed := false
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || filepath.Base(part) != part {
			return changed, "", fmt.Errorf("invalid agent skill directory component %q", part)
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if mkdirErr := os.Mkdir(current, 0o755); mkdirErr != nil {
				return changed, "", fmt.Errorf("create agent skill directory %q: %w", current, mkdirErr)
			}
			changed = true
		case err != nil:
			return changed, "", fmt.Errorf("inspect agent skill directory %q: %w", current, err)
		case info.Mode()&os.ModeSymlink != 0:
			return changed, fmt.Sprintf("parent path %q is a symlink; preserved", current), nil
		case !info.IsDir():
			return changed, fmt.Sprintf("parent path %q is not a directory; preserved", current), nil
		}
	}
	return changed, "", nil
}

func legacyCanonicalManaged(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false
	}
	skillPath := filepath.Join(path, canonicalFilename)
	skillInfo, err := os.Lstat(skillPath)
	if err != nil || skillInfo.Mode()&os.ModeSymlink != 0 || !skillInfo.Mode().IsRegular() {
		return false
	}
	contents, err := os.ReadFile(skillPath)
	if err != nil || !bytes.Equal(contents, canonicalSkill) {
		return false
	}
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) == 1 && entries[0].Name() == canonicalFilename
}

func legacyCanonicalInUse(root, legacyPath string) bool {
	for _, relative := range []string{canonicalRelativePath, codexLink.relative} {
		linkPath := filepath.Join(root, filepath.FromSlash(relative))
		info, err := os.Lstat(linkPath)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(linkPath)
		if err != nil {
			continue
		}
		if resolveLinkTarget(linkPath, target) == legacyPath {
			return true
		}
	}
	return false
}

func removeLegacyCanonical(root, path string) (bool, error) {
	if !legacyCanonicalManaged(path) || legacyCanonicalInUse(root, path) {
		return false, nil
	}
	if err := os.Remove(filepath.Join(path, canonicalFilename)); err != nil {
		return false, fmt.Errorf("remove legacy canonical skill %q: %w", path, err)
	}
	if err := os.Remove(path); err != nil {
		return false, fmt.Errorf("remove legacy canonical skill directory %q: %w", path, err)
	}
	for _, parent := range []string{filepath.Dir(path), filepath.Dir(filepath.Dir(path))} {
		info, err := os.Lstat(parent)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return false, fmt.Errorf("inspect legacy skill parent %q: %w", parent, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			break
		}
		entries, err := os.ReadDir(parent)
		if err != nil {
			return false, fmt.Errorf("inspect legacy skill parent %q: %w", parent, err)
		}
		if len(entries) != 0 {
			break
		}
		if err := os.Remove(parent); err != nil {
			return false, fmt.Errorf("remove empty legacy skill parent %q: %w", parent, err)
		}
	}
	return true, nil
}

func writeFileAtomic(path string, contents []byte, mode fs.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".jejak-skill-*")
	if err != nil {
		return fmt.Errorf("create temporary skill file: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set temporary skill permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary skill: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary skill: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish canonical skill %q: %w", path, err)
	}
	removeTemporary = false
	return nil
}
