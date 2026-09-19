package graphdb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
)

func TestGCDryRunAndCollectionRetainActiveGeneration(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	first := createActiveGeneration(t, store, target)
	second, err := store.CreateGeneration(ctx, graph.Generation{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: graph.CommitSHA(target.Worktree.Head), BuildFingerprint: "second", AnalyzerVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailGeneration(ctx, target.Repository.ID, target.Worktree.ID, second.ID, errors.New("synthetic failure")); err != nil {
		t.Fatal(err)
	}
	third, err := store.CreateGeneration(ctx, graph.Generation{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: graph.CommitSHA(target.Worktree.Head), BuildFingerprint: "third", AnalyzerVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailGeneration(ctx, target.Repository.ID, target.Worktree.ID, third.ID, errors.New("newer failure")); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanGC(ctx, target.Repository.ID, GCOptions{KeepGenerations: 1, OlderThan: -1})
	if err != nil {
		t.Fatal(err)
	}
	if plan.RetainedGenerations != 2 || !hasGenerationCandidate(plan, second.ID) {
		t.Fatalf("GC plan = %#v, want active + newest and generation %d candidate", plan, second.ID)
	}
	dryRun, err := store.GC(ctx, target.Repository.ID, GCOptions{KeepGenerations: 1, OlderThan: -1, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !dryRun.DryRun || dryRun.DeletedItems != 0 {
		t.Fatalf("dry-run report = %#v", dryRun)
	}
	report, err := store.CollectGC(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if report.DeletedGenerations != 1 {
		t.Fatalf("collection report = %#v", report)
	}
	if _, err := store.Generation(ctx, target.Repository.ID, target.Worktree.ID, second.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("collected generation lookup = %v, want ErrNotFound", err)
	}
	if _, err := store.Generation(ctx, target.Repository.ID, target.Worktree.ID, first.ID); err != nil {
		t.Fatalf("active generation was collected: %v", err)
	}
}

func TestGCProtectsOpenRetiredReader(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	first := createActiveGeneration(t, store, target)
	view, err := store.OpenView(ctx, target.Repository.ID, target.Worktree.ID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateGeneration(ctx, graph.Generation{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: graph.CommitSHA(target.Worktree.Head), BuildFingerprint: "second", AnalyzerVersion: "test"})
	if err != nil {
		_ = view.Close()
		t.Fatal(err)
	}
	if err := store.ValidateGeneration(ctx, target.Repository.ID, target.Worktree.ID, second.ID); err != nil {
		_ = view.Close()
		t.Fatal(err)
	}
	if err := store.ActivateGeneration(ctx, target.Repository.ID, target.Worktree.ID, second.ID, &first.ID); err != nil {
		_ = view.Close()
		t.Fatal(err)
	}
	plan, err := store.PlanGC(ctx, target.Repository.ID, GCOptions{KeepGenerations: 1, OlderThan: -1})
	if err != nil {
		_ = view.Close()
		t.Fatal(err)
	}
	if hasGenerationCandidate(plan, first.ID) || store.ReaderCount(target.Repository.ID, target.Worktree.ID, first.ID) != 1 {
		_ = view.Close()
		t.Fatalf("open reader was not retained: plan=%#v readers=%d", plan, store.ReaderCount(target.Repository.ID, target.Worktree.ID, first.ID))
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if store.ActiveReaders() {
		t.Fatal("reader reference remained after Close")
	}
	plan, err = store.PlanGC(ctx, target.Repository.ID, GCOptions{KeepGenerations: 1, OlderThan: -1})
	if err != nil {
		t.Fatal(err)
	}
	if !hasGenerationCandidate(plan, first.ID) {
		t.Fatalf("closed reader generation was not eligible: %#v", plan)
	}
}

func TestGCCollectsUnreferencedCacheBlobAndOldTemporary(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	createActiveGeneration(t, store, target)
	if err := store.WriteParseCache(ctx, target.Repository.ID, graph.SyntaxCacheEntry{BlobSHA: "unused-cache", ParserVersion: "test", SyntaxJSON: []byte(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(filepath.Dir(store.DBPath()), "tmp", "jejak-snapshot-old")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "source.go"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(tmp, old, old); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanGC(ctx, target.Repository.ID, GCOptions{OlderThan: time.Hour, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCandidate(plan, GCParseCache, "unused-cache") || !hasCandidatePath(plan, GCTemporary, tmp) {
		t.Fatalf("cleanup plan = %#v", plan)
	}
	if _, err := store.CollectGC(ctx, plan); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM parse_cache WHERE blob_sha='unused-cache'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("parse cache rows = %d, want 0", count)
	}
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary path stat = %v, want not exists", err)
	}
}

func TestGCCollectsInterruptedOldBuildGeneration(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	createActiveGeneration(t, store, target)
	old := time.Now().Add(-48 * time.Hour)
	building, err := store.CreateGeneration(ctx, graph.Generation{
		RepoID:           target.Repository.ID,
		WorktreeID:       target.Worktree.ID,
		Commit:           graph.CommitSHA(target.Worktree.Head),
		BuildFingerprint: "interrupted",
		AnalyzerVersion:  "test",
		CreatedAt:        old,
	})
	if err != nil {
		t.Fatal(err)
	}
	newer, err := store.CreateGeneration(ctx, graph.Generation{
		RepoID:           target.Repository.ID,
		WorktreeID:       target.Worktree.ID,
		Commit:           graph.CommitSHA(target.Worktree.Head),
		BuildFingerprint: "newer-failed",
		AnalyzerVersion:  "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailGeneration(ctx, target.Repository.ID, target.Worktree.ID, newer.ID, errors.New("synthetic failure")); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanGC(ctx, target.Repository.ID, GCOptions{KeepGenerations: 1, OlderThan: time.Hour, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if !hasGenerationCandidate(plan, building.ID) {
		t.Fatalf("old building generation was not eligible: %#v", plan)
	}
	report, err := store.CollectGC(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if report.DeletedGenerations != 1 {
		t.Fatalf("collection report = %#v", report)
	}
	if _, err := store.Generation(ctx, target.Repository.ID, target.Worktree.ID, building.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("interrupted generation lookup = %v, want ErrNotFound", err)
	}
}

func createActiveGeneration(t *testing.T, store *Store, target repository.Target) graph.Generation {
	t.Helper()
	ctx := context.Background()
	generation, err := store.CreateGeneration(ctx, graph.Generation{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: graph.CommitSHA(target.Worktree.Head), BuildFingerprint: "active", AnalyzerVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID, nil); err != nil {
		t.Fatal(err)
	}
	return generation
}

func hasGenerationCandidate(plan GCPlan, id graph.GenerationID) bool {
	for _, candidate := range plan.Candidates {
		if candidate.Kind == GCGeneration && candidate.GenerationID == id {
			return true
		}
	}
	return false
}

func hasCandidate(plan GCPlan, kind GCItemKind, blob string) bool {
	for _, candidate := range plan.Candidates {
		if candidate.Kind == kind && candidate.BlobSHA == blob {
			return true
		}
	}
	return false
}

func hasCandidatePath(plan GCPlan, kind GCItemKind, path string) bool {
	for _, candidate := range plan.Candidates {
		if candidate.Kind == kind && candidate.Path == path {
			return true
		}
	}
	return false
}
