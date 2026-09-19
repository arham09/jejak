// Package graphdb owns Jejak's SQLite persistence boundary.
package graphdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
	_ "modernc.org/sqlite"
)

const (
	defaultBusyTimeout = 5 * time.Second
	defaultSchema      = 1
)

// Store is a concrete SQLite store for one repository database. Callers must
// close it when the command finishes.
type Store struct {
	db       *sql.DB
	dbPath   string
	lockPath string

	readersMu sync.Mutex
	readers   map[readerKey]int
}

// readerKey identifies a generation-pinned view held by this Store. Reader
// references are an in-process maintenance guard; SQLite's transaction
// snapshot remains the source of truth for the view itself.
type readerKey struct {
	repoID     string
	worktreeID string
	generation int64
}

// Open opens a repository database and configures per-connection SQLite
// behavior. It does not run migrations; callers should acquire the repository
// writer lock and call Migrate before using repository records.
func Open(ctx context.Context, dbPath string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolve graph database path: %w", err)
	}
	absPath = filepath.Clean(absPath)
	root := filepath.Dir(absPath)
	if err := ensureStorageLayout(root); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", sqliteDSN(absPath))
	if err != nil {
		return nil, fmt.Errorf("open graph database %q: %w", absPath, err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping graph database %q: %w", absPath, err)
	}
	if err := os.Chmod(absPath, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("protect graph database %q: %w", absPath, err)
	}
	store := &Store{
		db:       db,
		dbPath:   absPath,
		lockPath: filepath.Join(root, "locks", "writer.lock"),
		readers:  make(map[readerKey]int),
	}
	if err := store.verifyConnection(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close releases the underlying database connections.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close graph database %q: %w", s.dbPath, err)
	}
	s.db = nil
	return nil
}

// DBPath returns the database path owned by the store.
func (s *Store) DBPath() string {
	if s == nil {
		return ""
	}
	return s.dbPath
}

// LockPath returns the process-level writer lock path for the repository.
func (s *Store) LockPath() string {
	if s == nil {
		return ""
	}
	return s.lockPath
}

func (s *Store) database() (*sql.DB, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}
	return s.db, nil
}

func (s *Store) retainReader(repoID repository.RepoID, worktreeID repository.WorktreeID, generationID graph.GenerationID) {
	if s == nil {
		return
	}
	s.readersMu.Lock()
	if s.readers == nil {
		s.readers = make(map[readerKey]int)
	}
	key := readerKey{repoID: string(repoID), worktreeID: string(worktreeID), generation: int64(generationID)}
	s.readers[key]++
	s.readersMu.Unlock()
}

func (s *Store) releaseReader(repoID repository.RepoID, worktreeID repository.WorktreeID, generationID graph.GenerationID) {
	if s == nil {
		return
	}
	s.readersMu.Lock()
	key := readerKey{repoID: string(repoID), worktreeID: string(worktreeID), generation: int64(generationID)}
	if count := s.readers[key]; count <= 1 {
		delete(s.readers, key)
	} else {
		s.readers[key] = count - 1
	}
	s.readersMu.Unlock()
}

// ReaderCount returns the number of open views for one generation held by
// this Store. It is intended for maintenance and diagnostics.
func (s *Store) ReaderCount(repoID repository.RepoID, worktreeID repository.WorktreeID, generationID graph.GenerationID) int {
	if s == nil {
		return 0
	}
	s.readersMu.Lock()
	defer s.readersMu.Unlock()
	return s.readers[readerKey{repoID: string(repoID), worktreeID: string(worktreeID), generation: int64(generationID)}]
}

// ActiveReaders reports whether any generation-pinned views remain open on
// this Store. Database replacement must wait for this to become false.
func (s *Store) ActiveReaders() bool {
	if s == nil {
		return false
	}
	s.readersMu.Lock()
	defer s.readersMu.Unlock()
	return len(s.readers) > 0
}

// SchemaVersion returns the highest applied migration version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	db, err := s.database()
	if err != nil {
		return 0, err
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read graph schema version: %w", err)
	}
	return version, nil
}

func (s *Store) verifyConnection(ctx context.Context) error {
	var foreignKeys int
	if err := s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return fmt.Errorf("verify SQLite foreign keys: %w", err)
	}
	if foreignKeys != 1 {
		return errors.New("SQLite foreign keys are disabled")
	}
	var journalMode string
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		return fmt.Errorf("verify SQLite journal mode: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(journalMode), "wal") {
		return fmt.Errorf("SQLite journal mode is %q, want WAL", journalMode)
	}
	return nil
}

func ensureStorageLayout(root string) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create graph storage directory %q: %w", root, err)
	}
	for _, name := range []string{"cache", "tmp", "hooks", "locks", "logs"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create graph storage directory %q: %w", path, err)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("protect graph storage directory %q: %w", path, err)
		}
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return fmt.Errorf("protect graph storage directory %q: %w", root, err)
	}
	return nil
}

func sqliteDSN(path string) string {
	u := url.URL{Scheme: "file", Path: path}
	query := u.Query()
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", defaultBusyTimeout/time.Millisecond))
	u.RawQuery = query.Encode()
	return u.String()
}

func timestamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTimestamp(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", value, err)
	}
	return t, nil
}
