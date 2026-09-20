package graphdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
)

const (
	defaultGCKeepGenerations = 1
	defaultGCOlderThan       = 24 * time.Hour
)

// GCItemKind identifies one Jejak-owned object eligible for collection.
type GCItemKind string

const (
	GCGeneration GCItemKind = "generation"
	GCParseCache GCItemKind = "parse-cache"
	GCBlob       GCItemKind = "blob"
	GCTemporary  GCItemKind = "temporary"
	GCLog        GCItemKind = "log"
)

// GCOptions controls retention and preview behavior. KeepGenerations counts
// the newest generation records per worktree in addition to always-retained
// active/readers. A negative OlderThan disables filesystem cleanup.
type GCOptions struct {
	KeepGenerations int
	OlderThan       time.Duration
	Now             time.Time
	DryRun          bool
}

func (o GCOptions) normalize() GCOptions {
	if o.KeepGenerations <= 0 {
		o.KeepGenerations = defaultGCKeepGenerations
	}
	if o.OlderThan == 0 {
		o.OlderThan = defaultGCOlderThan
	}
	if o.Now.IsZero() {
		o.Now = time.Now().UTC()
	} else {
		o.Now = o.Now.UTC()
	}
	return o
}

// GCCandidate identifies one object selected by the deterministic GC plan.
// Path is populated only for Jejak-owned temporary/log entries.
type GCCandidate struct {
	Kind          GCItemKind            `json:"kind"`
	RepositoryID  repository.RepoID     `json:"repository_id"`
	WorktreeID    repository.WorktreeID `json:"worktree_id,omitempty"`
	GenerationID  graph.GenerationID    `json:"generation_id,omitempty"`
	BlobSHA       string                `json:"blob_sha,omitempty"`
	ObjectFormat  string                `json:"object_format,omitempty"`
	ParserVersion string                `json:"parser_version,omitempty"`
	Path          string                `json:"path,omitempty"`
	Bytes         int64                 `json:"bytes,omitempty"`
	Reason        string                `json:"reason"`
}

// GCPlan is the immutable selection produced before any destructive action.
// Callers should pass this exact plan to CollectGC so preview and execution
// share one selection algorithm.
type GCPlan struct {
	Version             string            `json:"version"`
	RepositoryID        repository.RepoID `json:"repository_id"`
	DBPath              string            `json:"db_path"`
	KeepGenerations     int               `json:"keep_generations"`
	OlderThan           time.Duration     `json:"older_than_ns"`
	GeneratedAt         time.Time         `json:"generated_at"`
	RetainedGenerations int               `json:"retained_generations"`
	Candidates          []GCCandidate     `json:"candidates"`
}

// GCReport is the result of a preview or collection. A dry-run has
// PlannedItems but zero DeletedItems and never mutates SQLite/filesystem data.
type GCReport struct {
	Version             string            `json:"version"`
	RepositoryID        repository.RepoID `json:"repository_id"`
	DBPath              string            `json:"db_path"`
	DryRun              bool              `json:"dry_run"`
	KeepGenerations     int               `json:"keep_generations"`
	RetainedGenerations int               `json:"retained_generations"`
	PlannedItems        int               `json:"planned_items"`
	DeletedItems        int               `json:"deleted_items"`
	DeletedGenerations  int               `json:"deleted_generations"`
	DeletedParseCache   int               `json:"deleted_parse_cache"`
	DeletedBlobs        int               `json:"deleted_blobs"`
	DeletedTemporary    int               `json:"deleted_temporary"`
	DeletedLogs         int               `json:"deleted_logs"`
	ReclaimedBytes      int64             `json:"reclaimed_bytes"`
	// CompactedBytes is how much the database file shrank when GC rebuilt it
	// after collection. It is zero for a dry run.
	CompactedBytes int64         `json:"compacted_bytes"`
	Candidates     []GCCandidate `json:"candidates"`
	Skipped        []GCSkip      `json:"skipped,omitempty"`
	Errors         []string      `json:"errors,omitempty"`
}

// GCSkip explains why a planned item was retained when the database changed
// between planning and collection.
type GCSkip struct {
	Kind         GCItemKind            `json:"kind"`
	WorktreeID   repository.WorktreeID `json:"worktree_id,omitempty"`
	GenerationID graph.GenerationID    `json:"generation_id,omitempty"`
	Path         string                `json:"path,omitempty"`
	Reason       string                `json:"reason"`
}

