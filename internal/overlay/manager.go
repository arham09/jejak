package overlay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arham09/jejak/internal/git"
	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
)

var (
	// ErrStaleSource means the checkout changed while an overlay was being
	// captured or analyzed. Callers may retry the bounded capture.
	ErrStaleSource = errors.New("working-tree source changed during overlay analysis")
	// ErrUnsupportedSource identifies a source layout that cannot be safely
	// copied into an overlay snapshot.
	ErrUnsupportedSource = errors.New("working-tree source is unsupported")
)

// Reader is the small committed reader surface needed to construct an
// effective view. It deliberately mirrors the impact consumer contract
// without importing the impact package.
type Reader interface {
	Generation() graph.Generation
	SearchSymbols(context.Context, []string, int) ([]graph.SymbolResult, error)
	FindSymbols(context.Context, string) ([]graph.SymbolResult, error)
	FindFile(context.Context, string) (graph.FileResult, error)
}

// SymbolChangeKind identifies declaration-level working-tree changes.
type SymbolChangeKind string

const (
	SymbolAdded    SymbolChangeKind = "added"
	SymbolModified SymbolChangeKind = "modified"
	SymbolDeleted  SymbolChangeKind = "deleted"
	SymbolRenamed  SymbolChangeKind = "renamed"
)

// SymbolChange carries an effective declaration and, for deletions or
// replacements, its committed predecessor.
type SymbolChange struct {
	Kind   SymbolChangeKind
	Before graph.Symbol
	After  graph.Symbol
}

// Manager builds command-scoped overlays. It has no persistence dependency;
// every result is released when its View is closed.
type Manager struct {
	client   *git.Client
	analyzer graph.Analyzer
	parent   string
}

// NewManager returns an overlay manager with explicit Git/analyzer ownership.
func NewManager(client *git.Client, analyzer graph.Analyzer, parent string) (*Manager, error) {
	if client == nil {
		return nil, errors.New("overlay manager requires a Git client")
	}
	if analyzer == nil {
		return nil, errors.New("overlay manager requires an analyzer")
	}
	return &Manager{client: client, analyzer: analyzer, parent: parent}, nil
}

// Build captures and analyzes the current on-disk checkout against base. A
// single bounded retry handles an editor write that races the first capture.
// It never creates or activates a graph generation.
func (m *Manager) Build(ctx context.Context, target repository.Target, base Reader, build graph.BuildConfig) (*View, error) {
	if m == nil || m.client == nil || m.analyzer == nil {
		return nil, errors.New("overlay manager is nil")
	}
	if base == nil {
		return nil, errors.New("overlay manager requires a committed reader")
	}
	generation := base.Generation()
	if generation.Commit == "" || !target.Worktree.HeadKnown || graph.CommitSHA(target.Worktree.Head) != generation.Commit {
		return nil, fmt.Errorf("%w: committed base does not match current HEAD", ErrStaleSource)
	}
	var lastMismatch error
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		manifest, err := CaptureManifest(ctx, m.client, target.Worktree.Path, m.analyzer)
		if err != nil {
			return nil, err
		}
		snapshot, err := materialize(ctx, m.client, target.Worktree.Path, generation.Commit, manifest, m.parent)
		if err != nil {
			return nil, err
		}
		baseFacts, err := collectBaseFacts(ctx, base, manifest)
		if err != nil {
			_ = snapshot.Close()
			return nil, err
		}
		analysis := graph.AnalysisResult{AnalyzerVersion: generation.AnalyzerVersion, BuildFingerprint: generation.BuildFingerprint}
		if manifest.Dirty() {
			input := graph.AnalyzeInput{
				Repository:  target.Repository.ID,
				Worktree:    target.Worktree.ID,
				Commit:      generation.Commit,
				Root:        snapshot.Root,
				Files:       append([]graph.SnapshotFile(nil), snapshot.Files...),
				Build:       build.Normalize(),
				Incremental: false,
			}
			analysis, err = m.analyzer.Analyze(ctx, input)
			if err != nil {
				_ = snapshot.Close()
				return nil, fmt.Errorf("analyze working-tree overlay: %w", err)
			}
			analysis = analysis.Normalize()
			if analysis.AnalyzerVersion == "" {
				analysis.AnalyzerVersion = generation.AnalyzerVersion
			}
			if analysis.BuildFingerprint == "" {
				analysis.BuildFingerprint = generation.BuildFingerprint
			}
		}
		fresh, err := CaptureManifest(ctx, m.client, target.Worktree.Path, m.analyzer)
		if err != nil {
			_ = snapshot.Close()
			return nil, err
		}
		if fresh.ID != manifest.ID {
			_ = snapshot.Close()
			lastMismatch = fmt.Errorf("%w: manifest changed from %s to %s", ErrStaleSource, manifest.ID, fresh.ID)
			continue
		}
		head, known, headErr := m.client.Head(ctx, target.Worktree.Path)
		if headErr != nil {
			_ = snapshot.Close()
			return nil, headErr
		}
		if !known || graph.CommitSHA(head) != generation.Commit {
			_ = snapshot.Close()
			lastMismatch = fmt.Errorf("%w: HEAD changed during overlay analysis", ErrStaleSource)
			continue
		}
		changes := deriveSymbolChanges(ctx, m.client, target.Worktree.Path, snapshot, manifest, analysis, baseFacts)
		view, err := newView(base, snapshot, manifest, analysis, baseFacts, changes)
		if err != nil {
			_ = snapshot.Close()
			return nil, err
		}
		return view, nil
	}
	if lastMismatch != nil {
		return nil, lastMismatch
	}
	return nil, ErrStaleSource
}

