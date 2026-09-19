package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func ensureBackup(path string, data []byte, mode uint32) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("external backup path is symlinked")
	}
	if existing, err := os.ReadFile(path); err == nil {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() {
			return errors.New("existing external backup is not a regular file")
		}
		if hashBytes(existing) != hashBytes(data) || fileMode(info.Mode()) != mode {
			return errors.New("existing external backup fingerprint does not match original hook")
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeAtomic(path, data, os.FileMode(mode))
}

func verifyBackup(entry manifestEntry) error {
	data, err := os.ReadFile(entry.BackupPath)
	if err != nil {
		return fmt.Errorf("external original backup is unavailable: %w", err)
	}
	if hashBytes(data) != entry.OriginalSHA256 {
		return errors.New("external original backup fingerprint does not match manifest")
	}
	info, err := os.Stat(entry.BackupPath)
	if err != nil {
		return err
	}
	if fileMode(info.Mode()) != entry.OriginalMode {
		return errors.New("external original backup mode does not match manifest")
	}
	return nil
}

func (m *Manager) restoreAfterManifestFailure(path string, entry manifestEntry) error {
	if entry.OriginalExists {
		data, err := os.ReadFile(entry.BackupPath)
		if err != nil {
			return err
		}
		return writeAtomic(path, data, os.FileMode(entry.OriginalMode))
	}
	current, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if hashBytes(current) != entry.InstalledSHA256 {
		return nil
	}
	return os.Remove(path)
}

func makeBackupPath(root, name, hookPath string) string {
	sum := sha256.Sum256([]byte(hookPath))
	return filepath.Join(root, "originals", fmt.Sprintf("%s-%s.hook", name, hex.EncodeToString(sum[:8])))
}