// PlanGC computes a repository-scoped, reference-aware cleanup plan without
// mutating the database or filesystem. The caller should hold the repository
// writer lease before executing a plan; planning itself is safe for readers.
func (s *Store) PlanGC(ctx context.Context, repoID repository.RepoID, options GCOptions) (GCPlan, error) {
	db, err := s.database()
	if err != nil {
		return GCPlan{}, err
	}
	if strings.TrimSpace(string(repoID)) == "" {
		return GCPlan{}, fmt.Errorf("%w: repository ID is empty", ErrInvalidGeneration)
	}
	options = options.normalize()
	plan := GCPlan{
		Version:         "jejak.gc.v1",
		RepositoryID:    repoID,
		DBPath:          s.DBPath(),
		KeepGenerations: options.KeepGenerations,
		OlderThan:       options.OlderThan,
		GeneratedAt:     options.Now,
		Candidates:      make([]GCCandidate, 0),
	}
	if err := ctx.Err(); err != nil {
		return GCPlan{}, err
	}

	type generationRow struct {
		worktreeID repository.WorktreeID
		id         graph.GenerationID
		state      graph.GenerationState
		createdAt  time.Time
	}
	rows, err := db.QueryContext(ctx, `SELECT worktree_id, generation_id, state, created_at FROM graph_generations WHERE repo_id = ? ORDER BY worktree_id, generation_id DESC`, string(repoID))
	if err != nil {
		return GCPlan{}, fmt.Errorf("list graph generations for GC: %w", err)
	}
	defer func() { _ = rows.Close() }()
	generations := make([]generationRow, 0)
	byWorktree := make(map[repository.WorktreeID]int)
	retained := make(map[readerKey]struct{})
	for rows.Next() {
		var worktree, state, created string
		var id int64
		if err := rows.Scan(&worktree, &id, &state, &created); err != nil {
			return GCPlan{}, fmt.Errorf("scan graph generation for GC: %w", err)
		}
		createdAt, parseErr := parseTimestamp(created)
		if parseErr != nil {
			// A malformed timestamp is evidence for doctor, but retaining the
			// row is the safe cleanup behavior.
			createdAt = options.Now
		}
		row := generationRow{worktreeID: repository.WorktreeID(worktree), id: graph.GenerationID(id), state: graph.GenerationState(state), createdAt: createdAt}
		generations = append(generations, row)
		if byWorktree[row.worktreeID] < options.KeepGenerations {
			retained[readerKey{repoID: string(repoID), worktreeID: worktree, generation: id}] = struct{}{}
			byWorktree[row.worktreeID]++
		}
	}
	if err := rows.Err(); err != nil {
		return GCPlan{}, fmt.Errorf("iterate graph generations for GC: %w", err)
	}
	cutoff := options.Now.Add(-options.OlderThan)
	for _, row := range generations {
		key := readerKey{repoID: string(repoID), worktreeID: string(row.worktreeID), generation: int64(row.id)}
		if row.state == graph.GenerationActive || s.ReaderCount(repoID, row.worktreeID, row.id) > 0 {
			retained[key] = struct{}{}
			continue
		}
		// Building/validated rows are command-scoped and protected from a
		// concurrent collector by the repository writer lock. Keep fresh rows
		// for operators that deliberately inspect a plan, but allow old rows
		// left by an interrupted process to converge under GC. A negative age
		// disables this database-age cleanup along with filesystem cleanup.
		if (row.state == graph.GenerationBuilding || row.state == graph.GenerationValidated) && (options.OlderThan < 0 || row.createdAt.IsZero() || row.createdAt.After(cutoff)) {
			retained[key] = struct{}{}
		}
	}
	plan.RetainedGenerations = len(retained)
	for _, row := range generations {
		key := readerKey{repoID: string(repoID), worktreeID: string(row.worktreeID), generation: int64(row.id)}
		if _, keep := retained[key]; keep {
			continue
		}
		if row.state != graph.GenerationRetired && row.state != graph.GenerationSuperseded && row.state != graph.GenerationFailed && row.state != graph.GenerationBuilding && row.state != graph.GenerationValidated {
			continue
		}
		plan.Candidates = append(plan.Candidates, GCCandidate{Kind: GCGeneration, RepositoryID: repoID, WorktreeID: row.worktreeID, GenerationID: row.id, Reason: "generation is not active and is outside retention"})
	}

	// Keep only blobs referenced by retained generations. The same blob may be
	// shared by several worktrees/generations, so scope each reference before
	// adding it to the retention set.
	retainedBlobs := make(map[string]struct{})
	for _, query := range []string{
		`SELECT DISTINCT f.blob_sha, g.worktree_id, g.generation_id FROM files f JOIN graph_generations g ON g.generation_key=f.generation_key WHERE g.repo_id=? AND f.blob_sha<>''`,
		`SELECT DISTINCT n.source_blob, g.worktree_id, g.generation_id FROM nodes n JOIN graph_generations g ON g.generation_key=n.generation_key WHERE g.repo_id=? AND n.source_blob<>''`,
		`SELECT DISTINCT ev.source_blob, g.worktree_id, g.generation_id FROM edge_evidence ev JOIN edges e ON e.edge_id=ev.edge_id JOIN graph_generations g ON g.generation_key=e.generation_key WHERE g.repo_id=? AND ev.source_blob<>''`,
	} {
		refRows, queryErr := db.QueryContext(ctx, query, string(repoID))
		if queryErr != nil {
			return GCPlan{}, fmt.Errorf("list scoped blob references for GC: %w", queryErr)
		}
		for refRows.Next() {
			var blob, worktree string
			var id int64
			if scanErr := refRows.Scan(&blob, &worktree, &id); scanErr != nil {
				_ = refRows.Close()
				return GCPlan{}, fmt.Errorf("scan scoped blob reference for GC: %w", scanErr)
			}
			if _, keep := retained[readerKey{repoID: string(repoID), worktreeID: worktree, generation: id}]; keep {
				retainedBlobs[blob] = struct{}{}
			}
		}
		if rowsErr := refRows.Err(); rowsErr != nil {
			_ = refRows.Close()
			return GCPlan{}, fmt.Errorf("iterate scoped blob references for GC: %w", rowsErr)
		}
		_ = refRows.Close()
	}

	cacheRows, err := db.QueryContext(ctx, `SELECT blob_sha, object_format, parser_version, length(syntax_json) FROM parse_cache WHERE repo_id=? ORDER BY blob_sha, object_format, parser_version`, string(repoID))
	if err != nil {
		return GCPlan{}, fmt.Errorf("list parse cache for GC: %w", err)
	}
	for cacheRows.Next() {
		var blob, format, parser string
		var bytes int64
		if err := cacheRows.Scan(&blob, &format, &parser, &bytes); err != nil {
			_ = cacheRows.Close()
			return GCPlan{}, fmt.Errorf("scan parse cache for GC: %w", err)
		}
		if _, keep := retainedBlobs[blob]; !keep {
			plan.Candidates = append(plan.Candidates, GCCandidate{Kind: GCParseCache, RepositoryID: repoID, BlobSHA: blob, ObjectFormat: format, ParserVersion: parser, Bytes: bytes, Reason: "parse cache blob is not referenced by retained graph state"})
		}
	}
	if err := cacheRows.Err(); err != nil {
		_ = cacheRows.Close()
		return GCPlan{}, fmt.Errorf("iterate parse cache for GC: %w", err)
	}
	_ = cacheRows.Close()

	blobRows, err := db.QueryContext(ctx, `SELECT blob_sha, byte_size FROM blobs WHERE repo_id=? ORDER BY blob_sha`, string(repoID))
	if err != nil {
		return GCPlan{}, fmt.Errorf("list blobs for GC: %w", err)
	}
	for blobRows.Next() {
		var blob string
		var bytes int64
		if err := blobRows.Scan(&blob, &bytes); err != nil {
			_ = blobRows.Close()
			return GCPlan{}, fmt.Errorf("scan blob for GC: %w", err)
		}
		if _, keep := retainedBlobs[blob]; !keep {
			plan.Candidates = append(plan.Candidates, GCCandidate{Kind: GCBlob, RepositoryID: repoID, BlobSHA: blob, Bytes: bytes, Reason: "blob is not referenced by retained graph state"})
		}
	}
	if err := blobRows.Err(); err != nil {
		_ = blobRows.Close()
		return GCPlan{}, fmt.Errorf("iterate blobs for GC: %w", err)
	}
	_ = blobRows.Close()

	plan.Candidates = append(plan.Candidates, s.ownedFileCandidates(repoID, options)...)
	sort.Slice(plan.Candidates, func(i, j int) bool {
		left, right := plan.Candidates[i], plan.Candidates[j]
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.WorktreeID != right.WorktreeID {
			return left.WorktreeID < right.WorktreeID
		}
		if left.GenerationID != right.GenerationID {
			return left.GenerationID < right.GenerationID
		}
		if left.BlobSHA != right.BlobSHA {
			return left.BlobSHA < right.BlobSHA
		}
		return left.Path < right.Path
	})
	return plan, nil
}

