package graphdb

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
)

func TestLatestMigrationMatchesEmbeddedFiles(t *testing.T) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	highest := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		version, err := migrationVersion(entry.Name())
		if err != nil {
			t.Fatalf("migrationVersion(%q) error = %v", entry.Name(), err)
		}
		if version > highest {
			highest = version
		}
	}
	if highest != latestMigration {
		t.Fatalf("latestMigration = %d, embedded migrations reach %d", latestMigration, highest)
	}
}

// TestMigrateIndexesEdgeEvidence guards the join index that keeps relationship
// and reference queries off a full evidence scan.
func TestMigrateIndexesEdgeEvidence(t *testing.T) {
	store := openMigratedStore(t)
	db, err := store.database()
	if err != nil {
		t.Fatal(err)
	}
	var name string
	if err := db.QueryRowContext(context.Background(), `
		SELECT name FROM sqlite_master
		WHERE type = 'index' AND tbl_name = 'edge_evidence' AND name = 'idx_edge_evidence_edge'
	`).Scan(&name); err != nil {
		t.Fatalf("edge_evidence join index missing: %v", err)
	}
}

// TestFreshStoreUsesIncrementalAutoVacuum guards the setting that lets pruned
// generations return their pages to the filesystem without a full VACUUM.
func TestFreshStoreUsesIncrementalAutoVacuum(t *testing.T) {
	store := openMigratedStore(t)
	db, err := store.database()
	if err != nil {
		t.Fatal(err)
	}
	var mode int
	if err := db.QueryRowContext(context.Background(), `PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != 2 {
		t.Fatalf("auto_vacuum = %d, want 2 (incremental)", mode)
	}
}

// TestRelationshipQueriesAvoidFullScans asserts the planner seeks every large
// table for the real relationship and reference queries. A regression here
// is invisible in output but makes queries grow with the whole generation
// instead of with the inspected symbol.
func TestRelationshipQueriesAvoidFullScans(t *testing.T) {
	store := openMigratedStore(t)
	db, err := store.database()
	if err != nil {
		t.Fatal(err)
	}
	view := &View{key: 1}
	incident := view.incidentEdgeArgs(7)
	for _, testCase := range []struct {
		name  string
		query string
		args  []any
	}{
		{name: "relationships", query: relationshipQuery, args: incident},
		{name: "bounded relationships", query: boundedRelationshipQuery, args: append(append([]any(nil), incident...), 16)},
		{name: "references", query: referenceQuery, args: []any{int64(1), int64(7), "references"}},
		{name: "bounded references", query: boundedReferenceQuery, args: []any{int64(1), int64(7), "references", 16}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+testCase.query, testCase.args...)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var id, parent, notUsed int
				var detail string
				if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
					t.Fatal(err)
				}
				for _, table := range []string{"edges", "edge_evidence", "nodes", "symbols", "files"} {
					if detail == "SCAN "+table || detail == "SCAN "+table+" AS e" || detail == "SCAN "+table+" AS ev" || detail == "SCAN "+table+" AS ev2" || detail == "SCAN "+table+" AS sn" || detail == "SCAN "+table+" AS tn" {
						t.Fatalf("query plan scans %s: %s", table, detail)
					}
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
