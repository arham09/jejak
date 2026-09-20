package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/arham09/jejak/internal/repository"
)

type managerTestStore struct {
	state       State
	generations map[GenerationID]Generation
	analyses    map[GenerationID]AnalysisResult
	nextID      GenerationID
}

func newManagerTestStore(target repository.Target) *managerTestStore {
	return &managerTestStore{state: State{RepoID: target.Repository.ID, WorktreeID: target.Worktree.ID, Status: StatusUnindexed}, generations: make(map[GenerationID]Generation), analyses: make(map[GenerationID]AnalysisResult)}
}

func (s *managerTestStore) State(context.Context, repository.RepoID, repository.WorktreeID) (State, error) {
	return s.state, nil
}

func (s *managerTestStore) Generation(_ context.Context, _ repository.RepoID, _ repository.WorktreeID, id GenerationID) (Generation, error) {
	generation, ok := s.generations[id]
	if !ok {
		return Generation{}, errors.New("generation not found")
	}
	return generation, nil
}

func (s *managerTestStore) CreateGeneration(_ context.Context, generation Generation) (Generation, error) {
	s.nextID++
	generation.ID = s.nextID
	generation.State = GenerationBuilding
	s.generations[generation.ID] = generation
	s.state.Status = StatusBuilding
	return generation, nil
}

func (s *managerTestStore) WriteAnalysis(_ context.Context, generation Generation, result AnalysisResult) error {
	s.analyses[generation.ID] = result
	return nil
}

func (s *managerTestStore) ValidateGeneration(_ context.Context, _ repository.RepoID, _ repository.WorktreeID, id GenerationID) error {
	generation := s.generations[id]
	generation.State = GenerationValidated
	s.generations[id] = generation
	return nil
}

func (s *managerTestStore) ActivateGeneration(_ context.Context, _ repository.RepoID, _ repository.WorktreeID, id GenerationID, expected *GenerationID) error {
	if expected == nil && s.state.ActiveGeneration != nil {
		return errors.New("unexpected active generation")
	}
	if expected != nil && (s.state.ActiveGeneration == nil || *s.state.ActiveGeneration != *expected) {
		return errors.New("stale generation")
	}
	if s.state.ActiveGeneration != nil && *s.state.ActiveGeneration != id {
		// Mirror the real store: the replaced generation is retired in the
		// same activation, which is what pruning later removes.
		previous := s.generations[*s.state.ActiveGeneration]
		previous.State = GenerationRetired
		s.generations[*s.state.ActiveGeneration] = previous
	}
	generation := s.generations[id]
	generation.State = GenerationActive
	s.generations[id] = generation
	s.state.ActiveGeneration = &id
	s.state.IndexedHead = generation.Commit
	s.state.Status = StatusReady
	return nil
}

func (s *managerTestStore) FailGeneration(_ context.Context, _ repository.RepoID, _ repository.WorktreeID, id GenerationID, cause error) error {
	generation := s.generations[id]
	generation.State = GenerationFailed
	generation.Error = cause.Error()
	s.generations[id] = generation
	s.state.Status = StatusFailed
	s.state.LastError = cause.Error()
	return nil
}

func (s *managerTestStore) GenerationCounts(_ context.Context, _ repository.RepoID, _ repository.WorktreeID, id GenerationID) (Counts, error) {
	return s.analyses[id].Count(), nil
}

type managerTestAnalyzer struct {
	inputs []AnalyzeInput
}

func (a *managerTestAnalyzer) Supports(string) bool { return true }

func (a *managerTestAnalyzer) Version() string { return "fake-v1" }

func (a *managerTestAnalyzer) BuildFingerprint(context.Context, AnalyzeInput) (string, error) {
	return "fake-fingerprint", nil
}

func (a *managerTestAnalyzer) Analyze(_ context.Context, input AnalyzeInput) (AnalysisResult, error) {
	a.inputs = append(a.inputs, input)
	return AnalysisResult{AnalyzerVersion: "fake-v1", BuildFingerprint: "fake-fingerprint"}, nil
}

type managerTestSnapshot struct{}

func (managerTestSnapshot) Snapshot(context.Context, string, string) (*Snapshot, error) {
	return NewSnapshot("/immutable-snapshot", "", nil, nil), nil
}

type managerTestObserver struct{ target *repository.Target }

func (o managerTestObserver) Observe(context.Context, string) (repository.Target, error) {
	return *o.target, nil
}

type managerTestDiff struct {
	changes []FileChange
	err     error
}

func (d managerTestDiff) Diff(context.Context, string, string, string) ([]FileChange, error) {
	return d.changes, d.err
}

type managerCacheStore struct {
	*managerTestStore
	entries []SyntaxCacheEntry
	writes  []SyntaxCacheEntry
}

