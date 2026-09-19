package graph

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
)

// ChangeKind identifies the Git tree change represented by a FileChange.
type ChangeKind string

const (
	ChangeAdded      ChangeKind = "added"
	ChangeModified   ChangeKind = "modified"
	ChangeDeleted    ChangeKind = "deleted"
	ChangeRenamed    ChangeKind = "renamed"
	ChangeCopied     ChangeKind = "copied"
	ChangeTypeChange ChangeKind = "type_changed"
)

// FileChange is a normalized old/new committed-tree path change. Blob and
// mode fields are optional because a name/status diff is sufficient for the
// conservative package invalidation decision.
type FileChange struct {
	Kind       ChangeKind
	OldPath    string
	NewPath    string
	Score      int
	OldBlobSHA string
	NewBlobSHA string
	OldMode    string
	NewMode    string
}

// Validate checks path and change-kind invariants before a change enters an
// analyzer input or invalidation plan.
func (c FileChange) Validate() error {
	switch c.Kind {
	case ChangeAdded, ChangeModified, ChangeDeleted, ChangeRenamed, ChangeCopied, ChangeTypeChange:
	default:
		return fmt.Errorf("unknown file change kind %q", c.Kind)
	}
	oldPath, err := normalizeChangePath(c.OldPath)
	if err != nil {
		return fmt.Errorf("invalid old change path: %w", err)
	}
	newPath, err := normalizeChangePath(c.NewPath)
	if err != nil {
		return fmt.Errorf("invalid new change path: %w", err)
	}
	switch c.Kind {
	case ChangeAdded:
		if newPath == "" {
			return fmt.Errorf("added change has no new path")
		}
	case ChangeDeleted:
		if oldPath == "" {
			return fmt.Errorf("deleted change has no old path")
		}
	case ChangeRenamed, ChangeCopied, ChangeModified, ChangeTypeChange:
		if oldPath == "" || newPath == "" {
			return fmt.Errorf("%s change requires old and new paths", c.Kind)
		}
	default:
		if oldPath == "" && newPath == "" {
			return fmt.Errorf("file change has no path")
		}
	}
	if c.Score < 0 || c.Score > 100 {
		return fmt.Errorf("file change score %d is outside 0..100", c.Score)
	}
	return nil
}

// NormalizeChanges returns a validated, copied, deterministic change list.
func NormalizeChanges(changes []FileChange) ([]FileChange, error) {
	result := make([]FileChange, 0, len(changes))
	seen := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		change.OldPath = normalizedChangePathOrEmpty(change.OldPath)
		change.NewPath = normalizedChangePathOrEmpty(change.NewPath)
		if err := change.Validate(); err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%d", change.Kind, change.OldPath, change.NewPath, change.Score)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, change)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].OldPath != result[j].OldPath {
			return result[i].OldPath < result[j].OldPath
		}
		if result[i].NewPath != result[j].NewPath {
			return result[i].NewPath < result[j].NewPath
		}
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Score < result[j].Score
	})
	return result, nil
}

func normalizeChangePath(path string) (string, error) {
	if strings.IndexByte(path, 0) >= 0 {
		return "", fmt.Errorf("path contains NUL")
	}
	path = filepath.ToSlash(path)
	if path == "" {
		return "", nil
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if filepath.IsAbs(filepath.FromSlash(clean)) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path %q escapes repository", path)
	}
	return clean, nil
}

