package graph

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/arham09/jejak/internal/repository"
)

// ErrNoCommittedHead indicates that a repository has no commit to analyze.
var ErrNoCommittedHead = errors.New("repository has no committed HEAD")

// ErrStaleState indicates that the worktree changed while a candidate was
// being analyzed and the candidate must not be activated.
var ErrStaleState = errors.New("repository state changed while building graph")

// GenerationStore is the consumer-owned persistence seam used by Manager.
// graphdb.Store satisfies it without exposing SQL to the analyzer.
type GenerationStore interface {
	State(context.Context, repository.RepoID, repository.WorktreeID) (State, error)
	Generation(context.Context, repository.RepoID, repository.WorktreeID, GenerationID) (Generation, error)
	CreateGeneration(context.Context, Generation) (Generation, error)
	WriteAnalysis(context.Context, Generation, AnalysisResult) error
	ValidateGeneration(context.Context, repository.RepoID, repository.WorktreeID, GenerationID) error
	ActivateGeneration(context.Context, repository.RepoID, repository.WorktreeID, GenerationID, *GenerationID) error
	FailGeneration(context.Context, repository.RepoID, repository.WorktreeID, GenerationID, error) error
}

type generationCounter interface {
	GenerationCounts(context.Context, repository.RepoID, repository.WorktreeID, GenerationID) (Counts, error)
}

// SnapshotProvider materializes one immutable committed tree.
type SnapshotProvider interface {
	Snapshot(context.Context, string, string) (*Snapshot, error)
}

// HeadObserver re-discovers a worktree before activation, preventing a build
// that raced a commit from publishing a graph under the wrong HEAD.
type HeadObserver interface {
	Observe(context.Context, string) (repository.Target, error)
}

// ManagerOptions configures optional synchronization integrations. Zero
// values preserve the Phase 3 full-build behavior while enabling the Phase 4
// diff/cache boundaries when concrete implementations are supplied.
type ManagerOptions struct {
	Diff       DiffProvider
	Thresholds SyncThresholds
}

// EnsureResult describes a fresh, rebuilt, incrementally selected, or reused
// graph generation.
type EnsureResult struct {
	Generation Generation
	Analysis   AnalysisResult
	Counts     Counts
	Mode       SyncMode
	Reason     string
	Changes    []FileChange
	Cache      SyntaxCacheStats
	Reused     bool
}

// Manager coordinates immutable analysis and atomic generation activation. It
// performs no SQL, Git commands, or language-specific parsing itself.
type Manager struct {
	store      GenerationStore
	analyzer   Analyzer
	snapshots  SnapshotProvider
	observer   HeadObserver
	diff       DiffProvider
	thresholds SyncThresholds
}

// NewManager returns a graph manager with explicit concrete dependencies.
func NewManager(store GenerationStore, analyzer Analyzer, snapshots SnapshotProvider, observer HeadObserver) (*Manager, error) {
	return NewManagerWithOptions(store, analyzer, snapshots, observer, ManagerOptions{})
}

// NewManagerWithOptions returns a graph manager with synchronization options.
func NewManagerWithOptions(store GenerationStore, analyzer Analyzer, snapshots SnapshotProvider, observer HeadObserver, options ManagerOptions) (*Manager, error) {
	if store == nil || analyzer == nil || snapshots == nil || observer == nil {
		return nil, errors.New("graph manager requires store, analyzer, snapshot provider, and head observer")
	}
	return &Manager{store: store, analyzer: analyzer, snapshots: snapshots, observer: observer, diff: options.Diff, thresholds: options.Thresholds.Normalize()}, nil
}

// EnsureGraph builds or reuses a graph for target's current committed HEAD.
// The caller owns repository writer coordination.
func (m *Manager) EnsureGraph(ctx context.Context, target repository.Target, build BuildConfig) (EnsureResult, error) {
	return m.ensureGraph(ctx, target, build, false)
}

// RebuildGraph forces a complete candidate reconstruction even when the
// current commit and build fingerprint already match the active generation.
func (m *Manager) RebuildGraph(ctx context.Context, target repository.Target, build BuildConfig) (EnsureResult, error) {
	return m.ensureGraph(ctx, target, build, true)
}

