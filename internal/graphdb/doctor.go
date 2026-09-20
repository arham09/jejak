package graphdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
)

// DoctorStatus is the aggregate state of a storage health report.
type DoctorStatus string

const (
	DoctorHealthy   DoctorStatus = "healthy"
	DoctorWarning   DoctorStatus = "warning"
	DoctorUnhealthy DoctorStatus = "unhealthy"
)

// DoctorCheckStatus is the result of one independent health check.
type DoctorCheckStatus string

const (
	DoctorCheckPass    DoctorCheckStatus = "pass"
	DoctorCheckWarning DoctorCheckStatus = "warning"
	DoctorCheckError   DoctorCheckStatus = "error"
)

// DoctorCheck is a deterministic, human-readable check summary.
type DoctorCheck struct {
	Name   string            `json:"name"`
	Status DoctorCheckStatus `json:"status"`
	Detail string            `json:"detail,omitempty"`
}

// DoctorIssue is an actionable structural or provenance diagnostic. It does
// not include source content or credentials.
type DoctorIssue struct {
	Code       string                   `json:"code"`
	Severity   graph.DiagnosticSeverity `json:"severity"`
	Message    string                   `json:"message"`
	Repository string                   `json:"repository_id,omitempty"`
	Worktree   string                   `json:"worktree_id,omitempty"`
	Generation graph.GenerationID       `json:"generation_id,omitempty"`
}

// DoctorHook is a generic hook status projection. The graphdb package does
// not import the hooks package; the CLI adapts its concrete report to this
// shape for text and JSON diagnostics.
type DoctorHook struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// DoctorReport is the versioned storage health result for one selected
// repository/worktree. Healthy reports may still contain warnings (for
// example an uninstalled hook or an unindexed worktree).
type DoctorReport struct {
	Version       string                `json:"version"`
	RepositoryID  repository.RepoID     `json:"repository_id"`
	WorktreeID    repository.WorktreeID `json:"worktree_id,omitempty"`
	DBPath        string                `json:"db_path"`
	Status        DoctorStatus          `json:"status"`
	Healthy       bool                  `json:"healthy"`
	SchemaVersion int                   `json:"schema_version,omitempty"`
	State         *graph.State          `json:"state,omitempty"`
	Generation    *graph.Generation     `json:"generation,omitempty"`
	Counts        *graph.Counts         `json:"counts,omitempty"`
	Checks        []DoctorCheck         `json:"checks"`
	Issues        []DoctorIssue         `json:"issues,omitempty"`
	Hooks         []DoctorHook          `json:"hooks,omitempty"`
}

// HasErrors reports whether the report contains an error-level issue.
func (r DoctorReport) HasErrors() bool {
	for _, issue := range r.Issues {
		if issue.Severity == graph.SeverityError {
			return true
		}
	}
	return false
}

// AddIssue appends a caller-supplied diagnostic, allowing adapters such as
// Git and hook inspection to contribute to the same versioned report.
func (r *DoctorReport) AddIssue(code string, severity graph.DiagnosticSeverity, message string) {
	if r == nil || strings.TrimSpace(message) == "" {
		return
	}
	r.Issues = append(r.Issues, DoctorIssue{Code: code, Severity: severity, Message: message, Repository: string(r.RepositoryID), Worktree: string(r.WorktreeID)})
	r.Checks = append(r.Checks, DoctorCheck{Name: code, Status: statusForSeverity(severity), Detail: message})
}

// AddCheck appends an informational check without changing aggregate health.
func (r *DoctorReport) AddCheck(name string, status DoctorCheckStatus, detail string) {
	if r == nil {
		return
	}
	r.Checks = append(r.Checks, DoctorCheck{Name: name, Status: status, Detail: detail})
}

// Finalize recomputes aggregate status after adapters add checks/issues.
func (r *DoctorReport) Finalize() {
	if r != nil {
		r.finish()
	}
}

