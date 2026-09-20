package graphdb

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
)

// writeActiveGeneration builds one small generation with rows in every
// generation-scoped table and activates it, replacing expected.
func writeActiveGeneration(t *testing.T, store *Store, target repository.Target, fingerprint string, expected *graph.GenerationID) graph.Generation {
	t.Helper()
	ctx := context.Background()
	generation, err := store.CreateGeneration(ctx, graph.Generation{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: graph.CommitSHA(target.Worktree.Head), BuildFingerprint: fingerprint, AnalyzerVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	analysis := searchAnalysis("symbol:"+fingerprint, "Answer", "main/answer.go", "func Answer() int")
	analysis.BuildFingerprint = fingerprint
	analysis.Edges = append(analysis.Edges, graph.Edge{SourceKey: "symbol:" + fingerprint, TargetKey: "symbol:" + fingerprint, Kind: graph.EdgeCalls, Confidence: graph.ConfidenceExact, OwnerPackage: "go:package:example.com/search"})
	analysis.Evidence = []graph.EdgeEvidence{{SourceKey: "symbol:" + fingerprint, TargetKey: "symbol:" + fingerprint, Kind: graph.EdgeCalls, AnalyzerSource: "test", SourceBlob: "symbol:" + fingerprint + "-blob", StartLine: 5, EndLine: 5, Details: "recursion"}}
	analysis.PackageDependencies = []graph.PackageDependency{{SourcePackage: "go:package:example.com/search", TargetPackage: "external:package:fmt"}}
	if err := store.WriteAnalysis(ctx, generation, analysis); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID, expected); err != nil {
		t.Fatal(err)
	}
	return generation
}

func countRows(t *testing.T, store *Store, table string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPruneGenerationsKeepsOnlyTheActiveGeneration(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	first := writeActiveGeneration(t, store, target, "first", nil)
	failed, err := store.CreateGeneration(ctx, graph.Generation{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: graph.CommitSHA(target.Worktree.Head), BuildFingerprint: "failed", AnalyzerVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailGeneration(ctx, target.Repository.ID, target.Worktree.ID, failed.ID, errors.New("synthetic failure")); err != nil {
		t.Fatal(err)
	}
	second := writeActiveGeneration(t, store, target, "second", &first.ID)
	if got := countRows(t, store, "graph_generations"); got != 3 {
		t.Fatalf("generations before pruning = %d, want 3", got)
	}
	edgesBefore := countRows(t, store, "edges")

	pruned, err := store.PruneGenerations(ctx, target.Repository.ID, target.Worktree.ID)
	if err != nil {
		t.Fatalf("PruneGenerations() error = %v", err)
	}
	if pruned != 2 {
		t.Fatalf("pruned = %d, want 2 (retired + failed)", pruned)
	}
	if got := countRows(t, store, "graph_generations"); got != 1 {
		t.Fatalf("generations after pruning = %d, want 1", got)
	}
	if _, err := store.Generation(ctx, target.Repository.ID, target.Worktree.ID, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retired generation lookup = %v, want ErrNotFound", err)
	}
	if _, err := store.Generation(ctx, target.Repository.ID, target.Worktree.ID, second.ID); err != nil {
		t.Fatalf("active generation was pruned: %v", err)
	}
	// Every generation-scoped table cascades; the blob and parse cache rows
	// stay for GC because several generations may share them.
	for _, table := range []string{"packages", "files", "symbols", "nodes", "package_dependencies"} {
		if got := countRows(t, store, table); got == 0 {
			t.Fatalf("%s lost the active generation's rows", table)
		}
	}
	if got := countRows(t, store, "edges"); got >= edgesBefore || got == 0 {
		t.Fatalf("edges after pruning = %d (before %d), want the active generation's rows only", got, edgesBefore)
	}
	if got := countRows(t, store, "edge_evidence"); got != 1 {
		t.Fatalf("edge_evidence after pruning = %d, want 1", got)
	}
	if got := countRows(t, store, "blobs"); got != 2 {
		t.Fatalf("blobs after pruning = %d, want both generations' blobs retained for GC", got)
	}
	state, err := store.State(ctx, target.Repository.ID, target.Worktree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != graph.StatusReady || state.ActiveGeneration == nil || *state.ActiveGeneration != second.ID {
		t.Fatalf("state after pruning = %#v", state)
	}
	view, err := store.OpenView(ctx, target.Repository.ID, target.Worktree.ID, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	results, err := view.FindSymbols(ctx, "Answer")
	if err != nil || len(results) != 1 || len(results[0].Calls) != 1 {
		t.Fatalf("active generation query after pruning = %#v err=%v", results, err)
	}
	// A second pruning pass finds nothing and is not an error.
	if pruned, err := store.PruneGenerations(ctx, target.Repository.ID, target.Worktree.ID); err != nil || pruned != 0 {
		t.Fatalf("repeated prune = %d err=%v, want 0 nil", pruned, err)
	}
}

func TestPruneGenerationsSkipsOpenReadersAndOtherWorktrees(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	other := target
	other.Worktree.ID = repository.WorktreeID("worktree-other")
	other.Worktree.Path = filepath.Join(t.TempDir(), "other")
	other.Worktree.GitDir = filepath.Join(other.Worktree.Path, ".git")
	if _, err := store.Register(ctx, other); err != nil {
		t.Fatal(err)
	}
	otherFirst := writeActiveGeneration(t, store, other, "other-first", nil)
	writeActiveGeneration(t, store, other, "other-second", &otherFirst.ID)

	first := writeActiveGeneration(t, store, target, "first", nil)
	view, err := store.OpenView(ctx, target.Repository.ID, target.Worktree.ID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	writeActiveGeneration(t, store, target, "second", &first.ID)
	pruned, err := store.PruneGenerations(ctx, target.Repository.ID, target.Worktree.ID)
	if err != nil {
		_ = view.Close()
		t.Fatal(err)
	}
	if pruned != 0 {
		_ = view.Close()
		t.Fatalf("pruned %d generation(s) while a reader was open", pruned)
	}
	if _, err := store.Generation(ctx, target.Repository.ID, target.Worktree.ID, first.ID); err != nil {
		_ = view.Close()
		t.Fatalf("generation with open reader was removed: %v", err)
	}
	if _, err := store.Generation(ctx, other.Repository.ID, other.Worktree.ID, otherFirst.ID); err != nil {
		_ = view.Close()
		t.Fatalf("another worktree's generation was pruned: %v", err)
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	pruned, err = store.PruneGenerations(ctx, target.Repository.ID, target.Worktree.ID)
	if err != nil || pruned != 1 {
		t.Fatalf("prune after reader closed = %d err=%v, want 1 nil", pruned, err)
	}
	if _, err := store.Generation(ctx, other.Repository.ID, other.Worktree.ID, otherFirst.ID); err != nil {
		t.Fatalf("another worktree's generation was pruned: %v", err)
	}
}

func TestCompactReturnsFreedPagesToTheFilesystem(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	first := writeActiveGeneration(t, store, target, "first", nil)
	writeActiveGeneration(t, store, target, "second", &first.ID)
	if _, err := store.PruneGenerations(ctx, target.Repository.ID, target.Worktree.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Compact(ctx); err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	var freePages int64
	if err := store.db.QueryRow(`PRAGMA freelist_count`).Scan(&freePages); err != nil {
		t.Fatal(err)
	}
	if freePages != 0 {
		t.Fatalf("free pages after Compact = %d, want 0", freePages)
	}
	var mode int
	if err := store.db.QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != 2 {
		t.Fatalf("auto_vacuum after Compact = %d, want 2 (incremental)", mode)
	}
	view, err := store.OpenView(ctx, target.Repository.ID, target.Worktree.ID, 2)
	if err != nil {
		t.Fatalf("view after Compact: %v", err)
	}
	defer view.Close()
	if results, err := view.FindSymbols(ctx, "Answer"); err != nil || len(results) != 1 {
		t.Fatalf("query after Compact = %#v err=%v", results, err)
	}
}

// TestMigrateUpgradesLegacyStore replays the first two migrations to build
// the previous text-keyed layout with an active generation, then upgrades.
// The upgrade must drop the old rows, reset the worktree to unindexed so the
// manager rebuilds, install the compact layout, and shrink the file.
func TestMigrateUpgradesLegacyStore(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireWriter(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = lock.Close()
		_ = store.Close()
	})
	for _, name := range []string{"001_initial.sql", "002_edge_evidence_index.sql"} {
		contents, err := fs.ReadFile(migrationFiles, "migrations/"+name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, string(contents)); err != nil {
			t.Fatalf("apply legacy migration %s: %v", name, err)
		}
		version, err := migrationVersion(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`, version, name, timestamp(now())); err != nil {
			t.Fatal(err)
		}
	}
	target := fixtureTarget(t)
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	repoID, worktreeID := string(target.Repository.ID), string(target.Worktree.ID)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO graph_generations(repo_id, worktree_id, generation_id, commit_sha, build_fingerprint, analyzer_version, schema_version, state, created_at, validated_at) VALUES (?, ?, 1, ?, 'legacy', 'go-packages-v1', 1, 'active', ?, ?)`, repoID, worktreeID, string(target.Worktree.Head), timestamp(now()), timestamp(now())); err != nil {
		t.Fatal(err)
	}
	// Enough legacy rows to leave free pages behind when their tables drop.
	statement, err := store.db.PrepareContext(ctx, `INSERT INTO nodes(repo_id, worktree_id, generation_id, node_key, node_kind, owned) VALUES (?, ?, 1, ?, 'symbol', 1)`)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2000; index++ {
		if _, err := statement.ExecContext(ctx, repoID, worktreeID, "symbol:legacy:"+string(rune('a'+index%26))+"/"+filepath.Join("deep", "path", "to", "declaration")+"@"+time.Duration(index).String()); err != nil {
			_ = statement.Close()
			t.Fatal(err)
		}
	}
	_ = statement.Close()
	if _, err := store.db.ExecContext(ctx, `UPDATE graph_state SET active_generation = 1, indexed_head = ?, status = 'ready' WHERE repo_id = ? AND worktree_id = ?`, string(target.Worktree.Head), repoID, worktreeID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(store.DBPath())
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() legacy store error = %v", err)
	}
	version, err := store.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != latestMigration {
		t.Fatalf("schema version = %d, want %d", version, latestMigration)
	}
	state, err := store.State(ctx, target.Repository.ID, target.Worktree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != graph.StatusUnindexed || state.ActiveGeneration != nil || state.IndexedHead != "" {
		t.Fatalf("state after upgrade = %#v, want unindexed with no generation", state)
	}
	if got := countRows(t, store, "graph_generations"); got != 0 {
		t.Fatalf("legacy generations survived upgrade: %d", got)
	}
	if got := countRows(t, store, "nodes"); got != 0 {
		t.Fatalf("legacy nodes survived upgrade: %d", got)
	}
	var column string
	if err := store.db.QueryRowContext(ctx, `SELECT name FROM pragma_table_info('edges') WHERE name = 'source_id'`).Scan(&column); err != nil {
		t.Fatalf("compact edges layout missing: %v", err)
	}
	var freePages int64
	if err := store.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages); err != nil {
		t.Fatal(err)
	}
	if freePages != 0 {
		t.Fatalf("free pages after upgrade = %d, want 0 (compacted)", freePages)
	}
	after, err := os.Stat(store.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("database did not shrink: before=%d after=%d", before.Size(), after.Size())
	}
	// The upgraded store accepts a new generation through the normal path.
	writeActiveGeneration(t, store, target, "rebuilt", nil)
	if got := countRows(t, store, "edges"); got == 0 {
		t.Fatal("rebuilt generation wrote no edges")
	}
}