func (m *Manager) ensureGraph(ctx context.Context, target repository.Target, build BuildConfig, forceRebuild bool) (result EnsureResult, err error) {
	if err := ctx.Err(); err != nil {
		return EnsureResult{}, err
	}
	if !target.Worktree.HeadKnown || target.Worktree.Head == "" {
		return EnsureResult{}, ErrNoCommittedHead
	}
	state, err := m.store.State(ctx, target.Repository.ID, target.Worktree.ID)
	if err != nil {
		return EnsureResult{}, fmt.Errorf("read graph state before analysis: %w", err)
	}
	snapshot, err := m.snapshots.Snapshot(ctx, target.Worktree.Path, string(target.Worktree.Head))
	if err != nil {
		return EnsureResult{}, fmt.Errorf("materialize committed source: %w", err)
	}
	if snapshot == nil {
		return EnsureResult{}, errors.New("snapshot provider returned nil snapshot")
	}
	defer func() {
		if closeErr := snapshot.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close committed source snapshot: %w", closeErr))
		}
	}()
	commit := CommitSHA(target.Worktree.Head)
	if snapshot.Commit != "" && snapshot.Commit != commit {
		return EnsureResult{}, fmt.Errorf("snapshot commit %s does not match target HEAD %s", snapshot.Commit, commit)
	}
	input := AnalyzeInput{
		Repository: target.Repository.ID,
		Worktree:   target.Worktree.ID,
		Commit:     commit,
		Root:       snapshot.Root,
		Files:      snapshot.Files,
		Snapshot:   snapshot,
		Build:      build.Normalize(),
	}

	active, activeErr := m.activeGeneration(ctx, target, state)
	fingerprint, fingerprintKnown := m.fingerprint(ctx, input)
	compatibilityChanged := activeErr != nil || active.State != GenerationActive || active.AnalyzerVersion != analyzerVersion(m.analyzer) || active.SchemaVersion != 1
	fingerprintChanged := false
	if activeErr == nil && state.IndexedHead == commit {
		fingerprintChanged = !fingerprintKnown || active.BuildFingerprint != fingerprint
	}
	if !forceRebuild && activeErr == nil && state.Status == StatusReady && state.ActiveGeneration != nil && state.IndexedHead == commit &&
		fingerprintKnown && !fingerprintChanged && !compatibilityChanged && active.Commit == commit && active.State == GenerationActive {
		if err := m.verifyObservedTarget(ctx, target, commit); err != nil {
			return EnsureResult{}, err
		}
		reused := EnsureResult{Generation: active, Mode: SyncModeReuse, Reason: "graph already synchronized", Reused: true}
		if counter, ok := m.store.(generationCounter); ok {
			reused.Analysis = AnalysisResult{BuildFingerprint: active.BuildFingerprint, AnalyzerVersion: active.AnalyzerVersion}
			reused.Counts, err = counter.GenerationCounts(ctx, target.Repository.ID, target.Worktree.ID, active.ID)
			if err != nil {
				return EnsureResult{}, fmt.Errorf("count reused graph generation: %w", err)
			}
		}
		return reused, nil
	}

	changes, baseAvailable, diffErr := m.changes(ctx, target.Worktree.Path, state.IndexedHead, commit)
	decision := DecideSync(changes, len(input.Files), baseAvailable, forceRebuild, fingerprintChanged || compatibilityChanged, m.thresholds)
	if state.IndexedHead == "" && !forceRebuild {
		decision.Mode = SyncModeRebuild
		decision.Reason = "no active indexed graph"
	}
	if diffErr != nil && state.IndexedHead != "" && state.IndexedHead != commit {
		decision.Mode = SyncModeRebuild
		decision.Reason = fmt.Sprintf("indexed base commit unavailable: %v", diffErr)
	}
	input.PreviousCommit = state.IndexedHead
	input.Changes = append([]FileChange(nil), changes...)
	input.Incremental = decision.Mode == SyncModeIncremental
	result = EnsureResult{Mode: decision.Mode, Reason: decision.Reason, Changes: append([]FileChange(nil), changes...)}

	input, pendingCache, cacheStats := m.prepareSyntaxCache(ctx, input)
	result.Cache = cacheStats
	analysis, err := m.analyzer.Analyze(ctx, input)
	if err != nil {
		return result, fmt.Errorf("analyze committed source: %w", err)
	}
	analysis = analysis.Normalize()
	result.Analysis = analysis
	generation, err := m.store.CreateGeneration(ctx, Generation{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Commit: commit, BuildFingerprint: analysis.BuildFingerprint, AnalyzerVersion: analysis.AnalyzerVersion, SchemaVersion: 1})
	if err != nil {
		return result, fmt.Errorf("create graph generation: %w", err)
	}
	result.Generation = generation
	fail := func(cause error) (EnsureResult, error) {
		failureErr := m.store.FailGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID, cause)
		if failureErr != nil {
			return result, errors.Join(cause, fmt.Errorf("record graph generation failure: %w", failureErr))
		}
		return result, cause
	}
	if err := ValidateAnalysis(analysis); err != nil {
		return fail(err)
	}
	if err := m.store.WriteAnalysis(ctx, generation, analysis); err != nil {
		return fail(fmt.Errorf("write graph generation: %w", err))
	}
	result.Cache = m.writeSyntaxCache(ctx, target.Repository.ID, pendingCache, result.Cache)
	if err := m.store.ValidateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID); err != nil {
		return fail(fmt.Errorf("validate graph generation: %w", err))
	}
	if err := m.verifyObservedTarget(ctx, target, commit); err != nil {
		return fail(err)
	}
	if err := m.store.ActivateGeneration(ctx, target.Repository.ID, target.Worktree.ID, generation.ID, state.ActiveGeneration); err != nil {
		return fail(fmt.Errorf("activate graph generation: %w", err))
	}
	generation.State = GenerationActive
	result.Generation = generation
	result.Counts = analysis.Count()
	return result, nil
}