// CollectGC executes a previously generated plan. It rechecks every database
// reference and in-process reader before deletion, so a plan cannot remove a
// generation that became live after preview.
func (s *Store) CollectGC(ctx context.Context, plan GCPlan) (GCReport, error) {
	db, err := s.database()
	if err != nil {
		return GCReport{}, err
	}
	if plan.RepositoryID == "" || filepath.Clean(plan.DBPath) != filepath.Clean(s.DBPath()) {
		return GCReport{}, fmt.Errorf("%w: GC plan does not belong to this store", ErrInvalidGeneration)
	}
	report := GCReport{Version: "jejak.gc.v1", RepositoryID: plan.RepositoryID, DBPath: s.DBPath(), KeepGenerations: plan.KeepGenerations, RetainedGenerations: plan.RetainedGenerations, PlannedItems: len(plan.Candidates), Candidates: append([]GCCandidate(nil), plan.Candidates...)}
	if err := ctx.Err(); err != nil {
		return GCReport{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return GCReport{}, fmt.Errorf("begin graph GC: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	filesystem := make([]GCCandidate, 0)
	// Database dependencies require a strict order: generation rows first,
	// then parse-cache rows, then unreferenced blobs. Plan presentation remains
	// deterministic, but execution must not attempt to delete a parent blob
	// before its generation/cache references are gone.
	orderedKinds := []GCItemKind{GCGeneration, GCParseCache, GCBlob, GCTemporary, GCLog}
	for _, kind := range orderedKinds {
		for _, candidate := range plan.Candidates {
			if candidate.Kind != kind {
				continue
			}
			switch candidate.Kind {
			case GCGeneration:
				if s.ReaderCount(plan.RepositoryID, candidate.WorktreeID, candidate.GenerationID) > 0 {
					report.Skipped = append(report.Skipped, GCSkip{Kind: candidate.Kind, WorktreeID: candidate.WorktreeID, GenerationID: candidate.GenerationID, Reason: "generation has an open reader"})
					continue
				}
				var state, created string
				err := tx.QueryRowContext(ctx, `SELECT state, created_at FROM graph_generations WHERE repo_id=? AND worktree_id=? AND generation_id=?`, string(plan.RepositoryID), string(candidate.WorktreeID), int64(candidate.GenerationID)).Scan(&state, &created)
				if errors.Is(err, sql.ErrNoRows) {
					report.Skipped = append(report.Skipped, GCSkip{Kind: candidate.Kind, WorktreeID: candidate.WorktreeID, GenerationID: candidate.GenerationID, Reason: "generation was already removed"})
					continue
				}
				if err != nil {
					return GCReport{}, fmt.Errorf("inspect generation %d for GC: %w", candidate.GenerationID, err)
				}
				if state == string(graph.GenerationActive) {
					report.Skipped = append(report.Skipped, GCSkip{Kind: candidate.Kind, WorktreeID: candidate.WorktreeID, GenerationID: candidate.GenerationID, Reason: "generation became active or in-flight"})
					continue
				}
				if (state == string(graph.GenerationBuilding) || state == string(graph.GenerationValidated)) && plan.OlderThan >= 0 {
					createdAt, parseErr := parseTimestamp(created)
					if parseErr != nil || !createdAt.Before(plan.GeneratedAt.Add(-plan.OlderThan)) {
						report.Skipped = append(report.Skipped, GCSkip{Kind: candidate.Kind, WorktreeID: candidate.WorktreeID, GenerationID: candidate.GenerationID, Reason: "generation is still fresh or has an invalid creation time"})
						continue
					}
				}
				result, err := tx.ExecContext(ctx, `DELETE FROM graph_generations WHERE repo_id=? AND worktree_id=? AND generation_id=? AND state <> ?`, string(plan.RepositoryID), string(candidate.WorktreeID), int64(candidate.GenerationID), string(graph.GenerationActive))
				if err != nil {
					return GCReport{}, fmt.Errorf("collect generation %d: %w", candidate.GenerationID, err)
				}
				deleted, err := result.RowsAffected()
				if err != nil {
					return GCReport{}, fmt.Errorf("check collected generation %d: %w", candidate.GenerationID, err)
				}
				if deleted == 1 {
					report.DeletedItems++
					report.DeletedGenerations++
					report.ReclaimedBytes += candidate.Bytes
				}
			case GCParseCache:
				result, err := tx.ExecContext(ctx, `DELETE FROM parse_cache WHERE repo_id=? AND blob_sha=? AND object_format=? AND parser_version=?`, string(plan.RepositoryID), candidate.BlobSHA, candidate.ObjectFormat, candidate.ParserVersion)
				if err != nil {
					return GCReport{}, fmt.Errorf("collect parse cache %q: %w", candidate.BlobSHA, err)
				}
				deleted, err := result.RowsAffected()
				if err != nil {
					return GCReport{}, fmt.Errorf("check collected parse cache %q: %w", candidate.BlobSHA, err)
				}
				if deleted == 1 {
					report.DeletedItems++
					report.DeletedParseCache++
					report.ReclaimedBytes += candidate.Bytes
				}
			case GCBlob:
				result, err := tx.ExecContext(ctx, `DELETE FROM blobs WHERE repo_id=? AND blob_sha=?
					AND NOT EXISTS (SELECT 1 FROM files f JOIN graph_generations g ON g.generation_key=f.generation_key WHERE g.repo_id=blobs.repo_id AND f.blob_sha=blobs.blob_sha)
					AND NOT EXISTS (SELECT 1 FROM nodes n JOIN graph_generations g ON g.generation_key=n.generation_key WHERE g.repo_id=blobs.repo_id AND n.source_blob=blobs.blob_sha)
					AND NOT EXISTS (SELECT 1 FROM edge_evidence ev JOIN edges e ON e.edge_id=ev.edge_id JOIN graph_generations g ON g.generation_key=e.generation_key WHERE g.repo_id=blobs.repo_id AND ev.source_blob=blobs.blob_sha)
					AND NOT EXISTS (SELECT 1 FROM parse_cache c WHERE c.repo_id=blobs.repo_id AND c.blob_sha=blobs.blob_sha)`, string(plan.RepositoryID), candidate.BlobSHA)
				if err != nil {
					return GCReport{}, fmt.Errorf("collect blob %q: %w", candidate.BlobSHA, err)
				}
				deleted, err := result.RowsAffected()
				if err != nil {
					return GCReport{}, fmt.Errorf("check collected blob %q: %w", candidate.BlobSHA, err)
				}
				if deleted == 1 {
					report.DeletedItems++
					report.DeletedBlobs++
					report.ReclaimedBytes += candidate.Bytes
				}
			case GCTemporary, GCLog:
				filesystem = append(filesystem, candidate)
			default:
				return GCReport{}, fmt.Errorf("%w: unknown GC candidate kind %q", ErrInvalidGeneration, candidate.Kind)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return GCReport{}, fmt.Errorf("commit graph GC: %w", err)
	}
	for _, candidate := range filesystem {
		if err := removeOwnedCandidate(s.dbPath, candidate); err != nil {
			report.Errors = append(report.Errors, err.Error())
			continue
		}
		report.DeletedItems++
		if candidate.Kind == GCTemporary {
			report.DeletedTemporary++
		} else {
			report.DeletedLogs++
		}
		report.ReclaimedBytes += candidate.Bytes
	}
	if len(report.Errors) > 0 {
		return report, fmt.Errorf("graph GC completed with %d filesystem error(s)", len(report.Errors))
	}
	return report, nil
}

// GC computes and optionally executes one scoped cleanup operation, then
// compacts the database file so the space freed by this and earlier
// deletions returns to the filesystem. Callers performing a mutation must
// hold the Store's writer lease and must not hold open views.
func (s *Store) GC(ctx context.Context, repoID repository.RepoID, options GCOptions) (GCReport, error) {
	plan, err := s.PlanGC(ctx, repoID, options)
	if err != nil {
		return GCReport{}, err
	}
	if options.DryRun {
		return reportForPlan(plan, true), nil
	}
	report, err := s.CollectGC(ctx, plan)
	if err != nil {
		return report, err
	}
	compacted, err := s.Compact(ctx)
	if err != nil {
		return report, fmt.Errorf("compact graph database after GC: %w", err)
	}
	report.CompactedBytes = compacted
	return report, nil
}

func reportForPlan(plan GCPlan, dryRun bool) GCReport {
	report := GCReport{Version: plan.Version, RepositoryID: plan.RepositoryID, DBPath: plan.DBPath, DryRun: dryRun, KeepGenerations: plan.KeepGenerations, RetainedGenerations: plan.RetainedGenerations, PlannedItems: len(plan.Candidates), Candidates: append([]GCCandidate(nil), plan.Candidates...)}
	return report
}

func (s *Store) ownedFileCandidates(repoID repository.RepoID, options GCOptions) []GCCandidate {
	if options.OlderThan < 0 {
		return nil
	}
	cutoff := options.Now.Add(-options.OlderThan)
	root := filepath.Dir(s.dbPath)
	result := make([]GCCandidate, 0)
	collect := func(directory string, kind GCItemKind, predicate func(os.DirEntry) bool) {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if !predicate(entry) {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			if !pathWithin(root, path) {
				continue
			}
			info, err := entry.Info()
			if err != nil || info.ModTime().After(cutoff) || info.ModTime().Equal(cutoff) {
				continue
			}
			bytes := int64(0)
			if info.IsDir() {
				bytes = directorySize(path)
			} else {
				bytes = info.Size()
			}
			result = append(result, GCCandidate{Kind: kind, RepositoryID: repoID, Path: path, Bytes: bytes, Reason: fmt.Sprintf("Jejak-owned entry is older than %s", options.OlderThan)})
		}
	}
	collect(filepath.Join(root, "tmp"), GCTemporary, func(entry os.DirEntry) bool {
		return entry.IsDir() && strings.HasPrefix(entry.Name(), "jejak-snapshot-")
	})
	collect(filepath.Join(root, "logs"), GCLog, func(entry os.DirEntry) bool {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return false
		}
		name := strings.ToLower(entry.Name())
		return strings.HasSuffix(name, ".log") || strings.HasSuffix(name, ".jsonl")
	})
	return result
}

func removeOwnedCandidate(dbPath string, candidate GCCandidate) error {
	root := filepath.Dir(dbPath)
	if candidate.Path == "" || !pathWithin(root, candidate.Path) {
		return fmt.Errorf("GC candidate path %q is outside the store", candidate.Path)
	}
	clean := filepath.Clean(candidate.Path)
	base := filepath.Base(clean)
	switch candidate.Kind {
	case GCTemporary:
		if !pathWithin(filepath.Join(root, "tmp"), clean) || !strings.HasPrefix(base, "jejak-snapshot-") {
			return fmt.Errorf("GC temporary candidate %q is not Jejak-owned", clean)
		}
		info, err := os.Lstat(clean)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect GC temporary candidate %q: %w", clean, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("GC temporary candidate %q is a symlink", clean)
		}
		if err := os.RemoveAll(clean); err != nil {
			return fmt.Errorf("remove GC temporary candidate %q: %w", clean, err)
		}
	case GCLog:
		if !pathWithin(filepath.Join(root, "logs"), clean) || (!strings.HasSuffix(strings.ToLower(base), ".log") && !strings.HasSuffix(strings.ToLower(base), ".jsonl")) {
			return fmt.Errorf("GC log candidate %q is not Jejak-owned", clean)
		}
		if err := os.Remove(clean); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove GC log candidate %q: %w", clean, err)
		}
	default:
		return fmt.Errorf("GC filesystem candidate has unsupported kind %q", candidate.Kind)
	}
	return nil
}

func directorySize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry == nil || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, statErr := entry.Info()
		if statErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func pathWithin(base, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(base), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
