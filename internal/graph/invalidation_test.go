package graph

import "testing"

func TestNormalizeChangesIsDeterministicAndDeduplicates(t *testing.T) {
	changes, err := NormalizeChanges([]FileChange{
		{Kind: ChangeModified, OldPath: "pkg//file.go", NewPath: "pkg/file.go"},
		{Kind: ChangeAdded, NewPath: "new.go"},
		{Kind: ChangeModified, OldPath: "pkg/file.go", NewPath: "pkg/file.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("normalized changes = %#v", changes)
	}
	if changes[0].OldPath != "" || changes[0].NewPath != "new.go" {
		t.Fatalf("changes are not sorted by path: %#v", changes)
	}
	if changes[1].OldPath != "pkg/file.go" || changes[1].NewPath != "pkg/file.go" {
		t.Fatalf("path normalization = %#v", changes[1])
	}
}

func TestDecideSyncUsesConservativeBoundaries(t *testing.T) {
	thresholds := SyncThresholds{MaxChangedFiles: 2, MaxChangedRatio: 0.5}
	cases := []struct {
		name   string
		input  []FileChange
		files  int
		base   bool
		force  bool
		finger bool
		mode   SyncMode
	}{
		{name: "small delta", input: []FileChange{{Kind: ChangeModified, OldPath: "a.go", NewPath: "a.go"}}, files: 10, base: true, mode: SyncModeIncremental},
		{name: "large count", input: []FileChange{{Kind: ChangeModified, OldPath: "a.go", NewPath: "a.go"}, {Kind: ChangeModified, OldPath: "b.go", NewPath: "b.go"}, {Kind: ChangeModified, OldPath: "c.go", NewPath: "c.go"}}, files: 10, base: true, mode: SyncModeRebuild},
		{name: "wide module input", input: []FileChange{{Kind: ChangeModified, OldPath: "go.mod", NewPath: "go.mod"}}, files: 10, base: true, mode: SyncModeRebuild},
		{name: "missing base", input: []FileChange{{Kind: ChangeModified, OldPath: "a.go", NewPath: "a.go"}}, files: 10, base: false, mode: SyncModeRebuild},
		{name: "forced", input: nil, files: 10, base: true, force: true, mode: SyncModeRebuild},
		{name: "fingerprint", input: nil, files: 10, base: true, finger: true, mode: SyncModeRebuild},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			decision := DecideSync(test.input, test.files, test.base, test.force, test.finger, thresholds)
			if decision.Mode != test.mode {
				t.Fatalf("decision = %#v, want mode %s", decision, test.mode)
			}
			if decision.Reason == "" {
				t.Fatal("decision has no reason")
			}
		})
	}
}

func TestPlanInvalidationIncludesOldOwnershipForRenameAndDelete(t *testing.T) {
	previous := AnalysisResult{Files: []File{
		{Key: "file:old.go", Path: "old.go", PackageKey: "example.com/app"},
		{Key: "file:pkg.go", Path: "pkg/pkg.go", PackageKey: "example.com/app/pkg"},
	}}
	plan, err := PlanInvalidation(previous, []FileChange{
		{Kind: ChangeRenamed, OldPath: "old.go", NewPath: "new.go", Score: 95},
		{Kind: ChangeDeleted, OldPath: "pkg/pkg.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ChangedPaths) != 3 || len(plan.DeletedPaths) != 2 {
		t.Fatalf("invalidation plan = %#v", plan)
	}
	if len(plan.AffectedPackages) != 2 || plan.AffectedPackages[0] != "example.com/app" || plan.AffectedPackages[1] != "example.com/app/pkg" {
		t.Fatalf("affected packages = %#v", plan.AffectedPackages)
	}
}
