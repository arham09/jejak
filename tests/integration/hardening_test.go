package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/arham09/jejak/internal/config"
	"github.com/arham09/jejak/internal/git"
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
	if err != nil || version != 1 {
		t.Fatalf("recovered schema version=%d err=%v", version, err)
	}
}
