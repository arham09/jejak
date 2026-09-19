package overlay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	"github.com/arham09/jejak/internal/graph"
)

// View is an in-memory effective graph over one pinned committed reader. It
// masks base facts for changed paths and retains deleted declarations only as
// historical records for actual-change impact.
type View struct {
	mu sync.RWMutex

	base      Reader
	snapshot  *Snapshot
	gen       graph.Generation
	manifest  Manifest
	analysis  graph.AnalysisResult
	baseFacts map[string]graph.FileResult
	changes   []SymbolChange

	analysisSymbols    map[string]graph.Symbol
	analysisNodes      map[string]graph.Node
	analysisEvidence   map[string]graph.EdgeEvidence
	analysisEdgesByKey map[string][]graph.Edge

	symbols          map[string]graph.SymbolResult
	symbolsByName    map[string][]graph.SymbolResult
	historicalByName map[string][]graph.SymbolResult
	files            map[string]graph.File
	historical       map[string]graph.SymbolResult
	maskedKeys       map[string]struct{}
	affectedPath     map[string]struct{}
	packageImports   map[string][]string
	closed           bool
	overlayID        string
}

var _ Reader = (*View)(nil)

func newView(base Reader, snapshot *Snapshot, manifest Manifest, analysis graph.AnalysisResult, baseFacts map[string]graph.FileResult, changes []SymbolChange) (*View, error) {
	if base == nil || snapshot == nil {
		return nil, errors.New("overlay view requires a base reader and snapshot")
	}
	view := &View{
		base: base, snapshot: snapshot, gen: base.Generation(), manifest: manifest,
		analysis: analysis.Normalize(), baseFacts: cloneFileFacts(baseFacts),
		changes: append([]SymbolChange(nil), changes...), symbols: make(map[string]graph.SymbolResult),
		symbolsByName: make(map[string][]graph.SymbolResult), files: make(map[string]graph.File),
		historicalByName: make(map[string][]graph.SymbolResult), historical: make(map[string]graph.SymbolResult), maskedKeys: make(map[string]struct{}),
		affectedPath: make(map[string]struct{}), packageImports: make(map[string][]string),
		analysisSymbols: make(map[string]graph.Symbol), analysisNodes: make(map[string]graph.Node),
		analysisEvidence: make(map[string]graph.EdgeEvidence), analysisEdgesByKey: make(map[string][]graph.Edge),
	}
	for _, symbol := range view.analysis.Symbols {
		view.analysisSymbols[symbol.Key] = symbol
	}
	for _, node := range view.analysis.Nodes {
		view.analysisNodes[node.Key] = node
	}
	for _, evidence := range view.analysis.Evidence {
		key := edgeIdentity(evidence.SourceKey, evidence.TargetKey, evidence.Kind)
		if _, exists := view.analysisEvidence[key]; !exists {
			view.analysisEvidence[key] = evidence
		}
	}
	for _, edge := range view.analysis.Edges {
		if edge.Kind == graph.EdgeDefines || edge.Kind == graph.EdgeContains || edge.Kind == graph.EdgeImports {
			continue
		}
		view.analysisEdgesByKey[edge.SourceKey] = append(view.analysisEdgesByKey[edge.SourceKey], edge)
		if edge.TargetKey != edge.SourceKey {
			view.analysisEdgesByKey[edge.TargetKey] = append(view.analysisEdgesByKey[edge.TargetKey], edge)
		}
	}
	for _, change := range manifest.Changes {
		if change.OldPath != "" {
			view.affectedPath[change.OldPath] = struct{}{}
		}
		if change.NewPath != "" {
			view.affectedPath[change.NewPath] = struct{}{}
		}
	}
	for path, fact := range view.baseFacts {
		for _, symbol := range fileSymbols(fact) {
			view.maskedKeys[symbol.Key] = struct{}{}
		}
		if fact.File.Path != "" {
			view.affectedPath[fact.File.Path] = struct{}{}
		}
		_ = path
	}
	for _, file := range view.analysis.Files {
		file.Path = filepath.ToSlash(file.Path)
		view.files[file.Path] = file
	}
	for _, file := range snapshot.Files {
		path := filepath.ToSlash(file.Path)
		if _, exists := view.files[path]; exists || !view.pathAffected(path) {
			continue
		}
		view.files[path] = graph.File{Key: "file:" + path, Path: path, BlobSHA: file.BlobSHA}
	}
	for _, dependency := range view.analysis.PackageDependencies {
		view.packageImports[dependency.SourcePackage] = append(view.packageImports[dependency.SourcePackage], dependency.TargetPackage)
	}
	for key := range view.packageImports {
		sort.Strings(view.packageImports[key])
	}
	view.buildEffectiveSymbols()
	view.buildHistoricalSymbols()
	view.overlayID = overlayIdentity(view.gen, manifest.ID, view.analysis.BuildFingerprint)
	return view, nil
}

