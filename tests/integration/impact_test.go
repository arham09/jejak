package integration

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arham09/jejak/internal/brief"
	"github.com/arham09/jejak/internal/impact"
	"github.com/arham09/jejak/internal/testrepo"
)

func TestImpactAndContextCommandsAreBoundedAndExplainable(t *testing.T) {
	repo := impactRepository(t, "example.com/impact")
	dataRoot := filepath.Join(t.TempDir(), "data")
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks"); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "impact", "--committed", "--task", "add retry handling to SubmitTransaction", "--context-limit", "8", "--implementation-limit", "8", "--validation-limit", "8")
	if code != 0 {
		t.Fatalf("impact code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, heading := range []string{"Jejak Impact Report v1", "CONTEXT RADIUS", "IMPLEMENTATION RADIUS", "VALIDATION RADIUS", "SubmitTransaction", "evidence"} {
		if !strings.Contains(stdout, heading) {
			t.Fatalf("impact output missing %q: %s", heading, stdout)
		}
	}
	if strings.Contains(stdout, "working tree") && !strings.Contains(stdout, "working tree changes excluded") {
		t.Fatalf("impact output suggests dirty source: %s", stdout)
	}

	code, jsonOutput, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "--json", "impact", "--committed", "--task=SubmitTransaction")
	if code != 0 {
		t.Fatalf("impact JSON code=%d stdout=%q stderr=%q", code, jsonOutput, stderr)
	}
	var report impact.Report
	if err := json.Unmarshal([]byte(jsonOutput), &report); err != nil {
		t.Fatalf("decode impact JSON: %v\n%s", err, jsonOutput)
	}
	if report.SchemaVersion != impact.SchemaVersion || report.Metadata.Commit == "" || len(report.Context.Items) == 0 || len(report.Implementation.Items) == 0 {
		t.Fatalf("impact JSON report = %#v", report)
	}

	// Change the checkout after indexing. Context must still read the committed
	// Git blob and must not expose this dirty value.
	repo.Write(t, "transaction.go", "package impact\n\nfunc SubmitTransaction() int { return 999 }\n")
	code, contextOutput, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "context", "--committed", "--task", "SubmitTransaction", "--excerpt-lines", "12")
	if code != 0 {
		t.Fatalf("context code=%d stdout=%q stderr=%q", code, contextOutput, stderr)
	}
	for _, heading := range []string{"Jejak Context Report v1", "SOURCE EXCERPTS", "func SubmitTransaction"} {
		if !strings.Contains(contextOutput, heading) {
			t.Fatalf("context output missing %q: %s", heading, contextOutput)
		}
	}
	if strings.Contains(contextOutput, "return 999") {
		t.Fatalf("context used dirty checkout bytes: %s", contextOutput)
	}
	code, contextJSON, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "--json", "context", "--committed", "--task", "SubmitTransaction")
	if code != 0 {
		t.Fatalf("context JSON code=%d stdout=%q stderr=%q", code, contextJSON, stderr)
	}
	var contextReport brief.Report
	if err := json.Unmarshal([]byte(contextJSON), &contextReport); err != nil {
		t.Fatalf("decode context JSON: %v\n%s", err, contextJSON)
	}
	if contextReport.SchemaVersion != brief.SchemaVersion || contextReport.Impact.Metadata.Commit == "" || len(contextReport.Excerpts) == 0 {
		t.Fatalf("context JSON report = %#v", contextReport)
	}

	code, workingOutput, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "impact", "--working-tree", "--task", "SubmitTransaction")
	if code != 0 || !strings.Contains(workingOutput, "working-tree overlay") || !strings.Contains(workingOutput, "Changed files 1") {
		t.Fatalf("working-tree response code=%d stdout=%q stderr=%q", code, workingOutput, stderr)
	}
}

func TestImpactAndContextRepositoryIsolation(t *testing.T) {
	alpha := impactRepository(t, "github.com/acme/impact-alpha")
	beta := impactRepository(t, "github.com/acme/impact-beta")
	dataRoot := filepath.Join(t.TempDir(), "data")
	for _, repo := range []*testrepo.Repository{alpha, beta} {
		if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks"); code != 0 {
			t.Fatalf("init %s code=%d stderr=%q", repo.Root, code, stderr)
		}
	}
	code, stdout, stderr := runCLI("--data-dir", dataRoot, "-C", alpha.Root, "impact", "--task", "SubmitTransaction")
	if code != 0 {
		t.Fatalf("alpha impact code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(stdout, "impact-beta") || strings.Contains(stdout, beta.Root) {
		t.Fatalf("alpha impact leaked beta records: %s", stdout)
	}
	code, stdout, stderr = runCLI("--data-dir", dataRoot, "-C", alpha.Root, "context", "--task", "SubmitTransaction")
	if code != 0 {
		t.Fatalf("alpha context code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(stdout, "impact-beta") || strings.Contains(stdout, beta.Root) {
		t.Fatalf("alpha context leaked beta records: %s", stdout)
	}
}

func impactRepository(t *testing.T, remote string) *testrepo.Repository {
	t.Helper()
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/impact\n\ngo 1.27\n")
	repo.Write(t, "transaction.go", `package impact

func SubmitTransaction() int { return debit() }

func debit() int { return 42 }

func Caller() int { return SubmitTransaction() }
`)
	repo.Write(t, "transaction_test.go", `package impact

import "testing"

func TestSubmitTransaction(t *testing.T) {
	if SubmitTransaction() != 42 { t.Fatal("bad") }
}
`)
	repo.Commit(t, "impact fixture")
	repo.AddRemote(t, "origin", "https://"+remote+".git")
	return repo
}
