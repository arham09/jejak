package graphdb

import (
	"context"
	"testing"

	"github.com/arham09/jejak/internal/graph"
)

func TestParseCacheRoundTripAndIsolation(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	entry := graph.SyntaxCacheEntry{BlobSHA: "blob-1", ObjectFormat: "sha1", ParserVersion: "go-syntax-v1", SyntaxJSON: []byte(`{"package":"fixture"}`)}
	if err := store.WriteParseCache(ctx, target.Repository.ID, entry); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.ReadParseCache(ctx, target.Repository.ID, entry.BlobSHA, entry.ObjectFormat, entry.ParserVersion)
	if err != nil || !found {
		t.Fatalf("ReadParseCache() got=%#v found=%v err=%v", got, found, err)
	}
	if string(got.SyntaxJSON) != string(entry.SyntaxJSON) {
		t.Fatalf("payload = %s", got.SyntaxJSON)
	}
	if _, found, err := store.ReadParseCache(ctx, target.Repository.ID, entry.BlobSHA, "sha256", entry.ParserVersion); err != nil || found {
		t.Fatalf("object-format mismatch found=%v err=%v", found, err)
	}
	if _, found, err := store.ReadParseCache(ctx, target.Repository.ID, entry.BlobSHA, entry.ObjectFormat, "go-syntax-v2"); err != nil || found {
		t.Fatalf("parser-version mismatch found=%v err=%v", found, err)
	}
}

func TestParseCacheCorruptPayloadIsMissAndInvalidWritesFail(t *testing.T) {
	store := openMigratedStore(t)
	target := fixtureTarget(t)
	ctx := context.Background()
	if _, err := store.Register(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteParseCache(ctx, target.Repository.ID, graph.SyntaxCacheEntry{BlobSHA: "bad", ParserVersion: "v1", SyntaxJSON: []byte("not json")}); err == nil {
		t.Fatal("invalid JSON write unexpectedly succeeded")
	}
	entry := graph.SyntaxCacheEntry{BlobSHA: "bad", ParserVersion: "v1", SyntaxJSON: []byte(`{"ok":true}`)}
	if err := store.WriteParseCache(ctx, target.Repository.ID, entry); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE parse_cache SET syntax_json = ? WHERE repo_id = ? AND blob_sha = ?`, []byte("{"), string(target.Repository.ID), entry.BlobSHA); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.ReadParseCache(ctx, target.Repository.ID, entry.BlobSHA, "sha1", entry.ParserVersion); err != nil || found {
		t.Fatalf("corrupt cache found=%v err=%v", found, err)
	}
}