// Doctor inspects the selected repository/worktree without mutating the
// database. The caller may use the report to decide whether a normal rebuild
// or the explicit quarantine recovery path is appropriate. Database/query
// failures are preserved as issues; only an unusable Store or cancellation is
// returned as an error.
func (s *Store) Doctor(ctx context.Context, repoID repository.RepoID, worktreeID repository.WorktreeID) (DoctorReport, error) {
	db, err := s.database()
	if err != nil {
		return DoctorReport{}, err
	}
	report := DoctorReport{
		Version:      "jejak.doctor.v1",
		RepositoryID: repoID,
		WorktreeID:   worktreeID,
		DBPath:       s.DBPath(),
		Status:       DoctorHealthy,
		Healthy:      true,
		Checks:       make([]DoctorCheck, 0, 16),
		Issues:       make([]DoctorIssue, 0),
	}
	if err := ctx.Err(); err != nil {
		return DoctorReport{}, err
	}

	report.checkPragmas(ctx, db)
	schemaOK := report.checkSchema(ctx, db)
	if !schemaOK {
		report.finish()
		return report, nil
	}
	if version, versionErr := s.SchemaVersion(ctx); versionErr != nil {
		report.errorIssue("schema.version_unreadable", "schema", fmt.Sprintf("read schema version: %v", versionErr))
	} else {
		report.SchemaVersion = version
		if version > latestMigration {
			report.errorIssue("schema.future", "schema", fmt.Sprintf("schema version %d is newer than supported version %d", version, latestMigration))
		} else if version < latestMigration {
			report.warningIssue("schema.pending", "schema", fmt.Sprintf("schema version %d is older than supported version %d; run init or sync to migrate", version, latestMigration))
		}
	}

	if repoID == "" || worktreeID == "" {
		report.infoCheck("scope", "repository/worktree scope was not supplied")
		report.checkGlobalGraph(ctx, db)
	} else {
		report.checkSelectedGraph(ctx, db, repoID, worktreeID)
	}
	report.checkParseCache(ctx, db, repoID)
	// Overlays are command-scoped and intentionally have no durable rows to
	// inspect. Keep the fact visible so an operator does not mistake absence of
	// an overlay table for a failed health check.
	report.infoCheck("overlay.base", "working-tree overlays are command-scoped; no durable overlay base is retained")
	report.finish()
	return report, nil
}

func (r *DoctorReport) checkPragmas(ctx context.Context, db *sql.DB) {
	var foreignKeys int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		r.errorIssue("sqlite.foreign_keys", "sqlite", fmt.Sprintf("read foreign_keys pragma: %v", err))
	} else if foreignKeys != 1 {
		r.errorIssue("sqlite.foreign_keys", "sqlite", "foreign-key enforcement is disabled")
	} else {
		r.passCheck("sqlite.foreign_keys", "enabled")
	}
	var journalMode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		r.errorIssue("sqlite.journal_mode", "sqlite", fmt.Sprintf("read journal_mode pragma: %v", err))
	} else if !strings.EqualFold(strings.TrimSpace(journalMode), "wal") {
		r.warningIssue("sqlite.journal_mode", "sqlite", fmt.Sprintf("journal mode is %q; WAL is recommended", journalMode))
	} else {
		r.passCheck("sqlite.journal_mode", "WAL")
	}
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		r.errorIssue("sqlite.integrity", "sqlite", fmt.Sprintf("run integrity check: %v", err))
		return
	}
	defer func() { _ = rows.Close() }()
	checked := false
	for rows.Next() {
		var result string
		if scanErr := rows.Scan(&result); scanErr != nil {
			r.errorIssue("sqlite.integrity", "sqlite", fmt.Sprintf("scan integrity check: %v", scanErr))
			continue
		}
		checked = true
		if !strings.EqualFold(strings.TrimSpace(result), "ok") {
			r.errorIssue("sqlite.integrity", "sqlite", fmt.Sprintf("integrity check reported %q", result))
		}
	}
	if err := rows.Err(); err != nil {
		r.errorIssue("sqlite.integrity", "sqlite", fmt.Sprintf("iterate integrity check: %v", err))
	} else if checked && !r.hasIssueCode("sqlite.integrity") {
		r.passCheck("sqlite.integrity", "ok")
	} else if !checked {
		r.errorIssue("sqlite.integrity", "sqlite", "integrity check returned no result")
	}
}