func normalizedChangePathOrEmpty(path string) string {
	normalized, err := normalizeChangePath(path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return normalized
}

// SyncMode explains how a graph freshness request was handled.
type SyncMode string

const (
	SyncModeReuse       SyncMode = "reuse"
	SyncModeIncremental SyncMode = "incremental"
	SyncModeRebuild     SyncMode = "rebuild"
)

// SyncThresholds controls when a source delta is considered too broad for an
// incremental decision. Zero values select safe defaults.
type SyncThresholds struct {
	MaxChangedFiles int
	MaxChangedRatio float64
}

// DefaultSyncThresholds returns conservative v1 defaults. The analyzer still
// builds a complete candidate for both decisions; thresholds select the
// explainable mode and the future package-region optimization boundary.
func DefaultSyncThresholds() SyncThresholds {
	return SyncThresholds{MaxChangedFiles: 100, MaxChangedRatio: 0.30}
}

func (t SyncThresholds) Normalize() SyncThresholds {
	defaults := DefaultSyncThresholds()
	if t.MaxChangedFiles <= 0 {
		t.MaxChangedFiles = defaults.MaxChangedFiles
	}
	if t.MaxChangedRatio <= 0 || t.MaxChangedRatio > 1 || math.IsNaN(t.MaxChangedRatio) || math.IsInf(t.MaxChangedRatio, 0) {
		t.MaxChangedRatio = defaults.MaxChangedRatio
	}
	return t
}

// SyncDecision is the manager's deterministic mode/reason result.
type SyncDecision struct {
	Mode          SyncMode
	Reason        string
	ChangedFiles  int
	ChangedRatio  float64
	BaseAvailable bool
}

// DecideSync selects a conservative synchronization mode. A rebuild still
// uses the same validated candidate pipeline; this function only chooses the
// invalidation boundary and public explanation.
func DecideSync(changes []FileChange, totalFiles int, baseAvailable, force, fingerprintChanged bool, thresholds SyncThresholds) SyncDecision {
	thresholds = thresholds.Normalize()
	normalized, err := NormalizeChanges(changes)
	if err != nil {
		return SyncDecision{Mode: SyncModeRebuild, Reason: "invalid Git delta", BaseAvailable: baseAvailable}
	}
	changed := len(normalized)
	ratio := 0.0
	if totalFiles <= 0 {
		if changed > 0 {
			ratio = 1
		}
	} else {
		ratio = float64(changed) / float64(totalFiles)
	}
	decision := SyncDecision{Mode: SyncModeIncremental, Reason: "compatible source delta", ChangedFiles: changed, ChangedRatio: ratio, BaseAvailable: baseAvailable}
	switch {
	case force:
		decision.Mode = SyncModeRebuild
		decision.Reason = "forced rebuild"
	case fingerprintChanged:
		decision.Mode = SyncModeRebuild
		decision.Reason = "build inputs or analyzer fingerprint changed"
	case !baseAvailable:
		decision.Mode = SyncModeRebuild
		decision.Reason = "indexed base commit unavailable"
	case changed > thresholds.MaxChangedFiles:
		decision.Mode = SyncModeRebuild
		decision.Reason = fmt.Sprintf("changed file count %d exceeds threshold %d", changed, thresholds.MaxChangedFiles)
	case ratio > thresholds.MaxChangedRatio:
		decision.Mode = SyncModeRebuild
		decision.Reason = fmt.Sprintf("changed file ratio %.2f exceeds threshold %.2f", ratio, thresholds.MaxChangedRatio)
	case hasWideBuildChange(normalized):
		decision.Mode = SyncModeRebuild
		decision.Reason = "module, workspace, vendor, or build input changed"
	case changed == 0:
		decision.Reason = "commit changed without source file changes"
	}
	return decision
}

func hasWideBuildChange(changes []FileChange) bool {
	for _, change := range changes {
		paths := []string{change.OldPath, change.NewPath}
		for _, path := range paths {
			path = filepath.ToSlash(path)
			base := filepath.Base(filepath.FromSlash(path))
			if base == "go.mod" || base == "go.sum" || base == "go.work" || base == "go.work.sum" ||
				strings.HasPrefix(path, "vendor/") || path == "vendor" {
				return true
			}
		}
	}
	return false
}

// InvalidationPlan describes the conservative package region affected by a
// tree delta. The first implementation uses this metadata for decisions while
// the complete analyzer replaces the candidate, avoiding unsafe partial facts.
type InvalidationPlan struct {
	ChangedPaths     []string
	DeletedPaths     []string
	AffectedPackages []string
	RepositoryWide   bool
	Reason           string
}

// PlanInvalidation maps old file ownership to changed paths. New ownership is
// intentionally resolved by the complete analyzer because added and renamed
// files may introduce packages or interface implementations not present in the
// old graph.
func PlanInvalidation(previous AnalysisResult, changes []FileChange) (InvalidationPlan, error) {
	normalized, err := NormalizeChanges(changes)
	if err != nil {
		return InvalidationPlan{}, err
	}
	filePackages := make(map[string]string, len(previous.Files))
	for _, file := range previous.Files {
		filePackages[filepath.ToSlash(file.Path)] = file.PackageKey
	}
	packages := make(map[string]struct{})
	changed := make(map[string]struct{})
	deleted := make(map[string]struct{})
	plan := InvalidationPlan{Reason: "package-level source invalidation"}
	for _, change := range normalized {
		for _, path := range []string{change.OldPath, change.NewPath} {
			if path == "" {
				continue
			}
			changed[path] = struct{}{}
			if packageKey := filePackages[path]; packageKey != "" {
				packages[packageKey] = struct{}{}
			}
			if change.Kind == ChangeDeleted || change.Kind == ChangeRenamed {
				if path == change.OldPath {
					deleted[path] = struct{}{}
				}
			}
			if hasWideBuildChange([]FileChange{change}) {
				plan.RepositoryWide = true
			}
		}
	}
	for path := range changed {
		plan.ChangedPaths = append(plan.ChangedPaths, path)
	}
	for path := range deleted {
		plan.DeletedPaths = append(plan.DeletedPaths, path)
	}
	for packageKey := range packages {
		plan.AffectedPackages = append(plan.AffectedPackages, packageKey)
	}
	sort.Strings(plan.ChangedPaths)
	sort.Strings(plan.DeletedPaths)
	sort.Strings(plan.AffectedPackages)
	if plan.RepositoryWide {
		plan.Reason = "repository-wide build input invalidation"
	}
	return plan, nil
}
