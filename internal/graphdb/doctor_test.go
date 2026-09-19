package graphdb

import (
	"context"
	"strings"
	"testing"

	"github.com/arham09/jejak/internal/graph"
)

func TestDoctorReportsHealthyUnindexedWorktree(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	if _, err := store.Register(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	report, err := store.Doctor(context.Background(), target.Repository.ID, target.Worktree.ID)
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}
	if !report.Healthy || report.Status != DoctorHealthy || report.HasErrors() {
		t.Fatalf("unindexed report = %#v", report)
	}
	if report.State == nil || report.State.Status != graph.StatusUnindexed {
		t.Fatalf("unindexed state = %#v", report.State)
	}
}

func TestDoctorDetectsGenerationProvenanceAndOwnershipFaults(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	generation, err := store.CreateGeneration(ctx, graph.Generation{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: graph.CommitSHA(target.Worktree.Head), BuildFingerprint: "test-fingerprint", AnalyzerVersion: "test-analyzer"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO files(repo_id, worktree_id, generation_id, file_key, path, blob_sha, package_key) VALUES (?, ?, ?, 'file:missing', 'missing.go', 'missing-blob', '')`, string(target.Repository.ID), string(target.Worktree.ID), int64(generation.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE files SET blob_sha = 'missing-blob' WHERE repo_id = ? AND worktree_id = ? AND generation_id = ?`, string(target.Repository.ID), string(target.Worktree.ID), int64(generation.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE graph_state SET indexed_head = 'wrong-head' WHERE repo_id = ? AND worktree_id = ?`, string(target.Repository.ID), string(target.Worktree.ID)); err != nil {
		t.Fatal(err)
	}
	report, err := store.Doctor(ctx, target.Repository.ID, target.Worktree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Healthy || !report.HasErrors() {
		t.Fatalf("fault report = %#v", report)
	}
	joined := make([]string, 0, len(report.Issues))
	for _, issue := range report.Issues {
		joined = append(joined, issue.Code)
	}
	for _, want := range []string{"graph.commit", "graph.file_blobs"} {
		if !strings.Contains(strings.Join(joined, "\n"), want) {
			t.Errorf("fault report missing issue %q: %#v", want, report.Issues)
		}
	}
}

func TestDoctorDetectsMalformedLifecycleTimestamps(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE graph_state SET updated_at = 'not-a-timestamp' WHERE repo_id = ? AND worktree_id = ?`, string(target.Repository.ID), string(target.Worktree.ID)); err != nil {
		t.Fatal(err)
	}
	report, err := store.Doctor(ctx, target.Repository.ID, target.Worktree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Healthy || !hasDoctorIssue(report, "graph.state") {
		t.Fatalf("malformed state timestamp report = %#v", report)
	}
}

func hasDoctorIssue(report DoctorReport, code string) bool {
	for _, issue := range report.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