func (m *Manager) activeGeneration(ctx context.Context, target repository.Target, state State) (Generation, error) {
	if state.ActiveGeneration == nil {
		return Generation{}, fmt.Errorf("active generation is missing")
	}
	return m.store.Generation(ctx, target.Repository.ID, target.Worktree.ID, *state.ActiveGeneration)
}

func (m *Manager) fingerprint(ctx context.Context, input AnalyzeInput) (string, bool) {
	fingerprinter, ok := m.analyzer.(Fingerprinter)
	if !ok {
		return "", false
	}
	fingerprint, err := fingerprinter.BuildFingerprint(ctx, input)
	return fingerprint, err == nil
}

func (m *Manager) changes(ctx context.Context, root string, oldCommit, newCommit CommitSHA) ([]FileChange, bool, error) {
	if oldCommit == "" || oldCommit == newCommit {
		return nil, oldCommit == newCommit, nil
	}
	if m.diff == nil {
		return nil, false, errors.New("git diff provider is unavailable")
	}
	changes, err := m.diff.Diff(ctx, root, string(oldCommit), string(newCommit))
	if err != nil {
		return nil, false, err
	}
	normalized, err := NormalizeChanges(changes)
	if err != nil {
		return nil, false, err
	}
	return normalized, true, nil
}