// Generation returns the committed base generation identity. Overlay fields
// are exposed through the metadata accessors below and never masquerade as a
// durable generation.
func (v *View) Generation() graph.Generation {
	if v == nil {
		return graph.Generation{}
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.gen
}

// Manifest returns a defensive copy of the captured source manifest.
func (v *View) Manifest() Manifest {
	if v == nil {
		return Manifest{}
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return cloneManifest(v.manifest)
}

// Changes returns declaration-level actual changes in deterministic order.
func (v *View) Changes() []SymbolChange {
	if v == nil {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return append([]SymbolChange(nil), v.changes...)
}

// OverlayID identifies the base generation, effective fingerprint, and source
// manifest used by this view.
func (v *View) OverlayID() string {
	if v == nil {
		return ""
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.overlayID
}

// EffectiveBuildFingerprint returns the analyzer fingerprint of the temporary
// source, or the base fingerprint when there are no dirty changes.
func (v *View) EffectiveBuildFingerprint() string {
	if v == nil {
		return ""
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.analysis.BuildFingerprint
}

// Incomplete reports whether the analyzer emitted an error diagnostic.
func (v *View) Incomplete() bool {
	if v == nil {
		return true
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.analysis.HasErrors()
}

// Diagnostics returns a defensive copy of analyzer diagnostics.
func (v *View) Diagnostics() []graph.Diagnostic {
	if v == nil {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return append([]graph.Diagnostic(nil), v.analysis.Diagnostics...)
}

// Root returns the external snapshot root for diagnostics and source adapters.
func (v *View) Root() string {
	if v == nil {
		return ""
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.snapshot == nil {
		return ""
	}
	return v.snapshot.Root
}

// ReadBlob implements the brief source-reader contract.
func (v *View) ReadBlob(ctx context.Context, root, objectID string) ([]byte, error) {
	if v == nil {
		return nil, errors.New("overlay view is nil")
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.closed || v.snapshot == nil {
		return nil, errors.New("overlay view is closed")
	}
	return v.snapshot.ReadBlob(ctx, root, objectID)
}

// Close releases the temporary snapshot. Reads already in progress finish
// before the snapshot is removed; subsequent reads return an error.
func (v *View) Close() error {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil
	}
	v.closed = true
	snapshot := v.snapshot
	v.snapshot = nil
	v.mu.Unlock()
	if snapshot != nil {
		return snapshot.Close()
	}
	return nil
}

func cloneKeySet(values map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for key := range values {
		result[key] = struct{}{}
	}
	return result
}

func cloneManifest(manifest Manifest) Manifest {
	manifest.Entries = append([]ManifestEntry(nil), manifest.Entries...)
	manifest.Changes = append([]graph.FileChange(nil), manifest.Changes...)
	return manifest
}

func overlayIdentity(generation graph.Generation, manifestID, fingerprint string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s", generation.RepoID, generation.WorktreeID, generation.ID, manifestID, fingerprint)))
	return "overlay:" + hex.EncodeToString(sum[:])
}
