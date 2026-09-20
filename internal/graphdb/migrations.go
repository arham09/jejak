package graphdb

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// migrationFiles is embedded so the database schema travels with the binary.
// Migrations are applied in lexical/version order inside one transaction.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrate applies pending schema migrations. Callers should hold the
// repository writer lock while invoking it.
//
// A brand-new file is switched to incremental auto-vacuum before its first
// table exists, so pruned generations can later return their pages to the
// filesystem. After a migration that freed pages, for example one that drops
// and recreates graph tables, the file is compacted once.
func (s *Store) Migrate(ctx context.Context) error {
	db, err := s.database()
	if err != nil {
		return err
	}
	if err := prepareEmptyDatabase(ctx, db); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin graph schema migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema migration table: %w", err)
	}
	var current int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read current graph schema version: %w", err)
	}
	if current > 0 {
		var domainTables int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM sqlite_master
			WHERE type = 'table' AND name IN ('repositories', 'worktrees', 'graph_state', 'graph_generations')
		`).Scan(&domainTables); err != nil {
			return fmt.Errorf("validate existing graph schema: %w", err)
		}
		if domainTables != 4 {
			return fmt.Errorf("graph schema version %d is missing required domain tables", current)
		}
	}

	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("read embedded graph migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	maxVersion := 0
	seenVersions := make(map[int]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		version, err := migrationVersion(entry.Name())
		if err != nil {
			return err
		}
		if previous, exists := seenVersions[version]; exists {
			return fmt.Errorf("duplicate graph migration version %d in %q and %q", version, previous, entry.Name())
		}
		seenVersions[version] = entry.Name()
		if version > maxVersion {
			maxVersion = version
		}
	}
	if current > maxVersion {
		return fmt.Errorf("graph schema version %d is newer than this binary supports (latest %d)", current, maxVersion)
	}
	applied := false
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		version, err := migrationVersion(entry.Name())
		if err != nil {
			return err
		}
		if version <= current {
			continue
		}
		contents, err := fs.ReadFile(migrationFiles, "migrations/"+entry.Name())
		if err != nil {
			return fmt.Errorf("read graph migration %q: %w", entry.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, string(contents)); err != nil {
			return fmt.Errorf("apply graph migration %q: %w", entry.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`, version, entry.Name(), timestamp(now())); err != nil {
			return fmt.Errorf("record graph migration %q: %w", entry.Name(), err)
		}
		current = version
		applied = true
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit graph schema migration: %w", err)
	}
	if applied {
		if err := s.compactAfterMigration(ctx, db); err != nil {
			return err
		}
	}
	return nil
}

// prepareEmptyDatabase enables incremental auto-vacuum on a file that has no
// tables yet. SQLite only accepts the change before the first table exists
// or through a full VACUUM, so this is the cheap moment to make it.
func prepareEmptyDatabase(ctx context.Context, db *sql.DB) error {
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
		return fmt.Errorf("inspect graph schema before migration: %w", err)
	}
	if tables > 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, `PRAGMA auto_vacuum = INCREMENTAL`); err != nil {
		return fmt.Errorf("enable incremental auto-vacuum: %w", err)
	}
	return nil
}

// compactAfterMigration rebuilds the file when a migration left free pages
// behind, which happens when a migration drops tables. Migrations that only
// add structure leave nothing to reclaim and skip the rebuild.
func (s *Store) compactAfterMigration(ctx context.Context, db *sql.DB) error {
	var freePages int64
	if err := db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages); err != nil {
		return fmt.Errorf("inspect graph free pages after migration: %w", err)
	}
	if freePages == 0 {
		return nil
	}
	if _, err := s.Compact(ctx); err != nil {
		return fmt.Errorf("compact graph database after migration: %w", err)
	}
	return nil
}

func migrationVersion(name string) (int, error) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	underscore := strings.IndexByte(base, '_')
	if underscore <= 0 {
		return 0, fmt.Errorf("invalid graph migration filename %q", name)
	}
	version, err := strconv.Atoi(base[:underscore])
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("invalid graph migration version %q", name)
	}
	return version, nil
}

func now() (t time.Time) { return time.Now().UTC() }
