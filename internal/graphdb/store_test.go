package graphdb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arham09/jejak/internal/git"
	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
	"github.com/arham09/jejak/internal/testrepo"
)

func openMigratedStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	lock, err := store.AcquireWriter(context.Background(), time.Second)
	if err != nil {
		_ = store.Close()
		t.Fatalf("AcquireWriter() error = %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		_ = lock.Close()
		_ = store.Close()
		t.Fatalf("Migrate() error = %v", err)
	}
	t.Cleanup(func() {
		if err := lock.Close(); err != nil {
			t.Errorf("close writer lock: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}

func fixtureTarget(t *testing.T) repository.Target {
	t.Helper()
	repo := testrepo.New(t)
	repo.Write(t, "README.md", "fixture\n")
	repo.Commit(t, "initial")
	target, err := repository.Resolve(context.Background(), git.NewClient("git"), repo.Root)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	return target
}

func TestMigrateCreatesSchemaAndIsIdempotent(t *testing.T) {
	store := openMigratedStore(t)
	version, err := store.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if version != latestMigration {
		t.Fatalf("schema version = %d, want %d", version, latestMigration)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != latestMigration {
		t.Fatalf("migration rows = %d, want %d", count, latestMigration)
	}
	for _, table := range []string{"repositories", "worktrees", "graph_state", "graph_generations", "nodes", "edges", "parse_cache"} {
		var got string
		if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&got); err != nil {
			t.Errorf("table %q missing: %v", table, err)
		}
	}
}

func TestStoreStorageUsesPrivatePermissions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "repo", "graph.db")
	store, err := Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rootInfo, err := os.Stat(filepath.Dir(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if mode := rootInfo.Mode().Perm(); mode != 0o700 {
		t.Fatalf("storage directory mode = %o, want 700", mode)
	}
	dbInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := dbInfo.Mode().Perm(); mode != 0o600 {
		t.Fatalf("database mode = %o, want 600", mode)
	}
}

func TestMigrationFailureRollsBackSchemaChanges(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	lock, err := store.AcquireWriter(context.Background(), time.Second)
	if err != nil {
		_ = store.Close()
		t.Fatalf("AcquireWriter() error = %v", err)
	}
	t.Cleanup(func() {
		_ = lock.Close()
		_ = store.Close()
	})
	// Leave an incompatible migration table in place. Migrate must report the
	// failure and roll back the migration's newly-created domain tables.
	if _, err := store.db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err == nil {
		t.Fatal("Migrate() unexpectedly accepted an incompatible schema")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'repositories'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed migration left repositories table behind")
	}
}

func TestMigrationRejectsFutureSchemaVersion(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	lock, err := store.AcquireWriter(context.Background(), time.Second)
	if err != nil {
		_ = store.Close()
		t.Fatalf("AcquireWriter() error = %v", err)
	}
	t.Cleanup(func() {
		_ = lock.Close()
		_ = store.Close()
	})
	if _, err := store.db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL); INSERT INTO schema_migrations VALUES (99, 'future.sql', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err == nil {
		t.Fatal("Migrate() unexpectedly accepted a future schema version")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'repositories'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("future schema check created repositories table")
	}
}

func TestRegisterIsIdempotentAndPreservesState(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	first, err := store.Register(context.Background(), target)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if first.Status != graph.StatusUnindexed || first.HeadKnown == false {
		t.Fatalf("initial state = %#v", first)
	}
	second, err := store.Register(context.Background(), target)
	if err != nil {
		t.Fatalf("second Register() error = %v", err)
	}
	if second.Status != graph.StatusUnindexed || second.ActiveGeneration != nil {
		t.Fatalf("repeated registration changed state = %#v", second)
	}
	entries, err := store.ListRepositories(context.Background())
	if err != nil {
		t.Fatalf("ListRepositories() error = %v", err)
	}
	if len(entries) != 1 || entries[0].WorktreeCount != 1 {
		t.Fatalf("catalog entries = %#v", entries)
	}
}

func TestRegisterMarksAnActiveGraphStaleWhenHEADChanges(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	generation, err := store.CreateGeneration(ctx, graph.Generation{
		RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: graph.CommitSHA(target.Worktree.Head),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID, nil); err != nil {
		t.Fatal(err)
	}
	changed := target
	changed.Worktree.Head = repository.CommitSHA("new-head")
	changed.Worktree.HeadKnown = true
	state, err := store.Register(ctx, changed)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != graph.StatusStale || state.ActiveGeneration == nil || *state.ActiveGeneration != generation.ID {
		t.Fatalf("changed-head state = %#v", state)
	}
	if state.IndexedHead != graph.CommitSHA(target.Worktree.Head) {
		t.Fatalf("indexed HEAD changed during observation: %s", state.IndexedHead)
	}
}

func TestGenerationActivationIsAtomicAndChecksExpectedState(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	if _, err := store.Register(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	generation, err := store.CreateGeneration(ctx, graph.Generation{
		RepoID:     target.Repository.ID,
		WorktreeID: target.Worktree.ID,
		Commit:     graph.CommitSHA(target.Worktree.Head),
	})
	if err != nil {
		t.Fatalf("CreateGeneration() error = %v", err)
	}
	if generation.ID != 1 || generation.State != graph.GenerationBuilding {
		t.Fatalf("generation = %#v", generation)
	}
	if err := store.ValidateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID); err != nil {
		t.Fatalf("ValidateGeneration() error = %v", err)
	}
	if err := store.ActivateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID, nil); err != nil {
		t.Fatalf("ActivateGeneration() error = %v", err)
	}
	state, err := store.State(ctx, target.Repository.ID, target.Worktree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != graph.StatusReady || state.ActiveGeneration == nil || *state.ActiveGeneration != generation.ID || state.IndexedHead != graph.CommitSHA(target.Worktree.Head) {
		t.Fatalf("active state = %#v", state)
	}

	second, err := store.CreateGeneration(ctx, graph.Generation{
		RepoID:     target.Repository.ID,
		WorktreeID: target.Worktree.ID,
		Commit:     graph.CommitSHA(target.Worktree.Head),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateGeneration(ctx, target.Repository.ID, target.Worktree.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	wrong := graph.GenerationID(999)
	if err := store.ActivateGeneration(ctx, target.Repository.ID, target.Worktree.ID, second.ID, &wrong); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale activation error = %v, want ErrStaleGeneration", err)
	}
	state, err = store.State(ctx, target.Repository.ID, target.Worktree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *state.ActiveGeneration != generation.ID || state.IndexedHead != graph.CommitSHA(target.Worktree.Head) {
		t.Fatalf("stale activation changed state = %#v", state)
	}
	if err := store.ActivateGeneration(ctx, target.Repository.ID, target.Worktree.ID, second.ID, &generation.ID); err != nil {
		t.Fatalf("second activation error = %v", err)
	}
	state, err = store.State(ctx, target.Repository.ID, target.Worktree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveGeneration == nil || *state.ActiveGeneration != second.ID {
		t.Fatalf("second generation not active = %#v", state)
	}
}

func TestFailedGenerationRetainsActiveGeneration(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateGeneration(ctx, graph.Generation{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: graph.CommitSHA(target.Worktree.Head)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateGeneration(ctx, target.Repository.ID, target.Worktree.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateGeneration(ctx, target.Repository.ID, target.Worktree.ID, first.ID, nil); err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateGeneration(ctx, graph.Generation{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: graph.CommitSHA(target.Worktree.Head)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailGeneration(ctx, target.Repository.ID, target.Worktree.ID, second.ID, errors.New("synthetic failure")); err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, target.Repository.ID, target.Worktree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveGeneration == nil || *state.ActiveGeneration != first.ID || state.Status != graph.StatusFailed || state.LastError != "synthetic failure" {
		t.Fatalf("failed state = %#v", state)
	}
	if err := store.FailGeneration(ctx, target.Repository.ID, target.Worktree.ID, first.ID, errors.New("must not overwrite active")); err == nil {
		t.Fatal("active generation was allowed to transition to failed")
	}
}

func TestActivationRejectsObservedHeadMismatch(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	generation, err := store.CreateGeneration(ctx, graph.Generation{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: "different-commit"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID, nil); !errors.Is(err, ErrStaleState) {
		t.Fatalf("activation error = %v, want ErrStaleState", err)
	}
}

func TestWriterLockSerializesProcesses(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	first, err := store.AcquireWriter(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("first AcquireWriter() error = %v", err)
	}
	defer first.Close()
	second, err := store.AcquireWriter(context.Background(), 0)
	if second != nil {
		_ = second.Close()
		t.Fatal("second writer lock unexpectedly acquired")
	}
	if !errors.Is(err, ErrWriterLockUnavailable) {
		t.Fatalf("second lock error = %v, want ErrWriterLockUnavailable", err)
	}
}

func TestStoreCloseIsIdempotent(t *testing.T) {
	store := openMigratedStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SchemaVersion(context.Background()); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("SchemaVersion() after close error = %v, want ErrStoreClosed", err)
	}
	if err := store.Migrate(context.Background()); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("Migrate() after close error = %v, want ErrStoreClosed", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
