package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arham09/jejak/internal/brief"
	"github.com/arham09/jejak/internal/impact"
	"github.com/arham09/jejak/internal/testrepo"
)

func TestWorkingTreeOverlayUsesEffectiveBytesAndPreservesCommittedGraph(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/overlay\n\ngo 1.27\n")
	repo.Write(t, "main.go", `package overlay

func SubmitTransaction() int { return debit() }

func debit() int { return 42 }

func Caller() int { return SubmitTransaction() }
`)
	repo.Write(t, "main_test.go", `package overlay

import "testing"

func TestSubmitTransaction(t *testing.T) {
	if SubmitTransaction() != 42 { t.Fatal("bad") }
}
`)
	repo.Commit(t, "overlay baseline")
	dataRoot := filepath.Join(t.TempDir(), "data")
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks"); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}
	before := directoryDigest(t, dataRoot)

	repo.Write(t, "main.go", `package overlay

func SubmitTransaction() int { return reserve() }

func debit() int { return 42 }

func reserve() int { return 99 }

func Caller() int { return SubmitTransaction() }
`)
	// Stage one version, then leave a different version in the checkout. The
	// overlay must use the latter while retaining both status bytes in its
	// manifest.
	repo.Run(t, "add", "main.go")
	repo.Write(t, "main.go", `package overlay

func SubmitTransaction() int { return reserve() + 1 }

func debit() int { return 42 }

func reserve() int { return 99 }

func Caller() int { return SubmitTransaction() }
`)
	repo.Write(t, "working.go", `package overlay

func NewWorkingSymbol() int { return 7 }
`)
	code, statusOutput, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "status")
	if code != 0 || !strings.Contains(statusOutput, "status      dirty") || !strings.Contains(statusOutput, "MM main.go") || !strings.Contains(statusOutput, "overlay     ready") {
		t.Fatalf("working-tree status code=%d stderr=%q output=%q", code, stderr, statusOutput)
	}

	code, output, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "--json", "context", "--task", "SubmitTransaction")
	if code != 0 {
		t.Fatalf("effective context code=%d stderr=%q output=%q", code, stderr, output)
	}
	var effectiveContext brief.Report
	if err := json.Unmarshal([]byte(output), &effectiveContext); err != nil {
		t.Fatalf("decode effective context: %v\n%s", err, output)
	}
	if effectiveContext.Impact.Metadata.SourceMode != "working-tree" || effectiveContext.Impact.Metadata.OverlayID == "" || effectiveContext.Impact.Metadata.SourceManifest == "" {
		t.Fatalf("effective metadata = %#v", effectiveContext.Impact.Metadata)
	}
	if effectiveContext.Impact.Metadata.ChangedFiles < 2 {
		t.Fatalf("effective changed files = %d", effectiveContext.Impact.Metadata.ChangedFiles)
	}
	if !containsString(effectiveContext.Impact.Metadata.ChangedPaths, "main.go") || !containsString(effectiveContext.Impact.Metadata.ChangedPaths, "working.go") {
		t.Fatalf("effective changed paths = %#v", effectiveContext.Impact.Metadata.ChangedPaths)
	}
	if !containsExcerpt(effectiveContext.Excerpts, "return reserve() + 1") {
		t.Fatalf("effective excerpts did not contain working-tree bytes: %#v", effectiveContext.Excerpts)
	}

	code, output, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "symbol", "NewWorkingSymbol")
	if code != 0 || !strings.Contains(output, "working-tree overlay") || !strings.Contains(output, "Symbol NewWorkingSymbol") {
		t.Fatalf("effective graph code=%d stderr=%q output=%q", code, stderr, output)
	}
	code, output, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "symbol", "SubmitTransaction")
	if code != 0 || !strings.Contains(output, "reserve calls") || strings.Contains(output, "debit calls") {
		t.Fatalf("effective graph retained stale call code=%d stderr=%q output=%q", code, stderr, output)
	}

	code, output, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "--json", "impact", "--working-tree")
	if code != 0 {
		t.Fatalf("actual-change impact code=%d stderr=%q output=%q", code, stderr, output)
	}
	var actual impact.Report
	if err := json.Unmarshal([]byte(output), &actual); err != nil {
		t.Fatalf("decode actual-change impact: %v\n%s", err, output)
	}
	if actual.Task != "working tree changes" || actual.Metadata.SourceMode != "working-tree" || len(actual.Seeds.Selected) == 0 {
		t.Fatalf("actual-change report = %#v", actual)
	}
	if !hasSeedName(actual, "SubmitTransaction") && !hasSeedName(actual, "NewWorkingSymbol") {
		t.Fatalf("actual-change seeds = %#v", actual.Seeds.Selected)
	}

	code, output, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "--json", "context", "--committed", "--task", "SubmitTransaction")
	if code != 0 {
		t.Fatalf("committed context code=%d stderr=%q output=%q", code, stderr, output)
	}
	var committedContext brief.Report
	if err := json.Unmarshal([]byte(output), &committedContext); err != nil {
		t.Fatalf("decode committed context: %v\n%s", err, output)
	}
	if committedContext.Impact.Metadata.SourceMode != "committed" || !containsExcerpt(committedContext.Excerpts, "return debit()") || containsExcerpt(committedContext.Excerpts, "+ 57") {
		t.Fatalf("committed context was not HEAD-only: %#v", committedContext.Excerpts)
	}

	after := directoryDigest(t, dataRoot)
	if before != after {
		t.Fatalf("overlay commands changed durable data: before=%s after=%s", before, after)
	}
}