func (s *managerCacheStore) ReadParseCache(_ context.Context, _ repository.RepoID, blobSHA, objectFormat, parserVersion string) (SyntaxCacheEntry, bool, error) {
	if objectFormat == "" {
		objectFormat = "sha1"
	}
	for _, entry := range s.entries {
		format := entry.ObjectFormat
		if format == "" {
			format = "sha1"
		}
		if entry.BlobSHA == blobSHA && format == objectFormat && entry.ParserVersion == parserVersion {
			entry.ObjectFormat = format
			return entry, true, nil
		}
	}
	return SyntaxCacheEntry{}, false, nil
}

func (s *managerCacheStore) WriteParseCache(_ context.Context, _ repository.RepoID, entry SyntaxCacheEntry) error {
	s.writes = append(s.writes, entry)
	return nil
}

type managerCacheAnalyzer struct {
	*managerTestAnalyzer
}

func (a *managerCacheAnalyzer) SyntaxParserVersion() string { return "fake-syntax-v1" }

func (a *managerCacheAnalyzer) SyntaxCacheEntries(_ context.Context, input AnalyzeInput) ([]SyntaxCacheEntry, error) {
	entries := append([]SyntaxCacheEntry(nil), input.CachedSyntax...)
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		format := entry.ObjectFormat
		if format == "" {
			format = "sha1"
		}
		seen[entry.BlobSHA+"\x00"+format] = struct{}{}
	}
	for _, file := range input.Files {
		if file.BlobSHA == "" {
			continue
		}
		format := file.ObjectFormat
		if format == "" {
			format = "sha1"
		}
		key := file.BlobSHA + "\x00" + format
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		entries = append(entries, SyntaxCacheEntry{BlobSHA: file.BlobSHA, ObjectFormat: format, ParserVersion: a.SyntaxParserVersion(), SyntaxJSON: []byte(`{"package":"miss"}`)})
	}
	return entries, nil
}

type managerCacheSnapshot struct{}

func (managerCacheSnapshot) Snapshot(context.Context, string, string) (*Snapshot, error) {
	return NewSnapshot("/immutable-snapshot", "", []SnapshotFile{
		{Path: "hit.go", BlobSHA: "hit"},
		{Path: "miss.go", BlobSHA: "miss"},
	}, nil), nil
}

