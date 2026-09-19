package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	golanganalyzer "github.com/arham09/jejak/internal/analyzer/golang"
	"github.com/arham09/jejak/internal/config"
	"github.com/arham09/jejak/internal/git"
	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/graphdb"
	"github.com/arham09/jejak/internal/hooks"
	"github.com/arham09/jejak/internal/overlay"
	"github.com/arham09/jejak/internal/repository"
)

const writerWait = 5 * time.Second

// Options contains command-level repository and storage selection.
type Options struct {
	WorktreePath         string
	DataDir              string
	DownloadDependencies bool
	Quiet                bool
	NoHooks              bool
	NoAgentSkills        bool
	JSON                 bool
	// Committed forces graph-dependent inspection commands to read only the
	// durable generation. Reports otherwise use a command-scoped working-tree
	// overlay by default.
	Committed  bool
	Tags       []string
	GOOS       string
	GOARCH     string
	CGOEnabled string
}

type snapshotProvider struct {
	client *git.Client
	parent string
}

func (p snapshotProvider) Snapshot(ctx context.Context, root, commit string) (*graph.Snapshot, error) {
	snapshot, err := p.client.SnapshotIn(ctx, root, commit, p.parent)
	if err != nil {
		return nil, err
	}
	files := make([]graph.SnapshotFile, 0, len(snapshot.Files))
	for _, file := range snapshot.Files {
		files = append(files, graph.SnapshotFile{Path: file.Path, BlobSHA: file.BlobSHA, ObjectFormat: file.ObjectFormat, Mode: file.Mode, Size: file.Size})
	}
	return graph.NewSnapshot(snapshot.Root, graph.CommitSHA(snapshot.Commit), files, snapshot.Close), nil
}

type headObserver struct{ client *git.Client }

func (o headObserver) Observe(ctx context.Context, path string) (repository.Target, error) {
	return repository.Resolve(ctx, o.client, path)
}

type diffProvider struct{ client *git.Client }

func (p diffProvider) Diff(ctx context.Context, root, oldCommit, newCommit string) ([]graph.FileChange, error) {
	changes, err := p.client.Diff(ctx, root, oldCommit, newCommit)
	if err != nil {
		return nil, err
	}
	result := make([]graph.FileChange, 0, len(changes))
	for _, change := range changes {
		mapped := graph.FileChange{OldPath: change.OldPath, NewPath: change.NewPath, Score: change.Score}
		switch change.Kind {
		case git.ChangeAdded:
			mapped.Kind = graph.ChangeAdded
		case git.ChangeModified:
			mapped.Kind = graph.ChangeModified
		case git.ChangeDeleted:
			mapped.Kind = graph.ChangeDeleted
		case git.ChangeRenamed:
			mapped.Kind = graph.ChangeRenamed
		case git.ChangeCopied:
			mapped.Kind = graph.ChangeCopied
		case git.ChangeTypeChange:
			mapped.Kind = graph.ChangeTypeChange
		default:
			return nil, fmt.Errorf("unsupported Git change kind %q", change.Kind)
		}
		result = append(result, mapped)
	}
	return graph.NormalizeChanges(result)
}

// App owns concrete dependencies for one CLI invocation. It deliberately
// contains no mutable global repository selection.
type App struct {
	git    *git.Client
	stdout io.Writer
	stderr io.Writer
}

// New returns a CLI application with explicit output streams and a concrete
// Git adapter.
func New(stdout, stderr io.Writer) *App {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return &App{git: git.NewClient("git"), stdout: stdout, stderr: stderr}
}

func (a *App) target(ctx context.Context, options Options) (repository.Target, error) {
	return repository.Resolve(ctx, a.git, options.WorktreePath)
}

func (a *App) manager(target repository.Target, handle *targetStore, dataRoot string) (*graph.Manager, error) {
	paths, err := config.RepositoryPathsFor(dataRoot, string(target.Repository.ID))
	if err != nil {
		return nil, err
	}
	return graph.NewManagerWithOptions(
		handle.store,
		golanganalyzer.New(),
		snapshotProvider{client: a.git, parent: filepath.Join(paths.Tmp)},
		headObserver{client: a.git},
		graph.ManagerOptions{Diff: diffProvider{client: a.git}, Thresholds: graph.DefaultSyncThresholds()},
	)
}

func (a *App) hookManager(dataRoot string) *hooks.Manager {
	return hooks.NewManager(a.git, dataRoot)
}

func (a *App) overlayManager(dataRoot string, target repository.Target) (*overlay.Manager, error) {
	paths, err := config.RepositoryPathsFor(dataRoot, string(target.Repository.ID))
	if err != nil {
		return nil, err
	}
	return overlay.NewManager(a.git, golanganalyzer.New(), paths.Tmp)
}

type targetStore struct {
	store *graphdb.Store
	lock  *graphdb.WriterLock
}

func (h *targetStore) Close() error {
	if h == nil {
		return nil
	}
	var result error
	if h.lock != nil {
		result = errors.Join(result, h.lock.Close())
	}
	if h.store != nil {
		result = errors.Join(result, h.store.Close())
	}
	return result
}

func (a *App) openTargetStore(ctx context.Context, options Options, target repository.Target) (*targetStore, string, error) {
	dataRoot, err := config.ResolveDataRoot(options.DataDir, target.Repository.Root)
	if err != nil {
		return nil, "", err
	}
	if err := config.EnsureDataRoot(dataRoot); err != nil {
		return nil, "", err
	}
	paths, err := config.RepositoryPathsFor(dataRoot, string(target.Repository.ID))
	if err != nil {
		return nil, "", err
	}
	store, err := graphdb.Open(ctx, paths.DB)
	if err != nil {
		return nil, "", err
	}
	lock, err := store.AcquireWriter(ctx, writerWait)
	if err != nil {
		_ = store.Close()
		return nil, "", err
	}
	handle := &targetStore{store: store, lock: lock}
	if err := store.Migrate(ctx); err != nil {
		_ = handle.Close()
		return nil, "", err
	}
	return handle, dataRoot, nil
}

func resolveDataRoot(options Options) (string, error) {
	dataRoot, err := config.ResolveDataRoot(options.DataDir, "")
	if err != nil {
		return "", err
	}
	if err := config.EnsureDataRoot(dataRoot); err != nil {
		return "", err
	}
	return filepath.Clean(dataRoot), nil
}

func joinClose(primary error, closeErr error) error {
	if closeErr == nil {
		return primary
	}
	if primary == nil {
		return closeErr
	}
	return errors.Join(primary, fmt.Errorf("close resources: %w", closeErr))
}
