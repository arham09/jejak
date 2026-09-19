package agentskill

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallCreatesOneClaudeSkillAndCodexLink(t *testing.T) {
	root := t.TempDir()
	report, err := Install(root)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Changed {
		t.Fatal("fresh installation did not report a change")
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.CanonicalPath != filepath.Join(canonicalRoot, filepath.FromSlash(canonicalRelativePath)) {
		t.Fatalf("canonical path = %q", report.CanonicalPath)
	}
	contents, err := os.ReadFile(filepath.Join(report.CanonicalPath, canonicalFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contents, canonicalSkill) {
		t.Fatal("materialized skill differs from the embedded canonical skill")
	}
	claudeInfo, err := os.Lstat(report.CanonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	if claudeInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatal("Claude skill directory must be a regular directory")
	}

	linkPath := filepath.Join(root, filepath.FromSlash(codexLink.relative))
	linkInfo, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatal("Codex skill path is not a symlink")
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(target) {
		t.Fatalf("Codex target %q is absolute", target)
	}
	resolved, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != report.CanonicalPath {
		t.Fatalf("Codex resolves to %q, want %q", resolved, report.CanonicalPath)
	}
	if _, err := os.Stat(filepath.Join(root, ".jejak")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy .jejak path exists after fresh install: %v", err)
	}

	second, err := Install(root)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed {
		t.Fatal("idempotent installation reported a change")
	}
	for _, state := range second.States {
		if state.Status != StatusUnchanged {
			t.Fatalf("repeated state = %#v, want unchanged", state)
		}
	}
}

func TestInstallPreservesDifferentClaudeSkill(t *testing.T) {
	root := t.TempDir()
	claudePath := filepath.Join(root, filepath.FromSlash(canonicalRelativePath))
	if err := os.MkdirAll(claudePath, 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("user-owned skill\n")
	if err := os.WriteFile(filepath.Join(claudePath, canonicalFilename), want, 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Install(root)
	if err != nil {
		t.Fatal(err)
	}
	if state := findState(t, report, "Claude"); state.Status != StatusConflict {
		t.Fatalf("Claude state = %#v", state)
	}
	if state := findState(t, report, "Codex"); state.Status != StatusSkipped {
		t.Fatalf("Codex state = %#v", state)
	}
	got, err := os.ReadFile(filepath.Join(claudePath, canonicalFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("conflicting Claude skill changed: got %q want %q", got, want)
	}
}

func TestInstallPreservesExistingClaudeDirectoryWithoutSkill(t *testing.T) {
	root := t.TempDir()
	claudePath := filepath.Join(root, filepath.FromSlash(canonicalRelativePath))
	if err := os.MkdirAll(claudePath, 0o755); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(claudePath, "user-owned.txt")
	if err := os.WriteFile(markerPath, []byte("preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Install(root)
	if err != nil {
		t.Fatal(err)
	}
	if state := findState(t, report, "Claude"); state.Status != StatusConflict {
		t.Fatalf("Claude state = %#v", state)
	}
	if _, err := os.Stat(filepath.Join(claudePath, canonicalFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("SKILL.md was created in a user directory: %v", err)
	}
	if got, err := os.ReadFile(markerPath); err != nil || string(got) != "preserve\n" {
		t.Fatalf("user marker changed: got=%q err=%v", got, err)
	}
}

func TestInstallAdoptsEmptyClaudeSkillDirectory(t *testing.T) {
	root := t.TempDir()
	claudePath := filepath.Join(root, filepath.FromSlash(canonicalRelativePath))
	if err := os.MkdirAll(claudePath, 0o755); err != nil {
		t.Fatal(err)
	}
	report, err := Install(root)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Changed {
		t.Fatal("adopting an empty Claude skill directory did not report a change")
	}
	if state := findState(t, report, "Claude"); state.Status != StatusInstalled {
		t.Fatalf("Claude state = %#v", state)
	}
	contents, err := os.ReadFile(filepath.Join(claudePath, canonicalFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contents, canonicalSkill) {
		t.Fatal("adopted skill differs from the embedded canonical skill")
	}
	if state := findState(t, report, "Codex"); state.Status != StatusInstalled {
		t.Fatalf("Codex state = %#v", state)
	}
}

func TestInstallAdoptsEmptyCodexSkillDirectory(t *testing.T) {
	root := t.TempDir()
	linkPath := filepath.Join(root, filepath.FromSlash(codexLink.relative))
	if err := os.MkdirAll(linkPath, 0o755); err != nil {
		t.Fatal(err)
	}
	report, err := Install(root)
	if err != nil {
		t.Fatal(err)
	}
	if state := findState(t, report, "Codex"); state.Status != StatusInstalled {
		t.Fatalf("Codex state = %#v", state)
	}
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("empty Codex directory was not replaced by a symlink")
	}
	resolved, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != report.CanonicalPath {
		t.Fatalf("Codex resolves to %q, want %q", resolved, report.CanonicalPath)
	}
}

func TestInstallPreservesConflictingCodexPath(t *testing.T) {
	root := t.TempDir()
	conflictPath := filepath.Join(root, filepath.FromSlash(codexLink.relative))
	if err := os.MkdirAll(conflictPath, 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("user-owned\n")
	if err := os.WriteFile(filepath.Join(conflictPath, canonicalFilename), want, 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Install(root)
	if err != nil {
		t.Fatal(err)
	}
	if state := findState(t, report, "Claude"); state.Status != StatusInstalled {
		t.Fatalf("Claude state = %#v", state)
	}
	if state := findState(t, report, "Codex"); state.Status != StatusConflict {
		t.Fatalf("Codex state = %#v", state)
	}
	got, err := os.ReadFile(filepath.Join(conflictPath, canonicalFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("conflicting Codex path changed: got %q want %q", got, want)
	}
}

func TestInstallDoesNotFollowProjectParentSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".agents")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	report, err := Install(root)
	if err != nil {
		t.Fatal(err)
	}
	if state := findState(t, report, "Codex"); state.Status != StatusConflict {
		t.Fatalf("Codex state = %#v", state)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("symlinked parent was followed: entries=%v", entries)
	}
}

func TestInstallMigratesLegacyProjectLayout(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, filepath.FromSlash(legacyCanonicalRelativePath))
	if err := os.MkdirAll(legacyPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyPath, canonicalFilename), canonicalSkill, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{".claude/skills/jejak", ".agents/skills/jejak"} {
		linkPath := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
			t.Fatal(err)
		}
		target, err := filepath.Rel(filepath.Dir(linkPath), legacyPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, linkPath); err != nil {
			t.Fatal(err)
		}
	}

	report, err := Install(root)
	if err != nil {
		t.Fatal(err)
	}
	if findState(t, report, "Claude").Status != StatusMigrated {
		t.Fatalf("Claude migration state = %#v", findState(t, report, "Claude"))
	}
	if findState(t, report, "Codex").Status != StatusMigrated {
		t.Fatalf("Codex migration state = %#v", findState(t, report, "Codex"))
	}
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy skill was not removed: %v", err)
	}
	claudePath := filepath.Join(root, filepath.FromSlash(canonicalRelativePath))
	if info, err := os.Lstat(claudePath); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("Claude path after migration = %#v err=%v", info, err)
	}
	linkPath := filepath.Join(root, filepath.FromSlash(codexLink.relative))
	if info, err := os.Lstat(linkPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("Codex path after migration = %#v err=%v", info, err)
	}
	resolved, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != filepath.Join(canonicalRoot, filepath.FromSlash(canonicalRelativePath)) {
		t.Fatalf("Codex migration target = %q want %q", resolved, filepath.Join(canonicalRoot, filepath.FromSlash(canonicalRelativePath)))
	}
}

func findState(t *testing.T, report Report, name string) State {
	t.Helper()
	for _, state := range report.States {
		if state.Name == name {
			return state
		}
	}
	t.Fatalf("missing %s state in %#v", name, report)
	return State{}
}