func TestManagerSelectsIncrementalAndRebuild(t *testing.T) {
	target := repository.Target{Repository: repository.Descriptor{ID: "repo"}, Worktree: repository.Worktree{ID: "worktree", Path: "/repo", Head: "first", HeadKnown: true}}
	store := newManagerTestStore(target)
	analyzer := &managerTestAnalyzer{}
	observer := managerTestObserver{target: &target}
	diff := managerTestDiff{changes: []FileChange{{Kind: ChangeModified, OldPath: "main.go", NewPath: "main.go"}}}
	manager, err := NewManagerWithOptions(store, analyzer, managerTestSnapshot{}, observer, ManagerOptions{Diff: diff, Thresholds: SyncThresholds{MaxChangedFiles: 10, MaxChangedRatio: 1}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.EnsureGraph(context.Background(), target, BuildConfig{})
	if err != nil || first.Mode != SyncModeRebuild {
		t.Fatalf("initial ensure result=%#v err=%v", first, err)
	}
	if reused, err := manager.EnsureGraph(context.Background(), target, BuildConfig{}); err != nil {
		t.Fatal(err)
	} else if !reused.Reused || reused.Mode != SyncModeReuse {
		t.Fatalf("reuse result=%#v", reused)
	}
	target.Worktree.Head = "second"
	second, err := manager.EnsureGraph(context.Background(), target, BuildConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Mode != SyncModeIncremental || len(second.Changes) != 1 || !analyzer.inputs[len(analyzer.inputs)-1].Incremental || analyzer.inputs[len(analyzer.inputs)-1].PreviousCommit != "first" {
		t.Fatalf("incremental ensure result=%#v inputs=%#v", second, analyzer.inputs)
	}
	forced, err := manager.RebuildGraph(context.Background(), target, BuildConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if forced.Mode != SyncModeRebuild || forced.Reason != "forced rebuild" {
		t.Fatalf("forced result=%#v", forced)
	}
}

func TestManagerFallsBackToRebuildWhenIndexedBaseUnavailable(t *testing.T) {
	target := repository.Target{Repository: repository.Descriptor{ID: "repo"}, Worktree: repository.Worktree{ID: "worktree", Path: "/repo", Head: "new", HeadKnown: true}}
	store := newManagerTestStore(target)
	store.state.IndexedHead = "old"
	store.state.Status = StatusStale
	store.state.ActiveGeneration = nil
	analyzer := &managerTestAnalyzer{}
	manager, err := NewManagerWithOptions(store, analyzer, managerTestSnapshot{}, managerTestObserver{target: &target}, ManagerOptions{Diff: managerTestDiff{err: errors.New("unknown revision")}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.EnsureGraph(context.Background(), target, BuildConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != SyncModeRebuild || result.Reason == "" {
		t.Fatalf("unavailable-base result=%#v", result)
	}
}

func TestManagerSuppliesSyntaxCacheHitsBeforeProducingSummaries(t *testing.T) {
	target := repository.Target{Repository: repository.Descriptor{ID: "repo"}, Worktree: repository.Worktree{ID: "worktree", Path: "/repo", Head: "first", HeadKnown: true}}
	baseStore := newManagerTestStore(target)
	store := &managerCacheStore{managerTestStore: baseStore, entries: []SyntaxCacheEntry{{BlobSHA: "hit", ObjectFormat: "sha1", ParserVersion: "fake-syntax-v1", SyntaxJSON: []byte(`{"package":"hit"}`)}}}
	analyzer := &managerCacheAnalyzer{managerTestAnalyzer: &managerTestAnalyzer{}}
	manager, err := NewManager(store, analyzer, managerCacheSnapshot{}, managerTestObserver{target: &target})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.EnsureGraph(context.Background(), target, BuildConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Cache.Hits != 1 || result.Cache.Misses != 1 || result.Cache.Errors != 0 {
		t.Fatalf("cache stats = %#v", result.Cache)
	}
	if len(analyzer.inputs) != 1 || len(analyzer.inputs[0].CachedSyntax) != 1 || analyzer.inputs[0].CachedSyntax[0].BlobSHA != "hit" {
		t.Fatalf("analyzer cache input = %#v", analyzer.inputs)
	}
	if len(store.writes) != 1 || store.writes[0].BlobSHA != "miss" {
		t.Fatalf("cache writes = %#v", store.writes)
	}
}

// managerPruningStore records pruning requests so the test can assert the
// manager prunes only after a generation became active.
type managerPruningStore struct {
	*managerTestStore
	pruneCalls int
}

func (s *managerPruningStore) PruneGenerations(_ context.Context, _ repository.RepoID, _ repository.WorktreeID) (int, error) {
	s.pruneCalls++
	pruned := 0
	for id, generation := range s.generations {
		if generation.State != GenerationActive {
			delete(s.generations, id)
			delete(s.analyses, id)
			pruned++
		}
	}
	return pruned, nil
}

func TestManagerPrunesSupersededGenerationsAfterActivation(t *testing.T) {
	target := repository.Target{Repository: repository.Descriptor{ID: "repo"}, Worktree: repository.Worktree{ID: "worktree", Path: "/repo", Head: "first", HeadKnown: true}}
	store := &managerPruningStore{managerTestStore: newManagerTestStore(target)}
	analyzer := &managerTestAnalyzer{}
	manager, err := NewManagerWithOptions(store, analyzer, managerTestSnapshot{}, managerTestObserver{target: &target}, ManagerOptions{Diff: managerTestDiff{}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.EnsureGraph(context.Background(), target, BuildConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if store.pruneCalls != 1 || first.PrunedGenerations != 0 {
		t.Fatalf("first ensure prune calls=%d pruned=%d, want 1 call removing nothing", store.pruneCalls, first.PrunedGenerations)
	}
	if reused, err := manager.EnsureGraph(context.Background(), target, BuildConfig{}); err != nil || !reused.Reused || store.pruneCalls != 1 {
		t.Fatalf("reuse must not prune: result=%#v err=%v calls=%d", reused, err, store.pruneCalls)
	}
	target.Worktree.Head = "second"
	second, err := manager.EnsureGraph(context.Background(), target, BuildConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if store.pruneCalls != 2 || second.PrunedGenerations != 1 {
		t.Fatalf("second ensure prune calls=%d pruned=%d, want the first generation pruned", store.pruneCalls, second.PrunedGenerations)
	}
	if len(store.generations) != 1 {
		t.Fatalf("generations after pruning = %d, want 1", len(store.generations))
	}
	if _, ok := store.generations[second.Generation.ID]; !ok {
		t.Fatalf("active generation %d was pruned", second.Generation.ID)
	}
}

func TestManagerDoesNotPruneWhenActivationFails(t *testing.T) {
	target := repository.Target{Repository: repository.Descriptor{ID: "repo"}, Worktree: repository.Worktree{ID: "worktree", Path: "/repo", Head: "first", HeadKnown: true}}
	store := &managerPruningStore{managerTestStore: newManagerTestStore(target)}
	moved := target
	moved.Worktree.Head = "moved"
	manager, err := NewManagerWithOptions(store, &managerTestAnalyzer{}, managerTestSnapshot{}, managerTestObserver{target: &moved}, ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.EnsureGraph(context.Background(), target, BuildConfig{}); !errors.Is(err, ErrStaleState) {
		t.Fatalf("ensure with moved HEAD error = %v, want ErrStaleState", err)
	}
	if store.pruneCalls != 0 {
		t.Fatalf("failed activation pruned generations: calls=%d", store.pruneCalls)
	}
}
