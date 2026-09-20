package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arham09/jejak/internal/config"
	"github.com/arham09/jejak/internal/git"
	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/graphdb"
	"github.com/arham09/jejak/internal/repository"
)

func TestDoctor(t *testing.T) {
	repo := newCommittedRepository(t, "github.com/acme/doctor")
	dataRoot := filepath.Join(t.TempDir(), "data")
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init"); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "doctor")
	if code != 0 {
		t.Fatalf("doctor code=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "Jejak Doctor Report v1") || !strings.Contains(stdout, "Status      healthy") || !strings.Contains(stdout, "sqlite.integrity") || !strings.Contains(stdout, "graph.edge_endpoints") {
		t.Fatalf("doctor output = %q", stdout)
	}
	code, stdout, stderr = runCLI("--json", "--data-dir", dataRoot, "-C", repo.Root, "doctor")
	if code != 0 {
		t.Fatalf("JSON doctor code=%d stderr=%q", code, stderr)
	}
	var report graphdb.DoctorReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode doctor JSON: %v (%q)", err, stdout)
	}
	if report.Version != "jejak.doctor.v1" || !report.Healthy {
		t.Fatalf("doctor JSON report = %#v", report)
	}
}

func TestGCDryRun(t *testing.T) {
	repo := newCommittedRepository(t, "github.com/acme/gc-dry-run")
	dataRoot := filepath.Join(t.TempDir(), "data")
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init"); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}
	repo.Write(t, "main.go", "package fixture\n\nfunc Answer() int { return 43 }\n")
	repo.Commit(t, "second")
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "sync"); code != 0 {
		t.Fatalf("sync code=%d stderr=%q", code, stderr)
	}
	target, err := repository.Resolve(context.Background(), git.NewClient("git"), repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := config.RepositoryPathsFor(dataRoot, string(target.Repository.ID))
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI("--json", "--data-dir", dataRoot, "-C", repo.Root, "gc", "--dry-run", "--keep-generations", "1")
	if code != 0 {
		t.Fatalf("dry-run code=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
	var report graphdb.GCReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode GC JSON: %v (%q)", err, stdout)
	}
	if !report.DryRun || report.DeletedItems != 0 || report.PlannedItems == 0 {
		t.Fatalf("dry-run report = %#v", report)
	}
	after, err := os.ReadFile(paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("GC dry-run changed the database file")
	}
}

func TestGCRetention(t *testing.T) {
	repo := newCommittedRepository(t, "github.com/acme/gc-retention")
	dataRoot := filepath.Join(t.TempDir(), "data")
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init"); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}
	for value := 43; value <= 44; value++ {
		repo.Write(t, "main.go", "package fixture\n\nfunc Answer() int { return "+strconv.Itoa(value)+" }\n")
		repo.Commit(t, "advance")
		if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "sync"); code != 0 {
			t.Fatalf("sync %d code=%d stderr=%q", value, code, stderr)
		}
	}
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "gc", "--keep-generations", "1")
	if code != 0 {
		t.Fatalf("gc code=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "Mode        execute") || !strings.Contains(stdout, "Deleted") {
		t.Fatalf("gc output = %q", stdout)
	}
	if code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "status"); code != 0 || !strings.Contains(stdout, "status      ready") {
		t.Fatalf("status after GC code=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
}

func TestGCRepositoryIsolation(t *testing.T) {
	alpha := newCommittedRepository(t, "github.com/acme/gc-alpha")
	beta := newCommittedRepository(t, "github.com/acme/gc-beta")
	dataRoot := filepath.Join(t.TempDir(), "data")
	for _, root := range []string{alpha.Root, beta.Root} {
		if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", root, "init"); code != 0 {
			t.Fatalf("init %s code=%d stderr=%q", root, code, stderr)
		}
	}
	alpha.Write(t, "main.go", "package fixture\n\nfunc Answer() int { return 99 }\n")
	alpha.Commit(t, "alpha update")
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", alpha.Root, "sync"); code != 0 {
		t.Fatalf("alpha sync code=%d stderr=%q", code, stderr)
	}
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", alpha.Root, "gc"); code != 0 {
		t.Fatalf("alpha gc code=%d stderr=%q", code, stderr)
	}
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", beta.Root, "status")
	if code != 0 || !strings.Contains(stdout, "Identity    github.com/acme/gc-beta") || !strings.Contains(stdout, "status      ready") {
		t.Fatalf("beta after alpha GC code=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
}

func TestCorruptionRecovery(t *testing.T) {
	repo := newCommittedRepository(t, "github.com/acme/recovery")
	dataRoot := filepath.Join(t.TempDir(), "data")
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init"); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}
	target, err := repository.Resolve(context.Background(), git.NewClient("git"), repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := config.RepositoryPathsFor(dataRoot, string(target.Repository.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.DB, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "doctor")
	if code == 0 || !strings.Contains(strings.ToLower(stderr), "doctor") {
		t.Fatalf("corrupt doctor code=%d stderr=%q", code, stderr)
	}
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "doctor", "--repair")
	if code != 0 {
		t.Fatalf("repair code=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "Status      healthy") || !strings.Contains(stdout, "recovered storage") {
		t.Fatalf("repair output = %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(paths.Root, "quarantine")); err != nil {
		t.Fatalf("quarantine evidence missing: %v", err)
	}
}

func TestMigrationRecovery(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "graph.db")
	if err := os.WriteFile(dbPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := graphdb.RecoverDatabase(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Migrated || report.QuarantinePath == "" {
		t.Fatalf("recovery report = %#v", report)
	}
	store, err := graphdb.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	version, err := store.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Compare against a database this binary builds from nothing rather than
	// a literal, so adding a migration does not require editing this test.
	if want := freshSchemaVersion(t); version != want {
		t.Fatalf("recovered schema version = %d, want %d", version, want)
	}
}

// freshSchemaVersion reports the schema version of a newly migrated database.
func freshSchemaVersion(t *testing.T) int {
	t.Helper()
	store, err := graphdb.Open(context.Background(), filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lock, err := store.AcquireWriter(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	version, err := store.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return version
}

// TestSynchronizationReplacesInsteadOfAccumulatingGenerations covers the
// retention contract: repeated init and every sync leave exactly one durable
// generation per worktree, so a store stays the size of one graph.
func TestSynchronizationReplacesInsteadOfAccumulatingGenerations(t *testing.T) {
	repo := newCommittedRepository(t, "github.com/acme/retention")
	dataRoot := filepath.Join(t.TempDir(), "data")
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks", "--no-agent-skills"); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}
	// A rebuild writes a new generation for the same commit; init must
	// replace the previous one rather than keep both.
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "rebuild", "--quiet"); code != 0 {
		t.Fatalf("rebuild code=%d stderr=%q", code, stderr)
	}
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks", "--no-agent-skills"); code != 0 {
		t.Fatalf("repeated init code=%d stderr=%q", code, stderr)
	}
	for value := 43; value <= 45; value++ {
		repo.Write(t, "main.go", "package fixture\n\nfunc Answer() int { return "+strconv.Itoa(value)+" }\n")
		repo.Commit(t, "advance")
		if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "sync", "--quiet"); code != 0 {
			t.Fatalf("sync %d code=%d stderr=%q", value, code, stderr)
		}
	}
	target, err := repository.Resolve(context.Background(), git.NewClient("git"), repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := config.RepositoryPathsFor(dataRoot, string(target.Repository.ID))
	if err != nil {
		t.Fatal(err)
	}
	store, err := graphdb.Open(context.Background(), paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.State(context.Background(), target.Repository.ID, target.Worktree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != graph.StatusReady || state.ActiveGeneration == nil || *state.ActiveGeneration < 5 {
		t.Fatalf("state after five builds = %#v", state)
	}
	for id := int64(1); id < int64(*state.ActiveGeneration); id++ {
		if _, err := store.Generation(context.Background(), target.Repository.ID, target.Worktree.ID, graph.GenerationID(id)); err == nil {
			t.Fatalf("superseded generation %d still stored", id)
		}
	}
	counts, err := store.GenerationCounts(context.Background(), target.Repository.ID, target.Worktree.ID, *state.ActiveGeneration)
	if err != nil || counts.Symbols == 0 {
		t.Fatalf("active generation counts = %#v err=%v", counts, err)
	}
}