func (r *DoctorReport) checkSchema(ctx context.Context, db *sql.DB) bool {
	const required = "schema_migrations,repositories,worktrees,graph_state,graph_generations,blobs,packages,files,symbols,nodes,edges,edge_evidence,package_dependencies,test_relationships,parse_cache"
	missing := make([]string, 0)
	for _, table := range strings.Split(required, ",") {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			r.errorIssue("schema.tables", "schema", fmt.Sprintf("inspect table %q: %v", table, err))
			continue
		}
		if count != 1 {
			missing = append(missing, table)
		}
	}
	if len(missing) > 0 {
		r.errorIssue("schema.tables", "schema", fmt.Sprintf("missing required table(s): %s", strings.Join(missing, ", ")))
		return false
	}
	r.passCheck("schema.tables", "all required tables are present")
	return true
}

func (r *DoctorReport) checkSelectedGraph(ctx context.Context, db *sql.DB, repoID repository.RepoID, worktreeID repository.WorktreeID) {
	var repositoryCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM repositories WHERE repo_id = ?`, string(repoID)).Scan(&repositoryCount); err != nil {
		r.errorIssue("catalog.repository", "catalog", fmt.Sprintf("read repository metadata: %v", err))
	} else if repositoryCount == 0 {
		r.errorIssue("catalog.repository", "catalog", fmt.Sprintf("repository %s is not registered", repoID))
	} else {
		r.passCheck("catalog.repository", "registered")
	}

	state, err := readStateWithoutValidation(ctx, db, repoID, worktreeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			r.warningIssue("graph.state", "state", fmt.Sprintf("worktree %s is not registered", worktreeID))
		} else {
			r.errorIssue("graph.state", "state", fmt.Sprintf("read graph state: %v", err))
		}
		return
	}
	if err := state.Validate(); err != nil {
		r.errorIssue("graph.state", "state", fmt.Sprintf("invalid graph state: %v", err))
	} else {
		r.State = &state
		r.passCheck("graph.state", string(state.Status))
	}
	if state.ActiveGeneration == nil {
		if state.Status == graph.StatusUnindexed {
			r.passCheck("graph.active_generation", "unindexed worktree has no active generation")
		} else {
			r.warningIssue("graph.active_generation", "graph.active_generation", "state has no active generation")
		}
		r.checkSelectedGraphRows(ctx, db, repoID, worktreeID, 0)
		return
	}

	generation, genErr := readGenerationWithoutValidation(ctx, db, repoID, worktreeID, *state.ActiveGeneration)
	if genErr != nil {
		r.errorIssue("graph.active_generation", "generation", fmt.Sprintf("read active generation %d: %v", *state.ActiveGeneration, genErr))
		return
	}
	if generation.State != graph.GenerationActive {
		r.errorIssue("graph.active_generation", "generation", fmt.Sprintf("state points to generation %d in %s state", generation.ID, generation.State))
	} else {
		r.Generation = &generation
		r.passCheck("graph.active_generation", fmt.Sprintf("generation %d is active", generation.ID))
	}
	if generation.Commit == "" {
		r.errorIssue("graph.commit", "provenance", fmt.Sprintf("active generation %d has no commit", generation.ID))
	} else if state.IndexedHead != generation.Commit {
		r.errorIssue("graph.commit", "provenance", fmt.Sprintf("indexed HEAD %s does not match generation commit %s", state.IndexedHead, generation.Commit))
	} else {
		r.passCheck("graph.commit", "indexed HEAD matches active generation")
	}
	if generation.SchemaVersion != defaultSchema {
		r.errorIssue("graph.compatibility", "schema", fmt.Sprintf("generation %d uses schema version %d, want %d", generation.ID, generation.SchemaVersion, defaultSchema))
	}
	if strings.TrimSpace(generation.AnalyzerVersion) == "" || strings.TrimSpace(generation.BuildFingerprint) == "" {
		r.errorIssue("graph.completeness", "generation", fmt.Sprintf("generation %d is missing analyzer or build fingerprint", generation.ID))
	}
	counts, countErr := generationCounts(ctx, db, repoID, worktreeID, generation.ID)
	if countErr != nil {
		r.errorIssue("graph.completeness", "generation", fmt.Sprintf("count active graph records: %v", countErr))
	} else {
		r.Counts = &counts
		r.passCheck("graph.completeness", fmt.Sprintf("packages=%d files=%d symbols=%d edges=%d", counts.Packages, counts.Files, counts.Symbols, counts.Edges))
	}
	r.checkSelectedGraphRows(ctx, db, repoID, worktreeID, generation.ID)
}

func (r *DoctorReport) checkGlobalGraph(ctx context.Context, db *sql.DB) {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM graph_state`).Scan(&count); err != nil {
		r.errorIssue("graph.state", "state", fmt.Sprintf("read graph states: %v", err))
		return
	}
	if count == 0 {
		r.infoCheck("graph.state", "no repository/worktree has been registered")
		return
	}
	r.passCheck("graph.state", fmt.Sprintf("%d graph state(s) available; select a worktree for detailed checks", count))
}

