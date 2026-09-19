package graphdb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestQuarantineDatabaseMovesSQLiteSidecars(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "graph.db")
	store, err := Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(dbPath+suffix, []byte(suffix), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	quarantine, err := QuarantineDatabase(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("QuarantineDatabase() error = %v", err)
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database remained after quarantine: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(filepath.Join(quarantine, "graph.db"+suffix)); err != nil {
			t.Errorf("quarantined %s: %v", suffix, err)
		}
	}
}

func TestRecoverDatabaseMigratesFreshStore(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "graph.db")
	store, lock, err := openMigratedStoreAt(t, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	target := fixtureTarget(t)
	if _, err := store.Register(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	recovery, err := RecoverDatabase(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("RecoverDatabase() error = %v", err)
	}
	if !recovery.Migrated || recovery.QuarantinePath == "" {
		t.Fatalf("recovery = %#v", recovery)
	}
	fresh, err := Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	version, err := fresh.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version != defaultSchema {
		t.Fatalf("fresh schema version = %d, want %d", version, defaultSchema)
	}
	if _, err := fresh.State(context.Background(), target.Repository.ID, target.Worktree.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("fresh store retained graph state: %v", err)
	}
}

func TestStoreQuarantineRejectsOpenReader(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	generation := createActiveGeneration(t, store, target)
	view, err := store.OpenView(ctx, target.Repository.ID, target.Worktree.ID, generation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Quarantine(ctx); !errors.Is(err, ErrActiveReaders) {
		t.Fatalf("Quarantine() error = %v, want ErrActiveReaders", err)
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
}

func openMigratedStoreAt(t *testing.T, dbPath string) (*Store, *WriterLock, error) {
	t.Helper()
	store, err := Open(context.Background(), dbPath)
	if err != nil {
		return nil, nil, err
	}
	lock, err := store.AcquireWriter(context.Background(), 0)
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	if err := store.Migrate(context.Background()); err != nil {
		_ = lock.Close()
		_ = store.Close()
		return nil, nil, err
	}
	return store, lock, nil
}
