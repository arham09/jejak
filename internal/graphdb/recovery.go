package graphdb

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrActiveReaders prevents replacing a database while this Store still owns
// generation-pinned read transactions.
var ErrActiveReaders = errors.New("graph store has active readers")

// RecoveryReport records the durable evidence location and migration result
// for a database recovery operation.
type RecoveryReport struct {
	Version        string `json:"version"`
	DatabasePath   string `json:"database_path"`
	QuarantinePath string `json:"quarantine_path"`
	Migrated       bool   `json:"migrated"`
}

type quarantineMove struct {
	source string
	target string
}

// Quarantine closes this Store and moves its database plus SQLite sidecars to
// a private, timestamped directory. It refuses to replace storage while a
// view opened from this Store remains live.
func (s *Store) Quarantine(ctx context.Context) (string, error) {
	if s == nil {
		return "", errors.New("quarantine nil graph store")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.ActiveReaders() {
		return "", ErrActiveReaders
	}
	if _, err := s.database(); err != nil {
		return "", err
	}
	if err := s.Close(); err != nil {
		return "", err
	}
	return quarantineDatabase(ctx, s.dbPath)
}

// QuarantineDatabase moves an existing graph.db and its -wal/-shm sidecars
// into a private sibling quarantine directory. It does not create a
// replacement database. Callers performing cross-process maintenance should
// hold the repository writer lock before invoking it.
func QuarantineDatabase(ctx context.Context, dbPath string) (string, error) {
	return quarantineDatabase(ctx, dbPath)
}

// RecoverDatabase quarantines the selected database, creates a fresh store,
// and applies embedded migrations. Graph records are intentionally not
// fabricated; callers must re-register and rebuild through the normal graph
// manager after this function returns.
func RecoverDatabase(ctx context.Context, dbPath string) (RecoveryReport, error) {
	if err := ctx.Err(); err != nil {
		return RecoveryReport{}, err
	}
	absPath, err := canonicalDatabasePath(dbPath)
	if err != nil {
		return RecoveryReport{}, err
	}
	root := filepath.Dir(absPath)
	if err := ensureStorageLayout(root); err != nil {
		return RecoveryReport{}, err
	}
	lock, err := acquireWriter(ctx, filepath.Join(root, "locks", "writer.lock"), defaultBusyTimeout)
	if err != nil {
		return RecoveryReport{}, fmt.Errorf("acquire recovery writer lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	quarantine, err := quarantineDatabase(ctx, absPath)
	if err != nil {
		return RecoveryReport{}, fmt.Errorf("quarantine graph database: %w", err)
	}
	fresh, err := Open(ctx, absPath)
	if err != nil {
		return RecoveryReport{Version: "jejak.recovery.v1", DatabasePath: absPath, QuarantinePath: quarantine}, fmt.Errorf("open recovered graph database: %w", err)
	}
	migrateErr := fresh.Migrate(ctx)
	closeErr := fresh.Close()
	if migrateErr != nil {
		return RecoveryReport{Version: "jejak.recovery.v1", DatabasePath: absPath, QuarantinePath: quarantine}, fmt.Errorf("migrate recovered graph database: %w", migrateErr)
	}
	if closeErr != nil {
		return RecoveryReport{Version: "jejak.recovery.v1", DatabasePath: absPath, QuarantinePath: quarantine, Migrated: true}, closeErr
	}
	return RecoveryReport{Version: "jejak.recovery.v1", DatabasePath: absPath, QuarantinePath: quarantine, Migrated: true}, nil
}

func canonicalDatabasePath(dbPath string) (string, error) {
	if strings.TrimSpace(dbPath) == "" {
		return "", fmt.Errorf("%w: database path is empty", ErrInvalidGeneration)
	}
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return "", fmt.Errorf("resolve graph database path: %w", err)
	}
	absPath = filepath.Clean(absPath)
	if filepath.Base(absPath) != "graph.db" {
		return "", fmt.Errorf("%w: recovery target must be graph.db", ErrInvalidGeneration)
	}
	return absPath, nil
}

func quarantineDatabase(ctx context.Context, dbPath string) (string, error) {
	absPath, err := canonicalDatabasePath(dbPath)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	info, err := os.Lstat(absPath)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: graph database %q", ErrNotFound, absPath)
	}
	if err != nil {
		return "", fmt.Errorf("inspect graph database %q: %w", absPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: graph database %q is not a regular file", ErrInvalidGeneration, absPath)
	}
	root := filepath.Dir(absPath)
	quarantineRoot := filepath.Join(root, "quarantine")
	if err := os.MkdirAll(quarantineRoot, 0o700); err != nil {
		return "", fmt.Errorf("create graph quarantine directory: %w", err)
	}
	if err := os.Chmod(quarantineRoot, 0o700); err != nil {
		return "", fmt.Errorf("protect graph quarantine directory: %w", err)
	}
	name := time.Now().UTC().Format("20060102T150405.000000000Z")
	destination, err := os.MkdirTemp(quarantineRoot, name+"-")
	if err != nil {
		return "", fmt.Errorf("create graph quarantine: %w", err)
	}
	if err := os.Chmod(destination, 0o700); err != nil {
		_ = os.Remove(destination)
		return "", fmt.Errorf("protect graph quarantine: %w", err)
	}
	moved := make([]quarantineMove, 0, 3)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		source := absPath + suffix
		fileInfo, statErr := os.Lstat(source)
		if errors.Is(statErr, fs.ErrNotExist) {
			continue
		}
		if statErr != nil {
			rollbackQuarantine(moved)
			_ = os.Remove(destination)
			return "", fmt.Errorf("inspect graph sidecar %q: %w", source, statErr)
		}
		if fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
			rollbackQuarantine(moved)
			_ = os.Remove(destination)
			return "", fmt.Errorf("%w: graph sidecar %q is not a regular file", ErrInvalidGeneration, source)
		}
		target := filepath.Join(destination, filepath.Base(source))
		if renameErr := os.Rename(source, target); renameErr != nil {
			rollbackQuarantine(moved)
			_ = os.Remove(destination)
			return "", fmt.Errorf("quarantine graph file %q: %w", source, renameErr)
		}
		moved = append(moved, quarantineMove{source: source, target: target})
	}
	return destination, nil
}

func rollbackQuarantine(moved []quarantineMove) {
	for index := len(moved) - 1; index >= 0; index-- {
		_ = os.Rename(moved[index].target, moved[index].source)
	}
}