func (r *DoctorReport) checkSelectedGraphRows(ctx context.Context, db *sql.DB, repoID repository.RepoID, worktreeID repository.WorktreeID, generationID graph.GenerationID) {
	// scope selects the integer keys of the inspected generations. Every
	// check below binds it exactly once, so all checks share one argument
	// list.
	args := []any{string(repoID), string(worktreeID)}
	scope := `(SELECT generation_key FROM graph_generations WHERE repo_id = ? AND worktree_id = ?`
	if generationID > 0 {
		scope += ` AND generation_id = ?`
		args = append(args, int64(generationID))
	}
	scope += `)`
	checks := []struct {
		name   string
		code   string
		detail string
		query  string
	}{
		{"graph.edge_endpoints", "graph.edge_endpoints", "all edges reference nodes in the same generation", `SELECT COUNT(*) FROM edges e WHERE e.generation_key IN ` + scope + ` AND (NOT EXISTS (SELECT 1 FROM nodes n WHERE n.node_id=e.source_id AND n.generation_key=e.generation_key) OR NOT EXISTS (SELECT 1 FROM nodes n WHERE n.node_id=e.target_id AND n.generation_key=e.generation_key))`},
		{"graph.file_blobs", "graph.file_blobs", "all file blobs have repository provenance", `SELECT COUNT(*) FROM files f JOIN graph_generations g ON g.generation_key=f.generation_key WHERE f.generation_key IN ` + scope + ` AND f.blob_sha <> '' AND NOT EXISTS (SELECT 1 FROM blobs b WHERE b.repo_id=g.repo_id AND b.blob_sha=f.blob_sha)`},
		{"graph.symbol_ownership", "graph.symbol_ownership", "all symbols reference a package and file in the same generation", `SELECT COUNT(*) FROM symbols s WHERE s.generation_key IN ` + scope + ` AND (NOT EXISTS (SELECT 1 FROM packages p WHERE p.generation_key=s.generation_key AND p.package_key=s.package_key) OR NOT EXISTS (SELECT 1 FROM files f WHERE f.generation_key=s.generation_key AND f.file_key=s.file_key))`},
		{"graph.node_ownership", "graph.node_ownership", "owned nodes reference existing package/file/symbol records", `SELECT COUNT(*) FROM nodes n WHERE n.generation_key IN ` + scope + ` AND ((n.owned=1 AND n.package_key<>'' AND NOT EXISTS (SELECT 1 FROM packages p WHERE p.generation_key=n.generation_key AND p.package_key=n.package_key)) OR (n.owned=1 AND n.file_key<>'' AND NOT EXISTS (SELECT 1 FROM files f WHERE f.generation_key=n.generation_key AND f.file_key=n.file_key)) OR (n.owned=1 AND n.symbol_key<>'' AND NOT EXISTS (SELECT 1 FROM symbols s WHERE s.generation_key=n.generation_key AND s.symbol_key=n.symbol_key)))`},
		{"graph.evidence", "graph.evidence", "edge evidence references an existing edge and blob", `SELECT (SELECT COUNT(*) FROM edge_evidence ev WHERE NOT EXISTS (SELECT 1 FROM edges x WHERE x.edge_id=ev.edge_id)) + (SELECT COUNT(*) FROM edge_evidence ev JOIN edges x ON x.edge_id=ev.edge_id JOIN graph_generations g ON g.generation_key=x.generation_key WHERE x.generation_key IN ` + scope + ` AND ev.source_blob<>'' AND NOT EXISTS (SELECT 1 FROM blobs b WHERE b.repo_id=g.repo_id AND b.blob_sha=ev.source_blob))`},
		{"graph.package_dependencies", "graph.package_dependencies", "package dependency sources exist and external targets are labelled", `SELECT COUNT(*) FROM package_dependencies d WHERE d.generation_key IN ` + scope + ` AND (NOT EXISTS (SELECT 1 FROM packages p WHERE p.generation_key=d.generation_key AND p.package_key=d.source_package) OR (d.target_package NOT LIKE 'external:package:%' AND NOT EXISTS (SELECT 1 FROM packages p WHERE p.generation_key=d.generation_key AND p.package_key=d.target_package)))`},
		{"graph.test_relationships", "graph.test_relationships", "test relationships reference test symbols and valid targets", `SELECT COUNT(*) FROM test_relationships t WHERE t.generation_key IN ` + scope + ` AND (NOT EXISTS (SELECT 1 FROM symbols s WHERE s.generation_key=t.generation_key AND s.symbol_key=t.test_key) OR (NOT EXISTS (SELECT 1 FROM symbols s WHERE s.generation_key=t.generation_key AND s.symbol_key=t.target_key) AND NOT EXISTS (SELECT 1 FROM nodes n WHERE n.generation_key=t.generation_key AND n.node_key=t.target_key AND n.node_kind='package')))`},
		{"graph.duplicate_edges", "graph.duplicate_edges", "active semantic edges are normalized", `SELECT COUNT(*) FROM (SELECT generation_key, source_id, target_id, edge_kind, COUNT(*) AS duplicate_count FROM edges WHERE generation_key IN ` + scope + ` GROUP BY generation_key, source_id, target_id, edge_kind HAVING COUNT(*) > 1)`},
	}
	for _, check := range checks {
		count, err := countQuery(ctx, db, check.query, args...)
		if err != nil {
			r.errorIssue(check.code+".query", "graph", fmt.Sprintf("%s: %v", check.name, err))
			continue
		}
		if count != 0 {
			r.errorIssue(check.code, "graph", fmt.Sprintf("%s: %d violation(s)", check.detail, count))
		} else {
			r.passCheck(check.name, check.detail)
		}
	}
}