func TestWorkingTreeDeletionRetainsHistoricalSeedEvidence(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/overlaydelete\n\ngo 1.27\n")
	repo.Write(t, "main.go", `package overlaydelete

func Removed() int { return 42 }

func Caller() int { return Removed() }
`)
	repo.Commit(t, "deletion baseline")
	dataRoot := filepath.Join(t.TempDir(), "data")
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks"); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}
	repo.Write(t, "main.go", `package overlaydelete

func Caller() int { return 0 }
`)

	code, output, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "--json", "impact", "--working-tree")
	if code != 0 {
		t.Fatalf("deletion impact code=%d stderr=%q output=%q", code, stderr, output)
	}
	var report impact.Report
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("decode deletion impact: %v\n%s", err, output)
	}
	var historical *impact.SeedCandidate
	for index := range report.Seeds.Selected {
		if report.Seeds.Selected[index].Name == "Removed" {
			historical = &report.Seeds.Selected[index]
			break
		}
	}
	if historical == nil || !historical.Historical || historical.ChangeKind != "deleted" {
		t.Fatalf("deleted seed missing historical evidence: %#v", report.Seeds)
	}
	foundHistoricalItem := false
	for _, item := range report.Context.Items {
		if item.Name == "Removed" && item.Historical && item.Label == impact.LabelHistorical {
			foundHistoricalItem = true
			break
		}
	}
	if !foundHistoricalItem {
		t.Fatalf("historical context item missing: %#v", report.Context.Items)
	}
	code, output, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "--json", "context", "--working-tree")
	if code != 0 {
		t.Fatalf("deletion context code=%d stderr=%q output=%q", code, stderr, output)
	}
	var deletionContext brief.Report
	if err := json.Unmarshal([]byte(output), &deletionContext); err != nil {
		t.Fatalf("decode deletion context: %v\n%s", err, output)
	}
	if !containsExcerpt(deletionContext.Excerpts, "return 42") {
		t.Fatalf("deleted baseline source excerpt missing: %#v", deletionContext.Excerpts)
	}

	code, output, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "--working-tree", "symbol", "Removed")
	if code != 0 || !strings.Contains(output, "historical true (deleted)") {
		t.Fatalf("historical graph code=%d stderr=%q output=%q", code, stderr, output)
	}
	code, output, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "--committed", "symbol", "Removed")
	if code != 0 || strings.Contains(output, "historical true") || strings.Contains(output, "working-tree overlay") {
		t.Fatalf("committed graph changed after deletion code=%d stderr=%q output=%q", code, stderr, output)
	}
}