func collectBaseFacts(ctx context.Context, base Reader, manifest Manifest) (map[string]graph.FileResult, error) {
	paths := make(map[string]struct{}, len(manifest.Changes)*2)
	for _, change := range manifest.Changes {
		if change.OldPath != "" {
			paths[change.OldPath] = struct{}{}
		}
		if change.NewPath != "" {
			paths[change.NewPath] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	facts := make(map[string]graph.FileResult, len(ordered))
	for _, path := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := base.FindFile(ctx, path)
		if err != nil {
			// New/untracked paths have no committed file fact. Other errors
			// remain non-fatal here; the effective analysis can still report a
			// diagnostic and the base reader remains the source of truth.
			continue
		}
		facts[path] = result
	}
	return facts, nil
}

func deriveSymbolChanges(ctx context.Context, client *git.Client, repoRoot string, snapshot *Snapshot, manifest Manifest, analysis graph.AnalysisResult, baseFacts map[string]graph.FileResult) []SymbolChange {
	newByPath := make(map[string][]graph.Symbol)
	for _, symbol := range analysis.Symbols {
		path := symbol.Position.Path
		if path == "" {
			if file, ok := findAnalysisFile(analysis.Files, symbol.FileKey); ok {
				path = file.Path
			}
		}
		path = filepath.ToSlash(path)
		newByPath[path] = append(newByPath[path], symbol)
	}
	for path := range newByPath {
		sort.Slice(newByPath[path], func(i, j int) bool { return newByPath[path][i].Key < newByPath[path][j].Key })
	}
	changes := make([]SymbolChange, 0)
	seen := make(map[string]struct{})
	for _, fileChange := range manifest.Changes {
		oldPath, newPath := fileChange.OldPath, fileChange.NewPath
		oldSymbols := fileSymbols(baseFacts[oldPath])
		newSymbols := newByPath[filepath.ToSlash(newPath)]
		if newPath == "" {
			newSymbols = nil
		}
		matchedOld := make(map[int]struct{})
		matchedNew := make(map[int]struct{})
		for newIndex, after := range newSymbols {
			oldIndex := findUnmatchedIdentity(oldSymbols, matchedOld, after)
			if oldIndex < 0 {
				key := string(SymbolAdded) + "\x00" + after.Key
				if _, ok := seen[key]; !ok {
					changes = append(changes, SymbolChange{Kind: SymbolAdded, After: after})
					seen[key] = struct{}{}
				}
				matchedNew[newIndex] = struct{}{}
				continue
			}
			before := oldSymbols[oldIndex]
			matchedOld[oldIndex] = struct{}{}
			matchedNew[newIndex] = struct{}{}
			kind := SymbolChangeKind("")
			if oldPath != newPath {
				kind = SymbolRenamed
			} else if before.Signature != after.Signature || declarationChanged(ctx, client, repoRoot, snapshot, before, after, baseFacts[oldPath].File.BlobSHA) {
				kind = SymbolModified
			}
			if kind != "" {
				key := string(kind) + "\x00" + before.Key + "\x00" + after.Key
				if _, ok := seen[key]; !ok {
					changes = append(changes, SymbolChange{Kind: kind, Before: before, After: after})
					seen[key] = struct{}{}
				}
			}
		}
		for oldIndex, before := range oldSymbols {
			if _, ok := matchedOld[oldIndex]; ok {
				continue
			}
			key := string(SymbolDeleted) + "\x00" + before.Key
			if _, ok := seen[key]; !ok {
				changes = append(changes, SymbolChange{Kind: SymbolDeleted, Before: before})
				seen[key] = struct{}{}
			}
		}
		// A module/build-input change may not own a declaration. In that case
		// the affected package is still a validation seed; the view records
		// the package through its file masks and diagnostics.
		_ = matchedNew
	}
	// A malformed or globally affecting build edit can leave no declaration
	// diff. Mark all symbols in affected effective files as package changes so
	// actual-change impact still has a bounded seed set.
	if len(changes) == 0 && len(manifest.Changes) > 0 {
		for _, symbol := range analysis.Symbols {
			path := filepath.ToSlash(symbol.Position.Path)
			if path == "" {
				continue
			}
			if affectedPath(manifest, path) {
				changes = append(changes, SymbolChange{Kind: SymbolModified, After: symbol})
			}
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		left, right := changes[i], changes[j]
		leftKey, rightKey := changeSortKey(left), changeSortKey(right)
		return leftKey < rightKey
	})
	return changes
}

func findAnalysisFile(files []graph.File, key string) (graph.File, bool) {
	for _, file := range files {
		if file.Key == key {
			return file, true
		}
	}
	return graph.File{}, false
}

func fileSymbols(result graph.FileResult) []graph.Symbol {
	if len(result.Symbols) > 0 {
		return append([]graph.Symbol(nil), result.Symbols...)
	}
	resultSymbols := make([]graph.Symbol, 0, len(result.SymbolResults))
	for _, item := range result.SymbolResults {
		resultSymbols = append(resultSymbols, item.Symbol)
	}
	return resultSymbols
}

func stableIdentity(symbol graph.Symbol) string {
	return strings.Join([]string{symbol.PackageKey, string(symbol.Kind), symbol.Receiver, symbol.Name}, "\x00")
}

func findUnmatchedIdentity(symbols []graph.Symbol, matched map[int]struct{}, target graph.Symbol) int {
	identity := stableIdentity(target)
	for index, symbol := range symbols {
		if _, ok := matched[index]; ok {
			continue
		}
		if stableIdentity(symbol) == identity {
			return index
		}
	}
	return -1
}

func declarationChanged(ctx context.Context, client *git.Client, repoRoot string, snapshot *Snapshot, before, after graph.Symbol, oldBlob string) bool {
	oldContents, oldErr := client.ReadBlob(ctx, repoRoot, oldBlob)
	newBlob := ""
	for _, file := range snapshot.Files {
		if after.FileKey == "file:"+file.Path || after.FileKey == file.Path {
			newBlob = file.BlobSHA
			break
		}
	}
	newContents, newErr := snapshot.ReadBlob(ctx, snapshot.Root, newBlob)
	if oldErr != nil || newErr != nil {
		return before.Signature != after.Signature || before.Position != after.Position
	}
	return sourceSpanDigest(oldContents, before.Position) != sourceSpanDigest(newContents, after.Position)
}

func sourceSpanDigest(contents []byte, position graph.Position) string {
	lines := strings.Split(string(contents), "\n")
	start, end := position.StartLine, position.EndLine
	if start <= 0 || start > len(lines) {
		start = 1
	}
	if end < start || end > len(lines) {
		end = len(lines)
	}
	value := strings.Join(lines[start-1:end], "\n")
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func affectedPath(manifest Manifest, path string) bool {
	for _, change := range manifest.Changes {
		if change.OldPath == path || change.NewPath == path {
			return true
		}
	}
	return false
}

func changeSortKey(change SymbolChange) string {
	key := string(change.Kind) + "\x00"
	if change.After.Key != "" {
		key += change.After.Key
	} else {
		key += change.Before.Key
	}
	return key
}