func (r *DoctorReport) checkParseCache(ctx context.Context, db *sql.DB, repoID repository.RepoID) {
	query := `SELECT syntax_json FROM parse_cache`
	args := []any{}
	if repoID != "" {
		query += ` WHERE repo_id = ?`
		args = append(args, string(repoID))
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		r.errorIssue("cache.read", "cache", fmt.Sprintf("read parse cache: %v", err))
		return
	}
	defer func() { _ = rows.Close() }()
	invalid := 0
	for rows.Next() {
		var payload []byte
		if scanErr := rows.Scan(&payload); scanErr != nil {
			r.errorIssue("cache.read", "cache", fmt.Sprintf("scan parse cache: %v", scanErr))
			continue
		}
		if len(payload) == 0 || !json.Valid(payload) {
			invalid++
		}
	}
	if err := rows.Err(); err != nil {
		r.errorIssue("cache.read", "cache", fmt.Sprintf("iterate parse cache: %v", err))
		return
	}
	if invalid > 0 {
		r.warningIssue("cache.invalid_json", "cache", fmt.Sprintf("%d parse-cache row(s) contain invalid JSON and will be treated as misses", invalid))
	} else {
		r.passCheck("cache.json", "parse-cache payloads are valid JSON")
	}
}

func readStateWithoutValidation(ctx context.Context, db *sql.DB, repoID repository.RepoID, worktreeID repository.WorktreeID) (graph.State, error) {
	var state graph.State
	var active sql.NullInt64
	var path, branch, observed, indexed, status, lastError, updated, analyzer string
	var headKnown, schema int
	if err := db.QueryRowContext(ctx, `SELECT w.worktree_path, w.branch, w.observed_head, w.head_known, s.indexed_head, s.active_generation, s.status, s.last_error, s.updated_at, COALESCE(g.analyzer_version, ''), COALESCE(g.schema_version, 1) FROM graph_state s JOIN worktrees w ON w.repo_id=s.repo_id AND w.worktree_id=s.worktree_id LEFT JOIN graph_generations g ON g.repo_id=s.repo_id AND g.worktree_id=s.worktree_id AND g.generation_id=s.active_generation WHERE s.repo_id=? AND s.worktree_id=?`, string(repoID), string(worktreeID)).Scan(&path, &branch, &observed, &headKnown, &indexed, &active, &status, &lastError, &updated, &analyzer, &schema); err != nil {
		return graph.State{}, err
	}
	state = graph.State{RepoID: repoID, WorktreeID: worktreeID, WorktreePath: path, Branch: branch, CurrentHead: graph.CommitSHA(observed), HeadKnown: headKnown == 1, IndexedHead: graph.CommitSHA(indexed), Status: graph.GraphStatus(status), LastError: lastError, GraphSchema: schema, AnalyzerVersion: analyzer}
	if active.Valid {
		id := graph.GenerationID(active.Int64)
		state.ActiveGeneration = &id
	}
	updatedAt, err := parseTimestamp(updated)
	if err != nil {
		return graph.State{}, fmt.Errorf("parse graph state timestamp: %w", err)
	}
	state.UpdatedAt = updatedAt
	return state, nil
}