func TestWorkingTreeRenameExposesReplacementAndHistoricalNames(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/overlayrename\n\ngo 1.27\n")
	repo.Write(t, "old.go", `package overlayrename

func Renamed() int { return 42 }
`)
	repo.Commit(t, "rename baseline")
	dataRoot := filepath.Join(t.TempDir(), "data")
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks"); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}
	repo.Run(t, "mv", "old.go", "new.go")

	code, output, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "--json", "impact", "--working-tree")
	if code != 0 {
		t.Fatalf("rename impact code=%d stderr=%q output=%q", code, stderr, output)
	}
	var report impact.Report
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("decode rename impact: %v\n%s", err, output)
	}
	var current, historical bool
	for _, seed := range report.Seeds.Selected {
		if seed.Name != "Renamed" {
			continue
		}
		if seed.Historical && seed.ChangeKind == "renamed" {
			historical = true
		}
		if !seed.Historical && seed.ChangeKind == "renamed" {
			current = true
		}
	}
	if !current || !historical {
		t.Fatalf("rename seeds missing replacement/history: %#v", report.Seeds.Selected)
	}
	code, output, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "graph", "symbol", "Renamed")
	if code != 0 || !strings.Contains(output, "working-tree overlay") {
		t.Fatalf("rename graph code=%d stderr=%q output=%q", code, stderr, output)
	}
}

func TestIncompleteWorkingTreeOverlayIsExplicit(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/overlayincomplete\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package overlayincomplete\n\nfunc Answer() int { return 42 }\n")
	repo.Commit(t, "incomplete baseline")
	dataRoot := filepath.Join(t.TempDir(), "data")
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks"); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}
	repo.Write(t, "main.go", "package overlayincomplete\n\nfunc Answer( int { return 99 }\n")
	code, output, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "--json", "impact", "--working-tree", "--task", "Answer")
	if code != 0 {
		t.Fatalf("incomplete impact code=%d stderr=%q output=%q", code, stderr, output)
	}
	var report impact.Report
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("decode incomplete impact: %v\n%s", err, output)
	}
	if !report.Metadata.Incomplete || report.Metadata.SourceMode != "working-tree" {
		t.Fatalf("incomplete metadata = %#v", report.Metadata)
	}
	foundIncomplete := false
	for _, diagnostic := range report.Diagnostics {
		if diagnostic.Code == "overlay-incomplete" {
			foundIncomplete = true
			break
		}
	}
	if !foundIncomplete {
		t.Fatalf("incomplete diagnostic missing: %#v", report.Diagnostics)
	}
}

func TestWorkingTreeOverlayRecapturesAfterCommitAndReset(t *testing.T) {
	repo := testrepo.New(t)
	repo.Write(t, "go.mod", "module example.com/overlaylifecycle\n\ngo 1.27\n")
	repo.Write(t, "main.go", "package overlaylifecycle\n\nfunc Answer() int { return 42 }\n")
	first := repo.Commit(t, "lifecycle baseline")
	dataRoot := filepath.Join(t.TempDir(), "data")
	if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks"); code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr)
	}
	repo.Write(t, "main.go", "package overlaylifecycle\n\nfunc Answer() int { return 43 }\n")
	code, output, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "--json", "context", "--task", "Answer")
	if code != 0 || !strings.Contains(output, "working-tree") {
		t.Fatalf("dirty lifecycle context code=%d stderr=%q output=%q", code, stderr, output)
	}
	second := repo.Commit(t, "lifecycle commit")
	code, output, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "--json", "context", "--task", "Answer")
	if code != 0 {
		t.Fatalf("post-commit lifecycle context code=%d stderr=%q output=%q", code, stderr, output)
	}
	var committedOverlay brief.Report
	if err := json.Unmarshal([]byte(output), &committedOverlay); err != nil {
		t.Fatalf("decode post-commit context: %v", err)
	}
	if committedOverlay.Impact.Metadata.Commit != second || committedOverlay.Impact.Metadata.ChangedFiles != 0 || !containsExcerpt(committedOverlay.Excerpts, "return 43") {
		t.Fatalf("post-commit overlay = %#v", committedOverlay.Impact.Metadata)
	}
	repo.Run(t, "reset", "--hard", first)
	code, output, stderr = runCLI("--data-dir", dataRoot, "-C", repo.Root, "--json", "context", "--task", "Answer")
	if code != 0 {
		t.Fatalf("post-reset lifecycle context code=%d stderr=%q output=%q", code, stderr, output)
	}
	var resetOverlay brief.Report
	if err := json.Unmarshal([]byte(output), &resetOverlay); err != nil {
		t.Fatalf("decode post-reset context: %v", err)
	}
	if resetOverlay.Impact.Metadata.Commit != first || resetOverlay.Impact.Metadata.ChangedFiles != 0 || !containsExcerpt(resetOverlay.Excerpts, "return 42") {
		t.Fatalf("post-reset overlay = %#v", resetOverlay.Impact.Metadata)
	}
}