func (m *Manager) prepareSyntaxCache(ctx context.Context, input AnalyzeInput) (AnalyzeInput, []SyntaxCacheEntry, SyntaxCacheStats) {
	provider, providerOK := m.analyzer.(SyntaxCacheProvider)
	cache, cacheOK := m.store.(SyntaxCache)
	if !providerOK || !cacheOK {
		return input, nil, SyntaxCacheStats{}
	}
	versioner, versionOK := m.analyzer.(SyntaxParserVersioner)
	parserVersion := ""
	if versionOK {
		parserVersion = versioner.SyntaxParserVersion()
	}
	if parserVersion != "" {
		cached := make([]SyntaxCacheEntry, 0)
		seen := make(map[string]struct{}, len(input.Files))
		stats := SyntaxCacheStats{}
		for _, file := range input.Files {
			if filepath.Ext(file.Path) != ".go" || file.BlobSHA == "" {
				continue
			}
			format := file.ObjectFormat
			if format == "" {
				format = "sha1"
			}
			key := file.BlobSHA + "\x00" + format + "\x00" + parserVersion
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			entry, found, err := cache.ReadParseCache(ctx, input.Repository, file.BlobSHA, format, parserVersion)
			if err != nil {
				stats.Errors++
			}
			if found {
				stats.Hits++
				cached = append(cached, entry)
			} else {
				stats.Misses++
			}
		}
		input.CachedSyntax = cached
		entries, summaryErr := provider.SyntaxCacheEntries(ctx, input)
		if summaryErr != nil {
			stats.Errors++
		}
		pending := make([]SyntaxCacheEntry, 0, len(entries))
		cachedKeys := make(map[string]struct{}, len(cached))
		for _, entry := range cached {
			cachedKeys[entry.BlobSHA+"\x00"+defaultObjectFormat(entry.ObjectFormat)+"\x00"+entry.ParserVersion] = struct{}{}
		}
		for _, entry := range entries {
			key := entry.BlobSHA + "\x00" + defaultObjectFormat(entry.ObjectFormat) + "\x00" + entry.ParserVersion
			if _, exists := cachedKeys[key]; exists {
				continue
			}
			pending = append(pending, entry)
		}
		return input, pending, stats
	}
	entries, summaryErr := provider.SyntaxCacheEntries(ctx, input)
	stats := SyntaxCacheStats{}
	if summaryErr != nil {
		stats.Errors++
	}
	pending := make([]SyntaxCacheEntry, 0, len(entries))
	for _, entry := range entries {
		_, found, err := cache.ReadParseCache(ctx, input.Repository, entry.BlobSHA, entry.ObjectFormat, entry.ParserVersion)
		if err != nil || !found {
			stats.Misses++
			pending = append(pending, entry)
			if err != nil {
				stats.Errors++
			}
			continue
		}
		stats.Hits++
	}
	return input, pending, stats
}

func defaultObjectFormat(format string) string {
	if format == "" {
		return "sha1"
	}
	return format
}

func (m *Manager) writeSyntaxCache(ctx context.Context, repoID repository.RepoID, entries []SyntaxCacheEntry, stats SyntaxCacheStats) SyntaxCacheStats {
	cache, ok := m.store.(SyntaxCache)
	if !ok {
		return stats
	}
	for _, entry := range entries {
		if err := cache.WriteParseCache(ctx, repoID, entry); err != nil {
			// Parse cache is rebuildable optimization state. Count the error and
			// retain the validated graph rather than making cache storage a new
			// activation dependency.
			stats.Errors++
		}
	}
	return stats
}

func (m *Manager) verifyObservedTarget(ctx context.Context, target repository.Target, commit CommitSHA) error {
	observed, err := m.observer.Observe(ctx, target.Worktree.Path)
	if err != nil {
		return fmt.Errorf("recheck Git HEAD before graph use: %w", err)
	}
	if (observed.Repository.ID != "" && observed.Repository.ID != target.Repository.ID) ||
		(observed.Worktree.ID != "" && observed.Worktree.ID != target.Worktree.ID) {
		return fmt.Errorf("%w: observed target identity changed from repository %s/worktree %s to repository %s/worktree %s", ErrStaleState, target.Repository.ID, target.Worktree.ID, observed.Repository.ID, observed.Worktree.ID)
	}
	if !observed.Worktree.HeadKnown || CommitSHA(observed.Worktree.Head) != commit {
		return fmt.Errorf("%w: HEAD moved from %s to %s", ErrStaleState, commit, observed.Worktree.Head)
	}
	return nil
}

func analyzerVersion(analyzer Analyzer) string {
	if versioned, ok := analyzer.(interface{ Version() string }); ok {
		return versioned.Version()
	}
	return ""
}