func readGenerationWithoutValidation(ctx context.Context, db *sql.DB, repoID repository.RepoID, worktreeID repository.WorktreeID, id graph.GenerationID) (graph.Generation, error) {
	var commit, fingerprint, analyzer, state, message, created, validated, retired string
	var schema int
	if err := db.QueryRowContext(ctx, `SELECT commit_sha, build_fingerprint, analyzer_version, schema_version, state, error, created_at, COALESCE(validated_at, ''), COALESCE(retired_at, '') FROM graph_generations WHERE repo_id=? AND worktree_id=? AND generation_id=?`, string(repoID), string(worktreeID), int64(id)).Scan(&commit, &fingerprint, &analyzer, &schema, &state, &message, &created, &validated, &retired); err != nil {
		return graph.Generation{}, err
	}
	generation := graph.Generation{RepoID: repoID, WorktreeID: worktreeID, ID: id, Commit: graph.CommitSHA(commit), BuildFingerprint: fingerprint, AnalyzerVersion: analyzer, SchemaVersion: schema, State: graph.GenerationState(state), Error: message}
	createdAt, err := parseTimestamp(created)
	if err != nil {
		return graph.Generation{}, fmt.Errorf("parse graph generation creation time: %w", err)
	}
	generation.CreatedAt = createdAt
	if validated != "" {
		value, parseErr := parseTimestamp(validated)
		if parseErr != nil {
			return graph.Generation{}, fmt.Errorf("parse graph generation validation time: %w", parseErr)
		}
		generation.ValidatedAt = &value
	}
	if retired != "" {
		value, parseErr := parseTimestamp(retired)
		if parseErr != nil {
			return graph.Generation{}, fmt.Errorf("parse graph generation retirement time: %w", parseErr)
		}
		generation.RetiredAt = &value
	}
	return generation, nil
}