func TestWorkingTreeOverlayRepositoryIsolation(t *testing.T) {
	alpha := impactRepository(t, "github.com/acme/overlay-alpha")
	beta := impactRepository(t, "github.com/acme/overlay-beta")
	dataRoot := filepath.Join(t.TempDir(), "data")
	for _, repo := range []*testrepo.Repository{alpha, beta} {
		if code, _, stderr := runCLI("--data-dir", dataRoot, "-C", repo.Root, "init", "--no-hooks"); code != 0 {
			t.Fatalf("init %s code=%d stderr=%q", repo.Root, code, stderr)
		}
	}
	alpha.Write(t, "transaction.go", "package impact\n\nfunc SubmitTransaction() int { return 11 }\n")
	beta.Write(t, "transaction.go", "package impact\n\nfunc SubmitTransaction() int { return 22 }\n")

	code, output, stderr := runCLI("--data-dir", dataRoot, "-C", alpha.Root, "--json", "context", "--task", "SubmitTransaction")
	if code != 0 {
		t.Fatalf("alpha overlay context code=%d stderr=%q output=%q", code, stderr, output)
	}
	var alphaReport brief.Report
	if err := json.Unmarshal([]byte(output), &alphaReport); err != nil {
		t.Fatalf("decode alpha context: %v", err)
	}
	code, output, stderr = runCLI("--data-dir", dataRoot, "-C", beta.Root, "--json", "context", "--task", "SubmitTransaction")
	if code != 0 {
		t.Fatalf("beta overlay context code=%d stderr=%q output=%q", code, stderr, output)
	}
	var betaReport brief.Report
	if err := json.Unmarshal([]byte(output), &betaReport); err != nil {
		t.Fatalf("decode beta context: %v", err)
	}
	if alphaReport.Impact.Metadata.OverlayID == "" || betaReport.Impact.Metadata.OverlayID == "" || alphaReport.Impact.Metadata.OverlayID == betaReport.Impact.Metadata.OverlayID {
		t.Fatalf("overlay identities are not isolated: alpha=%q beta=%q", alphaReport.Impact.Metadata.OverlayID, betaReport.Impact.Metadata.OverlayID)
	}
	if !containsExcerpt(alphaReport.Excerpts, "return 11") || containsExcerpt(alphaReport.Excerpts, "return 22") {
		t.Fatalf("alpha source leaked: %#v", alphaReport.Excerpts)
	}
	if !containsExcerpt(betaReport.Excerpts, "return 22") || containsExcerpt(betaReport.Excerpts, "return 11") {
		t.Fatalf("beta source leaked: %#v", betaReport.Excerpts)
	}
}

func containsExcerpt(excerpts []brief.Excerpt, value string) bool {
	for _, excerpt := range excerpts {
		if strings.Contains(excerpt.Text, value) {
			return true
		}
	}
	return false
}

func hasSeedName(report impact.Report, name string) bool {
	for _, seed := range report.Seeds.Selected {
		if seed.Name == name {
			return true
		}
	}
	return false
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func directoryDigest(t *testing.T, root string) string {
	t.Helper()
	files := make([]string, 0)
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			files = append(files, relative)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk digest root: %v", err)
	}
	// Hash paths and bytes in lexical order so a temporary directory cannot
	// make the comparison dependent on filesystem traversal order.
	var builder strings.Builder
	for _, relative := range files {
		contents, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatalf("read digest file %s: %v", relative, err)
		}
		sum := sha256.Sum256(contents)
		builder.WriteString(relative)
		builder.WriteByte(0)
		builder.WriteString(hex.EncodeToString(sum[:]))
		builder.WriteByte(0)
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}
