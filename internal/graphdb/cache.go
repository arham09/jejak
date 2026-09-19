package graphdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
)

// ReadParseCache returns a versioned syntax summary. Invalid or empty payloads
// are treated as misses so a future write can repair a corrupt optimization
// row without affecting graph activation.
func (s *Store) ReadParseCache(ctx context.Context, repoID repository.RepoID, blobSHA, objectFormat, parserVersion string) (graph.SyntaxCacheEntry, bool, error) {
	if repoID == "" || blobSHA == "" || parserVersion == "" {
		return graph.SyntaxCacheEntry{}, false, fmt.Errorf("%w: parse-cache identity is incomplete", ErrInvalidGeneration)
	}
	if objectFormat == "" {
		objectFormat = "sha1"
	}
	db, err := s.database()
	if err != nil {
		return graph.SyntaxCacheEntry{}, false, err
	}
	var payload []byte
	err = db.QueryRowContext(ctx, `
		SELECT syntax_json FROM parse_cache
		WHERE repo_id = ? AND blob_sha = ? AND object_format = ? AND parser_version = ?
	`, string(repoID), blobSHA, objectFormat, parserVersion).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return graph.SyntaxCacheEntry{}, false, nil
	}
	if err != nil {
		return graph.SyntaxCacheEntry{}, false, fmt.Errorf("read parse cache %q: %w", blobSHA, err)
	}
	if len(payload) == 0 || !validJSON(payload) {
		return graph.SyntaxCacheEntry{}, false, nil
	}
	return graph.SyntaxCacheEntry{BlobSHA: blobSHA, ObjectFormat: objectFormat, ParserVersion: parserVersion, SyntaxJSON: append([]byte(nil), payload...)}, true, nil
}

// WriteParseCache upserts one serializable syntax summary. It also ensures the
// repository/blob parent exists, allowing callers to warm the cache directly;
// normal indexing has already written the same blob through WriteAnalysis.
func (s *Store) WriteParseCache(ctx context.Context, repoID repository.RepoID, entry graph.SyntaxCacheEntry) error {
	if repoID == "" || entry.BlobSHA == "" || entry.ParserVersion == "" || len(entry.SyntaxJSON) == 0 {
		return fmt.Errorf("%w: parse-cache entry is incomplete", ErrInvalidGeneration)
	}
	if !validJSON(entry.SyntaxJSON) {
		return fmt.Errorf("%w: parse-cache payload is not valid JSON", ErrInvalidGeneration)
	}
	objectFormat := entry.ObjectFormat
	if objectFormat == "" {
		objectFormat = "sha1"
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin parse-cache write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blobs(repo_id, blob_sha, object_format, byte_size, created_at)
		VALUES (?, ?, ?, 0, ?)
		ON CONFLICT(repo_id, blob_sha) DO UPDATE SET object_format = excluded.object_format
	`, string(repoID), entry.BlobSHA, objectFormat, timestamp(now())); err != nil {
		return fmt.Errorf("ensure parse-cache blob %q: %w", entry.BlobSHA, err)
	}
	nowValue := timestamp(now())
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO parse_cache(repo_id, blob_sha, object_format, parser_version, syntax_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_id, blob_sha, object_format, parser_version) DO UPDATE SET syntax_json = excluded.syntax_json, updated_at = excluded.updated_at
	`, string(repoID), entry.BlobSHA, objectFormat, entry.ParserVersion, entry.SyntaxJSON, nowValue, nowValue); err != nil {
		return fmt.Errorf("write parse cache %q: %w", entry.BlobSHA, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit parse-cache write %q: %w", entry.BlobSHA, err)
	}
	return nil
}

// GetParseCache is an alias for callers using read vocabulary.
func (s *Store) GetParseCache(ctx context.Context, repoID repository.RepoID, blobSHA, objectFormat, parserVersion string) (graph.SyntaxCacheEntry, bool, error) {
	return s.ReadParseCache(ctx, repoID, blobSHA, objectFormat, parserVersion)
}

// PutParseCache is an alias for callers using write vocabulary.
func (s *Store) PutParseCache(ctx context.Context, repoID repository.RepoID, entry graph.SyntaxCacheEntry) error {
	return s.WriteParseCache(ctx, repoID, entry)
}

func validJSON(payload []byte) bool {
	return json.Valid(payload)
}