func generationCounts(ctx context.Context, db *sql.DB, repoID repository.RepoID, worktreeID repository.WorktreeID, generationID graph.GenerationID) (graph.Counts, error) {
	key, _, err := generationRef(ctx, db, repoID, worktreeID, generationID)
	if err != nil {
		return graph.Counts{}, err
	}
	return countGeneration(ctx, db, key)
}

// GenerationFileBlobSHAs returns the distinct Git blob IDs used by source
// files in one generation. It is deliberately limited to immutable
// provenance, so callers such as the CLI doctor can verify Git object
// availability without loading source contents into a report.
func (s *Store) GenerationFileBlobSHAs(ctx context.Context, repoID repository.RepoID, worktreeID repository.WorktreeID, generationID graph.GenerationID) ([]string, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT f.blob_sha
		FROM files AS f
		JOIN graph_generations AS g ON g.generation_key = f.generation_key
		WHERE g.repo_id = ? AND g.worktree_id = ? AND g.generation_id = ? AND f.blob_sha <> ''
		ORDER BY f.blob_sha
	`, string(repoID), string(worktreeID), int64(generationID))
	if err != nil {
		return nil, fmt.Errorf("list generation file blobs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []string
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, fmt.Errorf("scan generation file blob: %w", err)
		}
		result = append(result, sha)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate generation file blobs: %w", err)
	}
	return result, nil
}

func countQuery(ctx context.Context, db *sql.DB, query string, args ...any) (int64, error) {
	var count int64
	if err := db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (r *DoctorReport) passCheck(name, detail string) {
	r.Checks = append(r.Checks, DoctorCheck{Name: name, Status: DoctorCheckPass, Detail: detail})
}

func (r *DoctorReport) infoCheck(name, detail string) {
	r.Checks = append(r.Checks, DoctorCheck{Name: name, Status: DoctorCheckWarning, Detail: detail})
}

func (r *DoctorReport) warningIssue(code, check, message string) {
	r.Checks = append(r.Checks, DoctorCheck{Name: check, Status: DoctorCheckWarning, Detail: message})
	r.Issues = append(r.Issues, DoctorIssue{Code: code, Severity: graph.SeverityWarning, Message: message, Repository: string(r.RepositoryID), Worktree: string(r.WorktreeID)})
}

func (r *DoctorReport) errorIssue(code, check, message string) {
	r.Checks = append(r.Checks, DoctorCheck{Name: check, Status: DoctorCheckError, Detail: message})
	r.Issues = append(r.Issues, DoctorIssue{Code: code, Severity: graph.SeverityError, Message: message, Repository: string(r.RepositoryID), Worktree: string(r.WorktreeID)})
}

func (r *DoctorReport) hasIssueCode(code string) bool {
	for _, issue := range r.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func (r *DoctorReport) finish() {
	r.Healthy = !r.HasErrors()
	r.Status = DoctorHealthy
	if !r.Healthy {
		r.Status = DoctorUnhealthy
		return
	}
	for _, issue := range r.Issues {
		if issue.Severity == graph.SeverityWarning {
			r.Status = DoctorWarning
			return
		}
	}
}

func statusForSeverity(severity graph.DiagnosticSeverity) DoctorCheckStatus {
	if severity == graph.SeverityError {
		return DoctorCheckError
	}
	if severity == graph.SeverityWarning {
		return DoctorCheckWarning
	}
	return DoctorCheckPass
}
